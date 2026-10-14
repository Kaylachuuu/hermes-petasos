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


def test_slash_command_shows_the_layout(hermes):
    from hermes_cli.plugins import get_plugin_command_handler
    text = get_plugin_command_handler("petasos")("")
    assert "Front button: hold = push-to-talk; tap = start or end conversation mode" in text
    assert "mapping.json" in text
