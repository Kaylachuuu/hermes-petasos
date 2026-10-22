# hermes-petasos

![Petasos, messenger of Hermes](art/petasos-social.png)

Turns a Spotify Car Thing into a pocket voice interface for Athena on Hermes: a Hermes plugin plus a thin on-device client for push-to-talk, knob and button controls, and replies played wherever you are.

The server plugin lives in `petasos/`, and the program that runs on the Car Thing in `client/`.

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
| Knob tap | Stop Athena speaking; when she isn't, pause or resume the music |
| Knob double tap | Next song |
| Knob triple tap | Back to the start of the song, or the previous one in its first 10 seconds |
| Knob hold, 1 second | Mic off or on |
| Knob turn | Volume (the Car Thing turns it itself, so a slow link doesn't make it lag) |
| Presets 1–4 | Soft keys you choose: one-touch requests, music actions, or a music page (SoundCloud, Navidrome, Spotify, Apple Music) |
| Top-right button | The other set of four soft keys (held two seconds: the settings) |

On a music page the soft keys are the page's own: Now Playing, Likes (Starred on Navidrome, Liked on Spotify), Playlists and Search, and none on Apple Music's. The front button's tap steps back instead (a list to Now Playing, Now Playing to the Petasos page), and in a list the knob moves the highlight and its press picks. Holding the front button still talks to Athena from any page.

The first version won't support interrupting by voice, because the mic would hear Athena through a nearby speaker. Press the knob instead.

### Install it

On Athenaeum, as the account Hermes runs under, with the sync script:

```sh
sync-plugin petasos --restart
```

The sync script links the plugin from the checkout, the same way it handles Talaria. Then turn it on with `hermes plugins enable petasos` (or add `petasos` under `plugins: enabled:` in `config.yaml`) and restart Hermes.

### Change the layout

In Hermes Desktop the same settings are under **Settings → Plugins → Petasos**
(`petasos/desktop/plugin.js`, which saves through the plugin's `/mapping` route). Desktop loads it
from the plugin's folder when Hermes runs on the same computer; with a remote Hermes, copy
`petasos/desktop/plugin.js` into `<Hermes home>/desktop-plugins/petasos/` on each computer (on a Mac
`~/.hermes`, on Windows `%LOCALAPPDATA%\hermes`), press **Rescan** in Capabilities → Plugins and
turn Petasos on. Repeat after an update that changes `plugin.js`.

The easiest way is the **Petasos** tab in the Hermes dashboard: the agent's name, and for each preset button what it does when pressed, what it asks and its label on the screen, with a preview of the top of the Car Thing's screen. It saves to the same file described below, with the same checks, and the Car Thing picks the change up on its next button press.

The preset buttons set there are the shared ones. Any device can have its own instead: a Car Thing in the Device tab of its settings, an app in its Settings page. Those are kept in the same file under `devices`, by device id (`GET`/`PUT /devices/<id>/presets`), and only the eight presets can differ; renaming a device carries its own presets to the new name.

The first button press creates `petasos/mapping.json` in the Hermes folder, holding the layout above. Edit it and the change applies on the next press; no restart needed. A file with a mistake is reported by `/petasos` and the last good layout stays in use. `/petasos` in any chat shows the current layout.

Each control (`preset-1` to `preset-4`, `preset-5` to `preset-8` for the second set of soft keys, `mute`, `front`, `knob`) takes gestures: `press`, or `tap`, `double_tap`, `triple_tap`, `hold` and `long_hold` (a release under `hold_ms` is a tap; a hold or long hold can give its own `"ms"`, and letting go before it does nothing), and `turn` for the knob. Actions are `push_to_talk` (hold only), `conversation`, `stop_speaking`, `volume` (turn only), `mic_toggle`, `prompt` (with `"text"`), `music_toggle`, `music_next`, `music_previous`, `music_stop`, `page_soundcloud`, `page_navidrome`, `page_spotify`, `page_apple` (open that page), `soft_keys` (show the other set) and `none`. A mapping that still has an older version's unchanged defaults (the knob's holds, the top-right button muting, presets 3 and 4 doing nothing, a second set starting with the Petasos page) gets today's; a soft key still set to `page_petasos` does nothing, since the front button goes home. While the knob's turn is `volume`, the screen command tells the device its `volume_step` (`knob_volume`) and the device turns the volume itself.

The device counts quick taps itself and reports them with the release (`"taps"`), since only it can wait to see whether another tap follows. It waits only while music is playing and Athena is quiet, so a tap that stops her is never delayed. While a control with timed holds is held, the screen shows what letting go would do, changing as each hold's time comes. For example, to make preset 3 ask about the weather:

```json
"preset-3": { "press": { "action": "prompt", "text": "What's the weather today?", "label": "Weather" } }
```

`agent_name` at the top of the file is the name of the agent the Car Thing talks to, as its screen shows it ("Hold the front button to talk to …", "Messenger of …") and in its messages. It defaults to `Hermes`; set it to your agent's name, for example `"agent_name": "Athena"`.

`car-play`, `car-next` and `car-previous` are a car's steering wheel media buttons, which reach the Car Thing over Bluetooth while it plays to the car, and `car-call` its call button. By default play/pause plays or pauses the music, the skip buttons skip, and the call button starts or ends a conversation; a mapping written before they existed, or still holding their earlier defaults (play starting a conversation), gets these. The Car Thing is a media player to the car (`client/carplayer.go`, registered with BlueZ): the car's screen shows the song's title, artist and length, and play and pause only do something when they'd change it, since many cars send play as they connect. The call button asks the Car Thing for voice recognition as it would a phone (`AT+BVRA=1` over the hands-free link, which petasos answers through BlueALSA's AT channel, `client/carcall.go`); a car only does so once the image says the Car Thing has it (image 0.3.0). Many cars ask on a long press of the call button. A Ford SYNC 2 keeps its call and voice buttons to itself, handing the voice button's hold only to an iPhone (Siri Eyes Free): the Bluetooth tab's "Shows itself as" cycles Speaker, Phone and iPhone, the last answering the car's AT+CGMI and AT+CGMM as Apple's. Holding a skip button reaches the Car Thing as `car-rewind` and `car-forward` (KEY_REWIND and KEY_FASTFORWARD), by default starting a conversation and stopping Athena speaking; the play a car sends as the button is let go is left alone.

`label` is the name the Car Thing's screen shows under the button (up to 32 characters, on two lines when it needs them). Without one, the screen shows the start of the prompt.

### Routes (behind the dashboard login)

| Route | What it does |
| --- | --- |
| `POST /api/plugins/petasos/input` | One event in, e.g. `{"input": "front", "action": "press", "t": 1234}`; returns `{"commands": [...]}` |
| `GET` / `PUT /api/plugins/petasos/mapping` | Read or replace the layout (a bad one is refused with the reasons) |
| `GET /api/plugins/petasos/status` | Each device's mic and listening state, and any layout problems |
| `GET /api/plugins/petasos/devices/{id}/commands` | Commands the agent's tools left for a device (music to play), handed over once |
| `GET /api/plugins/petasos/music/stream?source=&id=` | One library song as MP3 |
| `GET` / `PUT /api/plugins/petasos/music/settings`, `POST .../music/check` | The music sources (the Navidrome password is never sent back), and whether each one works |

Events: `press` and `release` for buttons, `turn` with `delta` for the knob, and from the client itself `device` `hello` (when it starts) and `conversation_ended`. `t` is the device's own clock in milliseconds; send it on every event or on none. Commands back: `listen` (`hold` or `conversation`, with the silence timings), `send`, `cancel`, `listen_stop`, `stop_speaking`, `volume`, `mic`, `prompt` and `notice`.

### Try it without the Car Thing

`tools/try_buttons.py` signs in like `chat_probe.py` and plays a set of presses against your server, printing what comes back:

```sh
~/.venvs/petasos/bin/python tools/try_buttons.py http://10.0.0.20:9119
```

Tested inside a fresh `hermes-agent` (`8f817911`): 26 tests in `petasos/tests`, and `try_buttons.py` against a running dashboard with password login.

## Step 4: the program on the Car Thing

`client/` is a small Go program (one static file, about 5 MB) that runs on the Car Thing. It reads the buttons and the knob, sends each press to the petasos plugin, and carries out the commands that come back. Sound isn't wired up yet, so for now it logs each command; the voice step fills them in.

- **Where it lives:** `/var/lib/petasos/`, on the Car Thing's data partition. Nocturne's system partition and `/etc` are read-only, but `/var` is writable and survives Nocturne updates.
- **Start at boot:** a systemd unit in `/etc/systemd/system`. Because `/etc` is read-only, the installer makes the system partition writable just long enough to add it. A Nocturne system update replaces that partition, so run the installer again afterwards.
- **Updates itself:** on start and every half hour it asks the plugin for the current version (`GET /api/plugins/petasos/client`), and if it differs, downloads it, checks its SHA-256 and restarts into it. Updating the plugin with the sync script is all it takes.
- **Signs in once:** the installer signs in to Hermes and gives the Car Thing its own session. The program renews it through `/auth/native/refresh` and saves each rotated token.
- **Takes the buttons:** by default it grabs the button and knob inputs, so Nocturne's own screen stops reacting to them. Install with `--no-grab` to share them.
- **Offline:** presses made while Hermes can't be reached are dropped, not replayed; it retries every 10 seconds and says `hello` on reconnecting so the plugin starts fresh.

### Install it

With the Car Thing plugged into a Mac by USB, from a folder holding `install_device.py` and `chat_probe.py`:

```sh
~/.venvs/petasos/bin/python install_device.py http://10.0.0.20:9119
```

It asks for your dashboard login, downloads the program from the plugin, and copies everything over one SSH connection (press Enter if ssh asks for a password). It finishes by printing the program's version, whether it's running, and its latest log lines. To watch it later: `ssh -4 root@petasos.local journalctl -u petasos -f`.

### Build it

```sh
client/build.sh            # writes client/dist/petasos-linux-arm64
```

Bump `client/VERSION` with every change: the Car Thing updates whenever the version the plugin serves differs from its own, and the Mac and Windows apps whenever it is newer. The build is reproducible, so the committed binary can be checked against the source.

Tested here: the plugin's routes in a fresh `hermes-agent` (27 tests), the event decoding (`go test`), a full run against a test Hermes (it updated itself from 0.0.9 to 0.1.0, then turned recorded presses into the expected commands), and the installer against a stand-in for ssh. Since then installed and running on the Car Thing (0.2.0, on the petasos image below).

## Step 5: our own Car Thing system, and getting online

Nocturne can't give petasos a network of its own (no Bluetooth tethering, no WireGuard), so the Car Thing runs our own image instead: `image/`, a layer on [yocto-superbird](https://github.com/JoeyEamigh/yocto-superbird). It adds Bluetooth and USB tethering and WireGuard to the kernel, Bluetooth audio, petasos itself (it starts at boot, then copies itself to `/var/lib/petasos` so it can update there), the winged hat boot logo, shown from power-on until petasos's screen appears (`image/splash/`), and root login over the USB cable only.

### Build and flash

On Athenaeum, from a yocto-superbird checkout (so the build cache is shared):

```sh
cd ~/Projects/yocto-superbird
KAS_CONTAINER_ENGINE=podman kas-container build ~/Projects/hermes-petasos/image/kas/petasos.yml
```

The result is `build/tmp/deploy/images/superbird/petasos-image-superbird-flashthing.zip`. Copy it to the Mac, hold buttons 1 and 4 while plugging the Car Thing in, and run `flashthing-cli` on it (install with rustup and `cargo install flashthing-cli`; Homebrew's Rust builds LLVM from source). Then run the installer as in step 4. On the USB cable the Car Thing is `petasos.local`; use `ssh -4`. If the Mac gets no `10.42.1.x` address on the cable, check that macOS Internet Sharing is off (or leaves the Car Thing out): it takes the cable into its own bridge. The image's Bluetooth audio also answers a car's hands-free connection (for the car's microphone, in the Device tab), so pair the car again after flashing for it to see that.

### Online through the phone

The Car Thing uses the phone's Personal Hotspot over Bluetooth. Pair once, from the phone: open Settings → Bluetooth → "Pair a new device" on the Car Thing, then pick the Car Thing in the phone's Bluetooth settings and confirm the code on both. Pairing from the phone's side matters: an iPhone the Car Thing paired with itself (with `bluetoothctl` or otherwise) shares its hotspot but never offers the Car Thing as a speaker.

From then on petasos reconnects to the hotspot by itself whenever the link drops (`client/tether.go`). A paired Bluetooth speaker (or headphones, or car stereo) is reconnected the same way, checked every 30 seconds. A computer can share its internet the same way: on a Mac, turn on Internet Sharing to Bluetooth PAN, then pair it from the Bluetooth tab. With several paired, petasos tries the one chosen with "Try this one first" before the others, and "Use its internet now" switches at once; the choice is kept in `network.json`. The installer's `--phone <address>` still pins one device and no other.

The phone's own VPN doesn't carry hotspot traffic, so the Car Thing has its own WireGuard peer. Make one on the home WireGuard server with `AllowedIPs` set to the home network only (such as `10.0.0.0/24`) and `PersistentKeepalive = 25`, save the config on the Mac, and install with:

```sh
~/.venvs/petasos/bin/python install_device.py http://10.0.0.20:9119 --wireguard ~/Downloads/petasos.conf
```

petasos turns it into a systemd-networkd tunnel (`wg-petasos`) under `/run` at start (`client/wireguard.go`). It refuses a config that sends all traffic home.

### Online through USB

From image 0.3.0, a phone can share its internet over USB instead, which keeps the Bluetooth radio for the car's audio and charges the phone. The Car Thing's one USB port is also its power, so the phone goes through a hub with power passed through (USB-C PD in, the Car Thing on the hub's host connector, the phone on one of its ports). Switch the port with Settings → Network → USB port: "Phone" makes the Car Thing the USB host (the BSP's `superbird-usb-role`, kept after a restart), "Computer" gives it back to a computer, for `ssh` over the cable (from petasos 0.29.4; before it, "Computer" kept a boot setting that left the cable without its network, which 0.29.4 clears as it starts). Then turn on the phone's Personal Hotspot (iPhone: trust the Car Thing when it asks; usbmuxd keeps that in `/var/lib/lockdown`, from image 0.3.1, so it asks only once) or USB tethering (Android). systemd-networkd gets an address from it (`40-petasos-usb-tether.network`, preferred to Bluetooth's), and petasos lets the Bluetooth tether go while USB is up, taking it back if the phone is unplugged (`client/tether.go`). The kernel has the iPhone (`ipheth`), RNDIS, NCM and CDC Ethernet drivers built in.

### An iPod's music

From image 0.4.0 and petasos 0.31.0, an iPod plugged into the Car Thing plays its music with no internet: on the iPod page (a soft key's "iPod page"), its albums, its playlists (with all its songs), and a search. Set the USB port to "Phone" and plug the iPod in, through the same powered hub when the phone is plugged in too; with its disk use on (a nano from the 5th generation on always has it), it shows up as a USB drive. petasos mounts it read-only at `/run/petasos/ipod` and reads its own database, `iPod_Control/iTunes/iTunesDB` (`iTunesCDB`, compressed, on the 6th and 7th nanos); a plain USB drive with music on it works too, from its files' tags (`client/ipod.go`, `ipod_linux.go`). ffmpeg, in the image, decodes the songs (AAC, MP3, Apple Lossless, WAV, AIFF) straight from it, through the same player as the home library. The kernel has USB storage, Mac and PC partition maps, FAT32 (an iPod set up on Windows), HFS+ (on a Mac) and exFAT built in. Songs bought from the iTunes Store before 2009 (`.m4p`) are copy-protected and left out.

## Step 6: voice

Hold the front button and talk; let go, and Athena's answer plays on the Bluetooth speaker. Tap it instead for conversation mode: the Car Thing sends each thing you say after a second of quiet and listens again after she answers, until you tap again or say nothing for a while. The knob press stops her mid-sentence, and turning it changes the speaker's volume. Presets ask their question the same way.

- **On the Car Thing** (`client/voice.go`): records with `arecord` from `hw:0,0` (16 kHz mono; the microphones only deliver 32-bit samples, which it cuts to 16-bit), finds the end of speech in conversation mode by loudness (it learns steady road noise), sends the recording, and plays the reply with `aplay` on `bluealsa` (the connected Bluetooth speaker). `mic` and `speaker` in `config.json` change those devices.
- **In the plugin** (`petasos/voice.py`): `POST /api/plugins/petasos/voice` takes the WAV, transcribes it with Hermes' own speech-to-text (`stt:` in `config.yaml`), sends the words to Athena over the dashboard's chat connection as a voice turn (`voice_turn`, `surface: voice-live`, which asks for a short spoken answer), and turns her reply into speech with Hermes' text-to-speech (`tts:`). `POST /ask` does the same for typed text (presets). The reply audio is fetched from `GET /voice/audio/<id>`.
- **One conversation per device** ("Petasos · Car Thing", "Petasos · Office Mac"), or one shared by all of them ("Petasos") if the dashboard's Petasos tab says so. It is resumed every time and listed under its own source, `petasos`, so the WebUI and the desktop app can show Petasos chats apart from the rest. The ids are kept in `<hermes root>/petasos/chats.json`; delete an entry to start fresh.
- **Approvals** can't be answered from the Car Thing; they're declined and Athena says what she couldn't do.
- **"Hey Athena"** (`client/wakeword.go`, `client/wakelisten.go`): while nothing else is going on and the mic isn't muted, petasos listens for the wake word itself, on the device, with nothing sent anywhere. Hearing it plays a short chime and starts a conversation, as a tap on the front button does, except that it ends after 8 seconds without speech rather than the usual wait. On the Car Thing it always listens through its own microphones, so the car's hands-free link isn't held open. The Device tab's **Wake word** row turns it on or off (on by default on the Car Thing, off in the apps, where the settings page has it under Audio). The model is openWakeWord's (its shared audio front end, plus a small "Hey Athena" head trained on generated speech), run in plain Go; `tools/wakeword/` regenerates it.

For hermes-whisper and hermes-kokoro, Hermes' config needs:

```yaml
stt:
  provider: openai
  openai: {base_url: "http://10.0.0.22:PORT/v1"}
tts:
  provider: openai
  openai: {base_url: "http://10.0.0.23:PORT/v1", api_key: "not-needed", voice: "af_heart"}
```

(The speech side needs some `api_key`; any text works for a local server.)

## Step 7: the screen

The Car Thing's screen shows what's going on: the preset labels under their buttons, the phone, home, speaker and mic links down the left edge (on every page), and in the middle, the Petasos title with a line that follows Athena: "Getting ready…" from the press until the mic is really recording, then "Athena is listening", "Athena is thinking…" under what she heard, and her reply while she speaks (a long one scrolls). Turning the knob lights the top Greek key band up to the volume; while Athena listens it follows your voice, and while she speaks or music plays, the sound, under a mark at the volume. The song playing shows above the bottom band, which fills with its progress. When a link drops, the warning emblem appears on the left and the line says what to do.

- **Served by petasos** (`client/screen.go`, `client/screen/`): the page and its fonts are built into the program and served on `127.0.0.1:8187`; the state reaches the page as server-sent events from `/events`. The image's kiosk browser is pointed at it over its DevTools port, 9223 (`client/kiosk.go`), so the screen arrives with petasos's own updates and works on an image built before it existed.
- **Settings** (`client/settings.go`): hold the mic button for two seconds. While they're open the preset buttons are tabs: the first for Bluetooth, the second for Screen, the third for Device (the fourth is spare). The knob moves and chooses, and the front button closes them. A short press of the mic button shows the other set of soft keys, on release.
- **Screen tab** (`client/backlight.go`): brightness (Auto, from the ambient light sensor, or 10–100%, lit along a Greek key band at the bottom the way the volume is), how long until the screen dims (Off, 15, 30, 45 or 60 seconds) and how far, and how long until it turns off (the same choices). The Greek key band sits under the tabs, where it is on the main screen, and lights up to the level while Brightness or Dim to is highlighted. Press the knob on a row, turn to change it (the screen follows as you turn), press again. Any button, the knob or a touch on the screen wakes it, and a voice turn, notice or volume change keeps it awake; a preset pressed while the screen is off only wakes it, since its label can't be seen. Kept in `screen.json` next to `config.json`.
- **Music from the phone** (`client/music.go`): the Car Thing is also a Bluetooth speaker to the phone. Once it is online through the phone it connects the phone's music link too, as a car stereo does when it comes on (an iPhone only lists a speaker whose link is up), and it calls itself a loudspeaker (its Bluetooth class); a phone paired before 0.22.3, or paired from the Car Thing's side, has to forget it and pair again from the phone. Choose it as the phone's audio output (on an iPhone, the AirPlay button in Control Center or the music app) and petasos passes the phone's music on to the car (`bluealsa-aplay`). The home screen shows what is playing; the car's next and previous buttons skip songs, and a preset can be set to Play / pause, Next song or Previous song (the plugin's `music_toggle`, `music_next`, `music_previous`). The phone is paused while Athena listens and speaks, and plays again after, unless it had been paused already.
- **Music from the home library** (`petasos/music.py`, `client/library.go`): ask Athena to play something ("play Rumours", "put on some Daft Punk", "play my Road Trip playlist") and it plays on the device you asked from. The sources are a Navidrome server (any Subsonic server works) and folders on the Hermes machine, network shares included once they are mounted there; set them in the Music section of the Mac or Windows app's Settings, in Hermes Desktop under Settings → Plugins → Petasos, the Music card on the dashboard's Petasos page (when Hermes runs its dashboard), or `<Hermes home>/petasos/music.json`. SoundCloud is a third source, after your own music: register an app at soundcloud.com/you/apps with the redirect URI `http://127.0.0.1:8187/app/soundcloud`, save its client ID and secret, and public tracks play; sign in from the Mac or Windows app's Settings → Music, which opens SoundCloud's sign-in in your browser (Hermes keeps the tokens, so every device uses that sign-in) and your likes and playlists play too. SoundCloud streams are HLS AAC, so it needs ffmpeg on Hermes. Hermes keeps SoundCloud's songs in `petasos/music-cache/` so a song played again isn't downloaded or converted again: `source` holds SoundCloud's own copy (the artist's upload when they allow downloads, otherwise the stream's AAC as it comes, not re-encoded) and `device` the MP3 made from it at the quality set; a new quality converts from the kept copies. Each is kept only once it has all come, and the songs played longest ago go first once a cache is full. Their sizes (music.json's `cache`, `source` 10 GB and `device` 2 GB by default, 0 to keep nothing) are set in the Music settings. The music pages' pictures come through Hermes too (`/music/art`, kept in `music-cache/art/`), which also gives Navidrome's songs and playlists their covers; the device keeps the latest in memory and your playlists' pictures on disk in `art/` beside `config.json`, each written once. The Car Thing's settings have a Music tab (the fourth preset) with the same fields, typed on its keyboard. The plugin gives the agent two tools, `play_music` and `music_control` (pause, resume, next, previous, stop, repeat one or all or off, shuffle on or off). A song named in full plays once and stops; an album, artist or playlist plays through and stops, unless Athena is asked to repeat it; the queue reaches the device with the reply, or within a few seconds when asked from another chat. The device streams each song from `/api/plugins/petasos/music/stream` as MP3 (Navidrome converts to 192 kbps; folder files that aren't MP3 go through ffmpeg, so only MP3s play without it), decodes it and plays it at 48 kHz. The streaming quality (music.json's `quality`: `high` 192 kbps, the default, `normal` 128, `low` 96 or `lowest` 64, set at the top of the Music settings) is the bit rate it is sent at; lower stutters less over a slow link, and below high even MP3 files are converted. Like the phone's music, it stops while Athena listens and speaks and picks up where it was. What was playing (the queue, the song and how far in, its name, repeat and shuffle) is kept in `library.json` beside `config.json` and comes back paused after a restart or an update. Folder songs are found by their tags when `mutagen` is installed in Hermes's Python, otherwise by their paths (Artist/Album/01 Title.mp3); the index is kept in `petasos/music-index.json` so a big share is only read once. The apps play it on the computer's speakers.
- **Device tab** (`client/device.go`): this Car Thing's name, its microphone, the wake word, the VPN tunnel home (shown once `wireguard.conf` is installed; turn it off when the Car Thing gets online through a network that reaches Hermes directly, such as an Android phone sharing the home Wi-Fi over Bluetooth) and its own soft keys. The name is what Bluetooth shows it as and names its chat ("Petasos · Kayla's Kia"); renaming it starts a new chat and restarts petasos. Text is typed on an on-screen keyboard, by touch or by turning the knob to a key and pressing it; Done saves, the front button cancels. The microphone is the Car Thing's own or the car's: with the car's chosen, petasos records over the car's hands-free link (BlueALSA's HFP audio gateway, `bluealsa:PROFILE=sco`) while the car is connected, and from its own microphones otherwise. Soft keys start as the shared ones; changing one gives this Car Thing its own set, and "Use the shared soft keys" goes back. The name and microphone are kept in `device.json`, which the installer leaves alone. At the foot, Versions shows (and pressing Petasos's row checks for and installs an update now, its download shown on the screen; any music stops first) the system image (`VERSION_ID` and the build day from `/etc/os-release`; bump `DISTRO_VERSION` in `image/meta-petasos/conf/distro/petasos.conf` with each image), the petasos program, and the plugin on Hermes. The apps show the last two at the foot of their own Device tab.
- **Music pages** (`client/musicpage.go`, `client/pagecontrols.go`): a soft key set to a page opens it, and there the soft keys open its views. Each service has its colour: SoundCloud orange, Navidrome blue (with your folders), Spotify green, Apple Music red; Petasos itself is bronze. Now Playing and the lists (Likes or Starred or Liked, Playlists, Search) are on the soft keys, the open one lit, and each fills the space between the meanders: what's playing has its artwork and big touch buttons for previous, play/pause, next, repeat (all, one, off) and shuffle; a list is touched, or worked with the knob. A page shows only its own service's song: with Apple Music playing, SoundCloud's page says nothing is playing. Picking a song plays its list from there and goes back to what's playing. The top meander shows the sound as it plays (Athena's voice and the library's music, worked out as petasos plays them, `client/audiometer.go`) under a mark at the volume, or the volume alone; the bottom one is the song's progress, in its service's colour, on the Petasos page too, under the song's name. Spotify's page is a remote for the Spotify app on the phone (needs Premium): sign in from the app's Settings → Music with your own app from developer.spotify.com (redirect URI `http://127.0.0.1:8187/app/spotify`), and what you pick plays on the phone and reaches the Car Thing over Bluetooth. Apple Music has no lists: choose on the iPhone, or ask Athena, and the page shows what's playing with its controls. The pages step aside for the home screen while Athena listens, thinks or speaks, and the wake word only listens on the Petasos page. The lists come from the plugin's `/music/browse`.
- **Bluetooth tab** (`client/bluetooth.go`): lists the paired phones and computers it can get online through (one marked first) and the speakers, connects, disconnect and forget them, and pair new ones: "Pair a new device" looks for speakers in pairing mode and makes the Car Thing visible to a phone's Bluetooth settings. Pair a phone from the phone, never from the Car Thing (phones aren't listed): an iPhone that the Car Thing paired with gives it the hotspot but never offers it as a speaker. Pairing codes show on the screen, confirmed with the knob.
- Fonts: Caladea (Apache 2.0) and Source Sans 3 (OFL 1.1); see `client/screen/fonts/README`.

## Step 8: the virtual Petasos on a Mac

`mac/` is a small Mac app that shows the same Petasos screen in a window and talks to Hermes the
same way the Car Thing does, without the Hermes desktop app. It runs the petasos program (built
for macOS into `client/dist`) in the background, in a mode with no device behind it: buttons
under the screen stand in for the hardware, and the Mac's microphone and speakers do the audio.
The window records the microphone itself, the way the Hermes desktop app does
(`client/pagemic.go`), which copes with AirPods switching into call mode as recording starts.

### Build it

Needs the Xcode command line tools (`xcode-select --install`); Go is not needed.

```sh
cd mac && ./install.sh
```

`install.sh` builds it, quits a running copy, puts it in Applications (`~/Applications` if
`/Applications` needs an administrator) and opens it; run it again after `git pull` to update.
`./build.sh` alone builds `Petasos.app` in `mac/` without installing it. Its icon is the
winged hat from the boot logo (`mac/icon/`); the window keeps the Car Thing's proportions as it
is resized.

The first time it shows a sign-in page for the Hermes address and the dashboard sign-in, then
keeps its own renewable sign-in in `~/Library/Application Support/Petasos`, like the Car Thing. macOS asks for
the microphone the first time you talk. **Sign In Again…** and **Show Log** are in the app menu.

### Controls

| Key | Does |
|---|---|
| Hold Space (or the talk button) | talk, as the front button does |
| Tap Space | start a conversation |
| Enter | the knob's press: stop speaking, or play and pause; hold it for the mic on or off |
| M | the other set of soft keys |
| 1 to 4, or click a label | the presets |
| Arrow keys, or scroll | the Mac's volume |

It shows as its own device in the plugin, so the presets and agent name from the dashboard's
Petasos tab apply here too.

### Updates

The program inside the app updates itself from the plugin, as the Car Thing's does: on start and
every half hour it asks for the macOS (or Windows) build of the current version, checks its
SHA-256, saves it in `engine/` beside the app's settings and restarts into it. The copy inside
the app is never changed (on a Mac it is signed with the app), so it only moves forward: a
download runs only while it is newer than the app's own copy, and rebuilding the app with a newer
version simply wins. Deleting `engine/` goes back to the app's own copy. The window around it
(menus, the microphone and speakers) still changes only with a rebuild.

The pin at the right of the controls keeps the Petasos window above other windows, or stops
(macOS has no pin for windows in general, but an app can float its own).

### Settings

**Settings…** (⌘,) or the cog under the screen opens the app's settings page. The page belongs to
the petasos program, so the Windows and Linux apps share it. Its tabs follow the Car Thing's,
less Bluetooth and the screen's light, which the computer looks after itself.

- **Device**: this computer's name (its chat with the agent is "Petasos · <name>" unless the
  dashboard's Petasos tab has all devices share one chat, "Petasos"), the microphone Petasos
  records from (by default the one the computer's sound settings use; AirPods and other
  Bluetooth headsets drop to call quality while their microphone is on, so choosing the Mac's
  own keeps them sounding their best for the replies), the wake word, and the versions.
- **Display**: the sound bands (Bands, Response, Decay, Peaks) and the screensaver (which, Start
  after, Preview), as on the Car Thing; these take effect at once. A key, a click or the mouse
  moving over the window ends the screensaver. On a Mac, Timing lines the bands up with the
  speaker playing now (its own, AirPods over Bluetooth, an AirPlay speaker, as macOS's
  `system_profiler` says, looked at every 10 seconds; `client/macoutput.go`), with Beep's test
  beeps (Beep goes back to the screen, where the bands can be seen, with Earlier and Later over
  them until Done), and each speaker keeps its own, as the Car Thing's Bluetooth speakers do.
- **Network**: the server and account signed in to, with Log out. The VPN: WireGuard, built
  into the program. Only Petasos's own connection to Hermes goes through it, so the rest of the
  computer is untouched and no administrator password is needed. Choose one of the VPN
  configurations, and whether to use it always or only when Hermes can't be reached directly;
  the network icon on the screen turns into a shield while it is in use. The configurations are
  the Car Thing's too, shared through Hermes: add one by pasting or importing a `.conf` (give
  each device a peer of its own), and leave out its PrivateKey to have a key made here.
- **Music**: the music sources on Hermes, and how much of a song to have in before it plays.
- **Soft keys**: the shared ones, or this computer's own: for each of the eight (two sets of
  four), what it does, what it asks and its label.

### Windows

The Windows app is the petasos program itself, built for Windows: `client/dist/petasos-windows-amd64.exe`
(or `-arm64.exe`). Copy it anywhere, rename it `Petasos.exe` if you like, and run it. It shows
the same screen, sign-in and settings pages as the Mac app, in a WebView2 window (part of
Windows 10 and 11), runs the program behind it from the same file, and records and plays
through Windows's own audio. It keeps its files in `%APPDATA%\Petasos`, `petasos.log` among
them. The volume keys (arrows, scroll, − and +) press Windows's own volume keys, so Windows
shows its volume display. The file isn't signed, so Windows SmartScreen asks once before the
first run: **More info**, then **Run anyway**.

Neither app's window has a title bar: drag it by the screen's background, and use the − and ×
at the right of the controls to minimize or close it. Resizing keeps its shape. Settings →
Display → Title bar → Shown gives it the computer's usual title bar instead (`title_bar` in
`window.json`, which the apps watch; the − and × then go). The window is the app's own part, so
this needs an app built from this version or later.

The screen keeps the Car Thing's shape in a window of any size, in both apps.

### Linux

On a Linux desktop (a Raspberry Pi 5 with Ubuntu and KDE, say), the app is the same program the
Car Thing runs, as `petasos desktop`. From a checkout of this repository:

```sh
linux/install.sh
```

It puts the program in `~/.local/share/petasos` and Petasos in the app menu, with the winged hat
for its icon. The screen shows in the app window of a Chromium-family browser (Chromium, Chrome,
Brave, Edge or Vivaldi: `sudo snap install chromium` if there's none), with its own profile, so
it is a window of its own and closing it quits Petasos; without one it opens in a browser tab.
The window records the microphone itself, as on the Mac; replies play through PulseAudio or
PipeWire (`paplay`), and the arrows and wheel change the desktop's volume (`pactl`). Its files
are in `~/.config/Petasos`, `petasos.log` among them, and it updates itself from the plugin like
the other apps. The window keeps the desktop's own title bar, and there's no pin, since a
browser's window can't keep itself on top.

### Petasos chats in the Hermes desktop app

Petasos makes its chats with their own source, `petasos`. The Hermes desktop app gives a source
its own sidebar section only if it is on the app's list, so the `claude/project-thread-48e57q`
branch of Kaylachuuu/hermes-agent adds it (`apps/desktop/src/lib/session-source.ts`). Chats made
before this have no source of their own and stay in the main list.
