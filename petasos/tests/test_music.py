"""The home library: finding music in folders and on a Navidrome server, and handing it to a device."""
import hashlib
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse

import pytest

import music


@pytest.fixture
def home(tmp_path, monkeypatch):
    monkeypatch.setattr(music, "_root", lambda: tmp_path)
    music.Folders._built = (0.0, ())
    (tmp_path / "petasos").mkdir()
    return tmp_path


def configure(home, cfg):
    (home / "petasos" / "music.json").write_text(json.dumps(cfg))


def library(home):
    root = home / "Music"
    for artist, album, songs in [("Daft Punk", "Discovery", ["01 One More Time.mp3", "02 Aerodynamic.mp3"]),
                                 ("Fleetwood Mac", "Rumours", ["01 Second Hand News.mp3", "02 Dreams.mp3"])]:
        (root / artist / album).mkdir(parents=True)
        for song in songs:
            (root / artist / album / song).write_bytes(b"ID3" + song.encode())
    (root / "notes.txt").write_text("not music")
    return root


def test_folders_find_albums_artists_and_songs_by_path(home):
    configure(home, {"folders": [str(library(home))]})
    said, tracks = music.find("rumours")
    assert said == "Rumours by Fleetwood Mac" and [t["title"] for t in tracks] == ["Second Hand News", "Dreams"]
    said, tracks = music.find("dreams", "song")
    assert said == "Dreams by Fleetwood Mac" and len(tracks) == 1
    said, tracks = music.find("daft punk", "artist")
    assert said == "songs by Daft Punk" and len(tracks) == 2
    assert music.find("nothing like this") == ("", [])
    assert len(music.find("", "random")[1]) == 4


def test_a_folder_track_streams_only_from_inside_the_folders(home):
    root = library(home)
    configure(home, {"folders": [str(root)]})
    _, tracks = music.find("aerodynamic", "song")
    assert b"".join(music.stream("folder", tracks[0]["id"])) == b"ID302 Aerodynamic.mp3"
    with pytest.raises(music.MusicError):
        music.stream("folder", music.Folders.ident(str(home / "petasos" / "music.json")))


def test_the_index_keeps_tags_until_a_file_changes(home, monkeypatch):
    configure(home, {"folders": [str(library(home))]})
    read = []
    real = music.Folders._tags
    monkeypatch.setattr(music.Folders, "_tags", staticmethod(lambda p, f: read.append(p) or real(p, f)))
    music.find("dreams")
    assert len(read) == 4
    music.Folders._built = (0.0, ())
    music.find("dreams")
    assert len(read) == 4


def test_play_music_leaves_a_queue_for_the_device_that_asked(home):
    configure(home, {"folders": [str(library(home))]})
    music.note_turn("kitchen")
    answer = json.loads(music.play_music({"query": "discovery"}))
    assert answer == {"playing": "Discovery by Daft Punk", "songs": 2, "first": "One More Time", "device": "kitchen"}
    json.loads(music.music_control({"action": "next"}))
    commands = music.take("kitchen")
    assert [c["op"] for c in commands] == ["play", "next"]
    assert commands[0]["tracks"][0]["source"] == "folder"
    assert music.take("kitchen") == []


def test_the_chat_names_the_device(home):
    (home / "petasos" / "chats.json").write_text(json.dumps({"car-thing": "s1", "mac": "s2", "*": "s3"}))
    assert music.device_for(None, "s2") == "mac"
    assert music.device_for(None, "s3") == "car-thing"  # shared chat, nobody asked lately
    assert music.device_for("windows", "s2") == "windows"


def test_a_new_queue_replaces_one_not_yet_picked_up(home):
    music.leave("car-thing", {"do": "music", "op": "play", "tracks": [1]})
    music.leave("car-thing", {"do": "music", "op": "play", "tracks": [2]})
    assert [c["tracks"] for c in music.take("car-thing")] == [[2]]


def test_no_library_says_how_to_set_one_up(home):
    assert "music.json" in json.loads(music.play_music({"query": "x"}))["error"]


class FakeNavidrome(BaseHTTPRequestHandler):
    calls = []

    def log_message(self, *_):
        pass

    def do_GET(self):
        url = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(url.query).items()}
        method = url.path.rsplit("/", 1)[-1].removesuffix(".view")
        FakeNavidrome.calls.append((method, q))
        ok = q.get("u") == "kayla" and q.get("t") == hashlib.md5(("secret" + q.get("s", "")).encode()).hexdigest()
        if method == "stream":
            self.send_response(200 if ok else 401)
            self.end_headers()
            self.wfile.write(b"mp3:" + q["id"].encode())
            return
        song = lambda i, t: {"id": i, "title": t, "artist": "Björk", "album": "Homogenic", "duration": 200}
        body = {"status": "ok" if ok else "failed", "error": {"message": "Wrong username or password"}}
        if method == "ping":
            pass
        elif method == "search3":
            body["searchResult3"] = {"album": [{"id": "al1", "name": "Homogenic", "artist": "Björk"}],
                                     "song": [song("s1", "Jóga")]}
        elif method == "getAlbum":
            body["album"] = {"song": [song("s1", "Jóga"), song("s2", "Bachelorette")]}
        elif method == "getPlaylists":
            body["playlists"] = {"playlist": [{"id": "p1", "name": "Road Trip"}]}
        elif method == "getPlaylist":
            body["playlist"] = {"entry": [song("s9", "Hyperballad")]}
        self.send_response(200)
        self.end_headers()
        self.wfile.write(json.dumps({"subsonic-response": body}).encode())


@pytest.fixture
def navidrome(home):
    server = HTTPServer(("127.0.0.1", 0), FakeNavidrome)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    FakeNavidrome.calls = []
    yield f"http://127.0.0.1:{server.server_port}"
    server.shutdown()


def test_navidrome_finds_albums_and_playlists_and_streams(home, navidrome):
    configure(home, {"navidrome": {"url": navidrome, "username": "kayla", "password": "secret"}})
    said, tracks = music.find("homogenic", "album")
    assert said == "Homogenic by Björk" and [t["id"] for t in tracks] == ["s1", "s2"]
    said, tracks = music.find("road trip")
    assert said == "the playlist Road Trip" and tracks[0]["title"] == "Hyperballad"
    assert b"".join(music.stream("navidrome", "s2")) == b"mp3:s2"
    assert FakeNavidrome.calls[-1][1]["format"] == "mp3"


def test_navidrome_says_when_it_refuses(home, navidrome):
    configure(home, {"navidrome": {"url": navidrome, "username": "kayla", "password": "wrong"}})
    assert "Wrong username or password" in json.loads(music.play_music({"query": "x"}))["error"]


def test_settings_keep_the_password_and_check_each_source(home, navidrome):
    root = library(home)
    music.save_settings({"navidrome": {"url": navidrome + "/", "username": "kayla", "password": "secret"},
                         "folders": [str(root), str(home / "not-mounted")]})
    shown = music.save_settings({"navidrome": {"url": navidrome, "username": "kayla", "password": ""},
                                 "folders": [str(root), str(home / "not-mounted")]})
    assert shown["navidrome"] == {"url": navidrome, "username": "kayla", "has_password": True}
    assert ': "secret"' not in json.dumps(shown)
    result = music.check()
    assert result["navidrome"] == {"ok": True}
    assert result["folders"][0] == {"path": str(root), "ok": True, "songs": 4}
    assert not result["folders"][1]["ok"] and "mounted" in result["folders"][1]["problem"]
    with pytest.raises(music.MusicError):
        music.save_settings({"folders": ["relative/path"]})


class FakeSoundCloud(BaseHTTPRequestHandler):
    grants = []
    hls_dir = None
    challenge = ""

    def log_message(self, *_):
        pass

    def _json(self, body, code=200):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(body).encode())

    def do_POST(self):
        form = parse_qs(self.rfile.read(int(self.headers["Content-Length"])).decode())
        import base64
        # As SoundCloud's guide has it: the app's own token takes the ID and secret as Basic auth,
        # the sign-in and its refreshes take them in the form.
        if form["grant_type"][0] == "client_credentials":
            if self.headers.get("Authorization") != "Basic " + base64.b64encode(b"cid:secret").decode():
                return self._json({"error": "invalid_client"}, 401)
        elif form.get("client_id") != ["cid"] or form.get("client_secret") != ["secret"] or self.headers.get("Authorization"):
            return self._json({"error": "invalid_request", "error_description": "client credentials"}, 400)
        FakeSoundCloud.grants.append(form["grant_type"][0])
        if form["grant_type"][0] == "authorization_code":
            import base64
            import hashlib as h
            challenge = base64.urlsafe_b64encode(h.sha256(form["code_verifier"][0].encode()).digest()).decode().rstrip("=")
            if form["code"][0] != "c0de" or challenge != FakeSoundCloud.challenge:
                return self._json({"error": "invalid_grant"}, 400)
            return self._json({"access_token": "tokuser", "refresh_token": "ru1", "expires_in": 3600})
        if form.get("refresh_token") == ["ru1"]:
            return self._json({"access_token": "tokuser2", "refresh_token": "ru2", "expires_in": 3600})
        self._json({"access_token": f"tok{len(FakeSoundCloud.grants)}", "refresh_token": "r", "expires_in": 3600})

    def do_GET(self):
        url = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(url.query).items()}
        if url.path.startswith("/hls/"):
            data = (FakeSoundCloud.hls_dir / url.path[5:]).read_bytes()
            self.send_response(200)
            self.end_headers()
            self.wfile.write(data)
            return
        if not (self.headers.get("Authorization") or "").startswith("OAuth tok"):
            return self._json({}, 401)
        user = {"username": "Lane 8"}
        mine = (self.headers.get("Authorization") or "").startswith("OAuth tokuser")
        if url.path.startswith("/me"):
            if not mine:
                return self._json({}, 401)
            if url.path == "/me":
                return self._json({"username": "kaylachuuu"})
            if url.path == "/me/likes/tracks":
                return self._json({"collection": [{"urn": "soundcloud:tracks:7", "title": "Loved One", "user": user}]})
            if url.path == "/me/playlists":
                return self._json({"collection": [{"urn": "soundcloud:playlists:9", "title": "Late Night Drive"}]})
            if url.path == "/me/likes/playlists":
                return self._json({"collection": []})
        if url.path == "/tracks":
            assert q["access"] == "playable"
            return self._json({"collection": [
                {"urn": "soundcloud:tracks:1", "title": "Brightest Lights", "user": user, "duration": 240000, "access": "playable"},
                {"urn": "soundcloud:tracks:2", "title": "Blocked", "user": user, "access": "blocked"}]})
        if url.path == "/playlists":
            return self._json({"collection": [{"urn": "soundcloud:playlists:9", "title": "Late Night Drive"}]})
        if url.path == "/playlists/soundcloud:playlists:9/tracks":
            return self._json({"collection": [{"urn": "soundcloud:tracks:3", "title": "Fingerprint", "user": user}]})
        if url.path == "/tracks/soundcloud:tracks:1/streams":
            port = self.server.server_port
            return self._json({"hls_aac_160_url": f"http://127.0.0.1:{port}/hls/out.m3u8"} if FakeSoundCloud.hls_dir
                              else {"http_mp3_128_url": f"http://127.0.0.1:{port}/mp3"})
        if url.path == "/mp3":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ID3 mp3")
            return
        self._json({}, 404)


@pytest.fixture
def soundcloud(home, monkeypatch):
    server = HTTPServer(("127.0.0.1", 0), FakeSoundCloud)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{server.server_port}"
    monkeypatch.setattr(music.SoundCloud, "API", base)
    monkeypatch.setattr(music.SoundCloud, "TOKEN_URL", base + "/oauth/token")
    FakeSoundCloud.grants, FakeSoundCloud.hls_dir = [], None
    configure(home, {"soundcloud": {"client_id": "cid", "client_secret": "secret"}})
    yield base
    server.shutdown()


def test_soundcloud_finds_tracks_and_playlists_with_one_token(home, soundcloud):
    said, tracks = music.find("brightest lights", "song")
    assert said == "Brightest Lights by Lane 8 on SoundCloud"
    assert tracks == [{"source": "soundcloud", "id": "soundcloud:tracks:1", "title": "Brightest Lights",
                       "artist": "Lane 8", "album": "", "seconds": 240}]
    said, tracks = music.find("late night drive", "playlist")
    assert said == "the SoundCloud playlist Late Night Drive" and tracks[0]["title"] == "Fingerprint"
    assert FakeSoundCloud.grants == ["client_credentials"]  # kept and reused, not asked for again
    assert b"".join(music.stream("soundcloud", "soundcloud:tracks:1")) == b"ID3 mp3"
    assert FakeSoundCloud.grants == ["client_credentials"]


def test_soundcloud_comes_after_your_own_music(home, soundcloud):
    configure(home, {"folders": [str(library(home))], "soundcloud": {"client_id": "cid", "client_secret": "secret"}})
    assert music.find("rumours")[0] == "Rumours by Fleetwood Mac"
    assert "SoundCloud" in json.loads(music.play_music({"query": "brightest lights", "source": "soundcloud"}))["playing"]


def test_soundcloud_hls_becomes_mp3(home, soundcloud, tmp_path):
    import shutil
    import subprocess
    if not shutil.which("ffmpeg"):
        pytest.skip("no ffmpeg")
    FakeSoundCloud.hls_dir = tmp_path / "hls"
    FakeSoundCloud.hls_dir.mkdir()
    subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:a", "aac",
                    "-f", "hls", "-hls_time", "1", str(FakeSoundCloud.hls_dir / "out.m3u8")], check=True)
    mp3 = b"".join(music.stream("soundcloud", "soundcloud:tracks:1"))
    assert len(mp3) > 10000 and (mp3[:3] == b"ID3" or mp3[0] == 0xFF)


def test_soundcloud_settings_keep_the_secret(home, soundcloud):
    shown = music.save_settings({"folders": [], "soundcloud": {"client_id": "cid", "client_secret": ""}})
    assert shown["soundcloud"] == {"client_id": "cid", "has_secret": True, "account": ""}
    assert music.check()["soundcloud"]["ok"]
    music.save_settings({"folders": []})  # an older settings page leaves SoundCloud alone
    assert music.config()["soundcloud"]["client_secret"] == "secret"
    configure(home, {"soundcloud": {"client_id": "cid", "client_secret": "wrong"}})
    assert "client ID and secret" in music.check()["soundcloud"]["problem"]


def test_signing_in_to_soundcloud_brings_your_likes_and_playlists(home, soundcloud):
    import base64
    import hashlib as h
    with pytest.raises(music.MusicError):
        music.find("", "likes")  # not signed in yet
    url = music.soundcloud().authorize_url("http://127.0.0.1:8187/app/soundcloud")
    q = {k: v[0] for k, v in parse_qs(urlparse(url).query).items()}
    assert q["redirect_uri"] == "http://127.0.0.1:8187/app/soundcloud" and q["code_challenge_method"] == "S256"
    FakeSoundCloud.challenge = q["code_challenge"]
    with pytest.raises(music.MusicError):
        music.soundcloud().connect("c0de", "some-other-state")
    assert music.soundcloud().connect("c0de", q["state"]) == "kaylachuuu"
    assert music.settings()["soundcloud"]["account"] == "kaylachuuu"
    said, tracks = music.find("", "likes")
    assert said == "your SoundCloud likes" and tracks[0]["title"] == "Loved One"
    said, _ = music.find("late night drive", "playlist")
    assert said == "your SoundCloud playlist Late Night Drive"
    assert "likes" in json.loads(music.play_music({"kind": "likes"}))["playing"]
    # An hour on, the single-use refresh token is swapped for the next one.
    account = json.loads((home / "petasos" / "soundcloud-account.json").read_text())
    account["expires_at"] = 0
    (home / "petasos" / "soundcloud-account.json").write_text(json.dumps(account))
    music.find("", "likes")
    assert json.loads((home / "petasos" / "soundcloud-account.json").read_text())["refresh_token"] == "ru2"
    music.soundcloud().disconnect()
    assert music.settings()["soundcloud"]["account"] == ""
