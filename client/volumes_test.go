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
