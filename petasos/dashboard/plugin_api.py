"""Petasos routes, mounted at /api/plugins/petasos/.

The dashboard's own login decides who may reach these routes; the Car Thing signs in once and
sends its session token like any other dashboard client.
"""
from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

from fastapi import APIRouter, HTTPException

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
