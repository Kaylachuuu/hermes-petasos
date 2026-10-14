#!/usr/bin/env python3
"""Try the petasos plugin's button layout against your Hermes server, without the Car Thing.

Sends a scripted set of button and knob events to /api/plugins/petasos/input, the way the Car
Thing will, and prints the commands that come back. It changes nothing on the server except the
plugin's in-memory state for a pretend device named "try-buttons". Presets only print the text
they would send; nothing reaches Athena.

  python try_buttons.py http://<hermes-server>:9119          # asks for your dashboard login
  python try_buttons.py http://<hermes-server>:9119 --token <hermes_session_at value>

Needs the same Python setup as chat_probe.py, which sits beside it.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from chat_probe import Http, sign_in  # noqa: E402

SCRIPT = [
    ("Hold the front button (push-to-talk)", [
        {"input": "front", "action": "press", "t": 0},
        {"input": "front", "action": "release", "t": 900}]),
    ("Tap the front button (start conversation mode)", [
        {"input": "front", "action": "press", "t": 2000},
        {"input": "front", "action": "release", "t": 2100}]),
    ("Tap it again (end conversation mode)", [
        {"input": "front", "action": "press", "t": 9000},
        {"input": "front", "action": "release", "t": 9100}]),
    ("Press the knob (stop Athena speaking)", [{"input": "knob", "action": "press"},
                                             {"input": "knob", "action": "release"}]),
    ("Turn the knob two clicks one way, then one click back", [
        {"input": "knob", "action": "turn", "delta": 1},
        {"input": "knob", "action": "turn", "delta": 1},
        {"input": "knob", "action": "turn", "delta": -1}]),
    ("Press each preset", [{"input": f"preset-{n}", "action": "press"} for n in range(1, 5)]),
    ("Press the top-right button (mic off), hold the front button, then mic back on", [
        {"input": "mute", "action": "press"},
        {"input": "front", "action": "press", "t": 20000},
        {"input": "front", "action": "release", "t": 21000},
        {"input": "mute", "action": "press"}]),
]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("base_url", help="the address you open the Hermes dashboard on")
    parser.add_argument("--token", help="a dashboard session access token instead of signing in")
    parser.add_argument("--provider", help="sign-in provider name, if there is more than one")
    parser.add_argument("--username", help="dashboard username (asked for if omitted)")
    parser.add_argument("--use-proxy", action="store_true", help="go through the system HTTP proxy")
    parser.add_argument("--insecure-tls", action="store_true", help="skip HTTPS certificate checks")
    args = parser.parse_args()

    h = Http(args.base_url, args.insecure_tls, args.use_proxy)
    token = sign_in(h, args)
    if not token:
        sys.exit(1)

    status, body = h.request("GET", "/api/plugins/petasos/mapping", bearer=token)
    if status == 404:
        sys.exit("\nThe petasos plugin isn't loaded on this server. Install it, restart Hermes, and try again.")
    if status != 200:
        sys.exit(f"\nReading the layout failed with HTTP {status}: {str(body)[:300]}")
    print(f"\nLayout file on the server: {body['path']}")
    for problem in body.get("problems", []):
        print(f"  Problem in that file (the last good layout is in use): {problem}")

    # Start the pretend device fresh, in case an earlier run stopped halfway.
    h.request("POST", "/api/plugins/petasos/input", {"input": "device", "action": "hello",
                                                     "device": "try-buttons"}, bearer=token)
    failed = False
    for title, events in SCRIPT:
        print(f"\n{title}")
        for event in events:
            status, body = h.request("POST", "/api/plugins/petasos/input",
                                     {**event, "device": "try-buttons"}, bearer=token)
            what = f"{event['input']} {event['action']}" + (f" {event['delta']:+d}" if "delta" in event else "")
            if status != 200:
                failed = True
                print(f"  {what:<22} -> HTTP {status}: {str(body)[:200]}")
                continue
            commands = body.get("commands", [])
            shown = ", ".join(json.dumps(c) for c in commands) or "(nothing)"
            print(f"  {what:<22} -> {shown}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
