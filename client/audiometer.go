package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"
)

// The sound's level, for the top meander: what Athena says and the library's music, as they
// are heard. Petasos plays both itself, so it knows each piece of the sound and when the speaker
// will play it; the levels are worked out as the sound is handed over and sent to the page when
// their time comes. The phone's music passes through BlueALSA without petasos seeing it, so it
// has no level (the page then shows the volume alone).

const meterStep = 50 * time.Millisecond

type timedLevel struct {
	at    time.Time
	level float64
}

type audioMeter struct {
	mu      sync.Mutex
	queue   []timedLevel
	sending bool // a level went out since the last 0
	started bool
}

var meter = &audioMeter{}

// feed adds a piece of 16-bit PCM that starts playing at the given time.
func (m *audioMeter) feed(pcm []byte, rate, channels int, at time.Time) {
	if rate <= 0 || channels <= 0 {
		return
	}
	frame := 2 * channels
	step := rate * int(meterStep/time.Millisecond) / 1000 * frame
	if step <= 0 {
		return
	}
	var levels []timedLevel
	for i := 0; i+frame <= len(pcm); i += step {
		end := min(i+step, len(pcm))
		levels = append(levels, timedLevel{at: at.Add(time.Duration(i/frame) * time.Second / time.Duration(rate)),
			level: pcmLevel(pcm[i:end])})
	}
	m.mu.Lock()
	m.queue = append(m.queue, levels...)
	if !m.started {
		m.started = true
		go m.run()
	}
	m.mu.Unlock()
}

// clear forgets the levels still to come: the sound stopped.
func (m *audioMeter) clear() {
	m.mu.Lock()
	m.queue = nil
	m.mu.Unlock()
}

func (m *audioMeter) run() {
	tick := time.NewTicker(meterStep)
	defer tick.Stop()
	for now := range tick.C {
		m.mu.Lock()
		due := -1
		for i, l := range m.queue {
			if l.at.After(now) {
				break
			}
			due = i
		}
		var level float64
		send := false
		switch {
		case due >= 0:
			level, send = m.queue[due].level, true
			m.queue = m.queue[due+1:]
			m.sending = true
		case m.sending && (len(m.queue) == 0 || m.queue[0].at.Sub(now) > 4*meterStep):
			send, m.sending = true, false // the sound is over (or paused): back to 0
		}
		m.mu.Unlock()
		if send {
			display.audioLevel(level)
		}
	}
}

// pcmLevel is how loud 16-bit samples are, 0 to 1: their RMS on a decibel scale from -50 dBFS
// (silence, near enough) to full scale.
func pcmLevel(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += s * s
	}
	rms := math.Sqrt(sum/float64(n)) / 32768
	if rms <= 0 {
		return 0
	}
	return math.Max(0, math.Min(1, (20*math.Log10(rms)+50)/50))
}

// audioLevel sends the sound's level (0 to 1) to every open page, like micLevel.
func (s *screen) audioLevel(level float64) {
	s.sendLevel([]byte(fmt.Sprintf(`{"lvl":%.2f}`, level)))
}
