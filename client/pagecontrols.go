package main

import (
	"sync/atomic"
	"time"
)

// The controls on a music page, before the plugin sees them. In a list the knob is the page's:
// turning moves the highlight and pressing picks what it's on. The front button's tap steps
// back (an open playlist to the playlists, a list to what's playing, what's playing to home),
// while holding it still talks to Athena, as everywhere: the press goes on to the plugin once it
// has been held long enough to be a hold.

// tapLimit is the plugin's hold_ms: shorter than this is a tap.
var tapLimit atomic.Int64

func init() { tapLimit.Store(350) }

func pageInputs(in <-chan map[string]any, out chan<- map[string]any) {
	var (
		front    map[string]any // the front button's press, held back while it may be a tap
		knobHeld bool           // the knob's press went to the page, so its release does too
		timer    = time.NewTimer(time.Hour)
	)
	timer.Stop()
	for {
		select {
		case ev := <-in:
			name, _ := ev["input"].(string)
			action, _ := ev["action"].(string)
			st := display.snapshot()
			onPage := st.Page != "" && st.BT == nil && st.Phase == "idle"
			listing := onPage && st.View != "" && st.View != "now"
			switch {
			case name == "knob" && action == "turn" && listing:
				display.send(map[string]any{"nav": intOf(ev["delta"], 0)})
				backlight.wake()
				continue
			case name == "knob" && action == "press" && listing:
				knobHeld = true
				display.send(map[string]any{"nav": "press"})
				continue
			case name == "knob" && action == "release" && knobHeld:
				knobHeld = false
				continue
			case name == "front" && action == "press" && onPage:
				front = ev
				timer.Reset(time.Duration(tapLimit.Load()) * time.Millisecond)
				continue
			case name == "front" && action == "release" && front != nil:
				timer.Stop()
				front = nil
				stepBack(st)
				continue
			}
			out <- ev
		case <-timer.C:
			if front != nil { // held: talking to Athena
				out <- front
				front = nil
			}
		}
	}
}

// stepBack is the front button's tap on a music page.
func stepBack(st screenState) {
	if st.View == "now" || st.View == "" || st.Page == "apple" {
		showPage("")
		return
	}
	display.send(map[string]any{"nav": "back"}) // the page knows where in its lists it is
}
