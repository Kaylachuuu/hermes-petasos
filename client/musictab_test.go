package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Music tab edits the music sources on Hermes: values typed on the keyboard stay a draft
// until Save and check, which sends them and shows what the check found.
func TestMusicTabSavesTheSourcesOnHermes(t *testing.T) {
	var mu sync.Mutex
	var put map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plugins/petasos/music/settings":
			if r.Method == "PUT" {
				mu.Lock()
				json.NewDecoder(r.Body).Decode(&put)
				mu.Unlock()
			}
			w.Write([]byte(`{"navidrome":{"url":"","username":"","has_password":false},"folders":["/srv/music"],
				"soundcloud":{"client_id":"","has_secret":false,"account":""},"spotify":{"client_id":"spid","account":"Kayla"},"ffmpeg":true}`))
		case "/api/plugins/petasos/music/check":
			w.Write([]byte(`{"navidrome":{"ok":true},"folders":[{"path":"/srv/music","ok":true,"songs":12},{"path":"/mnt/nas","ok":true,"songs":3}],"soundcloud":null,"spotify":{"ok":true,"account":"Kayla"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h := &hermes{dir: t.TempDir(), cfg: config{Server: srv.URL, Device: "car-thing"}, http: srv.Client()}
	h.servesDevice()
	defer func() { here.h = nil }()
	saved := bt
	defer func() { bt = saved }()
	bt = &bluetooth{open: true}
	defer display.update(func(st *screenState) { st.BT = nil })
	page := httptest.NewServer(http.HandlerFunc(typedText))
	defer page.Close()

	view := func() btScreen {
		bt.mu.Lock()
		defer bt.mu.Unlock()
		if bt.view == nil {
			return btScreen{}
		}
		return *bt.view
	}
	waitFor := func(what string, ok func(btScreen) bool) btScreen {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if v := view(); ok(v) {
				return v
			}
		}
		t.Fatalf("never got %s; view is %+v", what, view())
		return btScreen{}
	}
	ev := func(input, action string, delta int) {
		bt.handle(map[string]any{"input": input, "action": action, "delta": delta})
	}
	choose := func(label string) {
		t.Helper()
		v := waitFor("the Music tab", func(v btScreen) bool { return v.View == "music" })
		for i, it := range v.Items {
			if it.Label == label {
				for view().Sel != i {
					ev("knob", "turn", map[bool]int{true: 1, false: -1}[view().Sel < i])
				}
				ev("knob", "press", 0)
				return
			}
		}
		t.Fatalf("no %q in %+v", label, v.Items)
	}
	typeText := func(text string) {
		t.Helper()
		v := waitFor("the keyboard", func(v btScreen) bool { return v.View == "keyboard" })
		if !v.Plain {
			t.Fatal("the keyboard starts in capitals for an address")
		}
		raw, _ := json.Marshal(map[string]any{"keyboard": v.Keyboard, "text": text})
		resp, err := http.Post(page.URL, "application/json", bytes.NewReader(raw))
		if err != nil || resp.StatusCode != http.StatusNoContent {
			t.Fatalf("typing: %v %v", err, resp)
		}
		io.Copy(io.Discard, resp.Body)
	}

	bt.show(&btScreen{View: "screen"})
	ev("preset-4", "press", 0)
	v := waitFor("the Music tab", func(v btScreen) bool { return v.View == "music" && v.Tab == "music" })
	if v.Items[1].Value != "Not used" {
		t.Fatalf("Music tab: %+v", v.Items)
	}
	choose("Address")
	typeText("http://localhost:4533")
	choose("Username")
	typeText("kayla")
	choose("Password")
	typeText("hunter2")
	choose("Add a folder")
	typeText("/mnt/nas")
	v = waitFor("the draft", func(v btScreen) bool { return v.View == "music" && strings.Contains(v.Note, "Not saved") })
	mu.Lock()
	if put != nil {
		t.Fatal("saved before Save and check")
	}
	mu.Unlock()
	choose("Save and check")
	v = waitFor("the check", func(v btScreen) bool { return v.View == "message" && v.Title == "Music" })
	if !strings.Contains(v.Note, "/mnt/nas: 3 songs") || !strings.Contains(v.Note, "Navidrome answers") ||
		!strings.Contains(v.Note, "Spotify works, signed in as Kayla") {
		t.Fatalf("check: %+v", v)
	}
	mu.Lock()
	defer mu.Unlock()
	nav, _ := put["navidrome"].(map[string]any)
	if nav["url"] != "http://localhost:4533" || nav["username"] != "kayla" || nav["password"] != "hunter2" ||
		len(put["folders"].([]any)) != 2 || put["spotify"].(map[string]any)["client_id"] != "spid" {
		t.Fatalf("sent %+v", put)
	}
}

// Signing in to Spotify, like SoundCloud, opens its page in your own browser, which comes back
// with a code that Hermes turns into the sign-in.
func TestAppSpotifySignsInThroughTheBrowser(t *testing.T) {
	var connected, disconnected string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plugins/petasos/music/spotify/authorize":
			data, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(data), `"http://127.0.0.1:8187/app/spotify"`) {
				t.Errorf("authorize body %s", data)
			}
			w.Write([]byte(`{"url":"https://accounts.spotify.com/authorize?state=s1"}`))
		case "/api/plugins/petasos/music/spotify/connect":
			data, _ := io.ReadAll(r.Body)
			connected = string(data)
			w.Write([]byte(`{"account":"Kayla"}`))
		case "/api/plugins/petasos/music/spotify/disconnect":
			disconnected = r.Method
			w.Write([]byte(`{"account":""}`))
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
	appSpotify(rec, httptest.NewRequest("POST", "/app/spotify?start", nil))
	if rec.Code != 200 || opened != "https://accounts.spotify.com/authorize?state=s1" {
		t.Fatalf("%d %s, opened %q", rec.Code, rec.Body, opened)
	}
	rec = httptest.NewRecorder()
	appSpotify(rec, httptest.NewRequest("GET", "/app/spotify?code=c1&state=s1", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Signed in to Spotify") || connected != `{"code":"c1","state":"s1"}` {
		t.Fatalf("%d %s (sent %q)", rec.Code, rec.Body, connected)
	}
	rec = httptest.NewRecorder()
	appSpotify(rec, httptest.NewRequest("GET", "/app/spotify?error=access_denied", nil))
	if !strings.Contains(rec.Body.String(), "Couldn&#39;t sign in to Spotify") || !strings.Contains(rec.Body.String(), "access_denied") {
		t.Fatalf("error page: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	appSpotify(rec, httptest.NewRequest("POST", "/app/spotify?disconnect", nil))
	if rec.Code != 200 || disconnected != "POST" {
		t.Fatalf("disconnect: %d %s", rec.Code, rec.Body)
	}
}
