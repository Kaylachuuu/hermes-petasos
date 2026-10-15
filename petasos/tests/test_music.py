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
    assert "secret" not in json.dumps(shown)
    result = music.check()
    assert result["navidrome"] == {"ok": True}
    assert result["folders"][0] == {"path": str(root), "ok": True, "songs": 4}
    assert not result["folders"][1]["ok"] and "mounted" in result["folders"][1]["problem"]
    with pytest.raises(music.MusicError):
        music.save_settings({"folders": ["relative/path"]})
