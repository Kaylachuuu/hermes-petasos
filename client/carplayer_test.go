package main

import "testing"

// The car's play and pause buttons only start or pause the music when that changes something:
// a car sending play as it connects leaves playing music alone.
func TestCarPlayAndPauseOnlyWhenTheyChangeSomething(t *testing.T) {
	out := make(chan map[string]any, 16)
	c := &carPlayer{out: out}
	p := carControls{c}
	pressed := func() (names []string) {
		for len(out) > 0 {
			if ev := <-out; ev["action"] == "press" {
				names = append(names, ev["input"].(string))
			}
		}
		return
	}
	saved := display.snapshot().Music
	defer display.update(func(st *screenState) { st.Music = saved })

	display.update(func(st *screenState) { st.Music = &nowPlaying{Title: "One", Playing: true} })
	p.Play()
	if got := pressed(); len(got) != 0 {
		t.Fatalf("play while playing pressed %v", got)
	}
	p.Pause()
	p.Next()
	if got := pressed(); len(got) != 2 || got[0] != "car-play" || got[1] != "car-next" {
		t.Fatalf("pause and next pressed %v", got)
	}
	display.update(func(st *screenState) { st.Music = &nowPlaying{Title: "One"} })
	p.Pause()
	p.Play()
	p.PlayPause()
	if got := pressed(); len(got) != 2 || got[0] != "car-play" || got[1] != "car-play" {
		t.Fatalf("paused: pause, play and play/pause pressed %v", got)
	}
}
