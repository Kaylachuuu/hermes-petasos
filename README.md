# hermes-petasos

Turns a Spotify Car Thing into a pocket voice interface for Athena on Hermes: a Hermes plugin plus a thin on-device client for push-to-talk, knob and button controls, and replies played wherever you are.

Nothing here runs on the Car Thing yet. This repository starts with the groundwork checks.

## Step 1: can a non-browser client chat with Hermes?

The Car Thing client will be a small program, not a browser. `tools/chat_probe.py` checks that such a program can sign in to the Hermes dashboard, hold a reusable token, open the dashboard's chat connection (`/api/ws`) with it, and renew that token without a browser.

### Run it

You need Python 3.10 or newer, on any computer on your home network (or on your VPN).

```powershell
python -m pip install websockets
python tools\chat_probe.py http://<hermes-server>:9119
```

Use the address and port you open the Hermes dashboard on, including any path prefix.

- **Password sign-in:** it asks for your dashboard username and password. The password is sent only to your Hermes server.
- **Sign-in through another provider:** sign in to the dashboard in a browser, copy the `hermes_session_at` cookie value (dev tools → Application/Storage → Cookies), then run it with `--token <value>`.
- **Sending a real message:** add `--say "your message"`. This sends that one message to Athena and streams her reply. She will see it like any other chat, and may remember it.

Without `--say`, the probe opens an empty chat and closes it again, without sending anything.

### What it checks

1. `GET /api/status` (public): whether the dashboard requires sign-in, and which sign-in methods it offers.
2. Password sign-in, which gives a session access token. Or it uses the token you passed.
3. The token on a normal API call (`Authorization: Bearer`).
4. **`/api/ws?token=<access token>`**, the path the Car Thing would use. It waits for `gateway.ready`, then calls `session.create`, and with `--say` calls `prompt.submit` and streams `message.delta` up to `message.complete`.
5. For comparison, a 30-second single-use ticket from `POST /api/auth/ws-ticket`. This is the path the web dashboard uses.
6. Renewing the token with `POST /auth/native/refresh`, which the device would need to stay signed in.

### Already verified

All six steps passed against a fresh `hermes-agent` checkout (`8f817911`, 2026-10-13). The dashboard was bound to `0.0.0.0` with the built-in username/password provider, and the probe connected over a non-loopback address. The reply in that test was an error, because that throwaway install had no model configured. The connection, session and streaming all worked.

Things learned that the Car Thing client must handle:

- **Heartbeats:** the gateway expects `gateway.ping` while connected, every 15 seconds in the probe.
- **Token lifetime:** with the username/password provider, the access token lasts 12 hours and the refresh token 30 days. Each refresh rotates the refresh token, so the device must save the new one every time.
- **Sessions are tied to the socket:** when the WebSocket drops, Hermes closes or detaches the sessions it opened (`ws_disconnect`). The device must reconnect and resume.
- **A refused connection shows up as HTTP 403.** Hermes rejects the WebSocket upgrade before it opens, so a bad token and a blocked request look the same from the client.
- **Proxy:** `websockets` 15+ follows the system proxy by default. The probe turns that off unless you pass `--use-proxy`.
