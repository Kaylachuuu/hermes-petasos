"""Petasos music: the home library, played on the Car Thing.

Two kinds of source, set in ``<Hermes home>/petasos/music.json``:

    {"navidrome": {"url": "http://10.0.0.20:4533", "username": "kayla", "password": "..."},
     "folders": ["/srv/music", "/mnt/nas/music"]}

Navidrome (or any Subsonic server) is searched through its API and streams as MP3 at the
bit rate set here. A folder is any directory Hermes can read, a mounted network share
included; its files are indexed by their tags (mutagen, when installed) or else by their
paths (Artist/Album/01 Title.mp3), and anything that isn't MP3 is converted with ffmpeg.

Athena chooses what plays with the ``play_music`` tool and controls it with
``music_control``. Both leave a command for the device (``pending``), which picks it up with
its next request or its regular check (``/devices/<id>/commands``); the device then streams
each track from the plugin's ``/music/stream`` route, so the Navidrome password and the
folders never leave Hermes.

SoundCloud is a third source. Spotify isn't one: the device can't stream it, so the music page
only browses it and has the Spotify app on your phone play what you choose (``Spotify``).
"""
from __future__ import annotations

import hashlib
import json
import os
import random
import re
import secrets
import shutil
import subprocess
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Iterator

AUDIO_SUFFIXES = {".mp3", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wav", ".wma", ".aiff", ".alac"}
MAX_QUEUE = 200
BIT_RATE = 192
# The streaming quality chosen in the settings (music.json's "quality"): the MP3 bit rate the
# device is sent. Lower ones stutter less over a slow link (a phone's hotspot and the VPN).
QUALITIES = {"high": 192, "normal": 128, "low": 96, "lowest": 64}


def bit_rate() -> int:
    """The bit rate to stream at, in kbps."""
    return QUALITIES.get(config().get("quality"), BIT_RATE)
INDEX_MAX_AGE_S = 30 * 60
LAST_TURN_S = 15 * 60  # a play_music call within this of a device's turn is for that device


def _root() -> Path:
    try:
        from hermes_constants import get_default_hermes_root
        return Path(get_default_hermes_root())
    except ImportError:
        from hermes_constants import get_hermes_home
        return Path(get_hermes_home())


def petasos_dir() -> Path:
    return _root() / "petasos"


def _read_json(path: Path, default):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return default


def _write_json(path: Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + f".{os.getpid()}.tmp")
    tmp.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
    tmp.replace(path)


def config() -> dict:
    cfg = _read_json(petasos_dir() / "music.json", {})
    return cfg if isinstance(cfg, dict) else {}


# --- which device, and what it should do --------------------------------------------------------

def note_turn(device: str) -> None:
    """Records that a device is in a turn with the agent, so music asked for in it plays there."""
    _write_json(petasos_dir() / "last-turn.json", {"device": device, "at": time.time()})


def device_for(requested: str | None, session_id: str | None = None) -> str:
    """The device a request is for: the one named, else the one whose chat this is, else the one
    that last asked something (when chats are shared, or the request came from elsewhere)."""
    if requested:
        # Only a device that has asked for its commands: a name the agent made up ("Car Thing")
        # would leave them where no device looks, and the music would carry on.
        seen = _read_json(petasos_dir() / "devices.json", {})
        want = re.sub(r"[^a-z0-9]", "", requested.lower())
        for ident in (seen if isinstance(seen, dict) else {}):
            if re.sub(r"[^a-z0-9]", "", ident.lower()) == want:
                return ident
    if session_id:
        chats = _read_json(petasos_dir() / "chats.json", {})
        for key, sid in (chats.items() if isinstance(chats, dict) else ()):
            if sid == session_id and key != "*":
                return key
    last = _read_json(petasos_dir() / "last-turn.json", {})
    if isinstance(last, dict) and last.get("device") and time.time() - float(last.get("at") or 0) < LAST_TURN_S:
        return str(last["device"])
    return "car-thing"


_PENDING_LOCK = threading.Lock()


def leave(device: str, command: dict) -> None:
    """Leaves a command for the device to pick up."""
    path = petasos_dir() / "pending.json"
    with _PENDING_LOCK:
        pending = _read_json(path, {})
        if not isinstance(pending, dict):
            pending = {}
        queue = [c for c in pending.get(device, []) if isinstance(c, dict)]
        if command.get("op") == "play":  # a new queue replaces anything not yet picked up
            queue = [c for c in queue if c.get("op") != "play"]
        queue.append(command)
        pending[device] = queue[-10:]
        _write_json(path, pending)


def take(device: str) -> list[dict]:
    """The commands left for a device, removed as they are handed over."""
    path = petasos_dir() / "pending.json"
    with _PENDING_LOCK:
        seen = _read_json(petasos_dir() / "devices.json", {})
        if not isinstance(seen, dict):
            seen = {}
        if time.time() - float(seen.get(device) or 0) > 3600:  # the devices there are, for device_for
            seen[device] = time.time()
            _write_json(petasos_dir() / "devices.json", seen)
        pending = _read_json(path, {})
        if not isinstance(pending, dict) or not pending.get(device):
            return []
        out = pending.pop(device)
        _write_json(path, pending)
    return [c for c in out if isinstance(c, dict)]


# --- the sources --------------------------------------------------------------------------------

def _track(source: str, ident: str, title: str, artist: str = "", album: str = "", seconds: int = 0,
           art: str = "") -> dict:
    t = {"source": source, "id": ident, "title": title or "Untitled", "artist": artist or "",
         "album": album or "", "seconds": int(seconds or 0)}
    if art:
        t["art"] = art  # a picture for the music page, where the source has one anyone can fetch
    return t


def _playlist_entry(source: str, ident: str, title: str, count: int = 0, art: str = "") -> dict:
    return {"source": source, "id": ident, "title": title or "Untitled", "count": int(count or 0), "art": art or ""}


class MusicError(RuntimeError):
    pass


class Navidrome:
    """A Subsonic API server: Navidrome, or Airsonic, Gonic and the like."""

    def __init__(self, cfg: dict) -> None:
        self.url = str(cfg.get("url") or "").rstrip("/")
        self.username = str(cfg.get("username") or "")
        self.password = str(cfg.get("password") or "")
        if not (self.url and self.username and self.password):
            raise MusicError("music.json's navidrome needs url, username and password")

    def _params(self, **extra) -> dict:
        salt = secrets.token_hex(6)
        token = hashlib.md5((self.password + salt).encode()).hexdigest()
        return {"u": self.username, "t": token, "s": salt, "v": "1.16.1", "c": "petasos", "f": "json", **extra}

    def url_for(self, method: str, **params) -> str:
        return f"{self.url}/rest/{method}.view?" + urllib.parse.urlencode(self._params(**params))

    def call(self, method: str, **params) -> dict:
        try:
            with urllib.request.urlopen(self.url_for(method, **params), timeout=15) as resp:
                body = json.loads(resp.read())
        except (OSError, ValueError) as exc:
            raise MusicError(f"Navidrome didn't answer: {exc}") from None
        answer = body.get("subsonic-response") or {}
        if answer.get("status") != "ok":
            raise MusicError(f"Navidrome: {(answer.get('error') or {}).get('message') or 'failed'}")
        return answer

    @staticmethod
    def _song(s: dict) -> dict:
        return _track("navidrome", str(s.get("id")), s.get("title"), s.get("artist"), s.get("album"), s.get("duration"))

    def find(self, query: str, kind: str) -> tuple[str, list[dict]]:
        """What to play for a request: a playlist, an album, an artist's songs or songs."""
        if kind in ("playlist", "any"):
            lists = self.call("getPlaylists").get("playlists", {}).get("playlist", [])
            match = _best(query, lists, lambda p: p.get("name", ""))
            if match and (kind == "playlist" or _score(query, match.get("name", "")) >= 0.99):
                songs = self.call("getPlaylist", id=match["id"]).get("playlist", {}).get("entry", [])
                return f"the playlist {match['name']}", [self._song(s) for s in songs]
            if kind == "playlist":
                return "", []
        if kind == "random" or not query.strip():
            songs = self.call("getRandomSongs", size=50).get("randomSongs", {}).get("song", [])
            return "a random mix", [self._song(s) for s in songs]
        found = self.call("search3", query=query, songCount=50, albumCount=10, artistCount=5).get("searchResult3", {})
        if kind in ("album", "any") and found.get("album"):
            album = _best(query, found["album"], lambda a: f"{a.get('name', '')} {a.get('artist', '')}")
            if kind == "album" or _score(query, album.get("name", "")) >= 0.99:
                songs = self.call("getAlbum", id=album["id"]).get("album", {}).get("song", [])
                return f"{album.get('name')} by {album.get('artist')}", [self._song(s) for s in songs]
        if kind in ("artist", "any") and found.get("artist"):
            artist = _best(query, found["artist"], lambda a: a.get("name", ""))
            if kind == "artist" or _score(query, artist.get("name", "")) >= 0.99:
                albums = self.call("getArtist", id=artist["id"]).get("artist", {}).get("album", [])
                songs: list[dict] = []
                for album in albums[:20]:
                    songs += self.call("getAlbum", id=album["id"]).get("album", {}).get("song", [])
                return f"songs by {artist.get('name')}", [self._song(s) for s in songs]
        songs = found.get("song", [])
        if songs:
            one = kind == "song" or _names_song(query, songs[0].get("title") or "")
            return (f"{songs[0].get('title')} by {songs[0].get('artist')}" if one else f"songs matching {query}",
                    [self._song(s) for s in (songs[:1] if one else songs)])
        return "", []

    def stream_url(self, ident: str) -> str:
        return self.url_for("stream", id=ident, format="mp3", maxBitRate=bit_rate())

    # The music page's lists.
    def playlists(self) -> list[dict]:
        lists = self.call("getPlaylists").get("playlists", {}).get("playlist", [])
        return [_playlist_entry("navidrome", str(p.get("id")), p.get("name"), p.get("songCount")) for p in lists]

    def playlist_tracks(self, ident: str) -> list[dict]:
        return [self._song(s) for s in self.call("getPlaylist", id=ident).get("playlist", {}).get("entry", [])]

    def likes(self) -> list[dict]:
        """Your starred songs."""
        return [self._song(s) for s in self.call("getStarred2").get("starred2", {}).get("song", [])]

    def search(self, query: str) -> list[dict]:
        found = self.call("search3", query=query, songCount=30, albumCount=0, artistCount=0).get("searchResult3", {})
        return [self._song(s) for s in found.get("song", [])]


def _words(text: str) -> list[str]:
    return re.findall(r"[a-z0-9]+", text.lower().replace("&", " and "))


def _score(query: str, text: str) -> float:
    """How well text matches the query: the share of the query's words found in it."""
    want = _words(query)
    if not want:
        return 0.0
    have = set(_words(text))
    return sum(1 for w in want if w in have) / len(want)


def _names_song(query: str, title: str) -> bool:
    """Whether the request names this one song: every word of its title is in the query
    ("play Sin Made Me Holy", "Dreams by Fleetwood Mac"), so it plays alone rather than with
    whatever else matched."""
    bare = re.sub(r"[\(\[][^\)\]]*[\)\]]", " ", title)  # "(feat. …)", "[Official Video]"
    for name in (bare, bare.split(" - ", 1)[-1]):  # SoundCloud's "Artist - Title"
        if _words(name) and _score(name, query) >= 0.99:
            return True
    return False


def _best(query: str, items: list, key) -> dict | None:
    scored = [(_score(query, key(i)), n, i) for n, i in enumerate(items)]
    scored = [s for s in scored if s[0] > 0]
    if not scored:
        return None
    return max(scored, key=lambda s: (s[0], -s[1]))[2]


class Folders:
    """Music files under folders Hermes can read, mounted network shares included.

    The index (``music-index.json``) keeps each file's tags with its modification time, so a
    rescan only opens new or changed files: a big share is read once, not every half hour."""

    _lock = threading.Lock()
    _tracks: list[dict] = []
    _built: tuple[float, tuple] = (0.0, ())

    def __init__(self, folders: list) -> None:
        self.folders = tuple(os.path.abspath(os.path.expanduser(f)) for f in folders if isinstance(f, str) and f.strip())

    @staticmethod
    def ident(path: str) -> str:
        return hashlib.sha1(path.encode("utf-8", "surrogateescape")).hexdigest()[:20]

    @staticmethod
    def _index_path() -> Path:
        return petasos_dir() / "music-index.json"

    def tracks(self) -> list[dict]:
        with Folders._lock:
            built, folders = Folders._built
            if folders != self.folders or time.time() - built > INDEX_MAX_AGE_S:
                Folders._tracks = self._scan()
                Folders._built = (time.time(), self.folders)
            return list(Folders._tracks)

    def _inside(self, path: str) -> bool:
        return any(os.path.commonpath([path, f]) == f for f in self.folders)

    def path(self, ident: str) -> str | None:
        """A track's file, from the index the agent's search wrote (it runs in another process)."""
        files = _read_json(self._index_path(), {}).get("files", {})
        for path in files:
            if self.ident(path) == ident and self._inside(path) and os.path.isfile(path):
                return path
        return None

    def _scan(self) -> list[dict]:
        old = _read_json(self._index_path(), {}).get("files", {})
        files: dict[str, list] = {}
        can_convert = shutil.which("ffmpeg") is not None
        for folder in self.folders:
            for dirpath, dirnames, filenames in os.walk(folder, followlinks=True):
                dirnames.sort()
                for name in sorted(filenames):
                    suffix = Path(name).suffix.lower()
                    if suffix not in AUDIO_SUFFIXES or (suffix != ".mp3" and not can_convert):
                        continue
                    path = os.path.join(dirpath, name)
                    try:
                        mtime = os.stat(path).st_mtime
                    except OSError:
                        continue
                    known = old.get(path)
                    if isinstance(known, list) and len(known) == 4 and known[0] == mtime:
                        files[path] = known
                    else:
                        files[path] = [mtime, *self._tags(path, folder)]
        _write_json(self._index_path(), {"folders": list(self.folders), "files": files})
        return [_track("folder", self.ident(p), t, ar, al) for p, (_, t, ar, al) in files.items()]

    @staticmethod
    def _tags(path: str, folder: str) -> tuple[str, str, str]:
        rel = Path(os.path.relpath(path, folder))
        title = re.sub(r"^\d+[\s._-]+", "", rel.stem).strip() or rel.stem
        parts = rel.parts[:-1]
        artist = parts[-2] if len(parts) >= 2 else (parts[0] if parts else "")
        album = parts[-1] if parts else ""
        try:
            import mutagen
            tags = mutagen.File(path, easy=True)
            if tags:
                title = (tags.get("title") or [title])[0]
                artist = (tags.get("artist") or tags.get("albumartist") or [artist])[0]
                album = (tags.get("album") or [album])[0]
        except Exception:  # no mutagen, or a file it can't read: the path will do
            pass
        return str(title), str(artist), str(album)

    def find(self, query: str, kind: str) -> tuple[str, list[dict]]:
        tracks = self.tracks()
        if kind == "random" or not query.strip():
            return "a random mix", random.sample(tracks, min(50, len(tracks)))
        if kind in ("playlist",):
            return "", []
        best: tuple[float, str, list[dict]] = (0.0, "", [])
        groups: list[tuple[str, str, list[dict]]] = []
        if kind in ("album", "any"):
            albums: dict[tuple, list[dict]] = {}
            for t in tracks:
                albums.setdefault((t["album"], t["artist"]), []).append(t)
            groups += [(f"{a} {ar}", f"{a} by {ar}" if ar else a, ts) for (a, ar), ts in albums.items() if a]
        if kind in ("artist", "any"):
            artists: dict[str, list[dict]] = {}
            for t in tracks:
                artists.setdefault(t["artist"], []).append(t)
            groups += [(a, f"songs by {a}", ts) for a, ts in artists.items() if a]
        for text, said, ts in groups:
            s = _score(query, text)
            if s > best[0]:
                best = (s, said, ts)
        songs = sorted(((_score(query, f"{t['title']} {t['artist']} {t['album']}"), t) for t in tracks),
                       key=lambda p: -p[0])
        songs = [t for s, t in songs if s >= 0.5]
        if best[0] >= 0.99 or (best[0] > 0 and kind in ("album", "artist")):
            return best[1], best[2]
        if songs:
            if kind == "song" or _names_song(query, songs[0]["title"]):
                return f"{songs[0]['title']} by {songs[0]['artist']}", songs[:1]
            return f"songs matching {query}", songs[:50]
        return "", []

    def search(self, query: str) -> list[dict]:
        scored = sorted(((_score(query, f"{t['title']} {t['artist']} {t['album']}"), t) for t in self.tracks()),
                        key=lambda p: -p[0])
        return [t for sc, t in scored if sc >= 0.5][:30]


class SoundCloud:
    """SoundCloud's public tracks, through an app registered at soundcloud.com/you/apps.

    The app's client credentials get a token (kept in ``soundcloud-token.json`` and renewed
    with its refresh token, since SoundCloud allows only a few new tokens a day); searches and
    streams use it. Streams come as HLS AAC, which ffmpeg turns into MP3 for the device."""

    API = "https://api.soundcloud.com"
    TOKEN_URL = "https://secure.soundcloud.com/oauth/token"
    _lock = threading.Lock()

    def __init__(self, cfg: dict) -> None:
        self.client_id = str(cfg.get("client_id") or "")
        self.client_secret = str(cfg.get("client_secret") or "")
        if not (self.client_id and self.client_secret):
            raise MusicError("music.json's soundcloud needs client_id and client_secret")

    @staticmethod
    def _token_path() -> Path:
        return petasos_dir() / "soundcloud-token.json"

    def _key(self) -> str:  # which app a saved token belongs to: a changed ID or secret starts over
        return hashlib.sha256(f"{self.client_id}:{self.client_secret}".encode()).hexdigest()[:16]

    def _grant(self, form: dict) -> dict:
        import base64
        # SoundCloud wants the app's ID and secret as Basic auth for the app's own token, but in the
        # form for your sign-in and its refreshes (and refuses them the other way round).
        headers = {"Accept": "application/json", "Content-Type": "application/x-www-form-urlencoded"}
        if form.get("grant_type") == "client_credentials":
            basic = base64.b64encode(f"{self.client_id}:{self.client_secret}".encode()).decode()
            headers["Authorization"] = "Basic " + basic
        else:
            form = {**form, "client_id": self.client_id, "client_secret": self.client_secret}
        req = urllib.request.Request(self.TOKEN_URL, data=urllib.parse.urlencode(form).encode(), method="POST",
                                     headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                return json.loads(resp.read())
        except urllib.error.HTTPError as exc:
            said = ""
            try:
                body = json.loads(exc.read() or b"{}")
                said = str(body.get("error_description") or body.get("error") or "")[:200]
            except (OSError, ValueError, AttributeError):
                pass
            raise MusicError(f"SoundCloud refused the app's sign-in (HTTP {exc.code}"
                             + (f": {said}" if said else "") + "); check the client ID and secret") from None
        except (OSError, ValueError) as exc:
            raise MusicError(f"SoundCloud didn't answer: {exc}") from None

    # --- your account: a one-time sign-in (OAuth 2.1 with PKCE) from the Petasos app -------------

    AUTHORIZE_URL = "https://secure.soundcloud.com/authorize"

    @staticmethod
    def _account_path() -> Path:
        return petasos_dir() / "soundcloud-account.json"

    def account(self) -> dict:
        saved = _read_json(self._account_path(), {})
        return saved if saved.get("app") == self._key() and saved.get("refresh_token") else {}

    def authorize_url(self, redirect_uri: str) -> str:
        """Where to send the browser to sign in; the PKCE verifier stays here, by state."""
        import base64
        verifier = secrets.token_urlsafe(48)
        state = secrets.token_urlsafe(16)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
        pending = {k: v for k, v in _read_json(petasos_dir() / "soundcloud-pending.json", {}).items()
                   if time.time() - float(v.get("at") or 0) < 600}
        pending[state] = {"verifier": verifier, "redirect_uri": redirect_uri, "at": time.time()}
        _write_json(petasos_dir() / "soundcloud-pending.json", pending)
        return self.AUTHORIZE_URL + "?" + urllib.parse.urlencode({
            "client_id": self.client_id, "redirect_uri": redirect_uri, "response_type": "code",
            "code_challenge": challenge, "code_challenge_method": "S256", "state": state})

    def connect(self, code: str, state: str) -> str:
        """Finishes the sign-in: the code becomes your tokens. Returns your SoundCloud name."""
        pending = _read_json(petasos_dir() / "soundcloud-pending.json", {})
        ask = pending.pop(state, None)
        _write_json(petasos_dir() / "soundcloud-pending.json", pending)
        if not ask or time.time() - float(ask.get("at") or 0) > 600:
            raise MusicError("That sign-in took too long or didn't start here; try again")
        got = self._grant({"grant_type": "authorization_code", "code": code, "redirect_uri": ask["redirect_uri"],
                           "code_verifier": ask["verifier"]})
        if not got.get("refresh_token"):
            raise MusicError("SoundCloud didn't give a lasting sign-in")
        account = {"app": self._key(), "access_token": got["access_token"], "refresh_token": got["refresh_token"],
                   "expires_at": time.time() + float(got.get("expires_in") or 3600)}
        _write_json(self._account_path(), account)
        os.chmod(self._account_path(), 0o600)
        me = self.call("/me")
        account["username"] = me.get("username") or me.get("permalink") or ""
        _write_json(self._account_path(), account)
        return account["username"]

    def disconnect(self) -> None:
        try:
            self._account_path().unlink()
        except FileNotFoundError:
            pass

    def _account_token(self) -> str | None:
        account = self.account()
        if not account:
            return None
        if time.time() < float(account.get("expires_at") or 0) - 60:
            return account["access_token"]
        # Refresh tokens are single-use: the new one is saved before anything else.
        got = self._grant({"grant_type": "refresh_token", "refresh_token": account["refresh_token"]})
        account.update(access_token=got["access_token"], refresh_token=got.get("refresh_token") or account["refresh_token"],
                       expires_at=time.time() + float(got.get("expires_in") or 3600))
        _write_json(self._account_path(), account)
        return account["access_token"]

    def token(self) -> str:
        """Your account's token when you've signed in, otherwise the app's own."""
        with SoundCloud._lock:
            try:
                mine = self._account_token()
            except MusicError:
                mine = None  # a sign-in SoundCloud no longer takes: public tracks still play
            if mine:
                return mine
            return self._app_token()

    def _app_token(self) -> str:
        saved = _read_json(self._token_path(), {})
        if saved.get("app") == self._key() and saved.get("access_token") \
                and time.time() < float(saved.get("expires_at") or 0) - 60:
            return saved["access_token"]
        got = None
        if saved.get("app") == self._key() and saved.get("refresh_token"):
            try:
                got = self._grant({"grant_type": "refresh_token", "refresh_token": saved["refresh_token"]})
            except MusicError:
                got = None
        if not got:
            got = self._grant({"grant_type": "client_credentials"})
        if not got.get("access_token"):
            raise MusicError("SoundCloud gave no token")
        _write_json(self._token_path(), {"app": self._key(), "access_token": got["access_token"],
                                         "refresh_token": got.get("refresh_token") or saved.get("refresh_token"),
                                         "expires_at": time.time() + float(got.get("expires_in") or 3600)})
        os.chmod(self._token_path(), 0o600)
        return got["access_token"]

    def call(self, path: str, **params):
        url = self.API + path + ("?" + urllib.parse.urlencode(params) if params else "")
        req = urllib.request.Request(url, headers={"Authorization": "OAuth " + self.token(), "Accept": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                return json.loads(resp.read())
        except urllib.error.HTTPError as exc:
            if exc.code == 401:  # a token SoundCloud has dropped: get a new one next time
                _write_json(self._token_path(), {})
                account = self.account()
                if account:
                    account["expires_at"] = 0
                    _write_json(self._account_path(), account)
            raise MusicError(f"SoundCloud: HTTP {exc.code}") from None
        except (OSError, ValueError) as exc:
            raise MusicError(f"SoundCloud didn't answer: {exc}") from None

    @staticmethod
    def _items(answer) -> list:
        return answer.get("collection", []) if isinstance(answer, dict) else (answer or [])

    @staticmethod
    def _art(item: dict) -> str:
        """The item's artwork (else its uploader's picture), at a size for the screen."""
        url = item.get("artwork_url") or (item.get("user") or {}).get("avatar_url") or ""
        return url.replace("-large.", "-t300x300.") if isinstance(url, str) else ""

    @staticmethod
    def _song(t: dict) -> dict | None:
        if t.get("access") == "blocked" or t.get("streamable") is False:
            return None
        return _track("soundcloud", str(t.get("urn") or t.get("id")), t.get("title"),
                      (t.get("user") or {}).get("username", ""), "", int(t.get("duration") or 0) // 1000,
                      SoundCloud._art(t))

    def _songs(self, items: list) -> list[dict]:
        return [s for s in (self._song(t) for t in items if isinstance(t, dict)) if s]

    def find(self, query: str, kind: str) -> tuple[str, list[dict]]:
        signed_in = bool(self.account())
        if kind == "likes" or (kind == "random" and signed_in):
            if not signed_in:
                raise MusicError("Your SoundCloud likes need you signed in to SoundCloud (the Petasos app's Settings → Music)")
            songs = self._songs(self._items(self.call("/me/likes/tracks", limit=200, linked_partitioning="true", access="playable")))
            return ("your SoundCloud likes", songs) if songs else ("", [])
        if kind == "random" or not query.strip():
            return "", []
        if signed_in and kind in ("playlist", "any"):  # your own playlists, and ones you liked, first
            mine = self._items(self.call("/me/playlists", limit=200, linked_partitioning="true", show_tracks="false"))
            try:
                mine += self._items(self.call("/me/likes/playlists", limit=200, linked_partitioning="true"))
            except MusicError:
                pass
            match = _best(query, mine, lambda p: p.get("title", ""))
            if match and (kind == "playlist" or _score(query, match.get("title", "")) >= 0.99):
                songs = self._playlist(match)
                if songs:
                    return f"your SoundCloud playlist {match.get('title')}", songs
        if kind in ("playlist", "any"):
            lists = self._items(self.call("/playlists", q=query, limit=10, linked_partitioning="true", show_tracks="false"))
            match = _best(query, lists, lambda p: p.get("title", ""))
            if match and (kind == "playlist" or _score(query, match.get("title", "")) >= 0.99):
                songs = self._playlist(match)
                if songs:
                    return f"the SoundCloud playlist {match.get('title')}", songs
            if kind == "playlist":
                return "", []
        if kind == "artist":
            users = self._items(self.call("/users", q=query, limit=5, linked_partitioning="true"))
            user = _best(query, users, lambda u: u.get("username", ""))
            if user:
                ident = str(user.get("urn") or user.get("id"))
                songs = self._songs(self._items(self.call(f"/users/{urllib.parse.quote(ident, safe=':')}/tracks",
                                                          limit=50, linked_partitioning="true", access="playable")))
                if songs:
                    return f"{user.get('username')} on SoundCloud", songs
            return "", []
        songs = self._songs(self._items(self.call("/tracks", q=query, limit=50, linked_partitioning="true", access="playable")))
        if not songs:
            return "", []
        if kind == "song" or (kind == "any" and _names_song(query, songs[0]["title"])):
            return f"{songs[0]['title']} by {songs[0]['artist']} on SoundCloud", songs[:1]
        return f"SoundCloud tracks matching {query}", songs[:20]

    def _playlist(self, playlist: dict) -> list[dict]:
        ident = str(playlist.get("urn") or playlist.get("id"))
        return self._songs(self._items(self.call(f"/playlists/{urllib.parse.quote(ident, safe=':')}/tracks",
                                                 limit=200, linked_partitioning="true", access="playable")))

    # The music page's lists.
    def likes(self) -> list[dict]:
        if not self.account():
            raise MusicError("Sign in to SoundCloud in Petasos's settings (Music) to see your likes")
        return self._songs(self._items(self.call("/me/likes/tracks", limit=200, linked_partitioning="true", access="playable")))

    def playlists(self) -> list[dict]:
        if not self.account():
            return []
        mine = self._items(self.call("/me/playlists", limit=200, linked_partitioning="true", show_tracks="false"))
        try:
            mine += self._items(self.call("/me/likes/playlists", limit=200, linked_partitioning="true"))
        except MusicError:
            pass
        seen, out = set(), []
        for p in mine:
            ident = str(p.get("urn") or p.get("id"))
            if ident not in seen:
                seen.add(ident)
                out.append(_playlist_entry("soundcloud", ident, p.get("title"), p.get("track_count"), self._art(p)))
        return out

    def playlist_tracks(self, ident: str) -> list[dict]:
        return self._playlist({"urn": ident})

    def search(self, query: str) -> list[dict]:
        return self._songs(self._items(self.call("/tracks", q=query, limit=30, linked_partitioning="true", access="playable")))

    def stream(self, ident: str) -> Iterator[bytes]:
        urls = self.call(f"/tracks/{urllib.parse.quote(ident, safe=':')}/streams")
        hls = urls.get("hls_aac_160_url") or urls.get("hls_aac_96_url") or urls.get("hls_mp3_128_url")
        ffmpeg = shutil.which("ffmpeg")
        if hls and ffmpeg:
            proc = subprocess.Popen([ffmpeg, "-v", "error", "-headers", f"Authorization: OAuth {self.token()}\r\n",
                                     "-i", hls, "-vn", "-ac", "2", "-ar", "48000", "-b:a", f"{bit_rate()}k",
                                     "-f", "mp3", "pipe:1"], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            return _pieces(proc.stdout, proc)
        if urls.get("http_mp3_128_url"):  # older, plain MP3; SoundCloud is retiring it
            req = urllib.request.Request(urls["http_mp3_128_url"], headers={"Authorization": "OAuth " + self.token()})
            return _pieces(urllib.request.urlopen(req, timeout=30))
        if hls:
            raise MusicError("SoundCloud streams need ffmpeg installed on Hermes")
        raise MusicError("SoundCloud has no stream for this track")


class Spotify:
    """Your Spotify account, as a remote for the Spotify app on your phone (Spotify Connect).

    The Car Thing can't play Spotify's audio itself: the phone plays it, and its sound reaches
    the Car Thing over Bluetooth, which shows what's playing and skips. So Spotify is only on the
    music page (your saved songs, your playlists, a search), and choosing a song there tells the
    phone to play that list from that song. It isn't a source for play_music, which streams.

    Sign-in is OAuth with PKCE from the Petasos app (an app registered at
    developer.spotify.com/dashboard, no client secret), kept in ``spotify-account.json``.
    Choosing music needs Spotify Premium. An app in Spotify's development mode serves only the
    accounts listed in its User Management, shows the songs of only the playlists you made or
    collaborate on, and gives at most ten search results a call."""

    API = "https://api.spotify.com/v1"
    AUTHORIZE_URL = "https://accounts.spotify.com/authorize"
    TOKEN_URL = "https://accounts.spotify.com/api/token"
    SCOPES = ("user-library-read playlist-read-private playlist-read-collaborative "
              "user-read-playback-state user-modify-playback-state")
    SIGN_IN = "Sign in to Spotify in the Petasos app's settings (Music)"
    _lock = threading.Lock()

    def __init__(self, cfg: dict) -> None:
        self.client_id = str(cfg.get("client_id") or "")
        if not self.client_id:
            raise MusicError("music.json's spotify needs client_id")

    def _key(self) -> str:  # which app a saved sign-in belongs to: a changed ID starts over
        return hashlib.sha256(self.client_id.encode()).hexdigest()[:16]

    @staticmethod
    def _account_path() -> Path:
        return petasos_dir() / "spotify-account.json"

    @staticmethod
    def _pending_path() -> Path:
        return petasos_dir() / "spotify-pending.json"

    def account(self) -> dict:
        saved = _read_json(self._account_path(), {})
        return saved if isinstance(saved, dict) and saved.get("app") == self._key() and saved.get("refresh_token") else {}

    def _save_account(self, account: dict) -> None:
        _write_json(self._account_path(), account)
        os.chmod(self._account_path(), 0o600)

    def _grant(self, form: dict) -> dict:
        # With PKCE the app has no secret: its ID goes in the form.
        req = urllib.request.Request(self.TOKEN_URL, data=urllib.parse.urlencode({**form, "client_id": self.client_id}).encode(),
                                     method="POST", headers={"Accept": "application/json",
                                                             "Content-Type": "application/x-www-form-urlencoded"})
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                return json.loads(resp.read())
        except urllib.error.HTTPError as exc:
            said = ""
            try:
                body = json.loads(exc.read() or b"{}")
                said = str(body.get("error_description") or body.get("error") or "")[:200]
            except (OSError, ValueError, AttributeError):
                pass
            raise MusicError(f"Spotify refused the sign-in (HTTP {exc.code}" + (f": {said}" if said else "")
                             + "); sign in to Spotify again in the Petasos app's settings (Music)") from None
        except (OSError, ValueError) as exc:
            raise MusicError(f"Spotify didn't answer: {exc}") from None

    # --- your account: a one-time sign-in (OAuth with PKCE) from the Petasos app ------------------

    def authorize_url(self, redirect_uri: str) -> str:
        """Where to send the browser to sign in; the PKCE verifier stays here, by state."""
        import base64
        verifier = secrets.token_urlsafe(48)
        state = secrets.token_urlsafe(16)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
        pending = {k: v for k, v in _read_json(self._pending_path(), {}).items()
                   if time.time() - float(v.get("at") or 0) < 600}
        pending[state] = {"verifier": verifier, "redirect_uri": redirect_uri, "at": time.time()}
        _write_json(self._pending_path(), pending)
        return self.AUTHORIZE_URL + "?" + urllib.parse.urlencode({
            "client_id": self.client_id, "redirect_uri": redirect_uri, "response_type": "code", "scope": self.SCOPES,
            "code_challenge": challenge, "code_challenge_method": "S256", "state": state})

    def connect(self, code: str, state: str) -> str:
        """Finishes the sign-in: the code becomes your tokens. Returns your Spotify name."""
        pending = _read_json(self._pending_path(), {})
        ask = pending.pop(state, None)
        _write_json(self._pending_path(), pending)
        if not ask or time.time() - float(ask.get("at") or 0) > 600:
            raise MusicError("That sign-in took too long or didn't start here; try again")
        got = self._grant({"grant_type": "authorization_code", "code": code, "redirect_uri": ask["redirect_uri"],
                           "code_verifier": ask["verifier"]})
        if not got.get("refresh_token"):
            raise MusicError("Spotify didn't give a lasting sign-in")
        account = {"app": self._key(), "access_token": got["access_token"], "refresh_token": got["refresh_token"],
                   "expires_at": time.time() + float(got.get("expires_in") or 3600)}
        self._save_account(account)
        me = self.call("GET", "/me")
        account.update(username=me.get("display_name") or me.get("id") or "", user=me.get("id") or "")
        self._save_account(account)
        return account["username"]

    def disconnect(self) -> None:
        try:
            self._account_path().unlink()
        except FileNotFoundError:
            pass

    def token(self) -> str:
        """Your account's token, renewed when it has run out."""
        with Spotify._lock:
            account = self.account()
            if not account:
                raise MusicError(self.SIGN_IN)
            if time.time() < float(account.get("expires_at") or 0) - 60:
                return account["access_token"]
            # Spotify may send a new refresh token in place of the old one: it is saved before use.
            got = self._grant({"grant_type": "refresh_token", "refresh_token": account["refresh_token"]})
            if not got.get("access_token"):
                raise MusicError("Spotify gave no token")
            account.update(access_token=got["access_token"], refresh_token=got.get("refresh_token") or account["refresh_token"],
                           expires_at=time.time() + float(got.get("expires_in") or 3600))
            self._save_account(account)
            return account["access_token"]

    def call(self, method: str, path: str, body=None, **params):
        # A path, or the address of a list's next page, which Spotify gives in full.
        url = (path if path.startswith(self.API + "/") else self.API + path) + ("?" + urllib.parse.urlencode(params) if params else "")
        headers = {"Authorization": "Bearer " + self.token(), "Accept": "application/json"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None,
                                     method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                data = resp.read()
                return json.loads(data) if data.strip() else {}
        except urllib.error.HTTPError as exc:
            said, reason = "", ""
            try:
                err = json.loads(exc.read() or b"{}").get("error")
                if isinstance(err, dict):
                    said, reason = str(err.get("message") or "")[:200], str(err.get("reason") or "")
                elif err:
                    said = str(err)[:200]
            except (OSError, ValueError, AttributeError):
                pass
            if exc.code == 401:  # a token Spotify has dropped: get a new one next time
                account = self.account()
                if account:
                    account["expires_at"] = 0
                    self._save_account(account)
            if exc.code == 403 and (reason == "PREMIUM_REQUIRED" or "premium" in said.lower()):
                raise MusicError("Spotify only lets apps choose music with Spotify Premium") from None
            if exc.code == 403 and "not registered" in said.lower():  # development mode's allowed accounts
                raise MusicError("Add your Spotify account to the app's User Management at developer.spotify.com/dashboard") from None
            if exc.code == 404 and reason == "NO_ACTIVE_DEVICE":
                raise MusicError("Open Spotify on your phone first") from None
            raise MusicError(f"Spotify: HTTP {exc.code}" + (f": {said}" if said else "")) from None
        except (OSError, ValueError) as exc:
            raise MusicError(f"Spotify didn't answer: {exc}") from None

    def _pages(self, path: str, most: int = MAX_QUEUE, **params) -> list:
        """A list's items, a page at a time (Spotify gives at most 50 a call)."""
        items: list = []
        answer = self.call("GET", path, limit=min(50, most), **params)
        while True:
            items += [i for i in answer.get("items") or [] if isinstance(i, dict)]
            if len(items) >= most or not answer.get("next"):
                return items[:most]
            answer = self.call("GET", answer["next"])

    @staticmethod
    def _art(images) -> str:
        """The picture closest to 300 pixels across (Spotify sends them largest first)."""
        images = [i for i in images or [] if isinstance(i, dict) and i.get("url")]
        if not images:
            return ""
        big_enough = [i for i in images if (i.get("width") or 0) >= 300]
        return (big_enough[-1] if big_enough else images[0])["url"]

    @staticmethod
    def _song(t) -> dict | None:
        if not isinstance(t, dict) or t.get("type", "track") != "track" or not t.get("uri") or t.get("is_local"):
            return None
        if t.get("is_playable") is False:
            return None
        album = t.get("album") or {}
        return _track("spotify", t["uri"], t.get("name"), ", ".join(a.get("name", "") for a in t.get("artists") or []),
                      album.get("name", ""), int(t.get("duration_ms") or 0) // 1000, Spotify._art(album.get("images")))

    def _songs(self, tracks) -> list[dict]:
        return [s for s in (self._song(t) for t in tracks) if s]

    # The music page's lists.
    def likes(self) -> list[dict]:
        return self._songs(i.get("track") for i in self._pages("/me/tracks"))

    def playlists(self) -> list[dict]:
        # Spotify shows the songs of only the playlists you made or collaborate on, so the ones you
        # only follow, which would open empty, are left out.
        me = self.account().get("user") or ""
        out = []
        for p in self._pages("/me/playlists"):
            if me and (p.get("owner") or {}).get("id") != me and not p.get("collaborative"):
                continue
            count = (p.get("items") or p.get("tracks") or {}).get("total")
            out.append(_playlist_entry("spotify", str(p.get("id")), p.get("name"), count, self._art(p.get("images"))))
        return out

    def playlist_tracks(self, ident: str) -> list[dict]:
        ident = ident.rsplit(":", 1)[-1]  # an ID, or the playlist's spotify:playlist:… URI
        try:
            items = self._pages(f"/playlists/{urllib.parse.quote(ident, safe='')}/items")
        except MusicError as exc:
            if "HTTP 403" in str(exc):
                raise MusicError("Spotify only shows the songs of playlists you made or collaborate on") from None
            raise
        return self._songs(i.get("item") or i.get("track") for i in items)

    def search(self, query: str) -> list[dict]:
        # Spotify gives at most ten results a call to apps in development mode: three calls, then.
        songs: list[dict] = []
        for offset in (0, 10, 20):
            found = self.call("GET", "/search", q=query, type="track", limit=10, offset=offset).get("tracks") or {}
            songs += self._songs(found.get("items") or [])
            if not found.get("next"):
                break
        return songs

    def play(self, uris: list, at: int = 0) -> None:
        """Plays these songs from the one at ``at`` on the device Spotify is playing on, or else
        on your phone. A single playlist or album URI plays that, from its song at ``at``."""
        uris = [u for u in uris or [] if isinstance(u, str) and u.startswith("spotify:")]
        if not uris:
            raise MusicError("Nothing to play")
        at = max(0, int(at or 0))
        if len(uris) == 1 and uris[0].split(":")[1] not in ("track", "episode"):
            body: dict = {"context_uri": uris[0], "offset": {"position": at}}
        else:
            at = min(at, len(uris) - 1)
            if len(uris) > MAX_QUEUE:  # a long list: the part from the chosen song on
                uris, at = uris[at:at + MAX_QUEUE], 0
            body = {"uris": uris, "offset": {"position": at}}
        devices = [d for d in self.call("GET", "/me/player/devices").get("devices") or [] if isinstance(d, dict)]
        params = {}
        if not any(d.get("is_active") for d in devices):
            usable = [d for d in devices if d.get("id") and not d.get("is_restricted")]
            phones = [d for d in usable if d.get("type") == "Smartphone"]
            if not (phones or usable):
                raise MusicError("Open Spotify on your phone first")
            params["device_id"] = (phones or usable)[0]["id"]
        self.call("PUT", "/me/player/play", body, **params)


def sources() -> list:
    cfg = config()
    out: list = []
    if cfg.get("navidrome"):
        out.append(Navidrome(cfg["navidrome"]))
    if cfg.get("folders"):
        out.append(Folders(cfg["folders"]))
    if cfg.get("soundcloud"):  # last: your own music first
        out.append(SoundCloud(cfg["soundcloud"]))
    return out


def _name(src) -> str:
    return {Navidrome: "navidrome", Folders: "folders", SoundCloud: "soundcloud", Spotify: "spotify"}[type(src)]


def find(query: str, kind: str = "any", source: str = "") -> tuple[str, list[dict]]:
    """The first source that has something for the request."""
    found_any = False
    problems = []
    if kind == "likes":
        source = "soundcloud"
    for src in sources():
        if source and source != _name(src):
            continue
        found_any = True
        try:
            said, tracks = src.find(query, kind)
        except MusicError as exc:
            problems.append(str(exc))
            continue
        if tracks:
            return said, tracks
    if not found_any:
        raise MusicError(f"No music source {source + ' ' if source else ''}is set up yet: add Navidrome, folders or SoundCloud to petasos/music.json on Hermes.")
    if problems:
        raise MusicError(" ".join(problems))
    return "", []


def browse(what: str, source: str = "", ident: str = "", query: str = "") -> dict:
    """The device's music page: your likes, your playlists, one playlist's tracks, or a search
    of every source. Each answer has a title and tracks or playlists, and a note when a source
    couldn't help (not signed in, didn't answer).

    ``source`` names one source or several ("navidrome,folders"); empty means all but Spotify,
    which is only asked when named (the device can't stream it: it's a remote for your phone)."""
    wanted = {{"folder": "folders"}.get(n, n) for n in (p.strip().lower() for p in source.split(",")) if n}
    if what == "likes" and not wanted:
        wanted = {"soundcloud"}  # older devices ask for likes without a source: SoundCloud's
    srcs = sources()
    notes: list[str] = []
    if "spotify" in wanted:
        cfg = config().get("spotify")
        try:
            sp = Spotify(cfg if isinstance(cfg, dict) else {})
        except MusicError:
            sp = None
        if sp and sp.account():
            srcs.append(sp)
        else:
            notes.append(Spotify.SIGN_IN)
    if not srcs:
        if notes:
            raise MusicError(" ".join(notes))
        raise MusicError("No music source is set up yet: add SoundCloud, Navidrome or folders in Petasos's settings (Music).")
    out: dict = {"title": "", "tracks": [], "playlists": []}

    def each(method: str, *args) -> None:
        for src in srcs:
            if wanted and _name(src) not in wanted or not hasattr(src, method):
                continue
            try:
                got = getattr(src, method)(*args)
            except MusicError as exc:
                notes.append(str(exc))
                continue
            out["playlists" if method == "playlists" else "tracks"] += got

    if what == "likes":
        out["title"] = "Your likes"
        each("likes")
    elif what == "playlists":
        out["title"] = "Playlists"
        each("playlists")
    elif what == "playlist":
        if not (source and ident):
            raise MusicError("Which playlist?")
        out["title"] = "Playlist"
        each("playlist_tracks", ident)
    elif what == "search":
        if not query.strip():
            raise MusicError("Search for what?")
        out["title"] = f"“{query.strip()}”"
        each("search", query.strip())
    else:
        raise MusicError("what is likes, playlists, playlist or search")
    out["tracks"] = out["tracks"][:MAX_QUEUE]
    if notes and not (out["tracks"] or out["playlists"]):
        out["note"] = " ".join(notes)
    return out


# --- settings, from the dashboard --------------------------------------------------------------

def settings() -> dict:
    """music.json as the dashboard shows it: the Navidrome password is never sent back."""
    cfg = config()
    nav = cfg.get("navidrome") if isinstance(cfg.get("navidrome"), dict) else {}
    sc = cfg.get("soundcloud") if isinstance(cfg.get("soundcloud"), dict) else {}
    sp = cfg.get("spotify") if isinstance(cfg.get("spotify"), dict) else {}
    return {"navidrome": {"url": nav.get("url", ""), "username": nav.get("username", ""),
                          "has_password": bool(nav.get("password"))},
            "folders": [f for f in cfg.get("folders") or [] if isinstance(f, str)],
            "soundcloud": {"client_id": sc.get("client_id", ""), "has_secret": bool(sc.get("client_secret")),
                           "account": _soundcloud_account(sc)},
            "spotify": {"client_id": sp.get("client_id", ""), "account": _spotify_account(sp)},
            "quality": cfg.get("quality") if cfg.get("quality") in QUALITIES else "high",
            "ffmpeg": shutil.which("ffmpeg") is not None,
            "path": str(petasos_dir() / "music.json")}


def _soundcloud_account(sc: dict) -> str:
    """Your SoundCloud name when you've signed in with this app, else empty."""
    try:
        return SoundCloud(sc).account().get("username", "") if sc.get("client_id") else ""
    except MusicError:
        return ""


def soundcloud() -> "SoundCloud":
    cfg = config()
    if not cfg.get("soundcloud"):
        raise MusicError("Save SoundCloud's client ID and secret first")
    return SoundCloud(cfg["soundcloud"])


def _spotify_account(sp: dict) -> str:
    """Your Spotify name when you've signed in with this app, else empty."""
    try:
        return Spotify(sp).account().get("username", "") if sp.get("client_id") else ""
    except MusicError:
        return ""


def spotify() -> "Spotify":
    cfg = config()
    if not cfg.get("spotify"):
        raise MusicError("Save Spotify's client ID first")
    return Spotify(cfg["spotify"])


def save_quality(quality) -> dict:
    """Saves the streaming quality alone, leaving the rest of music.json as it is."""
    if quality not in QUALITIES:
        raise MusicError("quality is one of " + ", ".join(QUALITIES))
    cfg = config()
    cfg["quality"] = quality
    _write_json(petasos_dir() / "music.json", cfg)
    try:
        os.chmod(petasos_dir() / "music.json", 0o600)  # it holds a password
    except OSError:
        pass
    return settings()


def save_settings(body: dict) -> dict:
    """Saves the dashboard's changes; an empty password keeps the saved one."""
    if not isinstance(body, dict):
        raise MusicError("settings must be an object")
    cfg = config()
    nav = body.get("navidrome") if isinstance(body.get("navidrome"), dict) else {}
    url, username = str(nav.get("url") or "").strip().rstrip("/"), str(nav.get("username") or "").strip()
    password = str(nav.get("password") or "") or (cfg.get("navidrome") or {}).get("password", "")
    if url and not re.match(r"^https?://", url):
        raise MusicError("The Navidrome address starts with http:// or https://")
    if url:
        if not (username and password):
            raise MusicError("Navidrome needs a username and password")
        cfg["navidrome"] = {"url": url, "username": username, "password": password}
    else:
        cfg.pop("navidrome", None)
    folders = []
    for f in body.get("folders") or []:
        f = str(f).strip()
        if f and f not in folders:
            if not os.path.isabs(os.path.expanduser(f)):
                raise MusicError(f"{f} isn't a full path (it starts with /)")
            folders.append(f)
    cfg["folders"] = folders
    if isinstance(body.get("soundcloud"), dict):  # left out by older settings pages: keep what's saved
        sc = body["soundcloud"]
        client_id = str(sc.get("client_id") or "").strip()
        secret = str(sc.get("client_secret") or "").strip() or (cfg.get("soundcloud") or {}).get("client_secret", "")
        if client_id:
            if not secret:
                raise MusicError("SoundCloud needs the app's client secret too")
            cfg["soundcloud"] = {"client_id": client_id, "client_secret": secret}
        else:
            cfg.pop("soundcloud", None)
    if isinstance(body.get("spotify"), dict):  # likewise
        client_id = str(body["spotify"].get("client_id") or "").strip()
        if client_id:
            cfg["spotify"] = {"client_id": client_id}
        else:
            cfg.pop("spotify", None)
    if "quality" in body:  # left out by older settings pages: keep what's saved
        if body["quality"] not in QUALITIES:
            raise MusicError("quality is one of " + ", ".join(QUALITIES))
        cfg["quality"] = body["quality"]
    _write_json(petasos_dir() / "music.json", cfg)
    try:
        os.chmod(petasos_dir() / "music.json", 0o600)  # it holds a password
    except OSError:
        pass
    return settings()


def check() -> dict:
    """Whether each source works: Navidrome answers, and how many songs each folder has."""
    cfg = config()
    out: dict = {"navidrome": None, "folders": [], "soundcloud": None, "spotify": None}
    if cfg.get("soundcloud"):
        try:
            SoundCloud(cfg["soundcloud"]).token()
            out["soundcloud"] = {"ok": True, "ffmpeg": shutil.which("ffmpeg") is not None}
        except MusicError as exc:
            out["soundcloud"] = {"ok": False, "problem": str(exc)}
    if cfg.get("spotify"):  # whether your sign-in still works
        try:
            sp = Spotify(cfg["spotify"])
            if not sp.account():
                raise MusicError("Spotify: not signed in yet. " + Spotify.SIGN_IN + ".")
            me = sp.call("GET", "/me")
            out["spotify"] = {"ok": True, "account": me.get("display_name") or me.get("id") or ""}
        except MusicError as exc:
            out["spotify"] = {"ok": False, "problem": str(exc)}
    if cfg.get("navidrome"):
        try:
            Navidrome(cfg["navidrome"]).call("ping")
            out["navidrome"] = {"ok": True}
        except MusicError as exc:
            out["navidrome"] = {"ok": False, "problem": str(exc)}
    folders = Folders(cfg.get("folders") or [])
    tracks = folders.tracks() if folders.folders else []
    for folder in folders.folders:
        if not os.path.isdir(folder):
            out["folders"].append({"path": folder, "ok": False,
                                   "problem": "Hermes can't see this folder (is the share mounted?)"})
            continue
        n = sum(1 for p in _read_json(Folders._index_path(), {}).get("files", {}) if p.startswith(folder.rstrip("/") + "/"))
        out["folders"].append({"path": folder, "ok": True, "songs": n})
    out["songs"] = len(tracks)
    return out


# --- streaming ----------------------------------------------------------------------------------

def stream(source: str, ident: str) -> Iterator[bytes]:
    """A track as MP3, in pieces."""
    cfg = config()
    if source == "navidrome":
        if not cfg.get("navidrome"):
            raise MusicError("Navidrome isn't set up")
        url = Navidrome(cfg["navidrome"]).stream_url(ident)
        return _pieces(urllib.request.urlopen(url, timeout=30))
    if source == "folder":
        path = Folders(cfg.get("folders") or []).path(ident)
        if not path:
            raise MusicError("no such track")
        if path.lower().endswith(".mp3") and bit_rate() >= BIT_RATE:  # lower qualities convert MP3s too
            return _pieces(open(path, "rb"))
        ffmpeg = shutil.which("ffmpeg")
        if not ffmpeg:
            raise MusicError("ffmpeg isn't installed to convert this track")
        proc = subprocess.Popen([ffmpeg, "-v", "error", "-i", path, "-vn", "-ac", "2", "-ar", "48000",
                                 "-b:a", f"{bit_rate()}k", "-f", "mp3", "pipe:1"],
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        return _pieces(proc.stdout, proc)
    if source == "soundcloud":
        if not cfg.get("soundcloud"):
            raise MusicError("SoundCloud isn't set up")
        return SoundCloud(cfg["soundcloud"]).stream(ident)
    raise MusicError("unknown source")


def _pieces(f, proc=None) -> Iterator[bytes]:
    try:
        while True:
            chunk = f.read(32 * 1024)
            if not chunk:
                return
            yield chunk
    finally:
        f.close()
        if proc is not None:
            proc.kill()
            proc.wait()


# --- the agent's tools --------------------------------------------------------------------------

PLAY_MUSIC = {
    "name": "play_music",
    "description": ("Play music from the user's own library (their Navidrome server and music folders) or "
                    "SoundCloud on a "
                    "Petasos device: the Car Thing in their car, which plays it through the car's speakers. "
                    "Use it whenever the user asks to play, put on or listen to something, even when the "
                    "title reads like a statement or a question (\"play Sin Made Me Holy\"). "
                    "A single song plays once and stops; a playlist, album or artist plays through and stops, "
                    "unless repeat says otherwise. "
                    "It plays after your reply. Say what you put on, in a few words."),
    "parameters": {"type": "object", "properties": {
        "query": {"type": "string", "description": "What to play: a song, album, artist or playlist name. Empty for a random mix."},
        "kind": {"type": "string", "enum": ["any", "song", "album", "artist", "playlist", "random", "likes"],
                 "description": "What the query names, if you know; 'any' to let the library decide. 'likes' plays their liked tracks on SoundCloud"},
        "source": {"type": "string", "enum": ["navidrome", "folders", "soundcloud"],
                   "description": "Only when the user names where to play from; otherwise their own library is tried first, then SoundCloud"},
        "shuffle": {"type": "boolean", "description": "Shuffle the songs"},
        "repeat": {"type": "string", "enum": ["off", "one", "all"],
                   "description": "Only when asked: 'one' plays the same song over and over, 'all' starts the queue again when it ends"},
        "device": {"type": "string", "description": "Device id, only when the user names another device; normally left out"},
    }},
}

MUSIC_CONTROL = {
    "name": "music_control",
    "description": ("Pause, resume, skip or stop the music playing on the user's Petasos device (the Car Thing), "
                    "or turn repeat (one song, or the whole queue) and shuffle on or off."),
    "parameters": {"type": "object", "properties": {
        "action": {"type": "string", "enum": ["pause", "resume", "next", "previous", "stop", "repeat_one",
                                              "repeat_all", "repeat_off", "shuffle_on", "shuffle_off"]},
        "device": {"type": "string", "description": "Device id, only when the user names another device; normally left out"},
    }, "required": ["action"]},
}


_ASKS_FOR_MUSIC = re.compile(r"^\W*(?:(?:hey|ok|okay|please|can you|could you|would you|will you)\W+)*"
                             r"(?:play|put on|shuffle|queue up|listen to)\b", re.I)


def music_hint(user_message=None, **_) -> str | None:
    """The ``pre_llm_call`` hook: a request that starts with "play" gets a reminder that it means
    music, so a song whose title reads like a remark ("play Sin Made Me Holy") isn't answered as one,
    and that play_music may sit behind tool search."""
    text = user_message if isinstance(user_message, str) else ""
    if not _ASKS_FOR_MUSIC.search(text):
        return None
    return ("[Petasos] This reads as a request for music: if it is, call play_music with what to play, "
            "taking the title literally even if it sounds like a remark (find play_music with tool_search "
            "if it isn't listed). Don't answer the title as if it were said to you.")


def play_music(args: dict | None = None, session_id: str | None = None, **_) -> str:
    args = args or {}
    query = str(args.get("query") or "").strip()
    kind = args.get("kind") if args.get("kind") in PLAY_MUSIC["parameters"]["properties"]["kind"]["enum"] else "any"
    try:
        said, tracks = find(query, kind, args.get("source") if args.get("source") in ("navidrome", "folders", "soundcloud") else "")
    except MusicError as exc:
        return json.dumps({"error": str(exc)})
    if not tracks:
        return json.dumps({"error": f"Nothing in the library matches {query!r}."})
    if args.get("shuffle"):
        tracks = random.sample(tracks, len(tracks))
    tracks = tracks[:MAX_QUEUE]
    device = device_for(args.get("device"), session_id)
    repeat = args.get("repeat") if args.get("repeat") in ("one", "all") else ""
    leave(device, {"do": "music", "op": "play", "title": said, "tracks": tracks, "repeat": repeat,
                   "shuffle": bool(args.get("shuffle"))})
    return json.dumps({"playing": said, "songs": len(tracks), "first": tracks[0]["title"], "device": device})


def music_control(args: dict | None = None, session_id: str | None = None, **_) -> str:
    args = args or {}
    action = args.get("action")
    actions = MUSIC_CONTROL["parameters"]["properties"]["action"]["enum"]
    if action not in actions:
        return json.dumps({"error": "action is one of " + ", ".join(actions)})
    device = device_for(args.get("device"), session_id)
    leave(device, {"do": "music", "op": action})
    return json.dumps({"done": action, "device": device})
