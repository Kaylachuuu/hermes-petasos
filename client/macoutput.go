package main

import (
	"encoding/json"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// The computer's speaker: on a Mac, which output the sound goes to now (its own speakers,
// AirPods over Bluetooth, an AirPlay speaker), so the bands' timing can be set for each, as the
// Car Thing's is for each Bluetooth speaker. macOS's system_profiler says, without the app's
// help; the speaker is looked at again every few seconds, as it can change at any time.

const outputEvery = 10 * time.Second

type computerOutput struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // Built-in, Bluetooth, AirPlay, USB, HDMI or "", as macOS says
}

var outputNow struct {
	sync.Mutex
	out computerOutput
}

func currentOutput() computerOutput {
	outputNow.Lock()
	defer outputNow.Unlock()
	return outputNow.out
}

// outputKey is the key the bands' timing is kept under for an output: "output:" and its name,
// beside the Car Thing's Bluetooth addresses.
func outputKey(o computerOutput) string {
	if o.Name == "" {
		return ""
	}
	return "output:" + o.Name
}

// watchOutput follows the Mac's output, timing the bands to each.
func watchOutput(dir string) {
	if runtime.GOOS != "darwin" {
		return
	}
	for ; ; time.Sleep(outputEvery) {
		raw, err := exec.Command("system_profiler", "-json", "SPAudioDataType").Output()
		if err != nil {
			continue
		}
		o, ok := defaultOutput(raw)
		if !ok {
			continue
		}
		outputNow.Lock()
		changed := o != outputNow.out
		outputNow.out = o
		outputNow.Unlock()
		if changed {
			log.Printf("playing on %s (%s)", o.Name, o.Kind)
			applyMeter(dir)
		}
	}
}

var outputKinds = map[string]string{"builtin": "Built-in", "bluetooth": "Bluetooth", "airplay": "AirPlay",
	"usb": "USB", "hdmi": "HDMI", "displayport": "DisplayPort", "thunderbolt": "Thunderbolt", "virtual": "Virtual"}

// defaultOutput finds the default output in system_profiler's SPAudioDataType JSON.
func defaultOutput(raw []byte) (computerOutput, bool) {
	var report struct {
		Audio []struct {
			Items []map[string]any `json:"_items"`
		} `json:"SPAudioDataType"`
	}
	if json.Unmarshal(raw, &report) != nil {
		return computerOutput{}, false
	}
	for _, a := range report.Audio {
		for _, d := range a.Items {
			if d["coreaudio_default_audio_output_device"] != "spaudio_yes" {
				continue
			}
			name, _ := d["_name"].(string)
			transport, _ := d["coreaudio_device_transport"].(string)
			return computerOutput{Name: name, Kind: outputKinds[strings.TrimPrefix(transport, "coreaudio_device_type_")]}, name != ""
		}
	}
	return computerOutput{}, false
}
