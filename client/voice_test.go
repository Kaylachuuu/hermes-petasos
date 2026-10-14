package main

import (
	"encoding/binary"
	"math"
	"testing"
)

func tone(amp float64) []byte {
	f := make([]byte, frameBytes)
	for i := 0; i < frameBytes/2; i++ {
		binary.LittleEndian.PutUint16(f[2*i:], uint16(int16(amp*math.Sin(float64(i)/3))))
	}
	return f
}

func TestEndpointer(t *testing.T) {
	run := func(e *endpointer, frames ...[]byte) (vadResult, int) {
		for i, f := range frames {
			if r := e.feed(f); r != keepListening {
				return r, i
			}
		}
		return keepListening, len(frames)
	}
	repeat := func(f []byte, n int) [][]byte {
		out := make([][]byte, n)
		for i := range out {
			out[i] = f
		}
		return out
	}
	quiet, road, speech := tone(50), tone(900), tone(6000)

	// Speech after quiet, then a second of quiet: ends one second after speaking stops.
	frames := append(append(repeat(quiet, 25), repeat(speech, 100)...), repeat(quiet, 100)...)
	if r, i := run(newEndpointer(1000, 30), frames...); r != endOfSpeech || i != 25+100+50-1 {
		t.Fatalf("quiet room: %v at frame %d", r, i)
	}
	// Steady road noise louder than the starting guess: learned, then speech is still found.
	frames = append(append(repeat(road, 150), repeat(speech, 60)...), repeat(road, 80)...)
	if r, _ := run(newEndpointer(1000, 30), frames...); r != endOfSpeech {
		t.Fatalf("road noise: %v", r)
	}
	// Nobody speaks: gives up after idleS.
	if r, i := run(newEndpointer(1000, 5), repeat(road, 400)...); r != idle || i != 249 {
		t.Fatalf("idle: %v at %d", r, i)
	}
}

func TestWavFile(t *testing.T) {
	w := wavFile(make([]byte, 320), 16000)
	if string(w[0:4]) != "RIFF" || string(w[8:16]) != "WAVEfmt " || len(w) != 44+320 ||
		binary.LittleEndian.Uint32(w[24:]) != 16000 || binary.LittleEndian.Uint32(w[40:]) != 320 {
		t.Fatalf("bad header % x", w[:44])
	}
}

func TestTo16(t *testing.T) {
	wide := []byte{0xAA, 0xBB, 0x34, 0x12, 0x00, 0x00, 0xFF, 0xFF} // 0x1234BBAA, 0xFFFF0000
	out := make([]byte, 4)
	to16(wide, out)
	if binary.LittleEndian.Uint16(out) != 0x1234 || int16(binary.LittleEndian.Uint16(out[2:])) != -1 {
		t.Fatalf("got % x", out)
	}
}
