"""VPN configurations kept on Hermes for the devices."""
import os
import stat

import pytest

import vpn

CONF = """[Interface]
PrivateKey = aGVsbG8gdGhpcyBpcyBhIHRlc3Qga2V5IGZvciB3Zz0=
Address = 10.8.0.5/32

[Peer]
PublicKey = c2VydmVyIHB1YmxpYyBrZXkgZm9yIHRoZSB0ZXN0cz0=
AllowedIPs = 10.0.0.0/24
Endpoint = home.example.net:51820
"""


@pytest.fixture
def home(tmp_path, monkeypatch):
    monkeypatch.setattr(vpn, "_root", lambda: tmp_path)
    return tmp_path


def test_configs_are_kept_privately_and_keys_go_only_to_devices(home):
    shown = vpn.put("home", {"name": "Home", "conf": CONF, "updated": 1000})
    assert shown["name"] == "Home" and "PrivateKey" not in shown["conf"]
    assert stat.S_IMODE(os.stat(home / "petasos" / "vpns.json").st_mode) == 0o600
    assert "PrivateKey" not in vpn.listed()["configs"][0]["conf"]
    assert "PrivateKey = aGVs" in vpn.listed(keys=True)["configs"][0]["conf"]
    assert vpn.endpoint(CONF) == "home.example.net:51820"


def test_a_dashboard_edit_without_the_key_keeps_it(home):
    vpn.put("home", {"name": "Home", "conf": CONF, "updated": 1000})
    edited = CONF.replace("PrivateKey = aGVsbG8gdGhpcyBpcyBhIHRlc3Qga2V5IGZvciB3Zz0=\n", "").replace("51820", "51821")
    vpn.put("home", {"name": "Home", "conf": edited, "updated": 2000})
    conf = vpn.listed(keys=True)["configs"][0]["conf"]
    assert "PrivateKey = aGVs" in conf and "51821" in conf


def test_the_latest_change_wins_and_deletions_are_kept(home):
    vpn.put("home", {"name": "Home", "conf": CONF, "updated": 2000})
    assert vpn.put("home", {"name": "Older", "conf": CONF, "updated": 1000})["name"] == "Home"
    vpn.delete("home", 3000)
    (c,) = vpn.listed()["configs"]
    assert c["deleted"] and c["conf"] == "" and c["updated"] == 3000
    assert vpn.put("home", {"name": "Back", "conf": CONF, "updated": 2500})["deleted"]


@pytest.mark.parametrize("conf, problem", [
    ("[Interface]\nAddress = 10.8.0.5/32\n", "[Peer]"),
    (CONF.replace("Address = 10.8.0.5/32\n", ""), "Address"),
    (CONF.replace("10.0.0.0/24", "0.0.0.0/0"), "everything home"),
])
def test_a_broken_config_is_refused(home, conf, problem):
    with pytest.raises(vpn.VPNError, match=problem.replace("[", r"\[").replace("]", r"\]")):
        vpn.put("home", {"name": "Home", "conf": conf})
    with pytest.raises(vpn.VPNError):
        vpn.put("Bad Id", {"name": "Home", "conf": CONF})
