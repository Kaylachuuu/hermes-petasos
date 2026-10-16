package main

import (
	"os"
	"path/filepath"
	"testing"
)

// What was playing comes back after a restart, paused where it was, with repeat and shuffle.
func TestTheLibrarysMusicComesBackPaused(t *testing.T) {
	dir := t.TempDir()
	queue := []libTrack{{Source: "navidrome", ID: "1", Title: "Dreams"}, {Source: "navidrome", ID: "2", Title: "Gypsy"}}
	before := libraryState{Title: "Rumours", Queue: queue, Pos: 1, Seconds: 42.5, Repeat: "all", Shuffle: false}
	if err := writeJSON(filepath.Join(dir, libraryStateFile), before, 0o644); err != nil {
		t.Fatal(err)
	}
	l := &libraryPlayer{}
	was := display.snapshot()
	t.Cleanup(func() { display.update(func(st *screenState) { *st = was }) })
	l.restore(dir)
	if l.want != "paused" || l.pos != 1 || l.repeat != "all" || l.title != "Rumours" || l.frame != int(42.5*libraryRate) {
		t.Fatalf("restored %q %d %q %q %d", l.want, l.pos, l.repeat, l.title, l.frame)
	}
	if got := l.state(); got.Pos != 1 || got.Seconds != 42.5 || len(got.Queue) != 2 {
		t.Fatalf("state %+v", got)
	}
	if m := display.snapshot().Music; m == nil || m.Title != "Gypsy" || m.Playing {
		t.Fatalf("screen %+v", m)
	}
	// Nothing saved, or nothing queued: nothing comes back.
	os.Remove(filepath.Join(dir, libraryStateFile))
	l2 := &libraryPlayer{}
	l2.restore(dir)
	if l2.active() {
		t.Fatal("something came back from nothing")
	}
	if !sameState(libraryState{Pos: 1, Seconds: 3}, libraryState{Pos: 1, Seconds: 9}) || sameState(libraryState{Pos: 1}, libraryState{Pos: 2}) {
		t.Fatal("sameState")
	}
}
