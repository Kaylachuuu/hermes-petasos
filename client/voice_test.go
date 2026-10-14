package main

import (
	"bytes"
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
	quiet, road, loud := tone(50), tone(900), tone(6000)
	// Speech: syllables with short dips between them, over whatever the background is.
	speech := func(background []byte, n int) [][]byte {
		var out [][]byte
		for len(out) < n {
			out = append(append(out, repeat(loud, 12)...), repeat(background, 3)...)
		}
		return out[:n]
	}

	// Speech after quiet, then a second of quiet: ends one second after speaking stops.
	frames := append(append(repeat(quiet, 25), speech(quiet, 100)...), repeat(quiet, 100)...)
	if r, i := run(newEndpointer(1000, 30), frames...); r != endOfSpeech || i != 25+100+50-1 {
		t.Fatalf("quiet room: %v at frame %d", r, i)
	}
	// Steady road noise louder than the starting guess: learned, then speech is still found.
	frames = append(append(repeat(road, 150), speech(road, 60)...), repeat(road, 80)...)
	if r, _ := run(newEndpointer(1000, 30), frames...); r != endOfSpeech {
		t.Fatalf("road noise: %v", r)
	}
	// Road noise far louder than the first guess (a car at speed): it must not pass for endless
	// speech, and the speech over it must still be found.
	busy := tone(2000)
	e := newEndpointer(1000, 30)
	if r, i := run(e, repeat(busy, 300)...); r != keepListening {
		t.Fatalf("loud road noise taken for speech: %v at %d", r, i)
	}
	if r, _ := run(e, append(speech(busy, 80), repeat(busy, 80)...)...); r != endOfSpeech {
		t.Fatalf("speech over loud road noise: %v (%v)", r, e)
	}
	// Nobody speaks: gives up after idleS.
	if r, i := run(newEndpointer(1000, 5), repeat(road, 400)...); r != idle || i != 249 {
		t.Fatalf("idle: %v at %d", r, i)
	}
	// Someone talking without end (or noise that passes for it) still gets sent.
	if r, i := run(newEndpointer(1000, 30), speech(quiet, 2000)...); r != endOfSpeech || i > maxTurn+50 {
		t.Fatalf("endless talk: %v at %d", r, i)
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

func TestToSpeakerFormat(t *testing.T) {
	// One second of 24 kHz mono at a constant level becomes one second of 48 kHz stereo.
	pcm := make([]byte, 24000*2)
	for i := 0; i < 24000; i++ {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(1000)))
	}
	out, err := toSpeakerFormat(wavFile(pcm, 24000))
	if err != nil {
		t.Fatal(err)
	}
	ch, rate, bits, data, err := parseWav(out)
	if err != nil || ch != 2 || rate != 48000 || bits != 16 || len(data) != 48000*4 {
		t.Fatalf("got ch=%d rate=%d bits=%d len=%d err=%v", ch, rate, bits, len(data), err)
	}
	if v := int16(binary.LittleEndian.Uint16(data[4*1000+2:])); v != 1000 {
		t.Fatalf("sample changed: %d", v)
	}
	if _, err := toSpeakerFormat([]byte("ID3 not a wav")); err == nil {
		t.Fatal("accepted an MP3")
	}
	same := wavFileWith(make([]byte, 16), 48000, 2)
	if got, _ := toSpeakerFormat(same); !bytes.Equal(got, same) {
		t.Fatal("re-encoded audio that was already right")
	}
}
