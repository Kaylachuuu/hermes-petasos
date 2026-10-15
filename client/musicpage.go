package main

import (
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/url"
)

// The music page: what's playing, with its controls, and your likes, playlists and a search to
// pick from, by touch. The knob's long hold (or a soft key set to it) opens and closes it; it
// steps aside for the home screen while Athena listens, thinks or speaks. The lists come from
// the plugin's /music/browse; picking a song plays its list from there, through the library
// player (library.go), as if Athena had put it on.

func togglePage(page string) {
	display.update(func(st *screenState) {
		if st.Page == page {
			st.Page = ""
		} else {
			st.Page = page
		}
	})
	backlight.wake()
}

// musicBrowse passes the page's list requests on to the plugin: ?what=likes, playlists,
// playlist (with source and id) or search (with q).
func musicBrowse(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if h == nil {
		http.Error(w, "Petasos is still starting.", http.StatusServiceUnavailable)
		return
	}
	q := url.Values{}
	for _, k := range []string{"what", "source", "id", "q"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	var answer map[string]any
	if err := h.call("GET", "/api/plugins/petasos/music/browse?"+q.Encode(), nil, &answer); err != nil {
		answer = map[string]any{"tracks": []any{}, "playlists": []any{},
			"note": "Can't reach " + display.agent() + " for the music (its Petasos plugin may need updating)."}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(answer)
}

// musicPlay plays a list from the song picked: {"title", "tracks", "at"}.
func musicPlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Title  string     `json:"title"`
		Tracks []libTrack `json:"tracks"`
		At     int        `json:"at"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil || len(body.Tracks) == 0 {
		http.Error(w, "tracks required", http.StatusBadRequest)
		return
	}
	if len(body.Tracks) > 500 {
		body.Tracks = body.Tracks[:500]
	}
	l := library
	l.mu.Lock()
	repeat, shuffle := l.repeat, l.shuffle // the page's repeat and shuffle carry over to what's picked next
	l.mu.Unlock()
	tracks, at := body.Tracks, body.At
	if shuffle {
		tracks, at = shuffledFrom(tracks, at)
	}
	l.startAt(body.Title, tracks, at, repeat, shuffle)
	l.mu.Lock()
	l.order = body.Tracks // shuffle off goes back to the list as it was
	l.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// shuffledFrom is the list in a random order, starting with the song picked.
func shuffledFrom(tracks []libTrack, at int) ([]libTrack, int) {
	if at < 0 || at >= len(tracks) {
		at = 0
	}
	rest := append(append([]libTrack(nil), tracks[:at]...), tracks[at+1:]...)
	rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	return append([]libTrack{tracks[at]}, rest...), 0
}

// musicControlPost is the page's buttons: {"op": "toggle", "next", "previous", "repeat_one", ...}.
func musicControlPost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Op string `json:"op"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) != nil {
		http.Error(w, "op required", http.StatusBadRequest)
		return
	}
	switch body.Op {
	case "toggle", "next", "previous", "stop", "repeat_one", "repeat_all", "repeat_off", "shuffle_on", "shuffle_off":
		musicControl(body.Op)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unknown op", http.StatusBadRequest)
	}
}

// musicPagePost closes the page from its own back button.
func musicPagePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	display.update(func(st *screenState) { st.Page = "" })
	w.WriteHeader(http.StatusNoContent)
}
