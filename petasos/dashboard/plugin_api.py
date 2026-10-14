"""Petasos routes, mounted at /api/plugins/petasos/.

The dashboard's own login decides who may reach these routes; the Car Thing signs in once and
sends its session token like any other dashboard client.
"""
from __future__ import annotations

import asyncio
import hashlib
import importlib.util
import sys
from pathlib import Path

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import FileResponse, Response

router = APIRouter()


def _load(name: str, filename: str):
    """A module beside this folder, loaded once per process under a fixed name."""
    module = sys.modules.get(name)
    if module is None:
        spec = importlib.util.spec_from_file_location(name, Path(__file__).parent.parent / filename)
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        spec.loader.exec_module(module)
    return module


def _core():
    return _load("hermes_petasos_core", "core.py")


def _voice():
    return _load("hermes_petasos_voice", "voice.py")


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
    voice = _voice()
    try:
        return await voice.turn(text, voice.device_name(device))
    except voice.VoiceError as exc:
        raise HTTPException(status_code=502, detail=str(exc)) from None


@router.get("/voice/audio/{key}")
def voice_audio(key: str) -> Response:
    audio = _voice().shared()[1].get(key)
    if audio is None:
        raise HTTPException(status_code=404, detail="that reply is gone; ask again")
    return Response(audio, media_type="audio/wav")


@router.get("/status")
def status() -> dict:
    return _core().shared().status()


# The Car Thing program, built into client/dist/ beside this plugin in the hermes-petasos checkout.
_CLIENT = Path(__file__).resolve().parents[2] / "client"
_ARCHES = {"arm64", "amd64"}
_digests: dict[tuple, str] = {}


def _client_binary(arch: str) -> Path:
    if arch not in _ARCHES:
        raise HTTPException(status_code=400, detail=f"no build for {arch!r}; builds are {sorted(_ARCHES)}")
    path = _CLIENT / "dist" / f"petasos-linux-{arch}"
    if not path.is_file():
        raise HTTPException(status_code=404, detail=f"no device program at {path}; is the plugin a checkout of hermes-petasos?")
    return path


@router.get("/client")
def client_info(arch: str = "arm64") -> dict:
    """The current device program's version and checksum, for the Car Thing to update itself."""
    path = _client_binary(arch)
    stat = path.stat()
    key = (str(path), stat.st_mtime_ns, stat.st_size)
    if key not in _digests:
        _digests[key] = hashlib.sha256(path.read_bytes()).hexdigest()
    version = (_CLIENT / "VERSION").read_text(encoding="utf-8").strip()
    return {"version": version, "sha256": _digests[key], "size": stat.st_size}


@router.get("/client/download")
def client_download(arch: str = "arm64") -> FileResponse:
    return FileResponse(_client_binary(arch), media_type="application/octet-stream",
                        filename=f"petasos-linux-{arch}")
