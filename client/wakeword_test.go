package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

// The wake word front end and model give what openWakeWord's own ONNX models do on the same
// clip, someone saying "Hey Athena" (tools/wakeword/testvectors.py made the expected numbers),
// and it wakes. The model is the one built in, unless the test data brings its own.
func TestWakeWordMatchesTheReference(t *testing.T) {
	dir := os.Getenv("WAKE_TESTDATA")
	if dir == "" {
		dir = "testdata"
	}
	if _, err := os.Stat(dir + "/wake-clip.raw"); err != nil {
		t.Skip("no wake word test data")
	}
	model, err := os.ReadFile(dir + "/wake-model.bin")
	if err != nil {
		model = heyAthenaModel
	}
	m, err := loadWakeModel(model)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dir + "/wake-clip.raw")
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	var want struct {
		Embeddings [][]float32 `json:"embeddings"`
		Scores     []float32   `json:"scores"`
	}
	data, _ := os.ReadFile(dir + "/wake-clip.json")
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	s := newWakeSpotter(m)
	var embeds [][]float32
	var scores []float32
	start := time.Now()
	for i := 0; i < len(pcm); i += 1000 { // in odd-sized pieces, as audio arrives
		got := s.write(pcm[i:min(i+1000, len(pcm))])
		scores = append(scores, got...)
		for len(embeds) < len(want.Embeddings) && len(s.embeds) > 0 && (len(embeds) == 0 || &embeds[len(embeds)-1][0] != &s.embeds[len(s.embeds)-1][0]) {
			embeds = append(embeds, s.embeds[len(s.embeds)-1])
		}
	}
	t.Logf("%.1f s of audio in %v", float64(len(pcm))/wakeRate, time.Since(start))
	if len(embeds) != len(want.Embeddings) || len(scores) != len(want.Scores) {
		t.Fatalf("%d embeddings and %d scores; want %d and %d", len(embeds), len(scores), len(want.Embeddings), len(want.Scores))
	}
	worst := 0.0
	for i := range embeds {
		for j := range embeds[i] {
			worst = math.Max(worst, math.Abs(float64(embeds[i][j]-want.Embeddings[i][j])))
		}
	}
	if worst > 0.01 {
		t.Fatalf("embeddings differ by up to %g", worst)
	}
	top, run, woke := float32(0), 0, false
	for i := range scores {
		if scores[i] >= wakeThreshold {
			run++
		} else {
			run = 0
		}
		woke = woke || run >= wakeSteps
		if math.Abs(float64(scores[i]-want.Scores[i])) > 0.01 {
			t.Fatalf("score %d is %g; want %g (embeddings within %g)", i, scores[i], want.Scores[i], worst)
		}
		top = max(top, scores[i])
	}
	t.Logf("embeddings within %g; top score %.2f", worst, top)
	if os.Getenv("WAKE_TESTDATA") == "" && !woke {
		t.Errorf("it didn't wake: top score %.2f", top)
	}
}
