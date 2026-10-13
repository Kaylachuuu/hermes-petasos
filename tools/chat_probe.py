#!/usr/bin/env python3
"""Petasos chat probe: can a non-browser client talk to Hermes over the dashboard's /api/ws?

This is the first step of hermes-petasos. The Car Thing client will be a small program, not a
browser, so before building anything we check that such a program can:

  1. learn how the dashboard is protected (GET /api/status, which is public),
  2. sign in and hold a credential it can reuse (a session access token),
  3. open the /api/ws chat connection with that token, and
  4. optionally start a chat and stream one reply back.

It only reads from Hermes unless you pass --say, which sends one real message (Athena will see
it and may remember it).

Requires Python 3.10+ and the `websockets` package:  python -m pip install websockets
"""
from __future__ import annotations

import argparse
import asyncio
import getpass
import http.cookiejar
import json
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

try:
    import websockets
    import websockets.exceptions
except ImportError:  # pragma: no cover - friendly message for a missing dependency
    sys.exit("This probe needs the 'websockets' package: python -m pip install websockets")

_INVALID_STATUS = tuple(
    getattr(websockets.exceptions, n) for n in ("InvalidStatus", "InvalidStatusCode")
    if hasattr(websockets.exceptions, n))

SESSION_AT_COOKIE = "hermes_session_at"  # hermes_cli/dashboard_auth/cookies.py
SESSION_RT_COOKIE = "hermes_session_rt"


# --- HTTP helpers -----------------------------------------------------------------------------

class Http:
    def __init__(self, base: str, insecure_tls: bool, use_proxy: bool) -> None:
        self.base = base.rstrip("/")
        self.jar = http.cookiejar.CookieJar()
        handlers: list = [urllib.request.HTTPCookieProcessor(self.jar)]
        if not use_proxy:
            handlers.append(urllib.request.ProxyHandler({}))
        if insecure_tls:
            ctx = ssl.create_default_context()
            ctx.check_hostname = False
            ctx.verify_mode = ssl.CERT_NONE
            handlers.append(urllib.request.HTTPSHandler(context=ctx))
        self.opener = urllib.request.build_opener(*handlers)

    def request(self, method: str, path: str, body: dict | None = None,
                bearer: str | None = None) -> tuple[int, dict | str]:
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if bearer:
            req.add_header("Authorization", f"Bearer {bearer}")
        try:
            with self.opener.open(req, timeout=20) as resp:
                status, raw = resp.status, resp.read()
        except urllib.error.HTTPError as e:
            status, raw = e.code, e.read()
        text = raw.decode("utf-8", "replace")
        try:
            return status, json.loads(text)
        except json.JSONDecodeError:
            return status, text

    def cookie(self, name: str) -> str | None:
        # The cookie may carry a __Host- / __Secure- prefix on HTTPS.
        for c in self.jar:
            if c.name.endswith(name):
                return c.value
        return None


def ws_base(base: str) -> str:
    parts = urllib.parse.urlsplit(base.rstrip("/"))
    scheme = "wss" if parts.scheme == "https" else "ws"
    return urllib.parse.urlunsplit((scheme, parts.netloc, parts.path, "", ""))


# --- Steps ----------------------------------------------------------------------------------

def step(n: int, title: str) -> None:
    print(f"\n[{n}] {title}")


def ok(msg: str) -> None:
    print(f"    OK    {msg}")


def bad(msg: str) -> None:
    print(f"    FAIL  {msg}")


def info(msg: str) -> None:
    print(f"          {msg}")


def check_status(h: Http) -> dict | None:
    step(1, "Reading the dashboard's public status")
    status, body = h.request("GET", "/api/status")
    if status != 200 or not isinstance(body, dict):
        bad(f"GET /api/status returned {status}: {str(body)[:200]}")
        return None
    auth = {k: body.get(k) for k in ("auth_required", "auth_providers", "auth_flows")}
    ok(f"Hermes {body.get('version', '?')} is reachable")
    info(f"auth: {json.dumps(auth)}")
    if not auth.get("auth_required"):
        info("This dashboard has no login (loopback mode). It only accepts chat connections from")
        info("the server itself, so a Car Thing could not connect to it directly. Run this probe")
        info("on the Hermes server, or turn on dashboard login, to continue.")
    return body


def sign_in(h: Http, args: argparse.Namespace) -> str | None:
    step(2, "Getting a session access token")
    if args.token:
        ok("using the token passed with --token")
        return args.token
    status, body = h.request("GET", "/api/auth/providers")
    providers = body.get("providers", []) if isinstance(body, dict) else []
    pw = [p for p in providers if p.get("supports_password")]
    if not pw:
        bad("no password sign-in is configured on this dashboard")
        info("Sign in to the dashboard in a browser, copy the value of the 'hermes_session_at'")
        info("cookie (browser dev tools > Application/Storage > Cookies), and rerun with --token.")
        return None
    provider = args.provider or pw[0]["name"]
    username = args.username or input("    Dashboard username: ")
    password = getpass.getpass("    Dashboard password (not shown): ")
    status, body = h.request("POST", "/auth/password-login",
                             {"provider": provider, "username": username, "password": password})
    if status != 200:
        bad(f"sign-in failed with HTTP {status}: {str(body)[:200]}")
        return None
    token = h.cookie(SESSION_AT_COOKIE)
    if not token:
        bad("signed in, but no access-token cookie came back")
        return None
    ok(f"signed in with provider '{provider}' and received an access token")
    return token


def check_rest_bearer(h: Http, token: str) -> None:
    step(3, "Using the token on a normal API call (Authorization: Bearer)")
    status, body = h.request("GET", "/api/auth/me", bearer=token)
    if status == 200 and isinstance(body, dict):
        exp = body.get("expires_at")
        left = f", expires in about {int((exp - time.time()) / 60)} min" if isinstance(exp, (int, float)) else ""
        ok(f"token accepted for user {body.get('display_name') or body.get('user_id')}{left}")
    else:
        bad(f"/api/auth/me returned {status}: {str(body)[:200]}")


async def chat(url: str, label: str, args: argparse.Namespace, ssl_ctx, *, converse: bool) -> bool:
    """Open /api/ws, wait for gateway.ready, then optionally create a session and send --say."""
    shown = url.split("?")[0]
    try:
        async with _connect(url, ssl_ctx, args.use_proxy) as ws:
            ready = await asyncio.wait_for(_next_event(ws, "gateway.ready"), 20)
            ok(f"connected to {shown} with {label}; gateway.ready received")
            if ready.get("heartbeat"):
                info("server expects heartbeats (gateway.ping); the Car Thing client must send them")
            if not converse:
                return True
            rpc = _Rpc(ws)
            pinger = asyncio.create_task(rpc.heartbeat())
            try:
                created = await rpc.call("session.create", {})
                sid = created.get("session_id")
                if not sid:
                    bad(f"session.create returned no session_id: {json.dumps(created)[:200]}")
                    return False
                ok(f"session.create -> {sid}")
                if not args.say:
                    info("no --say given, so no message was sent")
                    await rpc.call("session.close", {"session_id": sid})
                    return True
                await rpc.call("prompt.submit", {"session_id": sid, "text": args.say})
                ok("prompt.submit accepted; streaming the reply:\n")
                reply = await rpc.stream_reply(sid, timeout=args.timeout)
                print()
                if reply is None:
                    bad(f"no message.complete within {args.timeout}s")
                    return False
                ok(f"message.complete received ({len(reply)} characters)")
                return True
            finally:
                pinger.cancel()
    except _INVALID_STATUS as e:
        code = getattr(getattr(e, "response", None), "status_code", None) or getattr(e, "status_code", "?")
        bad(f"upgrade refused for {label}: HTTP {code} (Hermes answers a refused credential or a "
            "blocked request with 403 before the socket opens)")
    except websockets.exceptions.ConnectionClosed as e:
        hint = {4401: "credential rejected", 4403: "request not allowed (chat disabled, Host/Origin, "
                "or a non-loopback client on a dashboard with no login)"}.get(e.rcvd.code if e.rcvd else 0, "")
        bad(f"connection closed for {label}: code {e.rcvd.code if e.rcvd else '?'} {hint}")
    except (OSError, asyncio.TimeoutError) as e:
        bad(f"could not connect for {label}: {e!r}")
    return False


def _connect(url: str, ssl_ctx, use_proxy: bool):
    kwargs: dict = {"max_size": None, "open_timeout": 20}
    if ssl_ctx is not None:
        kwargs["ssl"] = ssl_ctx
    if not use_proxy:
        # websockets 15+ follows the system proxy by default; a home-network Hermes never needs it.
        kwargs["proxy"] = None
    try:
        return websockets.connect(url, **kwargs)
    except TypeError:  # websockets < 15 has no proxy argument and never uses one
        kwargs.pop("proxy", None)
        return websockets.connect(url, **kwargs)


async def _next_event(ws, event_type: str) -> dict:
    while True:
        frame = json.loads(await ws.recv())
        params = frame.get("params") or {}
        if frame.get("method") == "event" and params.get("type") == event_type:
            return params.get("payload") or {}


class _Rpc:
    """Minimal JSON-RPC 2.0 client for tui_gateway (one JSON object per WebSocket frame)."""

    def __init__(self, ws) -> None:
        self.ws = ws
        self.next_id = 0
        self.pending: dict[int, asyncio.Future] = {}
        self.events: asyncio.Queue = asyncio.Queue()
        self.reader = asyncio.create_task(self._read())

    async def _read(self) -> None:
        async for raw in self.ws:
            frame = json.loads(raw)
            if "id" in frame and frame.get("id") in self.pending and "method" not in frame:
                fut = self.pending.pop(frame["id"])
                if "error" in frame:
                    fut.set_exception(RuntimeError(json.dumps(frame["error"])[:300]))
                else:
                    fut.set_result(frame.get("result") or {})
            elif frame.get("method") == "event":
                await self.events.put(frame.get("params") or {})
            elif "method" in frame and "id" in frame:
                # A server->client request (approval, clarify...). The probe never answers them.
                info(f"(server asked '{frame['method']}'; the probe ignores server requests)")

    async def call(self, method: str, params: dict) -> dict:
        self.next_id += 1
        rid = self.next_id
        fut = asyncio.get_running_loop().create_future()
        self.pending[rid] = fut
        await self.ws.send(json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params}))
        return await asyncio.wait_for(fut, 60)

    async def heartbeat(self) -> None:
        while True:
            await asyncio.sleep(15)
            await self.call("gateway.ping", {})

    async def stream_reply(self, sid: str, timeout: float) -> str | None:
        deadline = time.monotonic() + timeout
        parts: list[str] = []
        while (left := deadline - time.monotonic()) > 0:
            try:
                ev = await asyncio.wait_for(self.events.get(), left)
            except asyncio.TimeoutError:
                return None
            if ev.get("session_id") != sid:
                continue
            payload = ev.get("payload") or {}
            if ev.get("type") == "message.delta":
                text = payload.get("text") or ""
                parts.append(text)
                print(text, end="", flush=True)
            elif ev.get("type") == "message.complete":
                final = payload.get("text") or "".join(parts)
                if not parts and final:
                    print(final, end="")
                return final
            elif ev.get("type") == "error":
                print(f"\n    (error event: {json.dumps(payload)[:200]})")
        return None


async def main_async(args: argparse.Namespace) -> int:
    h = Http(args.url, args.insecure_tls, args.use_proxy)
    status = check_status(h)
    if status is None:
        return 1
    ssl_ctx = None
    if args.url.startswith("https") and args.insecure_tls:
        ssl_ctx = ssl.create_default_context()
        ssl_ctx.check_hostname = False
        ssl_ctx.verify_mode = ssl.CERT_NONE

    if not status.get("auth_required"):
        if not args.token:
            return 1
        # Loopback mode: the legacy per-process token, accepted only from the server itself.
        step(4, "Opening /api/ws with the loopback token")
        good = await chat(f"{ws_base(args.url)}/api/ws?token={urllib.parse.quote(args.token)}",
                          "the loopback token", args, ssl_ctx, converse=True)
        return 0 if good else 1

    token = sign_in(h, args)
    if not token:
        return 1
    check_rest_bearer(h, token)

    # This is the path the Car Thing would use: a stored token, no browser.
    step(4, "Opening /api/ws with ?token= (the path the Car Thing would use)")
    token_ok = await chat(f"{ws_base(args.url)}/api/ws?token={urllib.parse.quote(token)}",
                          "the session token", args, ssl_ctx, converse=True)

    # The browser path, for comparison: mint a 30-second single-use ticket and connect with it.
    step(5, "For comparison: a single-use ticket (the path the web dashboard uses)")
    st, body = h.request("POST", "/api/auth/ws-ticket", bearer=token)
    ticket_ok = False
    if st == 200 and isinstance(body, dict) and body.get("ticket"):
        ticket_ok = await chat(f"{ws_base(args.url)}/api/ws?ticket={urllib.parse.quote(body['ticket'])}",
                               "a ticket", args, ssl_ctx, converse=False)
    else:
        bad(f"ticket mint returned {st}: {str(body)[:200]}")

    # A device that stays signed in for weeks must renew its token without a browser.
    step(6, "Renewing the token without a browser (what the Car Thing would do daily)")
    refresh_ok = None
    rt = h.cookie(SESSION_RT_COOKIE)
    if not rt:
        info("skipped: no refresh token (only available after password sign-in in this probe)")
    else:
        st, body = h.request("POST", "/auth/native/refresh", {"refresh_token": rt})
        refresh_ok = st == 200 and isinstance(body, dict) and bool(body.get("access_token"))
        if refresh_ok:
            ok("refresh accepted; got a new access token and a rotated refresh token")
        else:
            bad(f"refresh returned {st}: {str(body)[:200]}")

    print("\nSummary")
    print(f"  session token on /api/ws: {'works' if token_ok else 'FAILED'}")
    print(f"  ticket on /api/ws:        {'works' if ticket_ok else 'FAILED'}")
    if refresh_ok is not None:
        print(f"  token renewal:            {'works' if refresh_ok else 'FAILED'}")
    return 0 if token_ok else 1


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("url", help="Hermes dashboard URL, e.g. http://10.0.0.20:9119 (include any path prefix)")
    p.add_argument("--token", help="a session access token (skips password sign-in), or the loopback token")
    p.add_argument("--provider", help="password provider name (default: the first one offered)")
    p.add_argument("--username", help="dashboard username (asked for if omitted)")
    p.add_argument("--say", help="send this one message to Athena and stream her reply")
    p.add_argument("--timeout", type=float, default=180, help="seconds to wait for the reply (default 180)")
    p.add_argument("--use-proxy", action="store_true", help="go through the system HTTP proxy (off by default)")
    p.add_argument("--insecure-tls", action="store_true", help="skip TLS certificate checks (self-signed HTTPS)")
    args = p.parse_args()
    sys.exit(asyncio.run(main_async(args)))


if __name__ == "__main__":
    main()
