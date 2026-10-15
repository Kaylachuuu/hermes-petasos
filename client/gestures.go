package main

import (
	"sync"
	"time"
)

// Gestures the plugin can't tell apart on its own: quick double and triple taps, which the
// device counts and reports with the release ("taps"), and holds with steps along the way,
// which the screen shows while the control is held (what letting go would do now). The plugin
// says which controls have them with its screen command ("gestures"); everything else passes
// straight through.
//
// Counting taps means waiting a moment after a tap to see whether another follows, so it only
// waits when a second tap could mean something: while music is playing and Athena is quiet.
// Otherwise a tap goes at once, so stopping Athena mid-sentence stays instant.

type gestureHint struct {
	Taps  int        `json:"taps"`   // how many quick taps to count, up to
	TapMs int        `json:"tap_ms"` // shorter than this is a tap
	Holds []holdStep `json:"holds"`  // the holds to show, in order
}

type holdStep struct {
	Ms    int    `json:"ms"`
	Label string `json:"label"`
}

// holdShown is the screen's note while a control is held (screenState.Hold).
type holdShown struct {
	ID    int        `json:"id"`
	Since int        `json:"since_ms"` // how long it had been held when the note appeared
	Holds []holdStep `json:"holds"`
}

// tapGap is how long after a tap another may start and still count with it.
const tapGap = 300 * time.Millisecond

var hints struct {
	mu  sync.Mutex
	got map[string]gestureHint
}

func setGestureHints(h map[string]gestureHint) {
	hints.mu.Lock()
	hints.got = h
	hints.mu.Unlock()
}

func gestureHintFor(name string) (gestureHint, bool) {
	hints.mu.Lock()
	defer hints.mu.Unlock()
	h, ok := hints.got[name]
	return h, ok
}

// tapsMatter is whether a second tap could change what a tap does: there is music to skip,
// and Athena isn't busy (when she is, a tap stops her, at once).
var tapsMatter = func() bool {
	st := display.snapshot()
	return st.Phase == "idle" && st.Music != nil && st.BT == nil
}

var holdNoteID int

// countGestures passes the controls' events on, counting quick taps and showing holds.
func countGestures(in <-chan map[string]any, out chan<- map[string]any) {
	var (
		name      string         // the control being counted, if any
		pressedT  float64        // its press's time, on the device's clock
		down      bool           // held now
		release   map[string]any // its first release, held back while taps are counted
		taps      int
		showing   bool
		tapWait   = time.NewTimer(time.Hour)
		holdTimer = time.NewTimer(time.Hour)
	)
	tapWait.Stop()
	holdTimer.Stop()
	clearHold := func() {
		holdTimer.Stop()
		if showing {
			showing = false
			display.update(func(st *screenState) { st.Hold = nil })
		}
	}
	flush := func() {
		tapWait.Stop()
		if release != nil {
			release["taps"] = taps
			out <- release
		}
		name, release, taps, down = "", nil, 0, false
	}
	for {
		select {
		case ev := <-in:
			input, _ := ev["input"].(string)
			action, _ := ev["action"].(string)
			hint, ok := gestureHintFor(input)
			if input != name && name != "" {
				flush() // another control: the count so far is done
			}
			if !ok || (action != "press" && action != "release") {
				out <- ev
				continue
			}
			switch action {
			case "press":
				if release != nil { // another tap in the count
					taps++
					down = true
					tapWait.Stop()
					continue
				}
				clearHold()
				name, down, taps = input, true, 0
				pressedT = floatOf(ev["t"])
				out <- ev
				if len(hint.Holds) > 0 {
					holdTimer.Reset(time.Duration(hint.TapMs) * time.Millisecond)
				}
			case "release":
				if release != nil {
					down = false
					if taps >= hint.Taps {
						flush()
					} else {
						tapWait.Reset(tapGap)
					}
					continue
				}
				clearHold()
				held := floatOf(ev["t"]) - pressedT
				if name != input || held >= float64(hint.TapMs) || hint.Taps <= 1 || !tapsMatter() {
					name, down = "", false
					out <- ev
					continue
				}
				release, taps, down = ev, 1, false
				tapWait.Reset(tapGap)
			}
		case <-tapWait.C:
			if !down {
				flush()
			}
		case <-holdTimer.C:
			if hint, ok := gestureHintFor(name); ok && down && release == nil {
				showing = true
				holdNoteID++
				note := &holdShown{ID: holdNoteID, Since: hint.TapMs, Holds: hint.Holds}
				display.update(func(st *screenState) { st.Hold = note })
				backlight.wake()
			}
		}
	}
}

func floatOf(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}
