"""Voice turns against a stand-in for the dashboard's chat connection."""
import asyncio
import io
import json
import wave

import pytest

import voice

websockets = pytest.importorskip("websockets")


def wav(frames: bytes, rate: int = 24000) -> bytes:
    out = io.BytesIO()
    with wave.open(out, "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(rate)
        w.writeframes(frames)
    return out.getvalue()


def test_join_wavs_keeps_every_chunk():
    joined = voice.join_wavs([wav(b"\x01\x00" * 10), wav(b"\x02\x00" * 5)])
    with wave.open(io.BytesIO(joined)) as r:
        assert r.getnframes() == 15 and r.getframerate() == 24000


class FakeGateway:
    """Speaks just enough of tui_gateway's JSON-RPC: ready, create/resume, submit, a reply."""

    def __init__(self):
        self.calls = []
        self.declined = []
        self.stored = {"stored-1"}

    async def handler(self, ws):
        await ws.send(json.dumps({"jsonrpc": "2.0", "method": "event",
                                  "params": {"type": "gateway.ready", "payload": {}}}))
        async for raw in ws:
            frame = json.loads(raw)
            if "method" not in frame:  # our answer to the server request
                self.declined.append(frame)
                continue
            method, params, rid = frame["method"], frame["params"], frame["id"]
            self.calls.append((method, params))
            if method == "session.create":
                result = {"session_id": "live-1", "stored_session_id": "stored-1"}
            elif method == "session.resume":
                if params["session_id"] not in self.stored:
                    await ws.send(json.dumps({"jsonrpc": "2.0", "id": rid,
                                              "error": {"code": 4001, "message": "not found"}}))
                    continue
                result = {"session_id": "live-2"}
            elif method == "prompt.submit":
                sid = params["session_id"]
                await ws.send(json.dumps({"jsonrpc": "2.0", "id": rid, "result": {"status": "streaming"}}))
                await ws.send(json.dumps({"jsonrpc": "2.0", "id": "srv-1", "method": "approval",
                                          "params": {"session_id": sid}}))
                for kind, payload in (("message.delta", {"text": "Your next "}),
                                      ("message.delta", {"text": "meeting is at noon."}),
                                      ("message.complete", {"text": "Your next meeting is at noon.",
                                                            "status": "complete"})):
                    await ws.send(json.dumps({"jsonrpc": "2.0", "method": "event", "params": {
                        "type": kind, "session_id": sid, "payload": payload}}))
                continue
            else:
                result = {}
            await ws.send(json.dumps({"jsonrpc": "2.0", "id": rid, "result": result}))


def run_turns(tmp_path, *texts):
    gateway = FakeGateway()
    sessions = voice.Sessions(tmp_path / "sessions.json")

    async def go():
        async with websockets.serve(gateway.handler, "127.0.0.1", 0) as server:
            url = f"ws://127.0.0.1:{server.sockets[0].getsockname()[1]}/api/ws"
            return [await voice.ask(t, "car-thing", sessions, url=url) for t in texts]

    return asyncio.run(go()), gateway, sessions


def test_first_turn_opens_a_session_and_later_turns_resume_it(tmp_path):
    replies, gateway, sessions = run_turns(tmp_path, "What's next?", "And after that?")
    assert replies == ["Your next meeting is at noon."] * 2
    methods = [m for m, _ in gateway.calls]
    assert methods == ["session.create", "prompt.submit", "session.resume", "prompt.submit"]
    submit = gateway.calls[1][1]
    assert submit == {"session_id": "live-1", "text": "What's next?", "voice_turn": True,
                      "surface": "voice-live"}
    assert gateway.calls[3][1]["session_id"] == "live-2"
    assert sessions.get("car-thing") == "stored-1"
    # The approval request was declined rather than left hanging.
    assert gateway.declined and gateway.declined[0]["id"] == "srv-1" and "error" in gateway.declined[0]


def test_a_session_deleted_elsewhere_is_replaced(tmp_path):
    voice.Sessions(tmp_path / "sessions.json").set("car-thing", "gone")
    replies, gateway, sessions = run_turns(tmp_path, "Hello")
    assert replies == ["Your next meeting is at noon."]
    assert [m for m, _ in gateway.calls] == ["session.resume", "session.create", "prompt.submit"]
    assert sessions.get("car-thing") == "stored-1"


def test_device_names_are_kept_tidy():
    assert voice.device_name("car-thing") == "car-thing"
    assert voice.device_name("../etc") == "car-thing"
    assert voice.device_name(None) == "car-thing"
