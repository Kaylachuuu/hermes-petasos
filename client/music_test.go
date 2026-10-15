package main

import (
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// The phone's music steps aside while Athena listens and speaks, and comes back after, but
// only when it was the Car Thing that paused it.
func TestMusicStepsAsideForAthena(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	saved := musicCall
	musicCall = func(_ dbus.ObjectPath, method string) { mu.Lock(); calls = append(calls, method); mu.Unlock() }
	defer func() { musicCall = saved }()
	got := func() []string {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}

	m := &phoneMusic{player: "/org/bluez/hci0/dev_X/player0", status: "playing"}
	m.phaseChanged("ready")
	m.phaseChanged("listening")
	m.phaseChanged("thinking")
	if c := got(); len(c) != 1 || c[0] != "Pause" {
		t.Fatalf("listening: %v", c)
	}
	m.status = "paused"
	m.freeSpeaker()
	m.phaseChanged("idle")
	if c := got(); len(c) != 1 || c[0] != "Play" {
		t.Fatalf("done: %v", c)
	}

	// Paused by hand: Athena leaves it paused.
	m.status = "paused"
	m.phaseChanged("listening")
	m.phaseChanged("idle")
	if c := got(); len(c) != 0 {
		t.Fatalf("left paused: %v", c)
	}
}
