package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputerNamesBecomeDeviceIDs(t *testing.T) {
	for name, want := range map[string]string{
		"Kayla's Office Mac": "kaylas-office-mac",
		"  MacBook Pro (2) ": "macbook-pro-2",
		"Ünïcode only":       "n-code-only", // as the settings page makes it
		"!!!":                "",
	} {
		if got := deviceID(name); got != want {
			t.Errorf("%q: %q, want %q", name, got, want)
		}
	}
	if titleFromDevice("kaylas-office-mac") != "Kaylas Office Mac" {
		t.Error(titleFromDevice("kaylas-office-mac"))
	}
}

func TestAppSettingsAreCheckedAndSaved(t *testing.T) {
	dir := t.TempDir()
	h := &hermes{dir: dir, cfg: config{Server: "http://10.0.0.20:9119", Device: "mac"}}
	if p := h.saveAppSettings(appSettingsChange{Name: "Office Mac", VPN: true}); !strings.Contains(p, "Import") {
		t.Fatalf("VPN without a config: %q", p)
	}
	bad := "[Interface]\nPrivateKey = nope\nAddress = 10.8.0.9/32\n[Peer]\nPublicKey = nope\nAllowedIPs = 10.0.0.0/24\n"
	if p := h.saveAppSettings(appSettingsChange{Name: "Office Mac", VPN: true, WireGuard: &bad}); !strings.Contains(p, "won't work") {
		t.Fatalf("bad config: %q", p)
	}
	if p := h.saveAppSettings(appSettingsChange{Name: "Car Thing"}); p == "" {
		t.Fatal("took the Car Thing's name")
	}
	good := "[Interface]\nPrivateKey = " + strings.Repeat("A", 43) + "=\nAddress = 10.8.0.9/32\n[Peer]\nPublicKey = " +
		strings.Repeat("B", 43) + "=\nEndpoint = 127.0.0.1:51820\nAllowedIPs = 0.0.0.0/0\n"
	if p := h.saveAppSettings(appSettingsChange{Name: "Office Mac", VPN: true, When: "always", WireGuard: &good}); p != "" {
		t.Fatal(p)
	}
	var cfg config
	var set vpnSettings
	readJSON(filepath.Join(dir, "config.json"), &cfg)
	readJSON(filepath.Join(dir, "vpn.json"), &set)
	info, err := os.Stat(filepath.Join(dir, "wireguard.conf"))
	if cfg.Device != "office-mac" || cfg.Name != "Office Mac" || cfg.Server == "" || !set.On || set.When != "always" ||
		err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved %+v %+v %v %v", cfg, set, info, err)
	}
}

// The microphone choice is saved, listed from the helper, and passed to the recorder.
func TestMicrophoneChoice(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "audio")
	os.WriteFile(helper, []byte(`#!/bin/sh
[ "$1" = --inputs ] && echo '[{"id":"BuiltInMicrophoneDevice","name":"MacBook Pro Microphone","builtin":true},{"id":"airpods","name":"AirPods"}]'
`), 0o755)
	oldA, oldM, oldV := audioHelper, micChoice, virtual
	audioHelper, virtual = helper, true
	defer func() { audioHelper, micChoice, virtual = oldA, oldM, oldV }()
	h := &hermes{dir: dir, cfg: config{Device: "mac"}}
	mic := "BuiltInMicrophoneDevice"
	if p := h.saveAppSettings(appSettingsChange{Name: "Mac", Mic: &mic, MicName: "MacBook Pro Microphone"}); p != "" {
		t.Fatal(p)
	}
	v := h.appSettingsView()
	if v.Mic != mic || v.MicName != "MacBook Pro Microphone" || len(v.Mics) != 2 || !v.Mics[0].Builtin {
		t.Fatalf("view %+v", v)
	}
	micChoice = loadAudioSettings(dir).Mic
	cmd := (&voice{}).recordCommand()
	if got := strings.Join(cmd.Args[1:], " "); got != "--record 16000 BuiltInMicrophoneDevice" {
		t.Fatalf("recorder run as %q", got)
	}
	none := ""
	h.saveAppSettings(appSettingsChange{Name: "Mac", Mic: &none, MicName: "x"})
	if a := loadAudioSettings(dir); a.Mic != "" || a.MicName != "" {
		t.Fatalf("saved %+v", a)
	}
}

func TestPinKeepsWindowOnTop(t *testing.T) {
	oldDir := appDir
	appDir = t.TempDir()
	defer func() { appDir = oldDir }()
	rec := httptest.NewRecorder()
	appWindow(rec, httptest.NewRequest("POST", "/app/window", strings.NewReader(`{"on_top":true}`)))
	if rec.Code != 200 || !loadWindowSettings(appDir).OnTop || !display.snapshot().OnTop {
		t.Fatalf("not kept on top: %d %s", rec.Code, rec.Body)
	}
}
