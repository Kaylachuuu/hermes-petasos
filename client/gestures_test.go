package main

import (
	"testing"
	"time"
)

func gestureRig(t *testing.T, matter bool) (chan map[string]any, chan map[string]any) {
	t.Helper()
	old := tapsMatter
	tapsMatter = func() bool { return matter }
	t.Cleanup(func() { tapsMatter = old })
	setGestureHints(map[string]gestureHint{"knob": {Taps: 3, TapMs: 350,
		Holds: []holdStep{{2000, "Stop the music"}, {3000, "Music page"}}}})
	t.Cleanup(func() { setGestureHints(nil) })
	in, out := make(chan map[string]any, 16), make(chan map[string]any, 16)
	go countGestures(in, out)
	return in, out
}

func knob(action string, t int64) map[string]any {
	return map[string]any{"input": "knob", "action": action, "t": t}
}

func next(t *testing.T, out chan map[string]any) map[string]any {
	t.Helper()
	select {
	case ev := <-out:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("nothing came through")
		return nil
	}
}

// Two quick taps reach the plugin as one release with taps 2; the second press and release
// stay on the device.
func TestQuickTapsAreCountedWhileMusicPlays(t *testing.T) {
	in, out := gestureRig(t, true)
	in <- knob("press", 0)
	in <- knob("release", 80)
	in <- knob("press", 200)
	in <- knob("release", 260)
	if ev := next(t, out); ev["action"] != "press" {
		t.Fatalf("first %v", ev)
	}
	ev := next(t, out)
	if ev["action"] != "release" || ev["taps"] != 2 || ev["t"] != int64(80) {
		t.Fatalf("got %v; want the first release with taps 2", ev)
	}
	select {
	case ev := <-out:
		t.Fatalf("unexpected %v", ev)
	case <-time.After(400 * time.Millisecond):
	}
}

// The third tap is the most counted, so it goes without waiting.
func TestATripleTapGoesAtOnce(t *testing.T) {
	in, out := gestureRig(t, true)
	for _, at := range []int64{0, 150, 300} {
		in <- knob("press", at)
		in <- knob("release", at+60)
	}
	next(t, out)
	start := time.Now()
	if ev := next(t, out); ev["taps"] != 3 {
		t.Fatalf("got %v", ev)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("waited after the third tap")
	}
}

// With Athena busy, or no music, a tap goes at once, so stopping her isn't delayed.
func TestATapGoesAtOnceWhenTapsDontMatter(t *testing.T) {
	in, out := gestureRig(t, false)
	in <- knob("press", 0)
	in <- knob("release", 80)
	next(t, out)
	select {
	case ev := <-out:
		if ev["action"] != "release" || ev["taps"] != nil {
			t.Fatalf("got %v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("the tap waited")
	}
}

// Held past a tap, the screen shows what letting go would do, and the release clears it.
func TestAHoldShowsWhatLettingGoDoes(t *testing.T) {
	in, out := gestureRig(t, true)
	in <- knob("press", 0)
	next(t, out)
	deadline := time.Now().Add(2 * time.Second)
	for display.snapshot().Hold == nil {
		if time.Now().After(deadline) {
			t.Fatal("no hold shown")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if h := display.snapshot().Hold; len(h.Holds) != 2 || h.Since != 350 {
		t.Fatalf("hold %+v", h)
	}
	in <- knob("release", 2500)
	if ev := next(t, out); ev["action"] != "release" || ev["taps"] != nil {
		t.Fatalf("got %v", ev)
	}
	if display.snapshot().Hold != nil {
		t.Fatal("the hold note stayed")
	}
}

// Other controls, and the knob's turns, pass straight through.
func TestOtherEventsPassThrough(t *testing.T) {
	in, out := gestureRig(t, true)
	in <- map[string]any{"input": "front", "action": "press", "t": int64(0)}
	in <- map[string]any{"input": "knob", "action": "turn", "delta": 1}
	if next(t, out)["input"] != "front" || next(t, out)["action"] != "turn" {
		t.Fatal("not passed on")
	}
}

func TestAPickedSongStartsTheShuffle(t *testing.T) {
	tracks := []libTrack{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	got, at := shuffledFrom(tracks, 2)
	if at != 0 || got[0].ID != "c" || len(got) != 4 {
		t.Fatalf("got %v from %d", got, at)
	}
}
