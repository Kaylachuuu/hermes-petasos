import copy
import json

import pytest

import core


class Clock:
    def __init__(self):
        self.now = 1000.0

    def __call__(self):
        return self.now


@pytest.fixture
def setup(tmp_path):
    clock = Clock()
    petasos = core.Petasos(core.MappingFile(tmp_path / "petasos" / "mapping.json"), clock=clock)
    return petasos, clock


def ev(name, action, t=None, **extra):
    event = {"input": name, "action": action, **extra}
    if t is not None:
        event["t"] = t
    return event


def test_default_mapping_is_valid_and_written_on_first_use(setup):
    petasos, _ = setup
    assert core.validate(core.DEFAULT_MAPPING) == []
    petasos.mapping.current()
    assert json.loads(petasos.mapping.path.read_text()) == core.DEFAULT_MAPPING


def test_hold_front_is_push_to_talk(setup):
    petasos, _ = setup
    assert petasos.handle(ev("front", "press", t=0)) == [{"do": "listen", "mode": "hold"}]
    assert petasos.handle(ev("front", "release", t=900)) == [{"do": "send"}]
    assert petasos.status()["devices"]["car-thing"]["listening"] is None


def test_tap_front_turns_the_open_mic_into_conversation_and_tap_ends_it(setup):
    petasos, _ = setup
    petasos.handle(ev("front", "press", t=0))
    out = petasos.handle(ev("front", "release", t=120))
    assert out == [{"do": "listen", "mode": "conversation", "end_of_speech_ms": 1000, "idle_end_s": 30}]
    # Pressing during conversation does not open a second, push-to-talk mic.
    assert petasos.handle(ev("front", "press", t=5000)) == []
    assert petasos.handle(ev("front", "release", t=5100)) == [{"do": "listen_stop"}]


def test_device_can_report_conversation_ended(setup):
    petasos, _ = setup
    petasos.handle(ev("front", "press", t=0))
    petasos.handle(ev("front", "release", t=100))
    assert petasos.handle(ev("device", "conversation_ended")) == []
    # The next tap starts a new conversation rather than ending a finished one.
    petasos.handle(ev("front", "press", t=9000))
    assert petasos.handle(ev("front", "release", t=9100))[0]["mode"] == "conversation"


def test_server_clock_is_used_without_device_timestamps(setup):
    petasos, clock = setup
    petasos.handle(ev("front", "press"))
    clock.now += 1.0
    assert petasos.handle(ev("front", "release")) == [{"do": "send"}]


def test_knob_stops_speaking_and_turns_volume(setup):
    petasos, _ = setup
    assert petasos.handle(ev("knob", "press")) == [{"do": "stop_speaking"}]
    assert petasos.handle(ev("knob", "release")) == []
    assert petasos.handle(ev("knob", "turn", delta=-1)) == [{"do": "volume", "delta": -5}]
    assert petasos.handle(ev("knob", "turn", delta=2)) == [{"do": "volume", "delta": 10}]


def test_presets_send_their_prompt_and_empty_ones_do_nothing(setup):
    petasos, _ = setup
    assert petasos.handle(ev("preset-1", "press")) == [{"do": "prompt", "text": "What's next on my calendar?"}]
    assert petasos.handle(ev("preset-4", "press")) == []


def test_mute_closes_the_mic_and_blocks_talking(setup):
    petasos, _ = setup
    petasos.handle(ev("front", "press", t=0))
    assert petasos.handle(ev("mute", "press")) == [{"do": "cancel"}, {"do": "mic", "muted": True}]
    assert petasos.handle(ev("front", "release", t=1000)) == [{"do": "notice", "text": "The mic is off."}]
    assert petasos.handle(ev("front", "press", t=2000)) == []
    assert petasos.handle(ev("front", "release", t=2100)) == [{"do": "notice", "text": "The mic is off."}]
    assert petasos.handle(ev("mute", "press")) == [{"do": "mic", "muted": False}]
    assert petasos.handle(ev("front", "press", t=3000)) == [{"do": "listen", "mode": "hold"}]


def test_devices_keep_separate_state(setup):
    petasos, _ = setup
    petasos.handle(ev("mute", "press", device="car"))
    assert petasos.handle(ev("front", "press", t=0, device="desk")) == [{"do": "listen", "mode": "hold"}]


def test_bad_events_are_rejected(setup):
    petasos, _ = setup
    for bad in ({"input": "volume-up", "action": "press"}, {"input": "knob", "action": "wiggle"},
                {"input": "knob", "action": "turn", "delta": "1"}, "press"):
        with pytest.raises(core.MappingError):
            petasos.handle(bad)


def test_release_without_press_is_ignored(setup):
    petasos, _ = setup
    assert petasos.handle(ev("front", "release", t=10)) == []


def test_edits_to_the_file_apply_without_restart(setup):
    petasos, _ = setup
    mapping = petasos.mapping.current()
    mapping["controls"]["preset-4"] = {"press": {"action": "prompt", "text": "Good morning"}}
    petasos.mapping.path.write_text(json.dumps(mapping))
    assert petasos.handle(ev("preset-4", "press")) == [{"do": "prompt", "text": "Good morning"}]


def test_a_broken_file_keeps_the_last_good_layout(setup):
    petasos, _ = setup
    petasos.mapping.current()
    petasos.mapping.path.write_text("{ not json")
    assert petasos.handle(ev("knob", "press")) == [{"do": "stop_speaking"}]
    assert "could not be read as JSON" in petasos.status()["mapping_problems"][0]
    bad = copy.deepcopy(core.DEFAULT_MAPPING)
    bad["controls"]["front"]["tap"] = {"action": "push_to_talk"}
    petasos.mapping.path.write_text(json.dumps(bad))
    assert petasos.handle(ev("knob", "press")) == [{"do": "stop_speaking"}]
    assert petasos.status()["mapping_problems"] == ["front.tap: push_to_talk only works on hold."]


@pytest.mark.parametrize("change, problem", [
    (lambda m: m["controls"].update({"wheel": {}}), "Unknown input 'wheel'"),
    (lambda m: m["controls"]["mute"].update({"tap": {"action": "none"}}), "cannot have press together"),
    (lambda m: m["controls"]["front"].update({"turn": {"action": "volume"}}), "Only the knob can turn"),
    (lambda m: m["controls"]["preset-3"].update({"press": {"action": "prompt", "text": " "}}), "needs some text"),
    (lambda m: m["controls"]["knob"].update({"press": {"action": "volume"}}), "volume only works on turn"),
    (lambda m: m.update({"hold_ms": 5}), "hold_ms must be"),
    (lambda m: m["conversation"].update({"idle_end_s": True}), "conversation.idle_end_s must be"),
])
def test_validate_explains_problems(change, problem):
    mapping = copy.deepcopy(core.DEFAULT_MAPPING)
    change(mapping)
    assert any(problem in p for p in core.validate(mapping)), core.validate(mapping)


def test_replace_rejects_a_bad_mapping_and_saves_a_good_one(setup):
    petasos, _ = setup
    with pytest.raises(core.MappingError):
        petasos.mapping.replace({"controls": {}})
    good = copy.deepcopy(core.DEFAULT_MAPPING)
    good["volume_step"] = 10
    petasos.mapping.replace(good)
    assert json.loads(petasos.mapping.path.read_text())["volume_step"] == 10
    assert petasos.handle(ev("knob", "turn", delta=1)) == [{"do": "volume", "delta": 10}]


def test_hello_resets_the_device(setup):
    petasos, _ = setup
    petasos.handle(ev("mute", "press"))
    petasos.handle(ev("front", "press", t=0))
    assert petasos.handle(ev("device", "hello")) == [{"do": "screen", "presets": ["Calendar", "Last message", "", ""], "agent": "Hermes"}]
    assert petasos.status()["devices"]["car-thing"]["muted"] is False
    assert petasos.handle(ev("front", "release", t=50)) == []
    assert petasos.handle(ev("front", "press", t=100)) == [{"do": "listen", "mode": "hold"}]


def test_screen_gets_preset_labels_on_hello_and_when_they_change(setup):
    petasos, _ = setup
    assert petasos.handle(ev("device", "hello")) == [
        {"do": "screen", "presets": ["Calendar", "Last message", "", ""], "agent": "Hermes"}]
    assert petasos.handle(ev("knob", "press")) == [{"do": "stop_speaking"}]
    mapping = petasos.mapping.current()
    mapping["controls"]["preset-3"] = {"press": {"action": "prompt", "text": "What's the weather tomorrow in Casper?"}}
    mapping["controls"]["preset-1"]["press"].pop("label")
    petasos.mapping.replace(mapping)
    assert petasos.handle(ev("knob", "press")) == [
        {"do": "screen", "presets": ["What's next on my calendar", "Last message", "What's the weather tomorrow in…", ""],
         "agent": "Hermes"},
        {"do": "stop_speaking"}]


def test_labels_must_be_short(setup):
    mapping = copy.deepcopy(core.DEFAULT_MAPPING)
    mapping["controls"]["preset-1"]["press"]["label"] = "x" * 33
    assert any("label" in p for p in core.validate(mapping))


def test_agent_name_reaches_the_screen(setup):
    petasos, _ = setup
    petasos.handle(ev("device", "hello"))
    mapping = petasos.mapping.current()
    mapping["agent_name"] = "Athena"
    petasos.mapping.replace(mapping)
    assert petasos.handle(ev("knob", "press"))[0] == {
        "do": "screen", "presets": ["Calendar", "Last message", "", ""], "agent": "Athena"}
    mapping["agent_name"] = ""
    assert any("agent_name" in p for p in core.validate(mapping))


def test_car_buttons_work_with_a_mapping_written_before_them(setup):
    petasos, _ = setup
    old = copy.deepcopy(core.DEFAULT_MAPPING)
    for name in ("car-play", "car-next", "car-previous"):
        del old["controls"][name]
    petasos.mapping.replace(old)
    started = petasos.handle(ev("car-play", "press"))
    assert started[-1]["do"] == "listen" and started[-1]["mode"] == "conversation"
    assert petasos.handle(ev("car-play", "release")) == []
    assert petasos.handle(ev("car-play", "press"))[-1] == {"do": "listen_stop"}
    assert petasos.handle(ev("car-next", "press"))[-1] == {"do": "stop_speaking"}
