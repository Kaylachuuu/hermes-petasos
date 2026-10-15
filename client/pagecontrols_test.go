package main

import (
	"encoding/json"
	"testing"
	"time"
)

// pageRig runs pageInputs with the screen in a given state, and listens to what is sent to the page.
func pageRig(t *testing.T, change func(*screenState)) (chan map[string]any, chan map[string]any, chan []byte) {
	t.Helper()
	before := display.snapshot()
	display.update(change)
	t.Cleanup(func() { display.update(func(st *screenState) { *st = before }) })
	page := make(chan []byte, 32)
	display.mu.Lock()
	display.subs[page] = struct{}{}
	display.mu.Unlock()
	t.Cleanup(func() { display.mu.Lock(); delete(display.subs, page); display.mu.Unlock() })
	in, out := make(chan map[string]any, 16), make(chan map[string]any, 16)
	go pageInputs(in, out)
	return in, out, page
}

// navSent is the next {"nav": ...} the page got.
func navSent(t *testing.T, page chan []byte) any {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-page:
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				if n, ok := m["nav"]; ok {
					return n
				}
			}
		case <-deadline:
			t.Fatal("no nav reached the page")
			return nil
		}
	}
}

func TestTheKnobMovesThroughAListAndPicks(t *testing.T) {
	in, out, page := pageRig(t, func(st *screenState) { st.Page, st.View, st.Phase, st.BT = "soundcloud", "likes", "idle", nil })
	in <- map[string]any{"input": "knob", "action": "turn", "delta": -1}
	if n := navSent(t, page); n != float64(-1) {
		t.Fatalf("turn: %v", n)
	}
	in <- knob("press", 0)
	in <- knob("release", 100)
	if n := navSent(t, page); n != "press" {
		t.Fatalf("press: %v", n)
	}
	in <- map[string]any{"input": "front", "action": "press", "t": int64(0)}
	in <- map[string]any{"input": "front", "action": "release", "t": int64(100)}
	if n := navSent(t, page); n != "back" {
		t.Fatalf("front tap: %v", n)
	}
	select {
	case ev := <-out:
		t.Fatalf("the plugin got %v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// On what's playing, the knob is the volume and the plugin's as ever; a front tap goes home.
func TestTheFrontButtonGoesHomeFromWhatsPlayingAndHoldingTalks(t *testing.T) {
	in, out, _ := pageRig(t, func(st *screenState) { st.Page, st.View, st.Phase, st.BT = "spotify", "now", "idle", nil })
	in <- map[string]any{"input": "knob", "action": "turn", "delta": 1}
	if ev := next(t, out); ev["action"] != "turn" {
		t.Fatalf("got %v", ev)
	}
	in <- map[string]any{"input": "front", "action": "press", "t": int64(0)}
	in <- map[string]any{"input": "front", "action": "release", "t": int64(100)}
	deadline := time.Now().Add(time.Second)
	for display.snapshot().Page != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if p := display.snapshot().Page; p != "" {
		t.Fatalf("still on %q", p)
	}
	display.update(func(st *screenState) { st.Page, st.View = "spotify", "now" })
	in <- map[string]any{"input": "front", "action": "press", "t": int64(1000)}
	if ev := next(t, out); ev["action"] != "press" || ev["t"] != int64(1000) {
		t.Fatalf("a hold should reach the plugin as it was pressed: %v", ev)
	}
	in <- map[string]any{"input": "front", "action": "release", "t": int64(2000)}
	if ev := next(t, out); ev["action"] != "release" {
		t.Fatalf("got %v", ev)
	}
}

func TestAPagesOwnSoftKeySwapsItsViews(t *testing.T) {
	_, _, page := pageRig(t, func(st *screenState) { st.Page, st.View, st.Phase, st.BT = "", "", "idle", nil })
	showPage("navidrome")
	if st := display.snapshot(); st.Page != "navidrome" || st.View != "now" {
		t.Fatalf("got %q %q", st.Page, st.View)
	}
	showPage("navidrome")
	if n := navSent(t, page); n != "swap" {
		t.Fatalf("got %v", n)
	}
	showPage("petasos")
	if p := display.snapshot().Page; p != "" {
		t.Fatalf("got %q", p)
	}
	showPage("nowhere")
	if p := display.snapshot().Page; p != "" {
		t.Fatalf("got %q", p)
	}
}

func TestPhoneAppsAreKnownByName(t *testing.T) {
	for name, want := range map[string]string{"Music": "apple", "Spotify": "spotify", "SoundCloud": "soundcloud", "Pocket Casts": ""} {
		if got := phoneService(name); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestTheSoundsLevel(t *testing.T) {
	silence := make([]byte, 4800)
	if l := pcmLevel(silence); l != 0 {
		t.Fatalf("silence: %v", l)
	}
	loud := make([]byte, 4800)
	for i := 0; i < len(loud); i += 4 {
		loud[i], loud[i+1] = 0xff, 0x7f // full scale, then 0
	}
	if l := pcmLevel(loud); l < 0.9 {
		t.Fatalf("loud: %v", l)
	}
}
