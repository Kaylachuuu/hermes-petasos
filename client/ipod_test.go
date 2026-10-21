package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// chunk builds an iTunesDB chunk: its name, a header hl long with fields set at their
// offsets, then its children. Lists (mhlt, mhlp) carry a count where others carry a length.
func chunk(name string, hl int, fields map[int]uint32, children ...[]byte) []byte {
	head := make([]byte, hl)
	copy(head, name)
	binary.LittleEndian.PutUint32(head[4:], uint32(hl))
	body := bytes.Join(children, nil)
	if name != "mhlt" && name != "mhlp" {
		binary.LittleEndian.PutUint32(head[8:], uint32(hl+len(body)))
	}
	for at, v := range fields {
		binary.LittleEndian.PutUint32(head[at:], v)
	}
	return append(head, body...)
}

func mhod(kind uint32, s string, utf8 bool) []byte {
	var raw []byte
	enc := uint32(1)
	if utf8 {
		raw, enc = []byte(s), 2
	} else {
		for _, u := range utf16.Encode([]rune(s)) {
			raw = binary.LittleEndian.AppendUint16(raw, u)
		}
	}
	m := make([]byte, 40, 40+len(raw))
	copy(m, "mhod")
	binary.LittleEndian.PutUint32(m[4:], 24)
	binary.LittleEndian.PutUint32(m[8:], uint32(40+len(raw)))
	binary.LittleEndian.PutUint32(m[12:], kind)
	binary.LittleEndian.PutUint32(m[24:], enc)
	binary.LittleEndian.PutUint32(m[28:], uint32(len(raw)))
	return append(m, raw...)
}

func mhit(id uint32, ms, number uint32, title, artist, album, loc string) []byte {
	return chunk("mhit", 0x184, map[int]uint32{12: 4, 16: id, 40: ms, 44: number, 92: 1},
		mhod(1, title, false), mhod(4, artist, false), mhod(3, album, true), mhod(2, loc, false))
}

func mhyp(name string, master bool, ids ...uint32) []byte {
	var flag uint32
	if master {
		flag = 1
	}
	kids := [][]byte{mhod(1, name, false)}
	for _, id := range ids {
		kids = append(kids, chunk("mhip", 0x4c, map[int]uint32{24: id}))
	}
	return chunk("mhyp", 0x6c, map[int]uint32{12: 1, 16: uint32(len(ids)), 20: flag}, kids...)
}

func testDB(compress bool) []byte {
	tracks := chunk("mhlt", 92, map[int]uint32{8: 4},
		mhit(10, 241000, 2, "Dreams", "Fleetwood Mac", "Rumours", ":iPod_Control:Music:F01:ABCD.m4a"),
		mhit(11, 200500, 1, "Second Hand News", "Fleetwood Mac", "Rumours", ":iPod_Control:Music:F02:EFGH.mp3"),
		mhit(12, 180000, 1, "Bought", "Someone", "Store", ":iPod_Control:Music:F03:IJKL.m4p"),
		mhit(13, 300000, 3, "Clocks", "Coldplay", "A Rush of Blood to the Head", ":iPod_Control:Music:F04:MNOP.m4a"))
	lists := chunk("mhlp", 92, map[int]uint32{8: 2},
		mhyp("Kayla’s iPod", true, 10, 11, 12, 13),
		mhyp("Road trip", false, 13, 12, 10))
	body := append(chunk("mhsd", 96, map[int]uint32{12: 1}, tracks), chunk("mhsd", 96, map[int]uint32{12: 2}, lists)...)
	head := chunk("mhbd", 0xbc, map[int]uint32{20: 2})
	if compress {
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		w.Write(body)
		w.Close()
		body = z.Bytes()
	}
	return append(head, body...)
}

func TestParseITunesDB(t *testing.T) {
	for _, compress := range []bool{false, true} {
		lib, err := parseITunesDB(testDB(compress))
		if err != nil {
			t.Fatal(err)
		}
		if lib.Name != "Kayla’s iPod" || len(lib.Tracks) != 3 || lib.Protected != 1 {
			t.Fatalf("compressed %v: %q, %d tracks, %d protected", compress, lib.Name, len(lib.Tracks), lib.Protected)
		}
		d := lib.Tracks[0]
		if d.Title != "Dreams" || d.Artist != "Fleetwood Mac" || d.Album != "Rumours" || d.Seconds != 241 ||
			d.Number != 2 || d.Disc != 1 || d.Path != "iPod_Control/Music/F01/ABCD.m4a" {
			t.Fatalf("track: %+v", d)
		}
		if len(lib.Playlists) != 1 || lib.Playlists[0].Name != "Road trip" || len(lib.Playlists[0].Tracks) != 2 {
			t.Fatalf("playlists: %+v", lib.Playlists) // the protected song left out
		}
	}
	if _, err := parseITunesDB([]byte("not a database at all")); err == nil {
		t.Fatal("parsed junk")
	}
}

func TestIPodBrowse(t *testing.T) {
	lib, err := parseITunesDB(testDB(true))
	if err != nil {
		t.Fatal(err)
	}
	ipodState.mu.Lock()
	ipodState.root, ipodState.lib = "/run/petasos/ipod", lib
	ipodState.mu.Unlock()
	defer func() {
		ipodState.mu.Lock()
		ipodState.root, ipodState.lib = "", nil
		ipodState.mu.Unlock()
	}()

	albums := ipodBrowse("albums", "", "")["playlists"].([]map[string]any)
	if len(albums) != 2 || albums[0]["title"] != "A Rush of Blood to the Head" || albums[1]["artist"] != "Fleetwood Mac" {
		t.Fatalf("albums: %v", albums)
	}
	rumours := ipodBrowse("playlist", albums[1]["id"].(string), "")["tracks"].([]libTrack)
	if len(rumours) != 2 || rumours[0].Title != "Second Hand News" || rumours[0].Source != ipodSource {
		t.Fatalf("Rumours in order: %+v", rumours)
	}
	lists := ipodBrowse("playlists", "", "")["playlists"].([]map[string]any)
	if len(lists) != 2 || lists[0]["title"] != "All songs" || lists[1]["title"] != "Road trip" {
		t.Fatalf("playlists: %v", lists)
	}
	trip := ipodBrowse("playlist", lists[1]["id"].(string), "")["tracks"].([]libTrack)
	if len(trip) != 2 || trip[0].Title != "Clocks" {
		t.Fatalf("a playlist keeps its order: %+v", trip)
	}
	found := ipodBrowse("search", "", "fleetwood dre")["tracks"].([]libTrack)
	if len(found) != 1 || found[0].Title != "Dreams" {
		t.Fatalf("search: %+v", found)
	}
	if f, _ := ipodFile("../../etc/passwd"); f != "/run/petasos/ipod/etc/passwd" {
		t.Fatalf("a track's file stays on the iPod: %s", f)
	}
}

func TestScanTags(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Music", "Album"), 0o755)
	os.WriteFile(filepath.Join(dir, "Music", "Album", "01 Song.mp3"), []byte("not really"), 0o644)
	os.WriteFile(filepath.Join(dir, "Music", "Album", "._01 Song.mp3"), []byte("fork"), 0o644)
	os.WriteFile(filepath.Join(dir, "Music", "cover.jpg"), []byte("jpg"), 0o644)
	lib, err := readIPod(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lib.From != "tags" || len(lib.Tracks) != 1 || lib.Tracks[0].Title != "01 Song" || lib.Tracks[0].Path != "Music/Album/01 Song.mp3" {
		t.Fatalf("%+v", lib)
	}
}

// An AAC song, as most on an iPod are: its tags read, and decoded from a second in.
func TestIPodAAC(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	song := filepath.Join(dir, "F00", "ABCD.m4a")
	os.MkdirAll(filepath.Dir(song), 0o755)
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2:sample_rate=44100",
		"-c:a", "aac", "-metadata", "title=Sine", "-metadata", "artist=Test", "-metadata", "album=Tones", song).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	lib, err := readIPod(dir)
	if err != nil || len(lib.Tracks) != 1 || lib.Tracks[0].Title != "Sine" || lib.Tracks[0].Album != "Tones" {
		t.Fatalf("%v %+v", err, lib)
	}
	ipodState.mu.Lock()
	ipodState.root, ipodState.lib = dir, lib
	ipodState.mu.Unlock()
	defer func() {
		ipodState.mu.Lock()
		ipodState.root, ipodState.lib = "", nil
		ipodState.mu.Unlock()
	}()
	pcm, stop, err := ipodPCM(lib.Tracks[0].lib(), libraryRate)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	data, err := io.ReadAll(pcm)
	if err != nil {
		t.Fatal(err)
	}
	if secs := float64(len(data)) / 4 / libraryRate; secs < 0.9 || secs > 1.1 {
		t.Fatalf("%.2f seconds left of a 2-second song from a second in", secs)
	}
	if _, _, err := ipodPCM(libTrack{Source: ipodSource, ID: "F00/gone.m4a"}, 0); err == nil {
		t.Fatal("played a song that isn't there")
	}
}
