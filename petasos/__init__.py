"""Petasos: the Car Thing as a voice remote for a Hermes agent.

The server half. The device reports raw button and knob events to the routes in ``dashboard/``;
``core.py`` turns them into commands using a mapping file the user can edit. This file only adds
the ``/petasos`` command, which shows the current layout and where to change it.
"""
from __future__ import annotations

import importlib.util
import sys
import threading
from pathlib import Path

_CORE = "hermes_petasos_core"

_LABELS = {"preset-1": "Preset 1", "preset-2": "Preset 2", "preset-3": "Preset 3",
           "preset-4": "Preset 4", "mute": "Top-right button", "front": "Front button", "knob": "Knob",
           "car-play": "Car play/pause", "car-next": "Car next track", "car-previous": "Car previous track"}


_LOADING = threading.Lock()


def _core():
    """core.py, loaded once per process under a fixed name shared with the dashboard routes, and
    published only once fully loaded (see dashboard/plugin_api.py)."""
    module = sys.modules.get(_CORE)
    if module is not None:
        return module
    with _LOADING:
        module = sys.modules.get(_CORE)
        if module is None:
            spec = importlib.util.spec_from_file_location(_CORE, Path(__file__).with_name("core.py"))
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            module = sys.modules.setdefault(_CORE, module)
    return module


def _describe(step: dict, agent: str) -> str:
    action = step.get("action", "none")
    if action == "prompt":
        return f'ask "{step.get("text", "")}"'
    return {"push_to_talk": "push-to-talk", "conversation": "start or end conversation mode",
            "stop_speaking": f"stop {agent} speaking", "volume": "volume", "mic_toggle": "mic on/off",
            "music_toggle": "play or pause the music", "music_next": "next song",
            "music_previous": "previous song", "none": "nothing"}.get(action, action)


def _slash(_args: str = "") -> str:
    core = _core()
    petasos = core.shared()
    mapping = petasos.mapping.current()
    agent = core.agent_name(mapping)
    lines = [f"Car Thing layout (talking to {agent}):"]
    for name in core.INPUTS:
        gestures = mapping["controls"].get(name)
        if gestures is None and name.startswith("car-"):
            gestures = core.DEFAULT_MAPPING["controls"][name]
        gestures = gestures or {}
        parts = [f"{g} = {_describe(s, agent)}" for g, s in gestures.items()] or ["nothing"]
        lines.append(f"- {_LABELS[name]}: {'; '.join(parts)}")
    for device, own in (mapping.get("devices") or {}).items():
        for name, gestures in (own.get("controls") or {}).items():
            parts = [f"{g} = {_describe(s, agent)}" for g, s in gestures.items()] or ["nothing"]
            lines.append(f"- {_LABELS[name]} on {own.get('name') or device}: {'; '.join(parts)}")
    lines.append(f"Edit {petasos.mapping.path} to change it; edits apply on the next button press.")
    if petasos.mapping.problems:
        lines.append("That file has problems, so the last good layout is still in use:")
        lines += [f"- {p}" for p in petasos.mapping.problems]
    devices = petasos.status()["devices"]
    for device, s in devices.items():
        mic = "off" if s["muted"] else "on"
        lines.append(f"{device}: last seen {s['seen_seconds_ago']}s ago, mic {mic}"
                     + (f", listening ({s['listening']})" if s["listening"] else ""))
    return "\n".join(lines)


def register(ctx) -> None:
    ctx.register_command("petasos", handler=_slash,
                         description="Show the Car Thing's button layout and where to change it.")
