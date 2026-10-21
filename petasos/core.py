"""Petasos core: turns the Car Thing's button and knob events into commands for the device.

The Car Thing reports only what was pressed (``{"input": "front", "action": "press"}``). Which
action a control performs lives here, in a mapping file on the server, so the layout can change
without touching the device. The device gets back a short list of commands to carry out, such as
``{"do": "listen", "mode": "hold"}`` or ``{"do": "stop_speaking"}``.

Inputs (named in tools/list_inputs.sh and the README):
  preset-1 .. preset-4   the four top buttons, left to right
  preset-5 .. preset-8   the same buttons while the second set of soft keys shows (the device
                         reports preset-1 .. preset-4; the plugin knows which set it shows)
  mute                   the top-right button
  front                  the front button
  knob                   the knob: "press" when pushed, "turn" with a delta of +1 or -1 per click
  device                 the client itself: "hello" when it starts (resets its state), or
                         "conversation_ended" when conversation mode ended on its own, or
                         "wake" when it heard its wake word ("Hey Athena")

Gestures in the mapping: "press" fires on press; "tap", "hold" and "long_hold" are told apart
on release by how long the control was held: under ``hold_ms`` is a tap, and a hold or long
hold step can give its own ``ms`` to wait for (released between the two, nothing happens).
"double_tap" and "triple_tap" are taps in quick succession, which the device counts and
reports with the release (``taps``); "turn" is for the knob.
"""
from __future__ import annotations

import copy
import datetime
import json
import os
import threading
import time
from pathlib import Path
from typing import Callable

# The car-* inputs are a car's steering wheel buttons, reaching the Car Thing over Bluetooth while
# it plays to the car: the media buttons (AVRCP), and the call button asking for voice
# recognition, as a car's hands-free does of a phone (HFP).
INPUTS = ("preset-1", "preset-2", "preset-3", "preset-4", "preset-5", "preset-6", "preset-7", "preset-8",
          "mute", "front", "knob", "car-play", "car-next", "car-previous", "car-call")
GESTURES = ("press", "hold", "long_hold", "tap", "double_tap", "triple_tap", "turn")
# The pages a soft key can open: page_petasos is the home screen (no longer offered: the front
# button's tap goes home from a music page; a key still holding it does nothing), the others a
# music service's.
PAGES = ("petasos", "soundcloud", "navidrome", "spotify", "apple", "ipod")
ACTIONS = ("push_to_talk", "conversation", "stop_speaking", "volume", "mic_toggle", "prompt",
           "music_toggle", "music_next", "music_previous", "music_stop", "music_page", "soft_keys",
           *("page_" + p for p in PAGES), "none")
LONG_HOLD_MS = 3000  # a long hold with no ms of its own

DEFAULT_MAPPING: dict = {
    "agent_name": "Hermes",
    "chats": "separate",  # each device its own chat with the agent, or "shared": one for all
    "hold_ms": 350,
    "volume_step": 5,
    "conversation": {"end_of_speech_ms": 1000, "idle_end_s": 30},
    "controls": {
        "front": {"hold": {"action": "push_to_talk"}, "tap": {"action": "conversation"}},
        # Tap: stop the agent speaking, or pause and resume the music when she isn't. Two taps
        # skip ahead, three go back. Hold a second: the mic off or on.
        "knob": {"tap": {"action": "stop_speaking"}, "double_tap": {"action": "music_next"},
                 "triple_tap": {"action": "music_previous"}, "hold": {"action": "mic_toggle", "ms": 1000},
                 "turn": {"action": "volume"}},
        # A press shows the other set of soft keys (holding it two seconds opens the settings,
        # on the device).
        "mute": {"press": {"action": "soft_keys"}},
        "preset-1": {"press": {"action": "prompt", "text": "What's next on my calendar?", "label": "Calendar"}},
        "preset-2": {"press": {"action": "prompt", "text": "Read me my last message.", "label": "Last message"}},
        "preset-3": {"press": {"action": "page_soundcloud"}},
        "preset-4": {"press": {"action": "page_spotify"}},
        "preset-5": {"press": {"action": "page_apple"}},
        "preset-6": {"press": {"action": "page_navidrome"}},
        "preset-7": {"press": {"action": "none"}},
        "preset-8": {"press": {"action": "none"}},
        "car-play": {"press": {"action": "music_toggle"}},
        "car-next": {"press": {"action": "music_next"}},
        "car-previous": {"press": {"action": "music_previous"}},
        "car-call": {"press": {"action": "conversation"}},
    },
}


# What the car's buttons did before they worked the music (play started a conversation); a
# mapping still holding these unchanged gets today's defaults.
_OLD_CAR_DEFAULTS = {"car-play": {"press": {"action": "conversation"}},
                     "car-next": {"press": {"action": "stop_speaking"}},
                     "car-previous": {"press": {"action": "none"}}}

# The knob as earlier versions set it up; a mapping still holding one unchanged gets today's.
_OLD_KNOBS = ({"press": {"action": "stop_speaking"}, "turn": {"action": "volume"}},
              {"tap": {"action": "stop_speaking"}, "hold": {"action": "music_stop"}, "turn": {"action": "volume"}},
              {"tap": {"action": "stop_speaking"}, "double_tap": {"action": "music_next"},
               "triple_tap": {"action": "music_previous"}, "hold": {"action": "music_stop", "ms": 2000},
               "long_hold": {"action": "music_page", "ms": 3000}, "turn": {"action": "volume"}})

# Controls as earlier versions set them up, unchanged: they get today's defaults. The mute button
# turned the mic off before the knob's hold did; soft keys 3 and 4 did nothing before the music
# pages; the second set didn't exist.
_OLD_DEFAULTS = {"mute": ({"press": {"action": "mic_toggle"}},),
                 "preset-3": ({"press": {"action": "none"}},), "preset-4": ({"press": {"action": "none"}},)}

# The second set as it first came, unchanged: it gets today's, without the Petasos page.
_OLD_SECOND_SET = ("page_petasos", "page_apple", "page_navidrome", "none")

# The screen's name for an action with no label of its own, where its words alone read badly.
_ACTION_LABELS = {"music_toggle": "Play / pause", "music_next": "Next song", "music_previous": "Previous song",
                  "music_stop": "Stop the music", "music_page": "Music page",
                  "mic_toggle": "Mic on / off", "soft_keys": "Other soft keys",
                  "page_petasos": "Petasos", "page_soundcloud": "SoundCloud", "page_navidrome": "Navidrome",
                  "page_spotify": "Spotify", "page_apple": "Apple Music", "page_ipod": "iPod"}

LABEL_MAX = 32
WAKE_IDLE_S = 8  # how long a conversation the wake word started waits for someone to speak
AGENT_NAME_MAX = 24


def local_zone() -> dict:
    """Hermes's time zone, for the screensaver's clock (a Car Thing keeps UTC): its name when it
    can be found, and its offset from UTC now, in minutes."""
    zone = os.environ.get("TZ", "").lstrip(":")
    if "/" not in zone:
        zone = ""
        try:
            zone = Path("/etc/timezone").read_text(encoding="utf-8").strip()
        except OSError:
            try:
                link = os.readlink("/etc/localtime")
                zone = link.split("zoneinfo/", 1)[1] if "zoneinfo/" in link else ""
            except OSError:
                pass
    offset = datetime.datetime.now().astimezone().utcoffset()
    return {"zone": zone, "utc_offset": int(offset.total_seconds() // 60) if offset is not None else 0}


def agent_name(mapping: dict) -> str:
    """Who the Car Thing talks to, as shown on its screen: the mapping's agent_name."""
    name = mapping.get("agent_name")
    return name.strip() if isinstance(name, str) and name.strip() else DEFAULT_MAPPING["agent_name"]


PRESETS = ("preset-1", "preset-2", "preset-3", "preset-4", "preset-5", "preset-6", "preset-7", "preset-8")
DEVICE_ID_MAX = 64


def _key_set(keys: int) -> tuple:
    """The soft keys shown as set 1 or 2."""
    return PRESETS[4:] if keys == 2 else PRESETS[:4]
DEVICE_NAME_MAX = 48


def controls_for(mapping: dict, device: str | None) -> dict:
    """The controls a device uses: the mapping's, with the preset buttons the device has its own
    of (``devices.<id>.controls``, set from its settings) in place of the shared ones."""
    controls = dict(mapping.get("controls") or {})
    for name, old in _OLD_DEFAULTS.items():
        if controls.get(name) in old:
            controls[name] = DEFAULT_MAPPING["controls"][name]
    for name in PRESETS[4:]:  # a mapping from before the second set
        controls.setdefault(name, DEFAULT_MAPPING["controls"][name])
    if controls.get("knob") in _OLD_KNOBS:
        controls["knob"] = DEFAULT_MAPPING["controls"]["knob"]
    own = ((mapping.get("devices") or {}).get(device or "") or {}).get("controls")
    if isinstance(own, dict):
        controls.update({name: own[name] for name in PRESETS if name in own})
    second = PRESETS[4:]
    if tuple(_press_action(controls.get(n)) for n in second) == _OLD_SECOND_SET:
        controls.update({n: DEFAULT_MAPPING["controls"][n] for n in second})
    for name in PRESETS:
        if _press_action(controls.get(name)) == "page_petasos":
            controls[name] = {"press": {"action": "none"}}
    return controls


def knob_volume(mapping: dict, device: str | None) -> int:
    """How far one click of the knob turns the volume, when turning it is the volume: the device
    then does it itself, without waiting on the plugin (a slow link made the knob lag). 0 when
    the knob's turn does something else."""
    turn = (controls_for(mapping, device).get("knob") or {}).get("turn")
    return mapping["volume_step"] if isinstance(turn, dict) and turn.get("action") == "volume" else 0


def _press_action(gestures) -> str | None:
    """The action a control's press does, if it has one."""
    press = gestures.get("press") if isinstance(gestures, dict) else None
    return press.get("action") if isinstance(press, dict) else None


def _step_of(gestures) -> dict | None:
    if not isinstance(gestures, dict):
        return None
    return next((s for s in gestures.values() if isinstance(s, dict) and s.get("action") not in (None, "none")), None)


def preset_labels(mapping: dict, device: str | None = None, names: tuple = PRESETS) -> list[str]:
    """What the screen shows under each preset button: its label, else the start of its prompt,
    else "" for a button that does nothing."""
    labels = []
    controls = controls_for(mapping, device)
    for name in names:
        step = _step_of(controls.get(name))
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


def preset_pages(mapping: dict, device: str | None, names: tuple) -> list[str]:
    """Which page each of these soft keys opens, or "", so the screen can light the open one."""
    controls = controls_for(mapping, device)
    out = []
    for name in names:
        action = (_step_of(controls.get(name)) or {}).get("action", "")
        out.append(action.removeprefix("page_") if action.startswith("page_") else "")
    return out


def has_second_set(mapping: dict, device: str | None) -> bool:
    return any(preset_labels(mapping, device, PRESETS[4:]))


def _hold_ms(controls: dict, gesture: str, mapping: dict) -> int:
    step = controls.get(gesture) or {}
    ms = step.get("ms")
    if isinstance(ms, int) and not isinstance(ms, bool):
        return ms
    return LONG_HOLD_MS if gesture == "long_hold" else mapping["hold_ms"]


def _gesture(controls: dict, mapping: dict, held_ms: float, taps: int) -> str:
    """Which gesture a release was: how long it was held, and how many quick taps it ended."""
    if "long_hold" in controls and held_ms >= _hold_ms(controls, "long_hold", mapping):
        return "long_hold"
    if held_ms >= mapping["hold_ms"]:
        # Let go before a hold's own time is up: nothing ("" has no step).
        return "hold" if held_ms >= _hold_ms(controls, "hold", mapping) else ""
    if taps >= 3 and "triple_tap" in controls:
        return "triple_tap"
    if taps >= 2 and "double_tap" in controls:
        return "double_tap"
    return "tap"


def _step_label(step: dict) -> str:
    action = step.get("action", "none")
    return (step.get("label") or "").strip() or _ACTION_LABELS.get(action) or action.replace("_", " ").capitalize()


def gesture_hints(mapping: dict, device: str | None = None) -> dict:
    """What the device needs to know to report the gestures a control has: how many quick taps
    to count, and the holds to show on the screen while the control is held (what letting go
    would do, and from when)."""
    out = {}
    controls = controls_for(mapping, device)
    for name, gestures in controls.items():
        if not isinstance(gestures, dict):
            continue
        taps = 3 if "triple_tap" in gestures else 2 if "double_tap" in gestures else 1
        holds = [{"ms": _hold_ms(gestures, g, mapping), "label": _step_label(gestures[g])}
                 for g in ("hold", "long_hold") if g in gestures and (gestures[g].get("ms") or g == "long_hold")
                 and gestures[g].get("action") not in ("none", "push_to_talk")]
        if taps > 1 or holds:
            out[name] = {"taps": taps, "tap_ms": mapping["hold_ms"], "holds": holds}
    return out


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
            if "ms" in step and not (gesture in ("hold", "long_hold") and isinstance(step["ms"], int)
                                     and not isinstance(step["ms"], bool) and 100 <= step["ms"] <= 10000):
                problems.append(f"{where}: ms is how long to hold, 100 to 10000, on a hold or long_hold.")
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
        self.keys = 1  # which set of soft keys the screen shows


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
                if action == "wake" and state.listening is None:
                    # Like the conversation button, but a wake that hears nothing more (a false
                    # one, say) gives up after a few seconds rather than half a minute.
                    out = self._start_conversation(state, mapping)
                    for c in out:
                        if c.get("do") == "listen":
                            c["idle_end_s"] = min(c["idle_end_s"], WAKE_IDLE_S)
                    return out
                return []
            if name in PRESETS[:4] and state.keys == 2:
                name = PRESETS[PRESETS.index(name) + 4]
            controls = controls_for(mapping, device_id).get(name)
            if name.startswith("car-") and (controls is None or controls == _OLD_CAR_DEFAULTS.get(name)):
                # A mapping written before the car buttons existed, or before they played
                # music, gets their defaults.
                controls = DEFAULT_MAPPING["controls"][name]
            controls = controls or {}
            if action == "press":
                out = self._press(state, mapping, name, controls, now_ms)
            elif action == "release":
                taps = event.get("taps")
                taps = taps if isinstance(taps, int) and not isinstance(taps, bool) and taps > 0 else 1
                out = self._release(state, mapping, name, controls, now_ms, taps)
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
    def _screen_command(mapping, device_id, keys: int = 1) -> dict:
        """The soft keys' labels (of the set shown), the pages they open, the agent's name and
        the gesture hints, for the device's screen."""
        shown = _key_set(keys)
        return {"do": "screen", "presets": preset_labels(mapping, device_id, shown), "agent": agent_name(mapping),
                "gestures": gesture_hints(mapping, device_id), "pages": preset_pages(mapping, device_id, shown),
                "keys": keys, "key_sets": 2 if has_second_set(mapping, device_id) else 1,
                "hold_ms": mapping["hold_ms"], "knob_volume": knob_volume(mapping, device_id),
                **local_zone()}

    def _screen(self, state, mapping, device_id) -> list[dict]:
        """The screen command: on hello, then whenever it changes."""
        if state.keys == 2 and not has_second_set(mapping, device_id):
            state.keys = 1
        screen = self._screen_command(mapping, device_id, state.keys)
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
        shared = {name: controls_for(mapping, None).get(name) or {"press": {"action": "none"}} for name in PRESETS}
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
            screen = self._screen(state, mapping, device_id) if state else [self._screen_command(mapping, device_id)]
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

    def _release(self, state, mapping, name, controls, now_ms, taps: int = 1) -> list[dict]:
        pressed = state.down.pop(name, None)
        if pressed is None:
            return []
        step = controls.get(_gesture(controls, mapping, now_ms - pressed, taps))
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
        if action == "music_page":
            return [{"do": "page", "page": "music"}]
        if action.startswith("page_"):
            return [{"do": "page", "page": action.removeprefix("page_")}]
        if action == "soft_keys":
            state.keys = 2 if state.keys == 1 else 1  # _screen sends the other set (or keeps set 1)
            return []
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
