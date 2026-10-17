package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLightDimsAndTurnsOff(t *testing.T) {
	l := &light{set: lightSettings{Brightness: 100, DimAfter: 30, DimTo: 50, OffAfter: 60}, swallow: map[string]bool{}}
	st := display.snapshot()
	l.lastSeen = [2]int{st.NoticeID, st.VolumeID}
	l.active = time.Now()
	if got := l.target(160, -1); got != 160 {
		t.Fatalf("awake: %d, want 160", got)
	}
	l.active = time.Now().Add(-45 * time.Second)
	if got := l.target(160, -1); got != lightFloor+(160-lightFloor)/2 {
		t.Fatalf("dimmed: %d", got)
	}
	l.active = time.Now().Add(-2 * time.Minute)
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
	waitTab := func(tab string) {
		t.Helper()
		for i := 0; i < 1000 && view().Tab != tab; i++ {
			time.Sleep(time.Millisecond)
		}
		if v := view(); v.Tab != tab {
			t.Fatalf("wanted the %s tab: %+v", tab, v)
		}
	}
	press("preset-1")
	waitTab("device")
	press("preset-2")
	waitTab("display")
	press("front")
	if b.isOpen() {
		t.Fatal("the front button on a tab closes the settings")
	}
	b.openScreens()
	if view().Tab != "display" {
		t.Fatal("the settings reopen on the last tab")
	}
	// Left on the second set of tabs, they reopen on the first.
	press("mute")
	press("preset-1")
	waitTab("music")
	b.mu.Lock()
	b.tab = "music" // what the Music tab sets once Hermes answers
	b.mu.Unlock()
	press("front")
	b.openScreens()
	waitTab("device")
	if v := view(); v.Set != 0 {
		t.Fatalf("reopened on set %d", v.Set)
	}
}

// The screensaver starts once the screen is left alone; the press or turn that ends it goes no
// further, and Athena busy ends it too. Preview shows it at once, from the Display tab.
func TestScreensaver(t *testing.T) {
	saved, shown := backlight, display.snapshot()
	defer func() {
		backlight = saved
		display.update(func(st *screenState) { *st = shown })
	}()
	display.update(func(st *screenState) { st.Phase, st.BT, st.Saver = "idle", nil, "" })
	backlight = &light{set: defaultLight, file: filepath.Join(t.TempDir(), "screen.json"), swallow: map[string]bool{}}
	l := backlight
	st := display.snapshot()
	l.lastSeen = [2]int{st.NoticeID, st.VolumeID}
	l.active = time.Now().Add(-5 * time.Minute)
	l.showSaver()
	if got := display.snapshot().Saver; got != "" {
		t.Fatalf("with the screensaver off, %q shows", got)
	}
	s := l.settings()
	s.Saver, s.SaverAfter = "clock", 90
	l.change(s)
	l.active = time.Now().Add(-60 * time.Second)
	l.showSaver()
	if got := display.snapshot().Saver; got != "" {
		t.Fatalf("%q shows before its time", got)
	}
	l.active = time.Now().Add(-91 * time.Second)
	l.showSaver()
	if got := display.snapshot().Saver; got != "clock" {
		t.Fatalf("after 91 seconds: %q", got)
	}
	if !l.touched(map[string]any{"input": "knob", "action": "press"}) || !l.touched(map[string]any{"input": "knob", "action": "release"}) {
		t.Fatal("the knob pressed to wake it went through")
	}
	if got := display.snapshot().Saver; got != "" {
		t.Fatalf("still showing %q after the knob", got)
	}
	if l.touched(map[string]any{"input": "knob", "action": "press"}) {
		t.Fatal("the next press was held back")
	}
	l.touched(map[string]any{"input": "knob", "action": "release"})
	// A turn wakes it; a car's button goes through.
	l.active = time.Now().Add(-2 * time.Minute)
	l.showSaver()
	if l.touched(map[string]any{"input": "car-next", "action": "press"}) {
		t.Fatal("the car's button was held back")
	}
	l.active = time.Now().Add(-2 * time.Minute)
	l.showSaver()
	if !l.touched(map[string]any{"input": "knob", "action": "turn", "delta": 1}) {
		t.Fatal("the turn that woke it went through")
	}
	// Athena listening ends it.
	l.active = time.Now().Add(-2 * time.Minute)
	l.showSaver()
	display.update(func(st *screenState) { st.Phase = "listening" })
	l.showSaver()
	if got := display.snapshot().Saver; got != "" {
		t.Fatalf("still showing %q while Athena listens", got)
	}
	display.update(func(st *screenState) { st.Phase = "idle" })
	// Preview, from the Display tab, until the next press.
	b := &bluetooth{open: true}
	b.screenViewAt(13, false)
	b.mu.Lock()
	v := *b.view
	b.mu.Unlock()
	display.update(func(st *screenState) { st.BT = &v })
	if it := v.Items[v.Sel]; it.Label != "Preview" || it.Off {
		t.Fatalf("on %+v", it)
	}
	v.Items[v.Sel].do()
	if got := display.snapshot().Saver; got != "clock" {
		t.Fatalf("Preview showed %q", got)
	}
	l.showSaver()
	if got := display.snapshot().Saver; got != "clock" {
		t.Fatal("the preview ended by itself")
	}
	if !l.touched(map[string]any{"input": "preset-2", "action": "press"}) {
		t.Fatal("the preset that ended the preview went through")
	}
	l.showSaver()
	if got := display.snapshot().Saver; got != "" {
		t.Fatalf("still previewing %q", got)
	}
	// With no screensaver, Start after and Preview are greyed out.
	s = l.settings()
	s.Saver = "off"
	l.change(s)
	b.screenViewAt(11, false)
	b.mu.Lock()
	v = *b.view
	b.mu.Unlock()
	if !v.Items[12].Off || !v.Items[13].Off || v.Items[11].Value != "Off" {
		t.Fatalf("with it off: %+v", v.Items[11:])
	}
	if s := (lightSettings{Saver: "lava", SaverAfter: 100}).valid(); s.Saver != "off" || s.SaverAfter != 90 {
		t.Fatalf("an unknown setting became %+v", s)
	}
}

// Unticking Dim screen greys out its two rows, which the knob then passes over, and the front
// button puts back a change being made.
func TestDisplayTabDimBoxAndCancelling(t *testing.T) {
	saved := backlight
	defer func() { backlight = saved }()
	backlight = &light{set: defaultLight, file: filepath.Join(t.TempDir(), "screen.json"), swallow: map[string]bool{}}
	b := &bluetooth{open: true}
	view := func() btScreen {
		b.mu.Lock()
		defer b.mu.Unlock()
		return *b.view
	}
	b.screenViewAt(1, false)
	if v := view(); v.Items[1].Label != "Dim screen" || v.Items[1].Check != "on" || v.Items[2].Off {
		t.Fatalf("Dim screen: %+v", v.Items[:3])
	}
	view().Items[1].do()
	if v := view(); !backlight.settings().NoDim || v.Items[1].Check != "off" || !v.Items[2].Off || !v.Items[3].Off {
		t.Fatalf("unticked: %+v", v.Items[:4])
	}
	b.move(1)
	if v := view(); v.Items[v.Sel].Label != "Turn off after" {
		t.Fatalf("the knob landed on %s", v.Items[v.Sel].Label)
	}
	view().Items[view().Sel].do() // change Turn off after, then think better of it
	b.adjust(1)
	if backlight.settings().OffAfter != 15 {
		t.Fatalf("turned to %d", backlight.settings().OffAfter)
	}
	b.handle(map[string]any{"input": "front", "action": "press"})
	if v := view(); v.Editing || backlight.settings().OffAfter != 0 || !b.isOpen() {
		t.Fatalf("the front button should put it back: %d %+v", backlight.settings().OffAfter, v)
	}
	// A setting from before the box: never dimming.
	if s := (lightSettings{DimAfter: 0, DimTo: 30}).valid(); !s.NoDim || s.DimAfter != 60 {
		t.Fatalf("never dimming became %+v", s)
	}
}

// Each speaker has its own microphone choice; one not yet set keeps the one choice from before.
func TestEachSpeakerHasItsOwnMicrophone(t *testing.T) {
	dir := t.TempDir()
	defer carMic.Store(false)
	carMic.Store(true)
	if !externalMic(dir, "aa:bb:cc:dd:ee:ff") {
		t.Fatal("a speaker not set should keep the car's microphone")
	}
	if err := setExternalMic(dir, "aa:bb:cc:dd:ee:ff", false); err != nil {
		t.Fatal(err)
	}
	if externalMic(dir, "AA:BB:CC:DD:EE:FF") || !externalMic(dir, "11:22:33:44:55:66") {
		t.Fatal("one speaker's choice should leave the others alone")
	}
}
