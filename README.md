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

## Step 2: name the Car Thing's buttons and knob

`tools/list_inputs.sh` runs on the Car Thing itself (plain busybox shell, nothing to install). It lists the input devices Linux sees, then prints every button press and knob turn for 60 seconds. Nothing on the device is changed.

From a Mac or Linux computer with the Car Thing plugged in by USB (on Nocturne's start screen, pick **Connect to PC** if it asks):

```sh
ping -c 2 nocturne.local                                   # should answer within a few seconds
ssh root@nocturne.local 'sh -s' < tools/list_inputs.sh     # add a number after -s for longer, e.g. 'sh -s 120'
```

While it listens, press each top button from left to right, then the front button, press the knob, and turn the knob a few clicks each way. Leave the touchscreen alone, or it floods the output.

- **Signing in:** Nocturne is built on yocto-superbird, whose images allow `root` with an empty password over USB. If `ssh` asks for a password, press Enter.
- **If `nocturne.local` doesn't answer:** run `ifconfig | grep -E "inet (10\.42\.1|172\.16\.42)"` on the Mac.
  - A `10.42.1.x` address means current Nocturne. The Car Thing hands the Mac an address and keeps the block's third one for itself: round the Mac's last number down to a multiple of 8 and add 2 (Mac `10.42.1.43` → Car Thing `10.42.1.42`).
  - A `172.16.42.x` address means an older, stock-based firmware. The Car Thing is then `172.16.42.2`.
  - No such address means the Mac sees no USB network at all: pick **Connect to PC** on the Car Thing, then try another cable or port.

### Confirmed on hardware

Kayla ran it on 2026-10-14 against current Nocturne (yocto, flashed with Terbium), reached at `nocturne.local` over USB from a Mac with an empty root password. Every control matched the community device tree:

| Control | Input device | Linux event |
| --- | --- | --- |
| Top buttons 1–4, left to right | `gpio-keys-polled` (`event0`) | `KEY_1`–`KEY_4` (codes 2–5) |
| Top-right button | `gpio-keys-polled` (`event0`) | `KEY_M` (50) |
| Front button | `gpio-keys-polled` (`event0`) | `KEY_ESC` (1) |
| Knob press | `gpio-keys-polled` (`event0`) | `KEY_ENTER` (28) |
| Knob turn | `rotary` (`event1`) | `REL_HWHEEL` (relative axis 6), ±1 per click; the first direction turned gave −1 |

The other inputs are the touchscreen (`tlsc6x`, `event2`, multitouch absolute events) and `Nocturne (… ) (MCS)` (`event3`), a Bluetooth media-control input Nocturne creates. The client should find devices by name, not by `eventN` number, since numbering can change.

Older Nocturne builds (3.0.0 and 4.0.x) use RNDIS at `172.16.42.2`, which macOS doesn't support; current Nocturne is the target.

## Planned control layout (step 3)

Agreed 2026-10-14. The mapping will live in an editable file on the Hermes server.

| Control | Action |
| --- | --- |
| Front button, hold | Push-to-talk: speak while holding, release to send |
| Front button, tap | Start or end conversation mode: sends after about a second of silence, listens again after each reply, ends after a longer silence |
| Knob press | Stop Athena speaking |
| Knob turn | Volume |
| Presets 1–4 | One-touch requests you choose |
| Top-right button | Mic off |

The first version won't support interrupting by voice, because the mic would hear Athena through a nearby speaker. Press the knob instead.
