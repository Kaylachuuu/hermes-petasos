#!/usr/bin/env python3
"""Install the petasos program on the Car Thing, from a computer it's plugged into by USB.

Signs in to Hermes, downloads the current device program from the petasos plugin, and copies it
to the Car Thing over SSH in one go, together with the server address and a sign-in of its own.
It then sets the program to start at boot and starts it.

  python install_device.py http://10.0.0.20:9119
  python install_device.py http://10.0.0.20:9119 --host nocturne.local --no-grab

Works on the petasos image (start-at-boot is built in) and on Nocturne (where it adds a
start-at-boot entry, which a Nocturne system update removes; run it again after one). Updates to
the program itself need no reinstall: it updates itself from the plugin.

Needs the same Python setup as chat_probe.py, which sits beside it, and ssh.
"""
from __future__ import annotations

import argparse
import hashlib
import io
import json
import subprocess
import sys
import tarfile
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from chat_probe import SESSION_RT_COOKIE, Http, sign_in  # noqa: E402

UNIT = """[Unit]
Description=Petasos: the Car Thing as a voice remote for Athena
After=network.target superbird-weston.service
RequiresMountsFor=/var/lib/petasos

[Service]
ExecStart=/var/lib/petasos/petasos run
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
"""

# Runs on the Car Thing from the unpacked staging folder. The system partition is read-only, so
# it is made writable just long enough to add the start-at-boot entry, then made read-only again.
INSTALL_SH = r"""set -e
cd /var/lib/petasos/.incoming
install -m 0755 petasos ../petasos.new && mv ../petasos.new ../petasos
install -m 0644 config.json ../config.json
install -m 0600 tokens.json ../tokens.json
if [ -e /usr/lib/systemd/system/petasos.service ]; then
    : # the petasos image: starting at boot is built in
elif ! cmp -s petasos.service /etc/systemd/system/petasos.service 2>/dev/null \
   || [ ! -e /etc/systemd/system/multi-user.target.wants/petasos.service ]; then
    mount -o remount,rw /
    trap 'mount -o remount,ro / || echo "Warning: the system partition is still writable until the next restart." >&2' EXIT
    install -m 0644 petasos.service /etc/systemd/system/petasos.service
    mkdir -p /etc/systemd/system/multi-user.target.wants
    ln -sf ../petasos.service /etc/systemd/system/multi-user.target.wants/petasos.service
    sync
fi
cd .. && rm -rf .incoming
systemctl daemon-reload
systemctl restart petasos
sleep 3
echo "== petasos $(/var/lib/petasos/petasos version) is $(systemctl is-active petasos)"
journalctl -u petasos -n 15 --no-pager -o cat
"""


def tar_member(tar: tarfile.TarFile, name: str, data: bytes, mode: int) -> None:
    info = tarfile.TarInfo(name)
    info.size, info.mode, info.mtime = len(data), mode, int(time.time())
    tar.addfile(info, io.BytesIO(data))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("base_url", help="the address you open the Hermes dashboard on, as the Car Thing will reach it")
    parser.add_argument("--host", default="petasos.local",
                        help="the Car Thing's address (default petasos.local; nocturne.local on Nocturne)")
    parser.add_argument("--no-grab", action="store_true",
                        help="let Nocturne's own screen keep seeing the buttons too")
    parser.add_argument("--token", help=argparse.SUPPRESS)
    parser.add_argument("--provider", help="sign-in provider name, if there is more than one")
    parser.add_argument("--username", help="dashboard username (asked for if omitted)")
    parser.add_argument("--use-proxy", action="store_true", help="go through the system HTTP proxy")
    parser.add_argument("--insecure-tls", action="store_true", help="skip HTTPS certificate checks")
    args = parser.parse_args()
    args.token = None  # the device needs its own refresh token, which only a password sign-in gives

    h = Http(args.base_url, args.insecure_tls, args.use_proxy)
    access = sign_in(h, args)
    refresh = h.cookie(SESSION_RT_COOKIE)
    if not access or not refresh:
        sys.exit("\nSigning in didn't give a renewable sign-in for the Car Thing; nothing was installed.")
    access, refresh = access.strip('"'), refresh.strip('"')

    print("\nDownloading the device program from the petasos plugin")
    status, latest = h.request("GET", "/api/plugins/petasos/client?arch=arm64", bearer=access)
    if status == 404 and not isinstance(latest, dict):
        sys.exit("  The petasos plugin isn't loaded on this server, or is older than the device program.")
    if status != 200 or not isinstance(latest, dict):
        sys.exit(f"  Asking for the device program failed with HTTP {status}: {str(latest)[:300]}")
    binary = h.download("/api/plugins/petasos/client/download?arch=arm64", bearer=access)
    if hashlib.sha256(binary).hexdigest() != latest["sha256"]:
        sys.exit("  The download doesn't match the server's checksum; nothing was installed.")
    print(f"  version {latest['version']}, {len(binary) // 1024} KB")

    config = {"server": h.base}
    if args.no_grab:
        config["grab"] = False
    bundle = io.BytesIO()
    with tarfile.open(fileobj=bundle, mode="w") as tar:
        tar_member(tar, "petasos", binary, 0o755)
        tar_member(tar, "config.json", (json.dumps(config, indent=2) + "\n").encode(), 0o644)
        tar_member(tar, "tokens.json", (json.dumps({"access_token": access, "refresh_token": refresh,
                                                    "provider": args.provider or ""},
                                                   indent=2) + "\n").encode(), 0o600)
        tar_member(tar, "petasos.service", UNIT.encode(), 0o644)
        tar_member(tar, "install.sh", INSTALL_SH.encode(), 0o755)

    print(f"\nCopying it to the Car Thing at {args.host} (if ssh asks for a password, press Enter)")
    remote = ("rm -rf /var/lib/petasos/.incoming && mkdir -p /var/lib/petasos/.incoming && "
              "tar -x -f - -C /var/lib/petasos/.incoming && sh /var/lib/petasos/.incoming/install.sh")
    # -4: over USB the Mac may pick the IPv6 link-local address, which the image's USB-only login
    # rule doesn't match; the cable's IPv4 network always works.
    done = subprocess.run(["ssh", "-4", "-o", "ConnectTimeout=10", f"root@{args.host}", remote], input=bundle.getvalue())
    if done.returncode != 0:
        sys.exit(f"\nThe install on the Car Thing stopped with exit code {done.returncode}; see the messages above.")
    print("\nInstalled. It starts by itself whenever the Car Thing boots.")


if __name__ == "__main__":
    main()
