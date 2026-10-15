"""Petasos core: turns the Car Thing's button and knob events into commands for the device.

The Car Thing reports only what was pressed (``{"input": "front", "action": "press"}``). Which
action a control performs lives here, in a mapping file on the server, so the layout can change
without touching the device. The device gets back a short list of commands to carry out, such as
``{"do": "listen", "mode": "hold"}`` or ``{"do": "stop_speaking"}``.

Inputs (named in tools/list_inputs.sh and the README):
  preset-1 .. preset-4   the four top buttons, left to right
  mute                   the top-right button
  front                  the front button
  knob                   the knob: "press" when pushed, "turn" with a delta of +1 or -1 per click
  device                 the client itself: "hello" when it starts (resets its state), or
                         "conversation_ended" when conversation mode ended on its own

Gestures in the mapping: "press" fires on press; "hold" and "tap" are told apart on release by
how long the control was held (``hold_ms``); "turn" is for the knob.
"""
from __future__ import annotations

import copy
import json
import threading
import time
from pathlib import Path
from typing import Callable

# The car-* inputs are a car's steering wheel media buttons, reaching the Car Thing over Bluetooth
# (AVRCP) while it plays to the car.
INPUTS = ("preset-1", "preset-2", "preset-3", "preset-4", "mute", "front", "knob",
          "car-play", "car-next", "car-previous")
GESTURES = ("press", "hold", "tap", "turn")
ACTIONS = ("push_to_talk", "conversation", "stop_speaking", "volume", "mic_toggle", "prompt",
           "music_toggle", "music_next", "music_previous", "none")

DEFAULT_MAPPING: dict = {
    "agent_name": "Hermes",
    "chats": "separate",  # each device its own chat with the agent, or "shared": one for all
    "hold_ms": 350,
    "volume_step": 5,
    "conversation": {"end_of_speech_ms": 1000, "idle_end_s": 30},
    "controls": {
        "front": {"hold": {"action": "push_to_talk"}, "tap": {"action": "conversation"}},
        "knob": {"press": {"action": "stop_speaking"}, "turn": {"action": "volume"}},
        "mute": {"press": {"action": "mic_toggle"}},
        "preset-1": {"press": {"action": "prompt", "text": "What's next on my calendar?", "label": "Calendar"}},
        "preset-2": {"press": {"action": "prompt", "text": "Read me my last message.", "label": "Last message"}},
        "preset-3": {"press": {"action": "none"}},
        "preset-4": {"press": {"action": "none"}},
        "car-play": {"press": {"action": "conversation"}},
        "car-next": {"press": {"action": "music_next"}},
        "car-previous": {"press": {"action": "music_previous"}},
    },
}


# What the car's skip buttons did before they skipped songs; a mapping still holding these
# unchanged gets today's defaults.
_OLD_CAR_DEFAULTS = {"car-next": {"press": {"action": "stop_speaking"}},
                     "car-previous": {"press": {"action": "none"}}}

# The screen's name for an action with no label of its own, where its words alone read badly.
_ACTION_LABELS = {"music_toggle": "Play / pause", "music_next": "Next song", "music_previous": "Previous song",
                  "mic_toggle": "Mic on / off"}

LABEL_MAX = 32  # two lines on the screen
AGENT_NAME_MAX = 24


def agent_name(mapping: dict) -> str:
    """Who the Car Thing talks to, as shown on its screen: the mapping's agent_name."""
    name = mapping.get("agent_name")
    return name.strip() if isinstance(name, str) and name.strip() else DEFAULT_MAPPING["agent_name"]


PRESETS = ("preset-1", "preset-2", "preset-3", "preset-4")
DEVICE_ID_MAX = 64
DEVICE_NAME_MAX = 48


def controls_for(mapping: dict, device: str | None) -> dict:
    """The controls a device uses: the mapping's, with the preset buttons the device has its own
    of (``devices.<id>.controls``, set from its settings) in place of the shared ones."""
    controls = dict(mapping.get("controls") or {})
    own = ((mapping.get("devices") or {}).get(device or "") or {}).get("controls")
    if isinstance(own, dict):
        controls.update({name: own[name] for name in PRESETS if name in own})
    return controls


def preset_labels(mapping: dict, device: str | None = None) -> list[str]:
    """What the screen shows under each preset button: its label, else the start of its prompt,
    else "" for a button that does nothing."""
    labels = []
    controls = controls_for(mapping, device)
    for name in PRESETS:
        gestures = controls.get(name) or {}
        step = next((s for s in gestures.values() if isinstance(s, dict) and s.get("action") not in (None, "none")), None)
        label = ""
        if step:
            label = (step.get("label") or "").strip()
            if not label and step.get("action") == "prompt":
                label = step["text"].strip().rstrip("?.!")
                if len(label) > LABEL_MAX:
                    label = label[:LABEL_MAX - 1].rsplit(" ", 1)[0] + "…"
            elif not label:
                label = _ACTION_LABELS.get(step["action"]) or step["action"].replace("_", " ").capitalize()
        labels.append(label)
    return labels


class MappingError(ValueError):
    pass


def validate(mapping: dict) -> list[str]:
    """Every problem with a mapping, as plain sentences. An empty list means it is usable."""
    problems: list[str] = []
    if not isinstance(mapping, dict):
        return ["The mapping must be a JSON object."]
    name = mapping.get("agent_name")
    if name is not None and not (isinstance(name, str) and 0 < len(name.strip()) <= AGENT_NAME_MAX):
        problems.append(f"agent_name is the name the Car Thing shows and says, 1 to {AGENT_NAME_MAX} characters.")
    if mapping.get("chats") not in (None, "separate", "shared"):
        problems.append('chats is "separate" (a chat for each device) or "shared" (one for all).')
    for key, low, high in (("hold_ms", 100, 3000), ("volume_step", 1, 50)):
        value = mapping.get(key)
        if not isinstance(value, int) or isinstance(value, bool) or not low <= value <= high:
            problems.append(f"{key} must be a whole number from {low} to {high}.")
    conv = mapping.get("conversation")
    if not isinstance(conv, dict):
        problems.append("conversation must be an object with end_of_speech_ms and idle_end_s.")
    else:
        for key, low, high in (("end_of_speech_ms", 300, 5000), ("idle_end_s", 5, 600)):
            value = conv.get(key)
            if not isinstance(value, int) or isinstance(value, bool) or not low <= value <= high:
                problems.append(f"conversation.{key} must be a whole number from {low} to {high}.")
    controls = mapping.get("controls")
    if not isinstance(controls, dict):
        return problems + ["controls must be an object keyed by input name."]
    problems += _control_problems(controls, INPUTS, "")
    devices = mapping.get("devices")
    if devices is None:
        return problems
    if not isinstance(devices, dict):
        return problems + ["devices must be an object keyed by device id."]
    for device, own in devices.items():
        where = f"devices.{device}"
        if not (isinstance(device, str) and 0 < len(device) <= DEVICE_ID_MAX):
            problems.append(f"A device id is 1 to {DEVICE_ID_MAX} characters.")
            continue
        if not isinstance(own, dict):
            problems.append(f"{where} must be an object.")
            continue
        name = own.get("name")
        if name is not None and not (isinstance(name, str) and len(name.strip()) <= DEVICE_NAME_MAX):
            problems.append(f"{where}.name is the device's name, at most {DEVICE_NAME_MAX} characters.")
        if not isinstance(own.get("controls", {}), dict):
            problems.append(f"{where}.controls must be an object keyed by preset button.")
            continue
        problems += _control_problems(own.get("controls", {}), PRESETS, f"{where}.controls.")
    return problems


def _control_problems(controls: dict, inputs: tuple, prefix: str) -> list[str]:
    problems: list[str] = []
    for name, gestures in controls.items():
        if name not in inputs:
            problems.append(f"Unknown input {prefix}{name!r}; inputs are {', '.join(inputs)}.")
            continue
        if not isinstance(gestures, dict):
            problems.append(f"{prefix}{name} must be an object keyed by gesture.")
            continue
        if "press" in gestures and ("hold" in gestures or "tap" in gestures):
            problems.append(f"{prefix}{name} cannot have press together with hold or tap; press fires first.")
        if "turn" in gestures and name != "knob":
            problems.append(f"Only the knob can turn, not {prefix}{name}.")
        for gesture, step in gestures.items():
            where = f"{prefix}{name}.{gesture}"
            if gesture not in GESTURES:
                problems.append(f"Unknown gesture {where}; gestures are {', '.join(GESTURES)}.")
                continue
            if not isinstance(step, dict) or step.get("action") not in ACTIONS:
                problems.append(f"{where} needs an action, one of {', '.join(ACTIONS)}.")
                continue
            action = step["action"]
            if action == "prompt" and not (isinstance(step.get("text"), str) and step["text"].strip()):
                problems.append(f"{where} is a prompt and needs some text.")
            label = step.get("label")
            if label is not None and not (isinstance(label, str) and 0 < len(label.strip()) <= LABEL_MAX):
                problems.append(f"{where}: a label is a short name for the screen, 1 to {LABEL_MAX} characters.")
            if action == "push_to_talk" and gesture != "hold":
                problems.append(f"{where}: push_to_talk only works on hold.")
            if action == "volume" and gesture != "turn":
                problems.append(f"{where}: volume only works on turn.")
            if gesture == "turn" and action not in ("volume", "none"):
                problems.append(f"{where}: a turn can only be volume or none.")
    return problems


class MappingFile:
    """The mapping on disk. Re-read whenever the file changes, so edits apply without a restart.
    A missing file is created with the defaults; a broken one is reported and the last good
    mapping (or the defaults) stays in use."""

    def __init__(self, path: Path) -> None:
        self.path = path
        self._lock = threading.Lock()
        self._stamp: tuple[int, int] | None = None
        self._mapping = copy.deepcopy(DEFAULT_MAPPING)
        self.problems: list[str] = []

    def current(self) -> dict:
        with self._lock:
            if not self.path.exists():
                self._write(DEFAULT_MAPPING)
            stat = self.path.stat()
            stamp = (stat.st_mtime_ns, stat.st_size)
            if stamp != self._stamp:
                self._stamp = stamp
                try:
                    loaded = json.loads(self.path.read_text(encoding="utf-8"))
                except (OSError, json.JSONDecodeError) as exc:
                    self.problems = [f"{self.path} could not be read as JSON: {exc}"]
                else:
                    problems = validate(loaded)
                    self.problems = problems
                    if not problems:
                        self._mapping = loaded
            return copy.deepcopy(self._mapping)

    def replace(self, mapping: dict) -> dict:
        problems = validate(mapping)
        if problems:
            raise MappingError(" ".join(problems))
        with self._lock:
            self._write(mapping)
            self._mapping = copy.deepcopy(mapping)
            self.problems = []
            stat = self.path.stat()
            self._stamp = (stat.st_mtime_ns, stat.st_size)
        return copy.deepcopy(mapping)

    def _write(self, mapping: dict) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.path.with_suffix(".tmp")
        tmp.write_text(json.dumps(mapping, indent=2) + "\n", encoding="utf-8")
        tmp.replace(self.path)


class DeviceState:
    def __init__(self) -> None:
        self.muted = False
        self.listening: str | None = None   # "hold" or "conversation"
        self.down: dict[str, float] = {}     # input -> press time in ms, for hold/tap
        self.last_seen = 0.0
        self.labels_sent: dict | None = None  # the screen command the device last got


class Petasos:
    """Turns events into commands, one state per device. Thread-safe."""

    def __init__(self, mapping: MappingFile, clock: Callable[[], float] = time.monotonic) -> None:
        self.mapping = mapping
        self.clock = clock
        self._lock = threading.Lock()
        self._devices: dict[str, DeviceState] = {}

    def handle(self, event: dict) -> list[dict]:
        if not isinstance(event, dict):
            raise MappingError("An event must be a JSON object.")
        name = event.get("input")
        action = event.get("action")
        device_id = str(event.get("device") or "car-thing")[:64]
        if name not in INPUTS + ("device",):
            raise MappingError(f"Unknown input {name!r}; inputs are {', '.join(INPUTS)}.")
        mapping = self.mapping.current()
        with self._lock:
            state = self._devices.setdefault(device_id, DeviceState())
            state.last_seen = self.clock()
            now_ms = event.get("t") if isinstance(event.get("t"), (int, float)) else state.last_seen * 1000
            if name == "device":
                if action == "hello":  # the client (re)started: forget mic, listening and held buttons
                    self._devices[device_id] = DeviceState()
                    self._devices[device_id].last_seen = state.last_seen
                    return self._screen(self._devices[device_id], mapping, device_id)
                if action == "conversation_ended" and state.listening == "conversation":
                    state.listening = None
                return []
            controls = controls_for(mapping, device_id).get(name)
            if name.startswith("car-") and (controls is None or controls == _OLD_CAR_DEFAULTS.get(name)):
                # A mapping written before the car buttons existed, or before they played
                # music, gets their defaults.
                controls = DEFAULT_MAPPING["controls"][name]
            controls = controls or {}
            if action == "press":
                out = self._press(state, mapping, name, controls, now_ms)
            elif action == "release":
                out = self._release(state, mapping, name, controls, now_ms)
            elif action == "turn":
                delta = event.get("delta")
                if not isinstance(delta, int) or isinstance(delta, bool):
                    raise MappingError("A turn needs a whole-number delta.")
                out = self._run(state, mapping, controls.get("turn"), delta=delta)
            else:
                raise MappingError(f"Unknown action {action!r}; actions are press, release and turn.")
            changed = self._screen(state, mapping, device_id) if state.labels_sent is not None else []
            return changed + out

    @staticmethod
    def _screen(state, mapping, device_id) -> list[dict]:
        """The preset labels and agent name for the device's screen: on hello, then whenever they
        change."""
        screen = {"do": "screen", "presets": preset_labels(mapping, device_id), "agent": agent_name(mapping)}
        if screen == state.labels_sent:
            return []
        state.labels_sent = screen
        return [screen]

    # --- a device's own preset buttons --------------------------------------------------------

    def presets(self, device_id: str) -> dict:
        """A device's preset buttons as its settings show them: the ones it uses, whether they
        are its own, and the shared ones it would go back to."""
        mapping = self.mapping.current()
        own = (mapping.get("devices") or {}).get(device_id)
        shared = {name: mapping["controls"].get(name) or {"press": {"action": "none"}} for name in PRESETS}
        used = controls_for(mapping, device_id)
        return {"device": device_id, "own": own is not None,
                "name": (own or {}).get("name", ""),
                "presets": {name: used.get(name) or {"press": {"action": "none"}} for name in PRESETS},
                "shared": shared, "labels": preset_labels(mapping, device_id)}

    def set_presets(self, device_id: str, presets: dict | None, name: str | None = None) -> tuple[dict, list[dict]]:
        """Gives a device its own preset buttons, or (presets None) puts it back on the shared
        ones. Returns its presets and the screen command for its new labels."""
        if not (isinstance(device_id, str) and 0 < len(device_id) <= DEVICE_ID_MAX):
            raise MappingError(f"A device id is 1 to {DEVICE_ID_MAX} characters.")
        mapping = self.mapping.current()
        devices = dict(mapping.get("devices") or {})
        if presets is None:
            devices.pop(device_id, None)
        else:
            if not isinstance(presets, dict):
                raise MappingError("presets must be an object keyed by preset button.")
            own = {"controls": {k: presets[k] for k in PRESETS if k in presets}}
            unknown = [k for k in presets if k not in PRESETS]
            if unknown:
                raise MappingError(f"Only the preset buttons can be a device's own, not {', '.join(unknown)}.")
            label = name if isinstance(name, str) else (devices.get(device_id) or {}).get("name")
            if label:
                own["name"] = label.strip()
            devices[device_id] = own
        if devices:
            mapping["devices"] = devices
        else:
            mapping.pop("devices", None)
        mapping = self.mapping.replace(mapping)
        with self._lock:
            state = self._devices.get(device_id)
            screen = self._screen(state, mapping, device_id) if state else [
                {"do": "screen", "presets": preset_labels(mapping, device_id), "agent": agent_name(mapping)}]
        return self.presets(device_id), screen

    def status(self) -> dict:
        now = self.clock()
        with self._lock:
            devices = {d: {"muted": s.muted, "listening": s.listening,
                           "seen_seconds_ago": round(now - s.last_seen, 1)}
                       for d, s in self._devices.items()}
        return {"devices": devices, "mapping_file": str(self.mapping.path),
                "mapping_problems": list(self.mapping.problems)}

    # --- gestures ---------------------------------------------------------------------------

    def _press(self, state, mapping, name, controls, now_ms) -> list[dict]:
        if "press" in controls:
            return self._run(state, mapping, controls["press"])
        if "hold" not in controls and "tap" not in controls:
            return []
        state.down[name] = now_ms
        # Open the mic the moment push-to-talk might start, so the first word isn't lost. If the
        # press turns out to be a tap, the release converts or cancels it.
        hold = controls.get("hold") or {}
        if hold.get("action") == "push_to_talk" and state.listening is None and not state.muted:
            state.listening = "hold"
            return [{"do": "listen", "mode": "hold"}]
        return []

    def _release(self, state, mapping, name, controls, now_ms) -> list[dict]:
        pressed = state.down.pop(name, None)
        if pressed is None:
            return []
        gesture = "hold" if now_ms - pressed >= mapping["hold_ms"] else "tap"
        step = controls.get(gesture)
        if state.listening == "hold":
            state.listening = None
            if step and step.get("action") == "push_to_talk":
                return [{"do": "send"}]
            if step and step.get("action") == "conversation":
                # A tap: the mic is already open, so keep it open in conversation mode.
                return self._start_conversation(state, mapping)
            return [{"do": "cancel"}] + self._run(state, mapping, step)
        return self._run(state, mapping, step)

    # --- actions ----------------------------------------------------------------------------

    def _run(self, state, mapping, step, delta: int = 0) -> list[dict]:
        action = (step or {}).get("action", "none")
        if action == "none":
            return []
        if action == "stop_speaking":
            return [{"do": "stop_speaking"}]
        if action.startswith("music_"):  # the phone's music, through the Car Thing (client music.go)
            return [{"do": "music", "op": action.removeprefix("music_")}]
        if action == "volume":
            return [{"do": "volume", "delta": delta * mapping["volume_step"]}] if delta else []
        if action == "prompt":
            return [{"do": "prompt", "text": step["text"]}]
        if action == "mic_toggle":
            state.muted = not state.muted
            out = []
            if state.muted and state.listening:
                out.append({"do": "cancel"})
                state.listening = None
            return out + [{"do": "mic", "muted": state.muted}]
        if action == "conversation":
            if state.listening == "conversation":
                state.listening = None
                return [{"do": "listen_stop"}]
            return self._start_conversation(state, mapping)
        if action == "push_to_talk":
            # Reached only when the press didn't open the mic, which means it was muted.
            return [{"do": "notice", "text": "The mic is off."}] if state.muted else []
        return []

    def _start_conversation(self, state, mapping) -> list[dict]:
        # A muted press never opens the mic, so there is nothing to cancel here.
        if state.muted:
            return [{"do": "notice", "text": "The mic is off."}]
        state.listening = "conversation"
        conv = mapping["conversation"]
        return [{"do": "listen", "mode": "conversation",
                 "end_of_speech_ms": conv["end_of_speech_ms"], "idle_end_s": conv["idle_end_s"]}]


_SHARED: Petasos | None = None
_SHARED_LOCK = threading.Lock()


def _mapping_path() -> Path:
    """One mapping per backend, in the root Hermes home, the same place Talaria keeps its devices."""
    try:
        from hermes_constants import get_default_hermes_root
        home = get_default_hermes_root()
    except ImportError:  # older Hermes
        from hermes_constants import get_hermes_home
        home = get_hermes_home()
    return Path(home) / "petasos" / "mapping.json"


def shared() -> Petasos:
    global _SHARED
    with _SHARED_LOCK:
        if _SHARED is None:
            _SHARED = Petasos(MappingFile(_mapping_path()))
        return _SHARED
