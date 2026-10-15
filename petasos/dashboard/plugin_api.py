"""Petasos routes, mounted at /api/plugins/petasos/.

The dashboard's own login decides who may reach these routes; the Car Thing signs in once and
sends its session token like any other dashboard client.
"""
from __future__ import annotations

import asyncio
import hashlib
import importlib.util
import sys
import threading
from pathlib import Path

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import FileResponse, Response, StreamingResponse

router = APIRouter()


_LOADING = threading.Lock()


def _load(name: str, filename: str):
    """A module beside this folder, loaded once per process under a fixed name. Routes run on
    several threads at once, so it is only published once it has fully loaded: a request that
    found it half-loaded failed with an AttributeError right after Hermes started."""
    module = sys.modules.get(name)
    if module is not None:
        return module
    with _LOADING:
        module = sys.modules.get(name)
        if module is None:
            spec = importlib.util.spec_from_file_location(name, Path(__file__).parent.parent / filename)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            # The plugin's /petasos command may have loaded it meanwhile; keep whichever came first.
            module = sys.modules.setdefault(name, module)
    return module


def _core():
    return _load("hermes_petasos_core", "core.py")


def _voice():
    return _load("hermes_petasos_voice", "voice.py")


def _music():
    return _load("hermes_petasos_music", "music.py")


@router.post("/input")
def input_event(body: dict) -> dict:
    """One button or knob event in; the commands for the device to carry out come back."""
    core = _core()
    try:
        return {"commands": core.shared().handle(body)}
    except core.MappingError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from None


@router.get("/mapping")
def get_mapping() -> dict:
    petasos = _core().shared()
    return {"mapping": petasos.mapping.current(), "problems": list(petasos.mapping.problems),
            "path": str(petasos.mapping.path)}


@router.put("/mapping")
def put_mapping(body: dict) -> dict:
    core = _core()
    try:
        return {"mapping": core.shared().mapping.replace(body)}
    except core.MappingError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from None


@router.get("/devices/{device}/presets")
def get_device_presets(device: str) -> dict:
    """A device's preset buttons, for its own settings: its own, or the shared ones it uses."""
    return _core().shared().presets(device[:64])


@router.put("/devices/{device}/presets")
def put_device_presets(device: str, body: dict) -> dict:
    """Gives a device its own preset buttons ({"presets": {...}, "name": ...}), or puts it back
    on the shared ones ({"presets": null}). The screen command for its labels comes back too."""
    core = _core()
    try:
        presets, screen = core.shared().set_presets(device, body.get("presets"), body.get("name"))
    except core.MappingError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from None
    return {**presets, "commands": screen}


# --- voice --------------------------------------------------------------------------------------

MAX_SPEECH_BYTES = 10 * 1024 * 1024  # about five minutes of 16 kHz mono WAV


@router.post("/voice")
async def voice(request: Request, device: str = "car-thing") -> dict:
    """A recording in (WAV body); what was heard, Athena's reply and an id for its audio out."""
    voice = _voice()
    audio = await request.body()
    if not audio:
        raise HTTPException(status_code=400, detail="empty recording")
    if len(audio) > MAX_SPEECH_BYTES:
        raise HTTPException(status_code=413, detail="recording too long")
    try:
        heard = await asyncio.get_running_loop().run_in_executor(None, voice.transcribe, audio)
    except Exception as exc:
        raise HTTPException(status_code=502, detail=f"couldn't transcribe: {exc}"[:300]) from None
    if not heard:
        return {"heard": "", "reply": "", "audio": None}
    return {"heard": heard, **await _turn(heard, device)}


@router.post("/ask")
async def ask(body: dict, device: str = "car-thing") -> dict:
    """Typed words (a preset) in; Athena's reply and an id for its audio out."""
    text = body.get("text") if isinstance(body, dict) else None
    if not isinstance(text, str) or not text.strip():
        raise HTTPException(status_code=400, detail="text required")
    return {"heard": text.strip(), **await _turn(text.strip(), device)}


async def _turn(text: str, device: str) -> dict:
    """One turn with the agent. Commands its tools left for the device (music it put on) come
    back with the reply, so they happen as soon as it has been said."""
    voice, music = _voice(), _music()
    device = voice.device_name(device)
    music.note_turn(device)
    try:
        core = _core()
        mapping = core.shared().mapping.current()
        answer = await voice.turn(text, device, core.agent_name(mapping),
                                  shared_chat=mapping.get("chats") == "shared")
    except voice.VoiceError as exc:
        raise HTTPException(status_code=502, detail=str(exc)) from None
    return {**answer, "commands": music.take(device)}


@router.get("/devices/{device}/commands")
def device_commands(device: str) -> dict:
    """Commands left for a device since it last asked: music the agent put on from another chat."""
    return {"commands": _music().take(_voice().device_name(device))}


@router.get("/music/settings")
def music_settings() -> dict:
    return _music().settings()


@router.put("/music/settings")
def put_music_settings(body: dict) -> dict:
    music = _music()
    try:
        return music.save_settings(body)
    except music.MusicError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from None


@router.post("/music/check")
def music_check() -> dict:
    """Tries each source; a folder's first check reads its files' tags, so it can take a while."""
    return _music().check()


@router.post("/music/soundcloud/authorize")
def soundcloud_authorize(body: dict) -> dict:
    """Starts signing in to your SoundCloud account: the page to send the browser to. The
    redirect is the Petasos app's own, as registered with the SoundCloud app."""
    music = _music()
    redirect = body.get("redirect_uri") if isinstance(body, dict) else None
    if not isinstance(redirect, str) or not redirect.startswith(("http://127.0.0.1:", "http://localhost:", "https://")):
        raise HTTPException(status_code=400, detail="redirect_uri required")
    try:
        return {"url": music.soundcloud().authorize_url(redirect)}
    except music.MusicError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from None


@router.post("/music/soundcloud/connect")
def soundcloud_connect(body: dict) -> dict:
    """Finishes signing in, with the code SoundCloud sent back."""
    music = _music()
    code, state = (body.get("code"), body.get("state")) if isinstance(body, dict) else (None, None)
    if not (isinstance(code, str) and isinstance(state, str)):
        raise HTTPException(status_code=400, detail="code and state required")
    try:
        return {"account": music.soundcloud().connect(code, state)}
    except music.MusicError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from None


@router.post("/music/soundcloud/disconnect")
def soundcloud_disconnect() -> dict:
    music = _music()
    try:
        music.soundcloud().disconnect()
    except music.MusicError:
        pass
    return {"account": ""}


@router.get("/music/stream")
def music_stream(source: str, id: str) -> StreamingResponse:
    """One track of the home library as MP3, from Navidrome or a music folder on this machine."""
    music = _music()
    try:
        pieces = music.stream(source, id)
    except (music.MusicError, OSError) as exc:
        raise HTTPException(status_code=404, detail=str(exc)[:300]) from None
    return StreamingResponse(pieces, media_type="audio/mpeg")


@router.get("/voice/audio/{key}")
def voice_audio(key: str) -> Response:
    audio = _voice().shared()[1].get(key)
    if audio is None:
        raise HTTPException(status_code=404, detail="that reply is gone; ask again")
    return Response(audio, media_type="audio/wav")


@router.get("/status")
def status() -> dict:
    return _core().shared().status()


# The device program, built into client/dist/ beside this plugin in the hermes-petasos checkout:
# Linux for the Car Thing, macOS and Windows for the Petasos app, which update themselves from here.
_CLIENT = Path(__file__).resolve().parents[2] / "client"
_ARCHES = {"arm64", "amd64"}
_SYSTEMS = {"linux", "darwin", "windows"}
_digests: dict[tuple, str] = {}


def _client_name(os: str, arch: str) -> str:
    if os not in _SYSTEMS:
        raise HTTPException(status_code=400, detail=f"no build for {os!r}; systems are {sorted(_SYSTEMS)}")
    if arch not in _ARCHES:
        raise HTTPException(status_code=400, detail=f"no build for {arch!r}; builds are {sorted(_ARCHES)}")
    return f"petasos-{os}-{arch}" + (".exe" if os == "windows" else "")


def _client_binary(os: str, arch: str) -> Path:
    path = _CLIENT / "dist" / _client_name(os, arch)
    if not path.is_file():
        raise HTTPException(status_code=404, detail=f"no device program at {path}; is the plugin a checkout of hermes-petasos?")
    return path


@router.get("/client")
def client_info(arch: str = "arm64", os: str = "linux") -> dict:
    """The current device program's version and checksum, for the Car Thing and the app to update themselves."""
    path = _client_binary(os, arch)
    stat = path.stat()
    key = (str(path), stat.st_mtime_ns, stat.st_size)
    if key not in _digests:
        _digests[key] = hashlib.sha256(path.read_bytes()).hexdigest()
    version = (_CLIENT / "VERSION").read_text(encoding="utf-8").strip()
    return {"version": version, "os": os, "sha256": _digests[key], "size": stat.st_size, "plugin": _plugin_version()}


def _plugin_version() -> str:
    """This plugin's own version, from plugin.yaml, for the devices to show."""
    try:
        for line in (Path(__file__).parent.parent / "plugin.yaml").read_text(encoding="utf-8").splitlines():
            if line.startswith("version:"):
                return line.split(":", 1)[1].strip().strip('"\'')
    except OSError:
        pass
    return ""


@router.get("/client/download")
def client_download(arch: str = "arm64", os: str = "linux") -> FileResponse:
    return FileResponse(_client_binary(os, arch), media_type="application/octet-stream",
                        filename=_client_name(os, arch))
