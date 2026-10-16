package main

import (
	"encoding/json"
	"log"
	"path/filepath"
	"time"
)

// The library's music is kept across restarts (an update, a power cycle): the queue and the song
// in it, how far in, its name, and repeat and shuffle, in library.json beside config.json. It
// comes back paused, on the screen and its music page, to carry on with the play button.

const libraryStateFile = "library.json"

type libraryState struct {
	Title   string     `json:"title,omitempty"`
	Queue   []libTrack `json:"queue,omitempty"`
	Order   []libTrack `json:"order,omitempty"` // the queue as it came, when shuffled
	Pos     int        `json:"pos"`
	Seconds float64    `json:"seconds"` // how far into the song
	Repeat  string     `json:"repeat,omitempty"`
	Shuffle bool       `json:"shuffle,omitempty"`
}

func (l *libraryPlayer) state() libraryState {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := libraryState{Repeat: l.repeat, Shuffle: l.shuffle}
	if l.want != "" && l.pos < len(l.queue) {
		s.Title, s.Queue, s.Pos = l.title, l.queue, l.pos
		s.Seconds = float64(int(float64(l.positionLocked())/libraryRate*10)) / 10
		if l.shuffle {
			s.Order = l.order
		}
	}
	return s
}

// restore puts back what was playing when petasos last ran, paused.
func (l *libraryPlayer) restore(dir string) {
	var s libraryState
	if readJSON(filepath.Join(dir, libraryStateFile), &s) != nil {
		return
	}
	l.mu.Lock()
	l.repeat, l.shuffle = repeatMode(s.Repeat), s.Shuffle
	if s.Pos >= 0 && s.Pos < len(s.Queue) {
		l.title, l.queue, l.order, l.pos = s.Title, s.Queue, s.Order, s.Pos
		if len(l.order) == 0 {
			l.order = s.Queue
		}
		l.frame = max(0, int(s.Seconds*libraryRate))
		l.want = "paused"
		log.Printf("the library's music is back, paused: %s", s.Queue[s.Pos].Title)
	}
	l.mu.Unlock()
	l.show()
}

// saveState saves the library's state now.
func (l *libraryPlayer) saveState(dir string) {
	if err := writeJSON(filepath.Join(dir, libraryStateFile), l.state(), 0o644); err != nil {
		log.Printf("can't keep the library's music: %v", err)
	}
}

// keepState saves the library's state whenever it changes, checking every few seconds. While a
// song plays only its place changes, which is saved every half minute, to spare the flash.
func (l *libraryPlayer) keepState(dir string) {
	path := filepath.Join(dir, libraryStateFile)
	var last libraryState
	var saved time.Time
	for ; ; time.Sleep(5 * time.Second) {
		s := l.state()
		if s.Seconds == last.Seconds && sameState(s, last) {
			continue
		}
		if sameState(s, last) && time.Since(saved) < 30*time.Second { // only the place moved
			continue
		}
		if err := writeJSON(path, s, 0o644); err != nil {
			log.Printf("can't keep the library's music: %v", err)
			continue
		}
		last, saved = s, time.Now()
	}
}

// sameState is whether two states differ in more than the place in the song.
func sameState(a, b libraryState) bool {
	a.Seconds, b.Seconds = 0, 0
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
