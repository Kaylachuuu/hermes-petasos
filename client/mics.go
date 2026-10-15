package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"time"
)

// The app's choice of microphone (the Mac and Windows apps alike), from its settings page. By
// default it records from whatever the computer's own sound settings use; choosing one here
// leaves those alone, so AirPods can carry Athena's voice while the Mac's microphone hears you
// and the AirPods never drop into call mode.

type micInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin,omitempty"`
}

// audioSettings is audio.json.
type audioSettings struct {
	Mic     string `json:"mic,omitempty"`      // the helper's id for it; empty for the computer's own choice
	MicName string `json:"mic_name,omitempty"` // for the settings page while it isn't connected
	Wake    bool   `json:"wake,omitempty"`     // listen for the wake word (off unless turned on)
}

var micChoice string // the microphone the recorder uses, from audio.json; empty for the default

func loadAudioSettings(dir string) audioSettings {
	var a audioSettings
	_ = readJSON(filepath.Join(dir, "audio.json"), &a)
	return a
}

// listMics asks the app's helper which microphones the computer has.
func listMics() ([]micInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, audioHelper, "--inputs").Output()
	if err != nil {
		return nil, err
	}
	var mics []micInfo
	if err := json.Unmarshal(out, &mics); err != nil {
		return nil, err
	}
	return mics, nil
}
