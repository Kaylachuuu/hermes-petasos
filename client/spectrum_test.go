package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
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
	if m := meterSetting(h.dir); m != (meterSettings{Bands: 3, Rise: "medium", Fall: "medium"}) {
		t.Fatalf("defaults: %+v", m)
	}
	var seen []int
	for _, delta := range []int{1, 1, -1, -1, -1} { // the knob stops at the ends
		if err := stepMeter(h.dir, "bands", delta); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, meterSetting(h.dir).Bands)
	}
	if fmt.Sprint(seen) != "[5 5 3 1 1]" || meterBands.Load() != 1 {
		t.Errorf("bands went %v, meter %d", seen, meterBands.Load())
	}
	stepMeter(h.dir, "fall", 1)
	stepMeter(h.dir, "rise", -1)
	stepMeter(h.dir, "peaks", 0)
	if m := meterSetting(h.dir); m.Fall != "slow" || m.Rise != "fast" || !m.Peaks {
		t.Errorf("went to %+v", m)
	}
	if s := display.snapshot(); s.Rise != "fast" || s.Fall != "slow" || !s.Peaks {
		t.Errorf("the screen has %s %s %v", s.Rise, s.Fall, s.Peaks)
	}
	// The fall-offs from before rising and falling were set apart, and 4 bands, carry over.
	for old, want := range map[string]string{"peaks": "fast slow", "quick": "fast fast", "gentle": "medium medium", "slow": "medium slow"} {
		writeJSON(filepath.Join(h.dir, ownSettingsFile), ownSettings{Bands: 4, Fall: old}, 0o644)
		if m := meterSetting(h.dir); m.Rise+" "+m.Fall != want || m.Bands != 3 {
			t.Errorf("%s became %+v", old, m)
		}
	}
	const car = "aa:bb:cc:dd:ee:ff"
	for _, delta := range []int{1, 1, 1, 1, 1, -1} {
		if err := setDelay(h.dir, car, delta); err != nil {
			t.Fatal(err)
		}
	}
	if d := delayFor(h.dir, car); d != 200 || delayLabel(d) != "200 ms later" || delayFor(h.dir, "11:22:33:44:55:66") != 0 {
		t.Errorf("timing went to %d (%s)", d, delayLabel(d))
	}
	if meterDelay.Load() != 0 {
		t.Errorf("another speaker's timing applied: %d", meterDelay.Load())
	}
	speakerNow.Store(car)
	defer speakerNow.Store("")
	applyMeter(h.dir)
	if meterDelay.Load() != 200 {
		t.Errorf("the car's timing isn't applied: %d", meterDelay.Load())
	}
	for range 20 { // the knob stops at the ends
		setDelay(h.dir, car, -1)
	}
	if d := delayFor(h.dir, car); d != -200 {
		t.Errorf("turned all the way down: %d", d)
	}
	meterDelay.Store(0)
}
