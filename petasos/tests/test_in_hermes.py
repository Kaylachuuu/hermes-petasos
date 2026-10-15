"""End to end inside a real Hermes: the dashboard app and the plugin loader, with simulated Car
Thing events sent to the plugin's routes. Skipped when Hermes is not installed."""
import os
from pathlib import Path

import pytest

pytest.importorskip("hermes_cli")


@pytest.fixture(scope="module")
def hermes(tmp_path_factory):
    home = tmp_path_factory.mktemp("hermes_home")
    (home / "plugins").mkdir()
    os.symlink(Path(__file__).resolve().parent.parent, home / "plugins" / "petasos")
    (home / "config.yaml").write_text("plugins:\n  enabled:\n    - petasos\n")
    old = os.environ.get("HERMES_HOME")
    os.environ["HERMES_HOME"] = str(home)
    os.environ["HERMES_DASHBOARD_SESSION_TOKEN"] = "test-token"
    from fastapi.testclient import TestClient
    from hermes_cli import web_server
    import model_tools  # noqa: F401  (plugin discovery)
    with TestClient(web_server.app) as client:
        client.headers["Authorization"] = "Bearer test-token"
        yield client, home
    if old is None:
        os.environ.pop("HERMES_HOME", None)
    else:
        os.environ["HERMES_HOME"] = old


def test_routes_need_the_dashboard_login(hermes):
    client, _ = hermes
    r = client.post("/api/plugins/petasos/input", json={"input": "knob", "action": "press"},
                    headers={"Authorization": ""})
    assert r.status_code == 401


def test_events_come_back_as_commands(hermes):
    client, home = hermes
    post = lambda e: client.post("/api/plugins/petasos/input", json=e)  # noqa: E731
    r = post({"input": "front", "action": "press", "t": 0})
    assert r.status_code == 200, r.text
    assert r.json() == {"commands": [{"do": "listen", "mode": "hold"}]}
    assert post({"input": "front", "action": "release", "t": 800}).json() == {"commands": [{"do": "send"}]}
    assert post({"input": "knob", "action": "press"}).json() == {"commands": [{"do": "stop_speaking"}]}
    assert post({"input": "nope", "action": "press"}).status_code == 400
    assert (home / "petasos" / "mapping.json").is_file()


def test_mapping_can_be_read_and_replaced(hermes):
    client, _ = hermes
    body = client.get("/api/plugins/petasos/mapping").json()
    assert body["problems"] == [] and body["path"].endswith("mapping.json")
    mapping = body["mapping"]
    mapping["controls"]["preset-4"] = {"press": {"action": "prompt", "text": "Good night"}}
    assert client.put("/api/plugins/petasos/mapping", json=mapping).status_code == 200
    r = client.post("/api/plugins/petasos/input", json={"input": "preset-4", "action": "press"})
    assert r.json() == {"commands": [{"do": "prompt", "text": "Good night"}]}
    assert client.put("/api/plugins/petasos/mapping", json={"controls": {}}).status_code == 400


def test_a_device_keeps_its_own_presets(hermes):
    client, _ = hermes
    url = "/api/plugins/petasos/devices/kitchen/presets"
    assert client.get(url).json()["own"] is False
    own = {"preset-2": {"press": {"action": "prompt", "text": "Any packages today?", "label": "Packages"}}}
    body = client.put(url, json={"presets": own, "name": "Kitchen"}).json()
    assert body["own"] and body["labels"][1] == "Packages"
    assert body["commands"][0]["presets"][1] == "Packages"
    r = client.post("/api/plugins/petasos/input", json={"input": "preset-2", "action": "press", "device": "kitchen"})
    assert r.json() == {"commands": [{"do": "prompt", "text": "Any packages today?"}]}
    assert client.put(url, json={"presets": {"knob": {}}}).status_code == 400
    assert client.put(url, json={"presets": None}).json()["own"] is False


def test_slash_command_shows_the_layout(hermes):
    from hermes_cli.plugins import get_plugin_command_handler
    text = get_plugin_command_handler("petasos")("")
    assert "Front button: hold = push-to-talk; tap = start or end conversation mode" in text
    assert "mapping.json" in text


def test_device_program_is_served_with_its_checksum(hermes):
    import hashlib
    client, _ = hermes
    root = Path(__file__).resolve().parents[2] / "client"
    info = client.get("/api/plugins/petasos/client").json()
    assert info["version"] == (root / "VERSION").read_text().strip()
    body = client.get("/api/plugins/petasos/client/download").content
    assert hashlib.sha256(body).hexdigest() == info["sha256"] and len(body) == info["size"]
    assert client.get("/api/plugins/petasos/client?arch=mips").status_code == 400
    assert client.get("/api/plugins/petasos/client?os=plan9").status_code == 400


def test_app_programs_are_served(hermes):
    import hashlib
    client, _ = hermes
    for system, arch, name in (("darwin", "arm64", "petasos-darwin-arm64"),
                               ("windows", "amd64", "petasos-windows-amd64.exe")):
        info = client.get(f"/api/plugins/petasos/client?os={system}&arch={arch}").json()
        assert info["os"] == system
        assert info["plugin"] and info["plugin"][0].isdigit()
        resp = client.get(f"/api/plugins/petasos/client/download?os={system}&arch={arch}")
        assert name in resp.headers["content-disposition"]
        assert hashlib.sha256(resp.content).hexdigest() == info["sha256"]


def test_voice_routes(hermes, monkeypatch):
    client, _ = hermes
    import importlib.util
    import sys
    v = sys.modules.get("hermes_petasos_voice")
    if v is None:  # the routes load it under this name; loading it first lets the test swap parts
        spec = importlib.util.spec_from_file_location(
            "hermes_petasos_voice", Path(__file__).resolve().parent.parent / "voice.py")
        v = importlib.util.module_from_spec(spec)
        sys.modules["hermes_petasos_voice"] = v
        spec.loader.exec_module(v)
    asked = []

    async def fake_ask(text, device, sessions, url=None, agent="The agent", shared_chat=False):
        asked.append((text, device, agent))
        return "It's sunny."

    monkeypatch.setattr(v, "transcribe", lambda audio, suffix=".wav": "what's the weather" if audio else "")
    monkeypatch.setattr(v, "ask", fake_ask)
    monkeypatch.setattr(v, "speak", lambda text: b"RIFF-fake-audio")
    r = client.post("/api/plugins/petasos/voice?device=car-thing", content=b"RIFF....",
                    headers={"Content-Type": "audio/wav"})
    assert r.status_code == 200, r.text
    body = r.json()
    assert body["heard"] == "what's the weather" and body["reply"] == "It's sunny."
    audio = client.get(f"/api/plugins/petasos/voice/audio/{body['audio']}")
    assert audio.status_code == 200 and audio.content == b"RIFF-fake-audio"
    assert audio.headers["content-type"] == "audio/wav"
    r = client.post("/api/plugins/petasos/ask", json={"text": "Read me my last message."})
    assert r.json()["reply"] == "It's sunny." and asked[-1] == ("Read me my last message.", "car-thing", "Hermes")
    assert client.post("/api/plugins/petasos/ask", json={"text": " "}).status_code == 400
    assert client.post("/api/plugins/petasos/voice", content=b"").status_code == 400
    assert client.get("/api/plugins/petasos/voice/audio/nope").status_code == 404


def test_the_agent_gets_the_music_tools_and_the_device_gets_their_commands(hermes):
    client, home = hermes
    from tools.registry import registry
    assert registry.get_entry("play_music") is not None and registry.get_entry("music_control") is not None
    music = (home / "petasos" / "music")
    music.mkdir()
    (music / "Song.mp3").write_bytes(b"ID3 tune")
    r = client.put("/api/plugins/petasos/music/settings", json={"folders": [str(music)]})
    assert r.status_code == 200, r.text
    assert client.post("/api/plugins/petasos/music/check").json()["folders"][0]["songs"] == 1
    import json
    answer = json.loads(registry.dispatch("play_music", {"query": "song", "device": "car-thing"}))
    assert answer["songs"] == 1, answer
    commands = client.get("/api/plugins/petasos/devices/car-thing/commands").json()["commands"]
    assert commands[0]["op"] == "play"
    track = commands[0]["tracks"][0]
    r = client.get(f"/api/plugins/petasos/music/stream?source={track['source']}&id={track['id']}")
    assert r.status_code == 200 and r.content == b"ID3 tune"
    assert client.get("/api/plugins/petasos/devices/car-thing/commands").json() == {"commands": []}
