//go:build linux

package main

import "testing"

// The desktop's microphones, from pactl, by the names the page knows them by; what's playing
// (a monitor) isn't one.
func TestDesktopMicrophonesFromPactl(t *testing.T) {
	got := parsePactlSources(`Source #55
	State: SUSPENDED
	Name: alsa_output.platform-hdmi.monitor
	Description: Monitor of Built-in Audio
Source #56
	State: RUNNING
	Name: alsa_input.usb-Blue_Yeti-00.analog-stereo
	Description: Yeti Stereo Microphone Analog Stereo
`)
	if len(got) != 1 || got[0].ID != "alsa_input.usb-Blue_Yeti-00.analog-stereo" || got[0].Name != "Yeti Stereo Microphone Analog Stereo" {
		t.Fatalf("got %+v", got)
	}
}
