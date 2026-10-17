package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"sync"
	"time"
)

// Test beeps, for lining the sound bands up with a speaker (its timing, in the Bluetooth tab):
// a short beep each second, which lights the bands as it's heard. The music steps aside meanwhile,
// as it does for Athena.

const (
	beepEvery  = time.Second
	beepLength = 120 * time.Millisecond
	beepsFor   = time.Minute // at most, if they're never stopped
)

var beeps struct {
	mu   sync.Mutex
	stop chan struct{}
}

// beepPCM is the beeps as 16-bit stereo at libraryRate.
func beepPCM(length time.Duration) []byte {
	frames := int(length.Seconds() * libraryRate)
	period := int(beepEvery.Seconds() * libraryRate)
	tone := int(beepLength.Seconds() * libraryRate)
	ramp := libraryRate / 200 // 5 ms in and out, so it doesn't click
	pcm := make([]byte, 0, frames*4)
	for i := range frames {
		at := i % period
		var s float64
		if at < tone {
			s = 0.5 * math.Sin(2*math.Pi*1000*float64(at)/libraryRate)
			s *= math.Min(1, math.Min(float64(at), float64(tone-at))/float64(ramp))
		}
		v := uint16(int16(s * 32767))
		pcm = binary.LittleEndian.AppendUint16(pcm, v)
		pcm = binary.LittleEndian.AppendUint16(pcm, v)
	}
	return pcm
}

func beeping() bool {
	beeps.mu.Lock()
	defer beeps.mu.Unlock()
	return beeps.stop != nil
}

// startBeeps plays the beeps until stopBeeps.
func startBeeps() {
	library.mu.Lock()
	v := library.v
	library.mu.Unlock()
	if v == nil {
		return
	}
	stop := make(chan struct{})
	beeps.mu.Lock()
	if beeps.stop != nil {
		beeps.mu.Unlock()
		return // already beeping
	}
	beeps.stop = stop
	beeps.mu.Unlock()
	music.phaseChanged("beeping") // the music steps aside
	library.phaseChanged("beeping")
	go func() {
		defer func() {
			beeps.mu.Lock()
			if beeps.stop == stop {
				beeps.stop = nil
			}
			beeps.mu.Unlock()
			meter.clear()
			phase := display.snapshot().Phase // the music comes back, unless Athena is busy
			music.phaseChanged(phase)
			library.phaseChanged(phase)
		}()
		time.Sleep(300 * time.Millisecond) // the music lets go of the speaker
		pcm := beepPCM(beepsFor)
		cmd := v.playCommand()
		cmd.Stdin = bytes.NewReader(wavFileWith(pcm, libraryRate, 2))
		if err := cmd.Start(); err != nil {
			return
		}
		// The levels are worked out a second at a time, a little ahead, rather than all at once.
		start, second := time.Now().Add(speakerLag), int(beepEvery.Seconds()*libraryRate)*4
		go func() {
			for i := 0; i*second < len(pcm); i++ {
				if wait := time.Until(start.Add(time.Duration(i-2) * beepEvery)); wait > 0 {
					select {
					case <-stop:
						return
					case <-time.After(wait):
					}
				}
				meter.feed(pcm[i*second:min(len(pcm), (i+1)*second)], libraryRate, 2, start.Add(time.Duration(i)*beepEvery))
			}
		}()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-stop:
			_ = cmd.Process.Kill()
			<-done
		case <-done:
		}
	}()
}

func stopBeeps() {
	beeps.mu.Lock()
	if beeps.stop != nil {
		close(beeps.stop)
		beeps.stop = nil
	}
	beeps.mu.Unlock()
}
