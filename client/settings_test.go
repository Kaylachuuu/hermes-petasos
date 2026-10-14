package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLightDimsAndTurnsOff(t *testing.T) {
	l := &light{set: lightSettings{Brightness: 100, DimAfter: 60, DimTo: 50, OffAfter: 300}, swallow: map[string]bool{}}
	st := display.snapshot()
	l.lastSeen = [2]int{st.NoticeID, st.VolumeID}
	l.active = time.Now()
	if got := l.target(160, -1); got != 160 {
		t.Fatalf("awake: %d, want 160", got)
	}
	l.active = time.Now().Add(-90 * time.Second)
	if got := l.target(160, -1); got != lightFloor+(160-lightFloor)/2 {
		t.Fatalf("dimmed: %d", got)
	}
	l.active = time.Now().Add(-10 * time.Minute)
	if got := l.target(160, -1); got != 0 {
		t.Fatalf("off: %d, want 0", got)
	}
	// A preset pressed in the dark only wakes the screen, and its release stays back too.
	if !l.touched(map[string]any{"input": "preset-1", "action": "press"}) {
		t.Fatal("a preset in the dark went through")
	}
	if !l.touched(map[string]any{"input": "preset-1", "action": "release"}) {
		t.Fatal("its release went through")
	}
	if got := l.target(160, -1); got != 160 {
		t.Fatalf("after waking: %d, want 160", got)
	}
	if l.touched(map[string]any{"input": "preset-1", "action": "press"}) {
		t.Fatal("a preset with the screen on was held back")
	}
}

func TestAutoBrightnessFollowsTheRoom(t *testing.T) {
	if ambientLevel(2, 160) != lightFloor || ambientLevel(5000, 160) != 160 {
		t.Fatal("ends of the curve")
	}
	if a, b := ambientLevel(20, 160), ambientLevel(400, 160); !(a < b) {
		t.Fatalf("brighter room, dimmer screen: %d, %d", a, b)
	}
}

func TestScreenSettingsWithTheKnob(t *testing.T) {
	saved := backlight
	defer func() { backlight = saved }()
	backlight = &light{set: defaultLight, file: filepath.Join(t.TempDir(), "screen.json"), swallow: map[string]bool{}}
	b := &bluetooth{open: true}
	b.screenView()
	press := func(input string) { b.handle(map[string]any{"input": input, "action": "press"}) }
	view := func() btScreen {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.view == nil {
			return btScreen{}
		}
		return *b.view
	}
	press("knob") // edit Brightness; choosing runs on its own goroutine
	for i := 0; i < 100 && !view().Editing; i++ {
		time.Sleep(time.Millisecond)
	}
	if v := view(); !v.Editing || v.Items[v.Sel].Label != "Brightness" {
		t.Fatalf("not editing brightness: %+v", v)
	}
	b.handle(map[string]any{"input": "knob", "action": "turn", "delta": 3})
	if got := backlight.settings().Brightness; got != 10 {
		t.Fatalf("one step from auto: %d%%, want 10%%", got)
	}
	press("front") // done
	if view().Editing || !b.isOpen() {
		t.Fatal("the front button should end editing and stay open")
	}
	press("preset-1")
	if v := view(); v.Tab != "bluetooth" {
		t.Fatalf("preset 1 should open the Bluetooth tab: %+v", v)
	}
	press("preset-2")
	if view().Tab != "screen" {
		t.Fatal("preset 2 should open the Screen tab")
	}
	press("front")
	if b.isOpen() {
		t.Fatal("the front button on a tab closes the settings")
	}
	b.openScreens()
	if view().Tab != "screen" {
		t.Fatal("the settings reopen on the last tab")
	}
}
