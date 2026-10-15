package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePresets is the plugin's /devices/ID/presets: each device's own soft keys, or the shared ones.
type fakePresets struct {
	mu  sync.Mutex
	own map[string]map[string]presetGestures
}

func (f *fakePresets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	device := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/plugins/petasos/devices/"), "/presets")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodPut {
		var body struct {
			Presets map[string]presetGestures `json:"presets"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Presets == nil {
			delete(f.own, device)
		} else {
			f.own[device] = body.Presets
		}
	}
	shared := map[string]presetGestures{
		"preset-1": {"press": {Action: "prompt", Text: "What's next on my calendar?", Label: "Calendar"}},
		"preset-2": {"press": {Action: "none"}}, "preset-3": {"press": {Action: "none"}}, "preset-4": {"press": {Action: "none"}},
		"preset-5": {"press": {Action: "page_petasos", Label: "Petasos"}}, "preset-6": {"press": {Action: "page_apple", Label: "Apple Music"}},
		"preset-7": {"press": {Action: "page_navidrome", Label: "Navidrome"}}, "preset-8": {"press": {Action: "none"}},
	}
	p, own := f.own[device]
	if !own {
		p = shared
	}
	labels := []string{}
	for _, n := range presetNames {
		s := p[n].press()
		labels = append(labels, map[bool]string{true: s.Label, false: ""}[s.Action != "none"])
	}
	json.NewEncoder(w).Encode(map[string]any{"own": own, "presets": p, "labels": labels,
		"commands": []map[string]any{{"do": "screen", "presets": labels}}})
}

func TestDeviceTabGivesTheCarThingItsOwnSoftKeysAndName(t *testing.T) {
	plugin := &fakePresets{own: map[string]map[string]presetGestures{}}
	srv := httptest.NewServer(plugin)
	defer srv.Close()
	dir := t.TempDir()
	h := &hermes{dir: dir, cfg: config{Server: srv.URL, Device: "car-thing"}, http: srv.Client()}
	h.servesDevice()
	defer func() { here.h = nil }()
	saved, savedRestart := bt, restartSoon
	restarted := false
	restartSoon = func() { restarted = true }
	defer func() { bt, restartSoon = saved, savedRestart }()
	bt = &bluetooth{open: true}
	defer display.update(func(st *screenState) { st.BT = nil })
	page := httptest.NewServer(http.HandlerFunc(typedText))
	defer page.Close()

	view := func() btScreen {
		bt.mu.Lock()
		defer bt.mu.Unlock()
		if bt.view == nil {
			return btScreen{}
		}
		return *bt.view
	}
	waitFor := func(what string, ok func(btScreen) bool) btScreen {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			if v := view(); ok(v) {
				return v
			}
		}
		t.Fatalf("never got %s; view is %+v", what, view())
		return btScreen{}
	}
	ev := func(input, action string, delta int) {
		bt.handle(map[string]any{"input": input, "action": action, "delta": delta})
	}
	typeText := func(text string) {
		t.Helper()
		v := waitFor("the keyboard", func(v btScreen) bool { return v.View == "keyboard" })
		raw, _ := json.Marshal(map[string]any{"keyboard": v.Keyboard, "text": text})
		resp, err := http.Post(page.URL, "application/json", bytes.NewReader(raw))
		if err != nil || resp.StatusCode != http.StatusNoContent {
			t.Fatalf("typing: %v %v", err, resp)
		}
		io.Copy(io.Discard, resp.Body)
	}

	bt.show(&btScreen{View: "screen"})
	ev("preset-3", "press", 0)
	v := waitFor("the Device tab", func(v btScreen) bool { return v.View == "mine" })
	if v.Tab != "device" || v.Items[0].Value != "Car Thing" || v.Items[4].Label != "Calendar" {
		t.Fatalf("Device tab: %+v", v)
	}
	// The second set follows the first under its own header, each key a row the knob reaches.
	if v.Items[8].Kind != "header" || v.Items[9].Label != "Petasos" || v.Items[9].Detail != "Petasos page" ||
		v.Items[10].Detail != "Apple Music page" || v.Items[12].Kind != "row" || v.Items[12].Label != "—" {
		t.Fatalf("second set: %+v", v.Items)
	}
	// Soft key 3: turn it into a question. Its words are asked for before it is saved.
	ev("knob", "turn", 1)
	for view().Sel != 6 {
		ev("knob", "turn", 1)
	}
	ev("knob", "press", 0)
	waitFor("soft key 3", func(v btScreen) bool { return v.View == "softkey" && v.Title == "Soft key 3" })
	ev("knob", "press", 0) // change what it does
	waitFor("editing", func(v btScreen) bool { return v.Editing })
	for i := 0; i < len(softKeyActions); i++ {
		ev("knob", "turn", -1)
	}
	if v := view(); !v.Editing || v.Items[0].Value != "Ask a question" {
		t.Fatalf("turning: %+v", v)
	}
	ev("knob", "press", 0)
	typeText("  Any packages today?  ")
	waitFor("it saved", func(v btScreen) bool { return v.View == "softkey" && len(v.Items) == 4 })
	plugin.mu.Lock()
	got := plugin.own["car-thing"]["preset-3"]["press"]
	plugin.mu.Unlock()
	if got != (presetStep{Action: "prompt", Text: "Any packages today?"}) {
		t.Fatalf("saved %+v", got)
	}
	// The front button cancels the keyboard without saving.
	bt.move(1)
	bt.choose()
	waitFor("the label keyboard", func(v btScreen) bool { return v.View == "keyboard" && v.Title == "Label" })
	ev("front", "press", 0)
	waitFor("soft key 3 again", func(v btScreen) bool { return v.View == "softkey" })
	ev("front", "press", 0)
	waitFor("the Device tab", func(v btScreen) bool { return v.View == "mine" })

	// Renaming carries the soft keys to the new name and restarts.
	bt.mu.Lock()
	bt.view.Sel = 0
	bt.mu.Unlock()
	bt.choose()
	typeText("Kayla's Kia")
	waitFor("the restart", func(v btScreen) bool { return v.View == "message" })
	cfg := config{Name: "From the installer", Device: "car-thing"}
	cfg.applyOwnSettings(dir)
	plugin.mu.Lock()
	_, old := plugin.own["car-thing"]
	moved := plugin.own["kaylas-kia"]["preset-3"]["press"].Text
	plugin.mu.Unlock()
	if cfg.Name != "Kayla's Kia" || cfg.Device != "kaylas-kia" || old || moved != "Any packages today?" || !restarted {
		t.Fatalf("rename: %+v old=%v moved=%q restarted=%v", cfg, old, moved, restarted)
	}
}

func TestBluetoothNameIsTheDeviceName(t *testing.T) {
	if n := (&hermes{cfg: config{Device: "car-thing"}}).shownName(); n != "Car Thing" {
		t.Fatal(n)
	}
	if n := (&hermes{cfg: config{Device: "kaylas-kia", Name: "Kayla's Kia"}}).shownName(); n != "Kayla's Kia" {
		t.Fatal(n)
	}
}

func TestImageVersionComesFromOsRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	os.WriteFile(path, []byte("ID=petasos\nVERSION_ID=\"0.2.0\"\nBUILD_ID=\"20261015\"\n"), 0o644)
	if got := imageVersion(path); got != "0.2.0, built 2026-10-15" {
		t.Fatal(got)
	}
	os.WriteFile(path, []byte("VERSION_ID=0.1.0\n"), 0o644)
	if got := imageVersion(path); got != "0.1.0" {
		t.Fatal(got)
	}
	items := versionItems(versions{Image: "0.2.0", Program: "0.17.2", Latest: "0.17.3", Plugin: "0.11.1"}, nil)
	if len(items) != 4 || items[2].Detail != "Updating to 0.17.3" || items[3].Value != "0.11.1" {
		t.Fatalf("%+v", items)
	}
}
