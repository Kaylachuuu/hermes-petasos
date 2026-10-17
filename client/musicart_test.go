package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// The page's pictures come from Hermes once, and a playlist's stay on disk, written only once;
// with an older plugin they come straight from SoundCloud or Spotify.
func TestMusicArtIsKept(t *testing.T) {
	dir := t.TempDir()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/plugins/petasos/music/art" && r.URL.Query().Get("ref") != "https://i1.sndcdn.com/old.jpg" {
			asked.Add(1)
			w.Write([]byte("\xff\xd8\xff\xe0 jpeg of " + r.URL.Query().Get("ref")))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	here.h = &hermes{dir: dir, cfg: config{Server: srv.URL, Device: "test"}, http: srv.Client()}
	defer func() { here.h = nil }()
	get := func(query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		musicArt(w, httptest.NewRequest("GET", "/music/art?"+query, nil))
		return w
	}

	pl := "keep=1&ref=" + url.QueryEscape("navidrome:pl-1")
	if w := get(pl); w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	kept := artFile(dir, "navidrome:pl-1")
	info, err := os.Stat(kept)
	if err != nil {
		t.Fatal("the playlist's picture wasn't kept")
	}
	get(pl)
	if again, _ := os.Stat(kept); !again.ModTime().Equal(info.ModTime()) {
		t.Fatal("a kept picture was written again")
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("Hermes asked %d times", n)
	}
	artMemory.Lock()
	artMemory.pics, artMemory.order, artMemory.size = map[string][]byte{}, nil, 0 // as after a restart
	artMemory.Unlock()
	if w := get(pl); w.Code != 200 || asked.Load() != 1 {
		t.Fatal("the kept picture wasn't used")
	}

	get("ref=" + url.QueryEscape("https://i1.sndcdn.com/track.jpg"))
	if entries, _ := os.ReadDir(filepath.Join(dir, "art")); len(entries) != 1 {
		t.Fatalf("%d pictures on disk; a track's stays in memory", len(entries))
	}
	if w := get("ref=" + url.QueryEscape("https://i1.sndcdn.com/old.jpg")); w.Code != http.StatusFound || w.Header().Get("Location") != "https://i1.sndcdn.com/old.jpg" {
		t.Fatalf("an older plugin's picture: %d %q", w.Code, w.Header().Get("Location"))
	}
}
