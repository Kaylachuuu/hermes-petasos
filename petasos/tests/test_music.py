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
    assert music.device_for("windows", "s2") == "mac"  # no device called windows has asked yet
    music.take("windows")
    music.take("kaylas-car-thing")
    assert music.device_for("windows", "s2") == "windows"
    # A name the agent made up for a real device finds it; one for no device is ignored.
    assert music.device_for("Kayla's Car Thing", "s2") == "kaylas-car-thing"
    assert music.device_for("the kitchen", "s2") == "mac"


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
        if method == "getCoverArt":
            self.send_response(200 if ok else 401)
            self.end_headers()
            self.wfile.write(b"\x89PNG-" + q["id"].encode())
            return
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
        elif method == "getStarred2":
            body["starred2"] = {"song": [song("s5", "Starred One")]}
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
    streams_asked = 0
    original = None  # the artist's upload, offered as a download when set

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
        if url.path == "/tracks/soundcloud:tracks:1":
            return self._json({"urn": "soundcloud:tracks:1", "downloadable": FakeSoundCloud.original is not None})
        if url.path == "/tracks/soundcloud:tracks:1/download" and FakeSoundCloud.original is not None:
            self.send_response(200)
            self.send_header("Content-Type", "audio/wav")
            self.end_headers()
            self.wfile.write(FakeSoundCloud.original)
            return
        if url.path == "/tracks/soundcloud:tracks:1/streams":
            FakeSoundCloud.streams_asked += 1
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
    FakeSoundCloud.grants, FakeSoundCloud.hls_dir, FakeSoundCloud.streams_asked, FakeSoundCloud.original = [], None, 0, None
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


def test_a_request_to_play_gets_a_reminder_that_it_means_music():
    for asked in ("Play Sin Made Me Holy", "hey, can you play my likes?", "Please put on some Daft Punk",
                  "shuffle Rumours"):
        assert "play_music" in (music.music_hint(user_message=asked) or ""), asked
    for said in ("Sin made me holy", "I'd love to play chess later", "What's on today?", None):
        assert music.music_hint(user_message=said) is None, said


def test_a_song_named_in_full_plays_alone_and_repeat_reaches_the_device(tmp_path, monkeypatch):
    assert music._names_song("play Sin Made Me Holy", "Sin Made Me Holy")
    assert music._names_song("dreams by fleetwood mac", "Dreams")
    assert not music._names_song("some daft punk", "One More Time")
    assert music._names_song("sin made me holy", "Someone - Sin Made Me Holy (feat. Another) [Official Audio]")
    left = []
    monkeypatch.setattr(music, "find", lambda q, k, s: ("Dreams by Fleetwood Mac", [{"source": "folder", "id": "1", "title": "Dreams"}]))
    monkeypatch.setattr(music, "leave", lambda device, cmd: left.append(cmd))
    monkeypatch.setattr(music, "device_for", lambda d, s: "car")
    music.play_music({"query": "Dreams", "repeat": "one"})
    assert left[-1]["repeat"] == "one" and left[-1]["shuffle"] is False
    assert json.loads(music.music_control({"action": "shuffle_on"}))["done"] == "shuffle_on"
    assert left[-1] == {"do": "music", "op": "shuffle_on"}
    assert "error" in json.loads(music.music_control({"action": "wiggle"}))


def test_the_music_page_lists_likes_playlists_and_searches(home, soundcloud, navidrome):
    configure(home, {"navidrome": {"url": navidrome, "username": "kayla", "password": "secret"},
                     "soundcloud": {"client_id": "cid", "client_secret": "secret"}})
    assert "Sign in" in music.browse("likes")["note"]
    assert [p["title"] for p in music.browse("playlists")["playlists"]] == ["Road Trip"]
    found = music.browse("search", query="brightest")
    assert [t["title"] for t in found["tracks"]] == ["Jóga", "Brightest Lights"]
    assert music.browse("playlist", "navidrome", "p1")["tracks"][0]["title"] == "Hyperballad"
    with pytest.raises(music.MusicError):
        music.browse("search", query=" ")
    # Signed in: the likes, and SoundCloud's playlists beside Navidrome's.
    url = music.soundcloud().authorize_url("http://127.0.0.1:8187/app/soundcloud")
    q = {k: v[0] for k, v in parse_qs(urlparse(url).query).items()}
    FakeSoundCloud.challenge = q["code_challenge"]
    music.soundcloud().connect("c0de", q["state"])
    assert [t["title"] for t in music.browse("likes")["tracks"]] == ["Loved One"]
    lists = music.browse("playlists")["playlists"]
    assert [(p["source"], p["title"]) for p in lists] == [("navidrome", "Road Trip"), ("soundcloud", "Late Night Drive")]
    assert music.browse("playlist", "soundcloud", lists[1]["id"])["tracks"][0]["title"] == "Fingerprint"


def test_the_music_page_says_when_nothing_is_set_up(home):
    with pytest.raises(music.MusicError):
        music.browse("playlists")


class FakeSpotify(BaseHTTPRequestHandler):
    challenge = ""
    devices = []
    premium = True
    played = []

    def log_message(self, *_):
        pass

    def _json(self, body, code=200):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps(body).encode())

    def _mine(self):
        return (self.headers.get("Authorization") or "").startswith("Bearer at")

    def do_POST(self):
        import base64
        form = {k: v[0] for k, v in parse_qs(self.rfile.read(int(self.headers["Content-Length"])).decode()).items()}
        # PKCE: the app's ID in the form, and no secret anywhere.
        if form.get("client_id") != "spid" or "client_secret" in form or self.headers.get("Authorization"):
            return self._json({"error": "invalid_client"}, 400)
        if form["grant_type"] == "authorization_code":
            challenge = base64.urlsafe_b64encode(hashlib.sha256(form["code_verifier"].encode()).digest()).decode().rstrip("=")
            if form["code"] != "c0de" or challenge != FakeSpotify.challenge:
                return self._json({"error": "invalid_grant"}, 400)
            return self._json({"access_token": "at1", "refresh_token": "rt1", "expires_in": 3600})
        if form["grant_type"] == "refresh_token" and form["refresh_token"] == "rt1":
            return self._json({"access_token": "at2", "refresh_token": "rt2", "expires_in": 3600})
        self._json({"error": "invalid_grant"}, 400)

    def do_PUT(self):
        if not self._mine():
            return self._json({"error": {"status": 401, "message": "Invalid access token"}}, 401)
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        q = {k: v[0] for k, v in parse_qs(urlparse(self.path).query).items()}
        if not FakeSpotify.premium:
            return self._json({"error": {"status": 403, "message": "Player command failed: Premium required",
                                         "reason": "PREMIUM_REQUIRED"}}, 403)
        FakeSpotify.played.append((q.get("device_id"), body))
        self.send_response(204)
        self.end_headers()

    def do_GET(self):
        url = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(url.query).items()}
        if not self._mine():
            return self._json({"error": {"status": 401, "message": "Invalid access token"}}, 401)
        base = f"http://127.0.0.1:{self.server.server_port}/v1"
        art = [{"url": "https://i.scdn.co/640", "width": 640}, {"url": "https://i.scdn.co/300", "width": 300},
               {"url": "https://i.scdn.co/64", "width": 64}]
        song = lambda n, title: {"type": "track", "uri": f"spotify:track:{n}", "name": title, "duration_ms": 200500,
                                 "artists": [{"name": "Robyn"}], "album": {"name": "Body Talk", "images": art}}
        if url.path == "/v1/me":
            return self._json({"id": "kayla.r", "display_name": "Kayla"})
        if url.path == "/v1/me/tracks":
            if q.get("offset") == "1":  # the second page
                return self._json({"items": [{"track": song(2, "Call Your Girlfriend")}], "next": None})
            return self._json({"items": [{"track": song(1, "Dancing On My Own")}], "next": base + "/me/tracks?offset=1&limit=50"})
        if url.path == "/v1/me/playlists":
            return self._json({"items": [
                {"id": "pl1", "name": "Drive", "owner": {"id": "kayla.r"}, "items": {"total": 2}, "images": [{"url": "https://i.scdn.co/pl"}]},
                {"id": "pl2", "name": "Shared", "owner": {"id": "friend"}, "collaborative": True, "items": {"total": 5}},
                {"id": "pl3", "name": "Today's Top Hits", "owner": {"id": "spotify"}, "items": {"total": 50}}], "next": None})
        if url.path == "/v1/playlists/pl1/items":
            return self._json({"items": [{"item": song(3, "Hang With Me")}, {"item": {"type": "episode", "uri": "spotify:episode:9"}}],
                               "next": None})
        if url.path == "/v1/playlists/pl3/items":
            return self._json({"error": {"status": 403, "message": "Forbidden"}}, 403)
        if url.path == "/v1/search":
            assert int(q["limit"]) <= 10  # development mode's limit
            if q["offset"] == "0":
                return self._json({"tracks": {"items": [song(4, "Robyn Search")], "next": base + "/search?offset=10"}})
            return self._json({"tracks": {"items": [song(5, "Second Page")], "next": None}})
        if url.path == "/v1/me/player/devices":
            return self._json({"devices": FakeSpotify.devices})
        self._json({"error": {"status": 404, "message": "Not found"}}, 404)


@pytest.fixture
def spotify(home, monkeypatch):
    server = HTTPServer(("127.0.0.1", 0), FakeSpotify)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{server.server_port}"
    monkeypatch.setattr(music.Spotify, "API", base + "/v1")
    monkeypatch.setattr(music.Spotify, "TOKEN_URL", base + "/api/token")
    FakeSpotify.devices, FakeSpotify.premium, FakeSpotify.played = [], True, []
    configure(home, {"spotify": {"client_id": "spid"}})
    yield base
    server.shutdown()


def sign_in_to_spotify():
    url = music.spotify().authorize_url("http://127.0.0.1:8187/app/spotify")
    q = {k: v[0] for k, v in parse_qs(urlparse(url).query).items()}
    assert q["redirect_uri"] == "http://127.0.0.1:8187/app/spotify" and q["code_challenge_method"] == "S256"
    assert "user-modify-playback-state" in q["scope"].split()
    FakeSpotify.challenge = q["code_challenge"]
    return music.spotify().connect("c0de", q["state"])


def test_signing_in_to_spotify_brings_your_songs_and_playlists(home, spotify):
    with pytest.raises(music.MusicError, match="Sign in to Spotify in the Petasos app"):
        music.browse("likes", "spotify")
    assert not music.check()["spotify"]["ok"]
    with pytest.raises(music.MusicError):
        music.spotify().connect("c0de", "some-other-state")
    assert sign_in_to_spotify() == "Kayla"
    assert oct((home / "petasos" / "spotify-account.json").stat().st_mode & 0o777) == "0o600"
    assert music.settings()["spotify"] == {"client_id": "spid", "account": "Kayla"}
    assert music.check()["spotify"] == {"ok": True, "account": "Kayla"}
    likes = music.browse("likes", "spotify")["tracks"]
    assert likes[0] == {"source": "spotify", "id": "spotify:track:1", "title": "Dancing On My Own", "artist": "Robyn",
                        "album": "Body Talk", "seconds": 200, "art": "https://i.scdn.co/300"}
    assert [t["title"] for t in likes] == ["Dancing On My Own", "Call Your Girlfriend"]  # both pages
    # Only playlists you made or collaborate on: Spotify won't show the songs of the others.
    lists = music.browse("playlists", "spotify")["playlists"]
    assert [(p["id"], p["count"]) for p in lists] == [("pl1", 2), ("pl2", 5)] and lists[0]["art"] == "https://i.scdn.co/pl"
    assert [t["title"] for t in music.browse("playlist", "spotify", "pl1")["tracks"]] == ["Hang With Me"]
    assert "you made or collaborate on" in music.browse("playlist", "spotify", "pl3")["note"]
    assert [t["title"] for t in music.browse("search", "spotify", query="robyn")["tracks"]] == ["Robyn Search", "Second Page"]
    # An hour on, the refresh token may come back a new one: it's kept.
    account = json.loads((home / "petasos" / "spotify-account.json").read_text())
    account["expires_at"] = 0
    (home / "petasos" / "spotify-account.json").write_text(json.dumps(account))
    music.browse("likes", "spotify")
    assert json.loads((home / "petasos" / "spotify-account.json").read_text())["refresh_token"] == "rt2"
    music.spotify().disconnect()
    assert music.settings()["spotify"]["account"] == ""


def test_spotify_is_only_browsed_never_streamed(home, spotify):
    configure(home, {"folders": [str(library(home))], "spotify": {"client_id": "spid"}})
    sign_in_to_spotify()
    assert all(not isinstance(s, music.Spotify) for s in music.sources())
    assert {t["source"] for t in music.browse("search", query="dreams")["tracks"]} == {"folder"}
    assert {t["source"] for t in music.browse("search", "folders,spotify", query="robyn")["tracks"]} == {"spotify"}
    with pytest.raises(music.MusicError):
        music.stream("spotify", "spotify:track:1")


def test_spotify_plays_on_the_active_device_or_else_your_phone(home, spotify):
    sign_in_to_spotify()
    uris = ["spotify:track:1", "spotify:track:2", "spotify:track:3"]
    with pytest.raises(music.MusicError, match="Open Spotify on your phone first"):
        music.spotify().play(uris, 1)
    FakeSpotify.devices = [{"id": "mac", "type": "Computer", "is_active": False},
                           {"id": "phone", "type": "Smartphone", "is_active": False}]
    music.spotify().play(uris, 1)
    assert FakeSpotify.played[-1] == ("phone", {"uris": uris, "offset": {"position": 1}})
    FakeSpotify.devices[0]["is_active"] = True  # already playing on the Mac: carry on there
    music.spotify().play(["spotify:playlist:pl1"], 4)
    assert FakeSpotify.played[-1] == (None, {"context_uri": "spotify:playlist:pl1", "offset": {"position": 4}})
    FakeSpotify.premium = False
    with pytest.raises(music.MusicError, match="Spotify Premium"):
        music.spotify().play(uris, 0)
    music.spotify().disconnect()
    with pytest.raises(music.MusicError, match="Sign in to Spotify"):
        music.spotify().play(uris, 0)


def test_spotify_settings_save_and_an_empty_client_id_removes_it(home):
    shown = music.save_settings({"folders": [], "spotify": {"client_id": " spid "}})
    assert shown["spotify"] == {"client_id": "spid", "account": ""}
    music.save_settings({"folders": []})  # an older settings page leaves Spotify alone
    assert music.config()["spotify"] == {"client_id": "spid"}
    music.save_settings({"folders": [], "spotify": {"client_id": ""}})
    assert "spotify" not in music.config() and music.check()["spotify"] is None


def test_likes_come_from_the_source_asked_for(home, soundcloud, navidrome):
    configure(home, {"navidrome": {"url": navidrome, "username": "kayla", "password": "secret"},
                     "folders": [str(library(home))], "soundcloud": {"client_id": "cid", "client_secret": "secret"}})
    assert [t["title"] for t in music.browse("likes", "navidrome")["tracks"]] == ["Starred One"]
    assert "Sign in" in music.browse("likes")["note"]  # no source, as older devices ask: SoundCloud's
    assert music.browse("likes", "folders")["tracks"] == []  # folders have no likes
    # Several sources, comma-separated.
    found = music.browse("search", "navidrome,folders", query="dreams")
    assert {t["source"] for t in found["tracks"]} == {"navidrome", "folder"}
    found = music.browse("search", "folder", query="dreams")
    assert {t["source"] for t in found["tracks"]} == {"folder"}


def test_the_streaming_quality_sets_the_bit_rate(home, navidrome):
    configure(home, {"navidrome": {"url": navidrome, "username": "kayla", "password": "secret"}})
    assert music.settings()["quality"] == "high" and music.bit_rate() == 192
    shown = music.save_settings({"folders": [], "navidrome": {"url": navidrome, "username": "kayla"}, "quality": "low"})
    assert shown["quality"] == "low" and music.bit_rate() == 96
    assert "maxBitRate=96" in music.Navidrome(music.config()["navidrome"]).stream_url("1")
    music.save_settings({"folders": [], "navidrome": {"url": navidrome, "username": "kayla"}})  # older pages keep it
    assert music.config()["quality"] == "low"
    with pytest.raises(music.MusicError):
        music.save_settings({"folders": [], "quality": "best"})


def test_quality_is_saved_alone(tmp_path, monkeypatch):
    monkeypatch.setattr(music, "petasos_dir", lambda: tmp_path)
    music.save_settings({"folders": [str(tmp_path)], "soundcloud": {"client_id": "cid", "client_secret": "sec"}})
    assert music.save_quality("low")["quality"] == "low"
    cfg = json.loads((tmp_path / "music.json").read_text())
    assert cfg["quality"] == "low" and cfg["folders"] == [str(tmp_path)] and cfg["soundcloud"]["client_secret"] == "sec"
    with pytest.raises(music.MusicError):
        music.save_quality("loud")


def _hls(tmp_path):
    import shutil
    import subprocess
    if not shutil.which("ffmpeg"):
        pytest.skip("no ffmpeg")
    FakeSoundCloud.hls_dir = tmp_path / "hls"
    FakeSoundCloud.hls_dir.mkdir()
    subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:a", "aac",
                    "-f", "hls", "-hls_time", "1", str(FakeSoundCloud.hls_dir / "out.m3u8")], check=True)


def _wait_for(check, seconds=10):
    import time
    until = time.time() + seconds
    while not check() and time.time() < until:
        time.sleep(0.05)
    return check()


def test_soundcloud_songs_are_kept_as_soundcloud_has_them_and_as_converted(home, soundcloud, tmp_path):
    _hls(tmp_path)
    cache = home / "petasos" / "music-cache"
    first = b"".join(music.stream("soundcloud", "soundcloud:tracks:1"))
    assert _wait_for(lambda: any(p.name.endswith("-own.aac") for p in (cache / "source").iterdir()))
    assert [p.name.split("-")[1] for p in (cache / "device").iterdir()] == ["192.mp3"]
    # Played again: from the converted copy, without asking SoundCloud.
    assert b"".join(music.stream("soundcloud", "soundcloud:tracks:1")) == first
    assert FakeSoundCloud.streams_asked == 1
    # Another quality: converted from SoundCloud's copy kept here.
    configure(home, {"soundcloud": {"client_id": "cid", "client_secret": "secret"}, "quality": "lowest"})
    low = b"".join(music.stream("soundcloud", "soundcloud:tracks:1"))
    assert 1000 < len(low) < len(first) and FakeSoundCloud.streams_asked == 1
    assert sorted(p.name.split("-")[1] for p in (cache / "device").iterdir()) == ["192.mp3", "64.mp3"]


def test_a_song_stopped_partway_is_not_kept(home, soundcloud, tmp_path):
    _hls(tmp_path)
    pieces = music.stream("soundcloud", "soundcloud:tracks:1")
    next(pieces)
    pieces.close()
    assert list((home / "petasos" / "music-cache" / "device").iterdir()) == []


def test_the_artists_upload_is_kept_when_they_allow_downloads(home, soundcloud, tmp_path):
    _hls(tmp_path)
    FakeSoundCloud.original = b"RIFF fake wav"
    b"".join(music.stream("soundcloud", "soundcloud:tracks:1"))
    source = home / "petasos" / "music-cache" / "source"
    assert _wait_for(lambda: [p.read_bytes() for p in source.iterdir() if p.name.endswith("-own.wav")] == [b"RIFF fake wav"])


def test_the_caches_keep_to_their_size_dropping_the_longest_unplayed(home, monkeypatch):
    import os
    monkeypatch.setattr(music, "cache_gb", lambda kind: 1)
    d = music._cache_dir("device")
    for i, name in enumerate(["old", "mid", "new"]):
        (d / name).write_bytes(b"")
        os.truncate(d / name, 400 << 20)  # sparse: no disk used
        os.utime(d / name, (1000 + i, 1000 + i))
    music._cache_prune("device")
    assert sorted(p.name for p in d.iterdir()) == ["mid", "new"]


def test_the_cache_sizes_are_set_and_none_keeps_nothing(home, soundcloud, tmp_path):
    shown = music.settings()["cache"]
    assert (shown["source"], shown["device"], shown["device_used"]) == (10, 2, 0)
    with pytest.raises(music.MusicError):
        music.save_settings({"folders": [], "cache": {"device": 3}})
    _hls(tmp_path)
    music.save_settings({"folders": [], "soundcloud": {"client_id": "cid", "client_secret": ""}, "cache": {"source": 0, "device": 0}})
    assert music.settings()["cache"]["device"] == 0
    b"".join(music.stream("soundcloud", "soundcloud:tracks:1"))
    cache = home / "petasos" / "music-cache"
    assert list((cache / "device").iterdir()) == [] and list((cache / "source").iterdir()) == []


def test_pictures_are_kept_and_only_the_musics_are_fetched(home, monkeypatch):
    fetched = []

    class Answer:
        def __init__(self, url): fetched.append(url)
        def __enter__(self): return self
        def __exit__(self, *_): pass
        def read(self, _n): return b"\xff\xd8jpeg"

    monkeypatch.setattr(music.urllib.request, "urlopen", lambda url, timeout: Answer(url))
    url = "https://i1.sndcdn.com/artworks-abc-t300x300.jpg"
    assert music.art(url) == (b"\xff\xd8jpeg", "image/jpeg")
    assert music.art(url) == (b"\xff\xd8jpeg", "image/jpeg")
    assert fetched == [url]
    for bad in ["http://i1.sndcdn.com/a.jpg", "https://evil.example/sndcdn.com.jpg", "file:///etc/passwd", "navidrome:al1"]:
        with pytest.raises(music.MusicError):
            music.art(bad)


def test_navidrome_covers_come_through_its_sign_in(home, navidrome):
    music.save_settings({"navidrome": {"url": navidrome, "username": "kayla", "password": "secret"}, "folders": []})
    data, kind = music.art("navidrome:al1")
    assert (data, kind) == (b"\x89PNG-al1", "image/png")
    assert FakeNavidrome.calls[-1][0] == "getCoverArt"
