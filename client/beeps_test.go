package main

import (
	"testing"
	"time"
)

func TestBeepsLightTheBandsOncePerSecond(t *testing.T) {
	pcm := beepPCM(3 * time.Second)
	if len(pcm) != 3*libraryRate*4 {
		t.Fatalf("%d bytes", len(pcm))
	}
	step := libraryRate / 20 * 4 // the meter's 50 ms
	var lit []int
	for i := 0; i+step <= len(pcm); i += step {
		if pcmLevel(pcm[i:i+step]) > 0.5 {
			lit = append(lit, i/step)
		}
	}
	// Each beep (120 ms) lights the first two or three steps of its second, and nothing else.
	for _, at := range lit {
		if at%20 > 2 {
			t.Errorf("lit at step %d", at)
		}
	}
	if len(lit) < 6 {
		t.Errorf("lit only at %v", lit)
	}
}
