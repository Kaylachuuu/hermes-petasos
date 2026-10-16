package main

import (
	"strings"
	"sync/atomic"
	"time"
)

// The controls on a music page, before the plugin sees them. The soft keys are the page's own
// there, the same on every page: what's playing, the likes, the playlists and the search (none
// on Apple Music's, whose music is chosen on the phone). In a list the knob is the page's:
// turning moves the highlight and pressing picks what it's on. The front button's tap steps
// back (a list to what's playing, what's playing to home), while holding it still talks to
// Athena, as everywhere: the press goes on to the plugin once it has been held long enough to be
// a hold.

// tapLimit is the plugin's hold_ms: shorter than this is a tap.
var tapLimit atomic.Int64

// knobVolume is how far a click of the knob turns the volume (the plugin's volume_step), when
// turning it is the volume; 0 when the plugin decides. The volume then changes here at once,
// rather than after a trip to Hermes and back, which a slow link made sluggish.
var knobVolume atomic.Int64

// localVolume changes the speaker's volume: the voice's, once it has started.
var localVolume = func(delta int) {}

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
			case name == "knob" && action == "turn" && knobVolume.Load() > 0:
				localVolume(intOf(ev["delta"], 0) * int(knobVolume.Load()))
				continue
			case strings.HasPrefix(name, "preset-") && onPage:
				if action == "press" {
					display.send(map[string]any{"nav": "key" + strings.TrimPrefix(name, "preset-")})
					backlight.wake()
				}
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
