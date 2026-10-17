"""VPN configurations for the Petasos devices: WireGuard configs in wg-quick form, kept on Hermes
so one added on the dashboard reaches every device, and one added on a device reaches the
others. Each device also keeps its own copy (the VPN must work when Hermes can't be reached),
and syncs it with this one: for each config the latest change wins, a deletion included.

vpns.json holds private keys, so only its owner may read it, and the dashboard is never sent
them: only a device asking with keys=1 gets them back.
"""
from __future__ import annotations

import json
import os
import re
import threading
import time
from pathlib import Path

NAME_MAX = 40
CONF_MAX = 8 << 10

_LOCK = threading.Lock()


class VPNError(ValueError):
    pass


def _root() -> Path:
    try:
        from hermes_constants import get_default_hermes_root
        return Path(get_default_hermes_root())
    except ImportError:
        from hermes_constants import get_hermes_home
        return Path(get_hermes_home())


def _path() -> Path:
    return _root() / "petasos" / "vpns.json"


def _read() -> dict:
    try:
        data = json.loads(_path().read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    configs = data.get("configs") if isinstance(data, dict) else None
    return {c["id"]: c for c in configs if isinstance(c, dict) and c.get("id")} if isinstance(configs, list) else {}


def _write(configs: dict) -> None:
    path = _path()
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + f".{os.getpid()}.tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(json.dumps({"configs": sorted(configs.values(), key=lambda c: c["id"])}, indent=2) + "\n")
    tmp.replace(path)


def _without_keys(conf: str) -> str:
    return "\n".join(line for line in conf.splitlines() if not re.match(r"\s*PrivateKey\s*=", line, re.I))


def _shown(c: dict, keys: bool) -> dict:
    out = dict(c)
    if not keys:
        out["conf"] = _without_keys(c.get("conf", ""))
    return out


def endpoint(conf: str) -> str:
    m = re.search(r"^\s*Endpoint\s*=\s*(\S+)", conf, re.M | re.I)
    return m.group(1) if m else ""


def check(conf: str) -> None:
    """The same checks a device makes before it uses a config, kept to what's needed to tell a
    typing mistake from a working config."""
    if len(conf) > CONF_MAX:
        raise VPNError("That's too long for a WireGuard config.")
    sections = [s.lower() for s in re.findall(r"^\s*\[(\w+)\]", conf, re.M)]
    if "interface" not in sections or "peer" not in sections:
        raise VPNError("A WireGuard config has an [Interface] and a [Peer].")
    for key in ("PrivateKey", "Address", "PublicKey", "AllowedIPs"):
        if not re.search(rf"^\s*{key}\s*=\s*\S", conf, re.M | re.I):
            raise VPNError(f"It has no {key}.")
    for ips in re.findall(r"^\s*AllowedIPs\s*=\s*(.+)$", conf, re.M | re.I):
        if any(ip.strip() in ("0.0.0.0/0", "::/0") for ip in ips.split(",")):
            raise VPNError("AllowedIPs sends everything home; list only the home network, such as 10.0.0.0/24.")


def listed(keys: bool = False) -> dict:
    """Every config, deletions included (so devices learn of them), newest change last."""
    with _LOCK:
        configs = _read()
    return {"configs": [_shown(c, keys) for c in sorted(configs.values(), key=lambda c: c.get("updated", 0))]}


def put(ident: str, body: dict) -> dict:
    """Adds or changes one config. A device sends when it changed it (updated, in ms); an older
    change than the one kept is ignored. A dashboard edit without a private key keeps the saved one."""
    if not re.fullmatch(r"[a-z0-9-]{1,40}", ident or ""):
        raise VPNError("A config's id is up to 40 lowercase letters, digits and dashes.")
    if not isinstance(body, dict):
        raise VPNError("A config is an object.")
    name = str(body.get("name") or "").strip()
    conf = str(body.get("conf") or "")
    if not name or len(name) > NAME_MAX:
        raise VPNError(f"A config needs a name of up to {NAME_MAX} characters.")
    updated = body.get("updated")
    updated = int(updated) if isinstance(updated, (int, float)) and updated > 0 else int(time.time() * 1000)
    with _LOCK:
        configs = _read()
        old = configs.get(ident)
        if old and old.get("updated", 0) > updated:
            return _shown(old, False)
        if old and not re.search(r"^\s*PrivateKey\s*=", conf, re.M | re.I):
            key = re.search(r"^\s*PrivateKey\s*=.*$", old.get("conf", ""), re.M | re.I)
            if key:
                conf = re.sub(r"^(\s*\[Interface\]\s*)$", lambda m: m.group(1) + "\n" + key.group(0).strip(), conf, count=1, flags=re.M | re.I)
        check(conf)
        configs[ident] = {"id": ident, "name": name, "conf": conf.strip() + "\n", "updated": updated, "deleted": False}
        _write(configs)
        return _shown(configs[ident], False)


def delete(ident: str, updated=None) -> dict:
    """Deletes a config, keeping a note of it so the devices delete their copies too."""
    updated = int(updated) if isinstance(updated, (int, float)) and updated > 0 else int(time.time() * 1000)
    with _LOCK:
        configs = _read()
        old = configs.get(ident)
        if old and old.get("updated", 0) > updated:
            return _shown(old, False)
        configs[ident] = {"id": ident, "name": (old or {}).get("name", ""), "conf": "", "updated": updated, "deleted": True}
        _write(configs)
        return configs[ident]
