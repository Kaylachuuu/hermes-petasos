"""Petasos voice: speech from the Car Thing in, Athena's spoken reply out.

The Car Thing only moves audio. Here the recording is transcribed with Hermes' own speech-to-text
(``stt:`` in config.yaml, e.g. hermes-whisper through the OpenAI-compatible provider), sent to
Athena as an ordinary chat turn, and her reply is turned into speech with Hermes' text-to-speech
(``tts:``, e.g. hermes-kokoro). The turn goes through the dashboard's own chat connection
(``/api/ws``) over loopback, so the conversation is a normal session that shows in the WebUI.

Each device keeps one conversation, resumed on every turn; its stored session id is kept in
``<hermes root>/petasos/sessions.json``.
"""
from __future__ import annotations

import asyncio
import contextlib
import io
import json
import tempfile
import threading
import time
import uuid
import wave
from collections import OrderedDict
from pathlib import Path

TURN_TIMEOUT_S = 180
SESSION_TITLE = "Car Thing"


class VoiceError(RuntimeError):
    pass


# --- speech to text and back -------------------------------------------------------------------

def _profile_scope():
    try:
        from hermes_cli.web_server_profiles import _config_profile_scope
    except ImportError:
        return contextlib.nullcontext()
    return _config_profile_scope(None)


def transcribe(audio: bytes, suffix: str = ".wav") -> str:
    """The words in a recording, or "" when there were none. Hermes filters Whisper's usual
    hallucinations on silence ("Thank you for watching" and the like)."""
    from tools.voice_mode import transcribe_recording
    with tempfile.TemporaryDirectory(prefix="petasos-") as tmp:
        path = Path(tmp) / f"speech{suffix}"
        path.write_bytes(audio)
        with _profile_scope():
            text = transcribe_recording(str(path))
    if isinstance(text, dict):  # some versions hand back the provider's result
        if not text.get("success", True):
            raise VoiceError(text.get("error") or "transcription failed")
        text = text.get("transcript") or ""
    return (text or "").strip()


def speak(text: str) -> bytes:
    """Athena's reply as one WAV file. Long replies come back from Hermes in chunks, which are
    joined here so the device plays a single file."""
    from tools.tts_tool import text_to_speech_tool
    with tempfile.TemporaryDirectory(prefix="petasos-") as tmp:
        with _profile_scope():
            result = json.loads(text_to_speech_tool(text, output_path=str(Path(tmp) / "reply.wav")))
        if not result.get("success"):
            raise VoiceError(result.get("error") or "speech synthesis failed")
        paths = result.get("file_paths") or [result.get("file_path")]
        return join_wavs([_as_wav(Path(p)) for p in paths if p])


def _as_wav(path: Path) -> bytes:
    """The file as WAV, which the Car Thing plays without decoders. Some speech providers ignore
    the requested format and write MP3 or Opus; ffmpeg (which Hermes' voice features use too)
    converts those."""
    data = path.read_bytes()
    if data[:4] == b"RIFF":
        return data
    import shutil
    import subprocess
    ffmpeg = shutil.which("ffmpeg")
    if not ffmpeg:
        raise VoiceError("speech came back as compressed audio and ffmpeg isn't installed to convert it")
    out = path.with_suffix(".converted.wav")  # a file, not a pipe, so the WAV header has its sizes
    done = subprocess.run([ffmpeg, "-v", "error", "-y", "-i", str(path), "-ac", "1", str(out)],
                          capture_output=True, timeout=60)
    if done.returncode != 0 or not out.is_file():
        raise VoiceError(f"couldn't convert the speech to WAV: {done.stderr.decode(errors='replace')[:200]}")
    return out.read_bytes()


def join_wavs(parts: list[bytes]) -> bytes:
    if len(parts) == 1:
        return parts[0]
    out = io.BytesIO()
    writer = None
    for part in parts:
        with wave.open(io.BytesIO(part)) as r:
            if writer is None:
                writer = wave.open(out, "wb")
                writer.setparams(r.getparams())
            writer.writeframes(r.readframes(r.getnframes()))
    if writer is None:
        raise VoiceError("no audio came back")
    writer.close()
    return out.getvalue()


# --- one chat turn with Athena -----------------------------------------------------------------

class Sessions:
    """Which stored Hermes session each device continues."""

    def __init__(self, path: Path) -> None:
        self.path = path
        self._lock = threading.Lock()

    def get(self, device: str) -> str | None:
        with self._lock:
            return self._read().get(device)

    def set(self, device: str, session_id: str | None) -> None:
        with self._lock:
            data = self._read()
            if session_id:
                data[device] = session_id
            else:
                data.pop(device, None)
            self.path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self.path.with_suffix(".tmp")
            tmp.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
            tmp.replace(self.path)

    def _read(self) -> dict:
        try:
            data = json.loads(self.path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return {}
        return data if isinstance(data, dict) else {}


def _gateway_url() -> str:
    from hermes_cli.web_server_chat import _build_gateway_ws_url
    url = _build_gateway_ws_url()
    if not url:
        raise VoiceError("the dashboard's chat connection isn't available")
    return url


class _Rpc:
    """JSON-RPC 2.0 over the dashboard's chat WebSocket, one object per frame."""

    def __init__(self, ws) -> None:
        self.ws = ws
        self.next_id = 0
        self.pending: dict[int, asyncio.Future] = {}
        self.events: asyncio.Queue = asyncio.Queue()
        self.reader = asyncio.create_task(self._read())

    async def _read(self) -> None:
        try:
            async for raw in self.ws:
                frame = json.loads(raw)
                if "method" not in frame and frame.get("id") in self.pending:
                    fut = self.pending.pop(frame["id"])
                    if "error" in frame:
                        fut.set_exception(VoiceError(str((frame["error"] or {}).get("message") or frame["error"])))
                    else:
                        fut.set_result(frame.get("result") or {})
                elif frame.get("method") == "event":
                    await self.events.put(frame.get("params") or {})
                elif "method" in frame and "id" in frame:
                    # Approvals and clarifying questions need someone at a screen. Decline them, so
                    # the turn carries on and Athena says what she couldn't do; the session is in
                    # the WebUI for anything that needs a closer look.
                    await self.ws.send(json.dumps({"jsonrpc": "2.0", "id": frame["id"], "error": {
                        "code": -32000, "message": "The Car Thing can't answer this; ask from the dashboard."}}))
        finally:
            for fut in self.pending.values():
                if not fut.done():
                    fut.set_exception(VoiceError("the chat connection closed"))
            await self.events.put({"type": "closed"})

    async def call(self, method: str, params: dict) -> dict:
        self.next_id += 1
        rid = self.next_id
        fut = asyncio.get_running_loop().create_future()
        self.pending[rid] = fut
        await self.ws.send(json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params}))
        return await asyncio.wait_for(fut, 60)

    async def event(self, deadline: float, session_id: str | None = None,
                    types: tuple[str, ...] = ()) -> dict:
        while (left := deadline - time.monotonic()) > 0:
            ev = await asyncio.wait_for(self.events.get(), left)
            if ev.get("type") == "closed":
                raise VoiceError("the chat connection closed")
            if session_id and ev.get("session_id") not in (None, session_id):
                continue
            if not types or ev.get("type") in types:
                return ev
        raise asyncio.TimeoutError


async def ask(text: str, device: str, sessions: Sessions, url: str | None = None,
              agent: str = "The agent") -> str:
    """Send one message to the agent in the device's conversation and return the full reply."""
    import websockets
    deadline = time.monotonic() + TURN_TIMEOUT_S
    async with websockets.connect(url or _gateway_url(), max_size=None, open_timeout=20) as ws:
        rpc = _Rpc(ws)
        try:
            await rpc.event(deadline, types=("gateway.ready",))
            session_id = None
            stored = sessions.get(device)
            if stored:
                try:
                    session_id = (await rpc.call("session.resume", {
                        "session_id": stored, "omit_messages": True})).get("session_id")
                except VoiceError:
                    session_id = None  # deleted in the WebUI, say; start a new one
            if not session_id:
                created = await rpc.call("session.create", {"title": SESSION_TITLE})
                session_id = created.get("session_id")
                sessions.set(device, created.get("stored_session_id") or session_id)
            if not session_id:
                raise VoiceError("Hermes didn't open a chat session")
            await rpc.call("prompt.submit", {"session_id": session_id, "text": text,
                                             "voice_turn": True, "surface": "voice-live"})
            parts: list[str] = []
            while True:
                ev = await rpc.event(deadline, session_id, ("message.delta", "message.complete", "error"))
                payload = ev.get("payload") or {}
                if ev["type"] == "message.delta":
                    parts.append(payload.get("text") or "")
                elif ev["type"] == "error":
                    raise VoiceError(str(payload.get("message") or payload)[:300])
                else:
                    if payload.get("status") == "error":
                        raise VoiceError(str(payload.get("error") or f"{agent}'s turn failed")[:300])
                    return (payload.get("text") or "".join(parts)).strip()
        except asyncio.TimeoutError:
            raise VoiceError(f"{agent} took too long to answer") from None
        finally:
            rpc.reader.cancel()


# --- replies waiting to be fetched --------------------------------------------------------------

class Replies:
    """Spoken replies kept briefly for the device to download, the newest few only."""

    def __init__(self, keep: int = 8) -> None:
        self.keep = keep
        self._items: OrderedDict[str, bytes] = OrderedDict()
        self._lock = threading.Lock()

    def put(self, audio: bytes) -> str:
        key = uuid.uuid4().hex
        with self._lock:
            self._items[key] = audio
            while len(self._items) > self.keep:
                self._items.popitem(last=False)
        return key

    def get(self, key: str) -> bytes | None:
        with self._lock:
            return self._items.get(key)


_SHARED: tuple[Sessions, Replies] | None = None
_SHARED_LOCK = threading.Lock()


def shared(root: Path | None = None) -> tuple[Sessions, Replies]:
    global _SHARED
    with _SHARED_LOCK:
        if _SHARED is None:
            if root is None:
                try:
                    from hermes_constants import get_default_hermes_root
                    root = Path(get_default_hermes_root())
                except ImportError:
                    from hermes_constants import get_hermes_home
                    root = Path(get_hermes_home())
            _SHARED = (Sessions(root / "petasos" / "sessions.json"), Replies())
        return _SHARED


async def turn(text: str, device: str, agent: str = "The agent") -> dict:
    """Ask, then speak: ``{"reply": ..., "audio": id or None}``. A reply that can't be spoken
    still comes back as text for the screen."""
    sessions, replies = shared()
    reply = await ask(text, device, sessions, agent=agent)
    if not reply:
        return {"reply": "", "audio": None}
    loop = asyncio.get_running_loop()
    try:
        audio = await loop.run_in_executor(None, speak, reply)
    except Exception as exc:  # text still reaches the screen
        return {"reply": reply, "audio": None, "speech_error": str(exc)[:300]}
    return {"reply": reply, "audio": replies.put(audio)}


def device_name(value: str | None) -> str:
    name = (value or "car-thing").strip()[:64]
    return name if name and all(c.isalnum() or c in "-_." for c in name) else "car-thing"

