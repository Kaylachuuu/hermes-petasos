package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func pcmOf(samples []float64, channels int) []byte {
	out := make([]byte, 0, 2*len(samples)*channels)
	for _, s := range samples {
		for range channels {
			out = binary.LittleEndian.AppendUint16(out, uint16(int16(s*32767)))
		}
	}
	return out
}

func TestBandsFollowThePitch(t *testing.T) {
	const rate = 44100
	tone := func(hz float64) []byte {
		s := make([]float64, rate/20)
		for i := range s {
			s[i] = 0.5 * math.Sin(2*math.Pi*hz*float64(i)/rate)
		}
		return pcmOf(s, 2)
	}
	for want, hz := range []float64{80, 500, 2000, 8000} {
		bands := pcmBands(tone(hz), rate, 2, 4)
		for b, l := range bands {
			if b == want && l < 0.7 {
				t.Errorf("%v Hz: band %d is %.2f, want it lit", hz, b, l)
			}
			if b != want && l > bands[want]-0.3 {
				t.Errorf("%v Hz: band %d is %.2f against %.2f", hz, b, l, bands[want])
			}
		}
	}
	for _, l := range pcmBands(make([]byte, rate/20*4), rate, 2, 4) {
		if l != 0 {
			t.Errorf("silence gave %v", l)
		}
	}
}

func TestPinkNoiseLightsTheBandsAlike(t *testing.T) {
	const rate = 48000
	r := rand.New(rand.NewSource(1))
	var b0, b1, b2, b3, b4, b5, b6 float64 // Paul Kellet's pink filter
	s := make([]float64, 4096)
	for i := range s {
		w := r.NormFloat64() * 0.02
		b0 = 0.99886*b0 + w*0.0555179
		b1 = 0.99332*b1 + w*0.0750759
		b2 = 0.96900*b2 + w*0.1538520
		b3 = 0.86650*b3 + w*0.3104856
		b4 = 0.55000*b4 + w*0.5329522
		b5 = -0.7616*b5 - w*0.0168980
		s[i] = b0 + b1 + b2 + b3 + b4 + b5 + b6 + w*0.5362
		b6 = w * 0.115926
	}
	for _, count := range []int{3, 4, 5} {
		bands := pcmBands(pcmOf(s, 1), rate, 1, count)
		lo, hi := 1.0, 0.0
		for _, l := range bands {
			lo, hi = math.Min(lo, l), math.Max(hi, l)
		}
		if len(bands) != count || hi-lo > 0.15 || lo < 0.2 {
			t.Errorf("pink noise in %d bands gave %v", count, bands)
		}
	}
}

func TestMeterSettingsStepThroughTheirChoices(t *testing.T) {
	h := &hermes{dir: t.TempDir()}
	if b, f := meterSetting(h.dir); b != 4 || f != "gentle" {
		t.Fatalf("defaults: %d %s", b, f)
	}
	var seen []int
	for range 4 {
		if err := h.setMeter(false); err != nil {
			t.Fatal(err)
		}
		b, _ := meterSetting(h.dir)
		seen = append(seen, b)
	}
	if fmt.Sprint(seen) != "[5 1 3 4]" || meterBands.Load() != 4 {
		t.Errorf("bands went %v, meter %d", seen, meterBands.Load())
	}
	if err := h.setMeter(true); err != nil {
		t.Fatal(err)
	}
	if _, f := meterSetting(h.dir); f != "slow" {
		t.Errorf("fall-off went to %s", f)
	}
}
