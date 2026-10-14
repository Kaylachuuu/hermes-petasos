# hermes-petasos

Turns a Spotify Car Thing into a pocket voice interface for Athena on Hermes: a Hermes plugin plus a thin on-device client for push-to-talk, knob and button controls, and replies played wherever you are.

The server plugin lives in `petasos/`. Nothing runs on the Car Thing yet; the device program comes next.

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

## Step 3: the petasos plugin

Agreed with Kayla on 2026-10-14. The plugin in `petasos/` turns the Car Thing's button and knob events into commands for the device, using a layout file on the Hermes server.

| Control | Action |
| --- | --- |
| Front button, hold | Push-to-talk: speak while holding, release to send |
| Front button, tap | Start or end conversation mode: sends after about a second of silence, listens again after each reply, ends after a longer silence |
| Knob press | Stop Athena speaking |
| Knob turn | Volume |
| Presets 1–4 | One-touch requests you choose |
| Top-right button | Mic off |

The first version won't support interrupting by voice, because the mic would hear Athena through a nearby speaker. Press the knob instead.

### Install it

On Athenaeum, with the sync script (until this is merged, pass the branch):

```sh
sync-plugin petasos --branch claude/project-thread-48e57q --restart
```

The sync script links the plugin from the checkout, the same way it handles Talaria. Then turn it on with `hermes plugins enable petasos` (or add `petasos` under `plugins: enabled:` in `config.yaml`) and restart Hermes.

### Change the layout

The first button press creates `petasos/mapping.json` in the Hermes folder, holding the layout above. Edit it and the change applies on the next press; no restart needed. A file with a mistake is reported by `/petasos` and the last good layout stays in use. `/petasos` in any chat shows the current layout.

Each control (`preset-1` to `preset-4`, `mute`, `front`, `knob`) takes gestures: `press`, or `hold` and `tap` (told apart by `hold_ms`), and `turn` for the knob. Actions are `push_to_talk` (hold only), `conversation`, `stop_speaking`, `volume` (turn only), `mic_toggle`, `prompt` (with `"text"`) and `none`. For example, to make preset 3 ask about the weather:

```json
"preset-3": { "press": { "action": "prompt", "text": "What's the weather today?" } }
```

### Routes (behind the dashboard login)

| Route | What it does |
| --- | --- |
| `POST /api/plugins/petasos/input` | One event in, e.g. `{"input": "front", "action": "press", "t": 1234}`; returns `{"commands": [...]}` |
| `GET` / `PUT /api/plugins/petasos/mapping` | Read or replace the layout (a bad one is refused with the reasons) |
| `GET /api/plugins/petasos/status` | Each device's mic and listening state, and any layout problems |

Events: `press` and `release` for buttons, `turn` with `delta` for the knob, and from the client itself `device` `hello` (when it starts) and `conversation_ended`. `t` is the device's own clock in milliseconds; send it on every event or on none. Commands back: `listen` (`hold` or `conversation`, with the silence timings), `send`, `cancel`, `listen_stop`, `stop_speaking`, `volume`, `mic`, `prompt` and `notice`.

### Try it without the Car Thing

`tools/try_buttons.py` signs in like `chat_probe.py` and plays a set of presses against your server, printing what comes back:

```sh
~/.venvs/petasos/bin/python tools/try_buttons.py http://10.0.0.20:9119
```

Tested inside a fresh `hermes-agent` (`8f817911`): 26 tests in `petasos/tests`, and `try_buttons.py` against a running dashboard with password login.
