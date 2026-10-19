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
	plugin := &fakePresets{own: map[string]map[string]presetGestures{
		"mac": {"preset-1": {"press": {Action: "stop_speaking"}}}}}
	srv := httptest.NewServer(plugin)
	defer srv.Close()
	h := &hermes{dir: dir, cfg: config{Server: srv.URL, Device: "mac"}, http: srv.Client()}
	if p := h.saveAppSettings(appSettingsChange{Name: "Office Mac", VPN: true}); !strings.Contains(p, "Add a VPN") {
		t.Fatalf("VPN without a config: %q", p)
	}
	bad := "[Interface]\nPrivateKey = nope\nAddress = 10.8.0.9/32\n[Peer]\nPublicKey = nope\nAllowedIPs = 10.0.0.0/24\n"
	if _, err := saveAppVPN(dir, "", "Bad", bad); err == nil || !strings.Contains(err.Error(), "won't work") {
		t.Fatalf("bad config: %v", err)
	}
	if p := h.saveAppSettings(appSettingsChange{Name: "Car Thing"}); p == "" {
		t.Fatal("took the Car Thing's name")
	}
	// Pasted without a private key, a new configuration gets one of its own.
	good := "[Interface]\nAddress = 10.8.0.9/32\n[Peer]\nPublicKey = " + strings.Repeat("B", 43) +
		"=\nEndpoint = 127.0.0.1:51820\nAllowedIPs = 0.0.0.0/0\n"
	id, err := saveAppVPN(dir, "", "Office", good)
	if err != nil || id != "office" {
		t.Fatalf("saving a config: %q %v", id, err)
	}
	if p := h.saveAppSettings(appSettingsChange{Name: "Office Mac", VPN: true, When: "always"}); !strings.Contains(p, "Choose") {
		t.Fatalf("VPN with no config chosen: %q", p)
	}
	if p := h.saveAppSettings(appSettingsChange{Name: "Office Mac", VPN: true, When: "always", Config: id}); p != "" {
		t.Fatal(p)
	}
	var cfg config
	var set vpnSettings
	readJSON(filepath.Join(dir, "config.json"), &cfg)
	readJSON(filepath.Join(dir, "vpn.json"), &set)
	info, err := os.Stat(filepath.Join(dir, vpnsFile))
	if cfg.Device != "office-mac" || cfg.Name != "Office Mac" || cfg.Server == "" || !set.On || set.When != "always" ||
		set.Config != "office" || err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved %+v %+v %v %v", cfg, set, info, err)
	}
	shown := appVPNs(dir)
	if len(shown) != 1 || !shown[0].Used || shown[0].PublicKey == "" || strings.Contains(shown[0].Conf, "PrivateKey") ||
		shown[0].Endpoint != "127.0.0.1:51820" {
		t.Fatalf("listed %+v", shown)
	}
	// Edited without its key (the page never has it), it keeps the one it had.
	before, _ := findVPN(dir, id)
	if _, err := saveAppVPN(dir, id, "Office", strings.Replace(shown[0].Conf, "51820", "51821", 1)); err != nil {
		t.Fatal(err)
	}
	after, _ := findVPN(dir, id)
	k1, _ := parseWireGuard([]byte(before.Conf))
	k2, _ := parseWireGuard([]byte(after.Conf))
	if k1.PrivateKey != k2.PrivateKey || endpointOf(after.Conf) != "127.0.0.1:51821" {
		t.Fatalf("edited: %q", after.Conf)
	}
	if _, old := plugin.own["mac"]; old || plugin.own["office-mac"]["preset-1"]["press"].Action != "stop_speaking" {
		t.Fatalf("the soft keys didn't follow the new name: %+v", plugin.own)
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
	srv := httptest.NewServer(&fakePresets{own: map[string]map[string]presetGestures{}})
	defer srv.Close()
	h := &hermes{dir: dir, cfg: config{Server: srv.URL, Device: "mac"}, http: srv.Client()}
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

// The Display tab's settings and the buffer take effect at once, and come back as saved.
func TestAppPrefsTakeEffectAtOnce(t *testing.T) {
	saved := backlight
	defer func() { backlight = saved }()
	dir := t.TempDir()
	backlight = &light{set: defaultLight, file: filepath.Join(dir, "screen.json"), swallow: map[string]bool{}}
	want := appPrefs{Bands: 5, Rise: "fast", Fall: "slow", Peaks: true, Saver: "emblem", SaverAfter: 120, Buffer: -1}
	if err := setAppPrefs(dir, want); err != nil {
		t.Fatal(err)
	}
	if got := getAppPrefs(dir); got.Bands != want.Bands || got.Rise != want.Rise || got.Fall != want.Fall || got.Peaks != want.Peaks ||
		got.Saver != want.Saver || got.SaverAfter != want.SaverAfter || got.Buffer != want.Buffer {
		t.Fatalf("saved %+v, got %+v", want, got)
	}
	if streamBuffer.Load() != -1 || meterBands.Load() != 5 {
		t.Fatalf("not applied: buffer %d, bands %d", streamBuffer.Load(), meterBands.Load())
	}
	setAppPrefs(dir, appPrefs{Bands: 4, Rise: "warp", Saver: "lava", Buffer: 7})
	if got := getAppPrefs(dir); got.Bands != 5 || got.Rise != "fast" || got.Saver != "off" || got.Buffer != 4 {
		t.Fatalf("nonsense became %+v", got)
	}
	setBufferTo(dir, 4)
}

// On a computer the bands' timing is kept for each speaker it plays on, as the Car Thing's is
// for each Bluetooth speaker; a page that leaves the timing out keeps it.
func TestTheBandsAreTimedForEachComputerSpeaker(t *testing.T) {
	oldV := virtual
	virtual = true
	defer func() { virtual = oldV }()
	saved := backlight
	defer func() { backlight = saved }()
	dir := t.TempDir()
	backlight = &light{set: defaultLight, file: filepath.Join(dir, "screen.json"), swallow: map[string]bool{}}
	use := func(o computerOutput) {
		outputNow.Lock()
		outputNow.out = o
		outputNow.Unlock()
	}
	defer use(computerOutput{})
	use(computerOutput{Name: "AirPods Pro", Kind: "Bluetooth"})
	p := getAppPrefs(dir)
	later := 250
	p.Delay = &later
	if err := setAppPrefs(dir, p); err != nil {
		t.Fatal(err)
	}
	if meterDelay.Load() != 250 {
		t.Fatalf("not applied: %d", meterDelay.Load())
	}
	use(computerOutput{Name: "MacBook Pro Speakers", Kind: "Built-in"})
	applyMeter(dir)
	if got := getAppPrefs(dir); *got.Delay != 0 || got.Output.Kind != "Built-in" || meterDelay.Load() != 0 {
		t.Fatalf("the built-in speakers have %+v", got)
	}
	use(computerOutput{Name: "AirPods Pro", Kind: "Bluetooth"})
	p = getAppPrefs(dir)
	p.Delay = nil
	setAppPrefs(dir, p)
	if *getAppPrefs(dir).Delay != 250 {
		t.Fatal("an older page's save lost the AirPods' timing")
	}
}

func TestTheMacsOutputIsFound(t *testing.T) {
	raw := []byte(`{"SPAudioDataType":[{"_name":"coreaudio_device","_items":[
	  {"_name":"MacBook Pro Microphone","coreaudio_default_audio_input_device":"spaudio_yes","coreaudio_device_transport":"coreaudio_device_type_builtin"},
	  {"_name":"MacBook Pro Speakers","coreaudio_device_transport":"coreaudio_device_type_builtin"},
	  {"_name":"Kayla's AirPods Pro","coreaudio_default_audio_output_device":"spaudio_yes","coreaudio_device_transport":"coreaudio_device_type_bluetooth"}]}]}`)
	if o, ok := defaultOutput(raw); !ok || o != (computerOutput{Name: "Kayla's AirPods Pro", Kind: "Bluetooth"}) {
		t.Fatalf("got %+v %v", o, ok)
	}
}
