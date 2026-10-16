package main

import "testing"

func TestVolumeIsRememberedPerSpeaker(t *testing.T) {
	dir := t.TempDir()
	m := &volumeMemory{levels: map[string]int{}}
	m.load(dir)
	m.remember("JBL Flip 5 - A2DP", 40)
	m.remember("Car - A2DP", 65)
	again := &volumeMemory{levels: map[string]int{}}
	again.load(dir)
	if n, ok := again.level("JBL Flip 5 - A2DP"); !ok || n != 40 {
		t.Fatalf("speaker: %d %v", n, ok)
	}
	if n, _ := again.level("Car - A2DP"); n != 65 {
		t.Fatalf("car: %d", n)
	}
	if _, ok := again.level("Other - A2DP"); ok {
		t.Fatal("a speaker never set has no level")
	}
}

// With the phone's music coming in, its A2DP link has a capture control; the volume is the
// speaker's playback one.
func TestTheVolumeIsTheSpeakersNotThePhones(t *testing.T) {
	out := []byte(`Simple mixer control 'Kayla's iPhone A2DP',0
  Capabilities: cvolume cswitch
  Capture channels: Front Left - Front Right
Simple mixer control 'Kayla's Kia A2DP',0
  Capabilities: pvolume pswitch
  Playback channels: Front Left - Front Right
  Front Left: Playback 96 [76%] [on]
`)
	if got := speakerControls(out); len(got) != 1 || got[0] != "Kayla's Kia A2DP" {
		t.Fatalf("got %q", got)
	}
}
