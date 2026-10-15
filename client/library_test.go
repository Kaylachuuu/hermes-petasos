package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A queue from the library plays through to the end on the speaker, at 48 kHz, stops for
// Athena and carries on from where it was rather than from the start.
func TestLibraryPlaysAndStepsAsideForAthena(t *testing.T) {
	dir := t.TempDir()
	tone, err := os.ReadFile("testdata/tone.mp3") // two seconds at 44.1 kHz
	if err != nil {
		t.Fatal(err)
	}
	var streams atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/plugins/petasos/music/stream" && r.URL.Query().Get("source") == "folder" {
			streams.Add(1)
			w.Write(tone)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	// aplay, checking it is asked for raw 48 kHz stereo and keeping what it was given, at twice
	// a speaker's pace.
	os.WriteFile(filepath.Join(dir, "aplay"), []byte("#!/bin/sh\ncase \"$*\" in *'-t raw -f S16_LE -r 48000 -c 2'*) ;; *) exit 3;; esac\n"+
		"while :; do n=$(dd bs=19200 count=1 iflag=fullblock status=none | tee -a "+dir+"/played.$$ | wc -c); [ $n -gt 0 ] || exit 0; sleep 0.05; done\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	oldV := virtual
	virtual = false
	defer func() { virtual = oldV }()
	display.setPhase("idle")

	l := &libraryPlayer{}
	h := &hermes{dir: dir, cfg: config{Server: srv.URL, Device: "test"}, http: srv.Client()}
	l.attach(h, &voice{h: h, speaker: "bluealsa"})
	saved := library
	library = l
	defer func() { library = saved }()

	l.start("Tones by Nobody", []libTrack{{Source: "folder", ID: "a", Title: "One"}, {Source: "folder", ID: "b", Title: "Two"}}, "", false)
	time.Sleep(300 * time.Millisecond)
	display.setPhase("listening") // Athena takes the speaker partway into the first song
	time.Sleep(100 * time.Millisecond)
	if l.playing() {
		t.Fatal("still playing while Athena listens")
	}
	if st := display.snapshot(); st.Music == nil || st.Music.Title != "One" || st.Music.Source != "Music" {
		t.Fatalf("screen shows %+v", st.Music)
	}
	display.setPhase("idle")
	deadline := time.Now().Add(10 * time.Second)
	for l.active() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if l.active() {
		t.Fatal("the queue never finished")
	}
	if n := streams.Load(); n != 3 {
		t.Fatalf("%d streams opened; want 3 (one, one again after Athena, two)", n)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "played.*"))
	total := 0
	for _, f := range files {
		data, _ := os.ReadFile(f)
		total += len(data)
	}
	// Both songs once each at 48 kHz stereo 16-bit (MP3 adds a little padding).
	if want := 2 * 2 * 48000 * 4; total < want*95/100 || total > want*110/100 {
		t.Fatalf("played %d bytes; want about %d", total, want)
	}
	if display.snapshot().Music != nil {
		t.Fatal("the screen still shows music after the queue ended")
	}
}

func (l *libraryPlayer) playing() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.run != nil
}

func TestResampleStereoKeepsLengthAndShape(t *testing.T) {
	in := make([]byte, 44100*4)
	for i := 0; i < 44100; i++ {
		binary.LittleEndian.PutUint16(in[4*i:], uint16(int16(i%1000)))
		binary.LittleEndian.PutUint16(in[4*i+2:], uint16(int16(-(i % 1000))))
	}
	out, err := io.ReadAll(resampleStereo(bytes.NewReader(in), 44100, 48000))
	if err != nil {
		t.Fatal(err)
	}
	if frames := len(out) / 4; frames < 47990 || frames > 48000 {
		t.Fatalf("%d frames from a second at 44.1 kHz", frames)
	}
	for i := 0; i < len(out)/4; i += 997 {
		l, r := int16(binary.LittleEndian.Uint16(out[4*i:])), int16(binary.LittleEndian.Uint16(out[4*i+2:]))
		if l != -r {
			t.Fatalf("frame %d: channels swapped or mixed (%d, %d)", i, l, r)
		}
	}
}

// Music Athena chose comes with her reply and goes to the library; the knob and buttons then
// control the library rather than the phone.
func TestMusicCommandsGoToTheLibraryOnceItPlays(t *testing.T) {
	l := &libraryPlayer{}
	saved := library
	library = l
	defer func() { library = saved }()
	display.setPhase("thinking")
	defer display.setPhase("idle")
	v := &voice{}
	v.carryOut(map[string]any{"do": "music", "op": "play", "title": "Rumours",
		"tracks": []any{map[string]any{"source": "navidrome", "id": "1", "title": "Dreams", "artist": "Fleetwood Mac"}}})
	deadline := time.Now().Add(2 * time.Second)
	for !l.active() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !l.active() || l.playing() {
		t.Fatalf("active %v, playing %v: it should wait for Athena", l.active(), l.run != nil)
	}
	if st := display.snapshot(); st.Music == nil || !strings.Contains(st.Music.Source, "Navidrome") {
		t.Fatalf("screen shows %+v", st.Music)
	}
	v.carryOut(map[string]any{"do": "music", "op": "toggle"})
	time.Sleep(50 * time.Millisecond)
	l.mu.Lock()
	want := l.want
	l.mu.Unlock()
	if want != "paused" {
		t.Fatalf("toggle left it %q", want)
	}
	v.carryOut(map[string]any{"do": "music", "op": "stop"})
	time.Sleep(50 * time.Millisecond)
	if l.active() {
		t.Fatal("still active after stop")
	}
}

// The app's Settings page saves the music sources on Hermes and shows what the check found.
func TestAppSettingsSaveTheMusicOnHermes(t *testing.T) {
	var saved string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plugins/petasos/music/settings":
			if r.Method == "PUT" {
				data, _ := io.ReadAll(r.Body)
				saved = string(data)
			}
			w.Write([]byte(`{"navidrome":{"url":"","username":"","has_password":false},"folders":["/srv/music"],"ffmpeg":true}`))
		case "/api/plugins/petasos/music/check":
			w.Write([]byte(`{"navidrome":null,"folders":[{"path":"/srv/music","ok":true,"songs":12}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldV := virtual
	virtual = true
	defer func() { virtual = oldV }()
	here.mu.Lock()
	oldH := here.h
	here.h = &hermes{cfg: config{Server: srv.URL, Device: "mac"}, http: srv.Client()}
	here.mu.Unlock()
	defer func() { here.mu.Lock(); here.h = oldH; here.mu.Unlock() }()

	rec := httptest.NewRecorder()
	appMusic(rec, httptest.NewRequest("PUT", "/app/music", strings.NewReader(`{"folders":["/srv/music"]}`)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"songs":12`) || saved != `{"folders":["/srv/music"]}` {
		t.Fatalf("%d %s (sent %q)", rec.Code, rec.Body, saved)
	}
	rec = httptest.NewRecorder()
	appMusic(rec, httptest.NewRequest("PUT", "/app/music", strings.NewReader(`not json`)))
	if rec.Code != 400 {
		t.Fatalf("bad body: %d", rec.Code)
	}
}

// Signing in to SoundCloud opens its page in your own browser, and the browser comes back with
// a code that Hermes turns into the sign-in.
func TestAppSoundCloudSignsInThroughTheBrowser(t *testing.T) {
	var connected string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plugins/petasos/music/soundcloud/authorize":
			data, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(data), soundCloudRedirect) {
				t.Errorf("authorize body %s", data)
			}
			w.Write([]byte(`{"url":"https://secure.soundcloud.com/authorize?state=s1"}`))
		case "/api/plugins/petasos/music/soundcloud/connect":
			data, _ := io.ReadAll(r.Body)
			connected = string(data)
			w.Write([]byte(`{"account":"kayla"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	oldV := virtual
	virtual = true
	defer func() { virtual = oldV }()
	here.mu.Lock()
	oldH := here.h
	here.h = &hermes{cfg: config{Server: srv.URL, Device: "mac"}, http: srv.Client()}
	here.mu.Unlock()
	defer func() { here.mu.Lock(); here.h = oldH; here.mu.Unlock() }()
	var opened string
	oldOpen := openInBrowser
	openInBrowser = func(page string) error { opened = page; return nil }
	defer func() { openInBrowser = oldOpen }()

	rec := httptest.NewRecorder()
	appSoundCloud(rec, httptest.NewRequest("POST", "/app/soundcloud?start", nil))
	if rec.Code != 200 || opened != "https://secure.soundcloud.com/authorize?state=s1" {
		t.Fatalf("%d %s, opened %q", rec.Code, rec.Body, opened)
	}
	rec = httptest.NewRecorder()
	appSoundCloud(rec, httptest.NewRequest("GET", "/app/soundcloud?code=c1&state=s1", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Signed in to SoundCloud") || connected != `{"code":"c1","state":"s1"}` {
		t.Fatalf("%d %s (sent %q)", rec.Code, rec.Body, connected)
	}
	rec = httptest.NewRecorder()
	appSoundCloud(rec, httptest.NewRequest("GET", "/app/soundcloud?error=access_denied", nil))
	if !strings.Contains(rec.Body.String(), "Couldn&#39;t sign in") || !strings.Contains(rec.Body.String(), "access_denied") {
		t.Fatalf("error page: %s", rec.Body)
	}
}

// Repeat and shuffle change what comes next without touching the song playing.
func TestLibraryRepeatAndShuffle(t *testing.T) {
	l := &libraryPlayer{}
	songs := []libTrack{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}, {ID: "5"}}
	l.queue, l.order, l.want, l.pos = songs, songs, "paused", 1
	l.control("shuffle_on")
	if l.queue[1].ID != "2" || len(l.queue) != 5 || !l.shuffle {
		t.Fatalf("shuffle moved the song playing: %+v", l.queue)
	}
	l.pos = 3
	now := l.queue[3]
	l.control("shuffle_off")
	if l.queue[l.pos] != now || l.queue[0].ID != "1" || l.shuffle {
		t.Fatalf("shuffle off: pos %d of %+v, want %+v", l.pos, l.queue, now)
	}
	l.control("repeat_all")
	l.pos = 4
	l.control("next")
	if l.pos != 0 || !l.active() {
		t.Fatalf("repeat all: next from the last song went to %d (active %v)", l.pos, l.active())
	}
	l.control("repeat_off")
	l.pos = 4
	l.control("next")
	if l.active() {
		t.Fatal("next past the last song should end the queue")
	}
}
