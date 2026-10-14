"""Petasos routes, mounted at /api/plugins/petasos/.

The dashboard's own login decides who may reach these routes; the Car Thing signs in once and
sends its session token like any other dashboard client.
"""
from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

import hashlib

from fastapi import APIRouter, HTTPException
from fastapi.responses import FileResponse

router = APIRouter()
_CORE = "hermes_petasos_core"


def _core():
    module = sys.modules.get(_CORE)
    if module is None:
        spec = importlib.util.spec_from_file_location(_CORE, Path(__file__).parent.parent / "core.py")
        module = importlib.util.module_from_spec(spec)
        sys.modules[_CORE] = module
        spec.loader.exec_module(module)
    return module


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
