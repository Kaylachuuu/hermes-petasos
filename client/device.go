package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// This device's own settings: its name, which tells its chat apart from other devices' and is
// what Bluetooth shows the Car Thing as, its microphone, and its own soft keys (the preset
// buttons). The soft keys are kept on Hermes, in the plugin's mapping.json under the device's
// id, so each Car Thing and each app can have its own; a device without its own uses the
// shared ones from the plugin's settings.
//
// On the Car Thing they are the Device tab of its settings (the third preset button while the
// settings are open). Words are typed on an on-screen keyboard, by touch or with the knob.

// here is the running device, for the settings screens.
var here struct {
	mu sync.Mutex
	h  *hermes
}

func (h *hermes) servesDevice() {
	here.mu.Lock()
	here.h = h
	here.mu.Unlock()
}

func hereNow() *hermes {
	here.mu.Lock()
	defer here.mu.Unlock()
	return here.h
}

// shownName is the device's name as people see it: "Car Thing" until it is given another.
func (h *hermes) shownName() string {
	if n := strings.TrimSpace(h.cfg.Name); n != "" {
		return n
	}
	return titleFromDevice(h.cfg.Device)
}

const (
	nameMax   = 48
	labelMax  = 32
	promptMax = 200
)

// --- the soft keys, on Hermes --------------------------------------------------------------

// presetStep is what one press of a soft key does.
type presetStep struct {
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	Label  string `json:"label,omitempty"`
}

type presetGestures map[string]presetStep // press (or hold and tap) to a step

type devicePresets struct {
	Own      bool                      `json:"own"`
	Name     string                    `json:"name"`
	Presets  map[string]presetGestures `json:"presets"` // preset-1 .. preset-8
	Shared   map[string]presetGestures `json:"shared"`  // the ones devices without their own use
	Labels   []string                  `json:"labels"`
	Commands []map[string]any          `json:"commands,omitempty"`
}

// presetNames are the soft keys: preset-1 .. preset-4 are the top buttons, and preset-5 ..
// preset-8 the same buttons while the second set shows (after a press of the top-right one).
var presetNames = []string{"preset-1", "preset-2", "preset-3", "preset-4",
	"preset-5", "preset-6", "preset-7", "preset-8"}

// softKeyActions are what a soft key can do, as its settings name them.
var softKeyActions = []choiceOf{
	{"prompt", "Ask a question"},
	{"conversation", "Start a conversation"},
	{"stop_speaking", "Stop speaking"},
	{"mic_toggle", "Mic on or off"},
	{"music_toggle", "Play or pause music"},
	{"music_next", "Next song"},
	{"music_previous", "Previous song"},
	{"music_stop", "Stop the music"},
	{"page_soundcloud", "SoundCloud page"},
	{"page_navidrome", "Navidrome page"},
	{"page_spotify", "Spotify page"},
	{"page_apple", "Apple Music page"},
	{"none", "Nothing"},
}

// oldActionLabels name actions a soft key can no longer be given but an older mapping may
// still give one, so its row reads well.
var oldActionLabels = map[string]string{"music_page": "Music page", "soft_keys": "Other soft keys"}

type choiceOf struct{ value, label string }

func actionLabel(action string) string {
	for _, a := range softKeyActions {
		if a.value == action {
			return a.label
		}
	}
	if label, ok := oldActionLabels[action]; ok {
		return label
	}
	return action
}

func presetsPath(device string) string {
	return "/api/plugins/petasos/devices/" + url.PathEscape(device) + "/presets"
}

func (h *hermes) presets() (devicePresets, error) {
	var p devicePresets
	err := h.call("GET", presetsPath(h.cfg.Device), nil, &p)
	return p, err
}

// setPresets gives this device its own soft keys, or with nil puts it back on the shared ones,
// and shows their labels.
func (h *hermes) setPresets(p map[string]presetGestures) (devicePresets, error) {
	out, err := h.putPresets(h.cfg.Device, p, h.shownName())
	if err == nil {
		for _, c := range out.Commands {
			showScreenCommand(c)
		}
	}
	return out, err
}

func (h *hermes) putPresets(device string, p map[string]presetGestures, name string) (devicePresets, error) {
	body := map[string]any{"presets": p, "name": name}
	if p == nil {
		body = map[string]any{"presets": nil}
	}
	var out devicePresets
	err := h.call("PUT", presetsPath(device), body, &out)
	return out, err
}

// press is a soft key's press step; a key set up with hold and tap shows its tap.
func (g presetGestures) press() presetStep {
	for _, gesture := range []string{"press", "tap", "hold"} {
		if s, ok := g[gesture]; ok {
			return s
		}
	}
	return presetStep{Action: "none"}
}

// describe says what a soft key does, for its row.
func (s presetStep) describe(agent string) string {
	switch s.Action {
	case "prompt":
		return "Asks “" + truncate(s.Text, 60) + "”"
	case "stop_speaking":
		return "Stops " + agent + " speaking, or pauses the music"
	case "none", "":
		return "Does nothing"
	}
	return actionLabel(s.Action)
}

// --- renaming ------------------------------------------------------------------------------

// appSoftKeys is the apps' /app/softkeys: GET this computer's soft keys, PUT
// {"presets": {...}} to give it its own or {"presets": null} for the shared ones.
func appSoftKeys(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if h == nil || !virtual {
		http.NotFound(w, r)
		return
	}
	var (
		p   devicePresets
		err error
	)
	switch r.Method {
	case http.MethodGet:
		p, err = h.presets()
	case http.MethodPut:
		var body struct {
			Presets map[string]presetGestures `json:"presets"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&body); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "That wasn't a soft key change."})
			return
		}
		p, err = h.setPresets(body.Presets)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
		return
	}
	p.Commands = nil
	writeJSONResponse(w, http.StatusOK, p)
}

// friendlyHermes turns a failed call to the plugin into a sentence, using the plugin's own
// explanation when it gave one.
func friendlyHermes(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, `{"detail":"`); i >= 0 {
		var body struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal([]byte(msg[i:]), &body) == nil && body.Detail != "" {
			return body.Detail
		}
	}
	return "Couldn't reach Hermes: " + msg
}

// carryPresets moves a device's own soft keys from one id to another as it is renamed.
func (h *hermes) carryPresets(from, to, name string) error {
	if from == to {
		return nil
	}
	var p devicePresets
	if err := h.call("GET", presetsPath(from), nil, &p); err != nil {
		return fmt.Errorf("Couldn't reach Hermes to carry the soft keys over: %v", err)
	}
	if !p.Own {
		return nil
	}
	if _, err := h.putPresets(to, p.Presets, name); err != nil {
		return fmt.Errorf("Couldn't carry the soft keys over: %v", err)
	}
	_, _ = h.putPresets(from, nil, "")
	return nil
}

// rename gives the device a new name and the id that goes with it, carrying its own soft keys
// over to the new id, then restarts so everything (the chat, Bluetooth) picks it up.
func (h *hermes) rename(name string) error {
	name = strings.TrimSpace(name)
	id := deviceID(name)
	if id == "" {
		return errors.New("A name needs a letter or a number in it.")
	}
	if err := h.carryPresets(h.cfg.Device, id, name); err != nil {
		return err
	}
	own := loadOwnSettings(h.dir)
	own.Name, own.Device = name, id
	if err := writeJSON(filepath.Join(h.dir, ownSettingsFile), own, 0o644); err != nil {
		return fmt.Errorf("Couldn't save the name: %v", err)
	}
	log.Printf("renamed %q (%s); restarting", name, id)
	return nil
}

// restartSoon exits so systemd (or the app) starts this program again.
var restartSoon = func() {
	go func() {
		time.Sleep(1500 * time.Millisecond)
		os.Exit(0)
	}()
}

// --- the microphone ------------------------------------------------------------------------

// carMic says the Car Thing records from the car's microphone, over Bluetooth (hands-free),
// while it is connected to the car; otherwise it uses its own.
var carMic atomic.Bool

const carMicDevice = "plug:'bluealsa:PROFILE=sco'" // converts the car's 8 or 16 kHz audio to what petasos records

func (h *hermes) setCarMic(on bool) error {
	own := loadOwnSettings(h.dir)
	own.Mic = ""
	if on {
		own.Mic = "car"
	}
	if err := writeJSON(filepath.Join(h.dir, ownSettingsFile), own, 0o644); err != nil {
		return err
	}
	h.cfg.Mic = own.Mic
	carMic.Store(on)
	return nil
}

// The Car Thing keeps what its settings set in device.json, beside config.json, which the
// installer writes afresh each time it runs; device.json's settings win.
const ownSettingsFile = "device.json"

type ownSettings struct {
	Name   string `json:"name,omitempty"`
	Device string `json:"device,omitempty"`
	Mic    string `json:"mic,omitempty"`
	Wake   *bool  `json:"wake,omitempty"`  // listen for the wake word; on unless turned off
	VPN    *bool  `json:"vpn,omitempty"`   // the tunnel home (wireguard.conf); on unless turned off
	Bands  int    `json:"bands,omitempty"` // the bands of pitch the top meander shows (meterBands), 4 if not set
	Fall   string `json:"fall,omitempty"`  // how quickly the meander's sound falls back (meterFalls), "gentle" if not set
	// Delays is how much later the meander shows the sound for each speaker, by its Bluetooth
	// address, in ms (meterDelays): one speaker plays what it's sent later than another.
	Delays map[string]int `json:"delays,omitempty"`
}

// The choices for the top meander's sound, in the order the Device tab steps through them.
var (
	meterBandChoices = []int{1, 3, 4, 5}
	meterFalls       = []string{"quick", "gentle", "slow", "peaks"} // peaks: rising at once, falling slowly
	meterDelays      = []int{-200, -100, 0, 100, 200, 300, 400, 500, 600, 700, 800}
)

// delayFor is how much later the meander shows the sound while a speaker plays it, in ms, as
// set in its page in the Bluetooth tab.
func delayFor(dir, address string) int {
	d := loadOwnSettings(dir).Delays[strings.ToUpper(address)]
	if slices.Contains(meterDelays, d) {
		return d
	}
	return 0
}

// setDelay steps a speaker's delay on by delta choices, as the knob turns.
func setDelay(dir, address string, delta int) error {
	at := slices.Index(meterDelays, delayFor(dir, address)) + delta
	return setDelayTo(dir, address, meterDelays[max(0, min(len(meterDelays)-1, at))])
}

// setDelayTo sets a speaker's delay, in ms.
func setDelayTo(dir, address string, d int) error {
	own := loadOwnSettings(dir)
	if own.Delays == nil {
		own.Delays = map[string]int{}
	}
	address = strings.ToUpper(address)
	if d == 0 {
		delete(own.Delays, address)
	} else {
		own.Delays[address] = d
	}
	if err := writeJSON(filepath.Join(dir, ownSettingsFile), own, 0o644); err != nil {
		return err
	}
	applyMeter(dir)
	return nil
}

// speakerNow is the Bluetooth address of the speaker connected now, or "" (watchLinks).
var speakerNow atomic.Value

func speakerAddress() string {
	a, _ := speakerNow.Load().(string)
	return a
}

func delayLabel(ms int) string {
	switch {
	case ms > 0:
		return fmt.Sprintf("%d ms later", ms)
	case ms < 0:
		return fmt.Sprintf("%d ms earlier", -ms)
	}
	return "As played"
}

// meterSetting is the top meander's bands and fall-off, as set in the Device tab.
func meterSetting(dir string) (int, string) {
	o := loadOwnSettings(dir)
	bands, fall := 4, "gentle"
	if slices.Contains(meterBandChoices, o.Bands) {
		bands = o.Bands
	}
	if slices.Contains(meterFalls, o.Fall) {
		fall = o.Fall
	}
	return bands, fall
}

// applyMeter passes the meander's settings on to the audio meter and the screen.
func applyMeter(dir string) {
	bands, fall := meterSetting(dir)
	meterBands.Store(int32(bands))
	meterDelay.Store(int32(delayFor(dir, speakerAddress())))
	display.update(func(s *screenState) { s.Fall = fall })
}

// setMeter steps one of the meander's settings, "bands" or "fall", on to its next choice.
func (h *hermes) setMeter(which string) error {
	own := loadOwnSettings(h.dir)
	bands, f := meterSetting(h.dir)
	switch which {
	case "fall":
		own.Fall = meterFalls[(slices.Index(meterFalls, f)+1)%len(meterFalls)]
	default:
		own.Bands = meterBandChoices[(slices.Index(meterBandChoices, bands)+1)%len(meterBandChoices)]
	}
	if err := writeJSON(filepath.Join(h.dir, ownSettingsFile), own, 0o644); err != nil {
		return err
	}
	applyMeter(h.dir)
	return nil
}

// vpnSetting is whether to bring up the tunnel home, when there is one: on unless turned off in
// the Device tab (on a network that reaches Hermes directly, such as an Android phone sharing
// the home Wi-Fi, it isn't needed).
func vpnSetting(dir string) bool {
	o := loadOwnSettings(dir)
	return o.VPN == nil || *o.VPN
}

func (h *hermes) setVPN(on bool) error {
	own := loadOwnSettings(h.dir)
	own.VPN = &on
	if err := writeJSON(filepath.Join(h.dir, ownSettingsFile), own, 0o644); err != nil {
		return err
	}
	go setUpTunnel(h.dir)
	return nil
}

// wakeSetting is whether to listen for the wake word: the Car Thing does unless it was turned
// off in its Device tab, the apps only once it is turned on in their Settings.
func wakeSetting(dir string) bool {
	if virtual {
		return loadAudioSettings(dir).Wake
	}
	o := loadOwnSettings(dir)
	return o.Wake == nil || *o.Wake
}

func (h *hermes) setWake(on bool) error {
	own := loadOwnSettings(h.dir)
	own.Wake = &on
	if err := writeJSON(filepath.Join(h.dir, ownSettingsFile), own, 0o644); err != nil {
		return err
	}
	wakeOn.Store(on)
	return nil
}

func loadOwnSettings(dir string) ownSettings {
	var o ownSettings
	_ = readJSON(filepath.Join(dir, ownSettingsFile), &o)
	return o
}

// applyOwnSettings puts device.json's settings over config.json's.
func (c *config) applyOwnSettings(dir string) {
	o := loadOwnSettings(dir)
	if o.Device != "" {
		c.Name, c.Device = o.Name, o.Device
	}
	if o.Mic != "" {
		c.Mic = o.Mic
	}
}

// --- the Device tab ------------------------------------------------------------------------

// mineView is the Device tab: the name, the microphone and the soft keys.
func (b *bluetooth) mineView() { b.mineViewAt(-1) }

func (b *bluetooth) mineViewAt(sel int) {
	h := hereNow()
	if h == nil {
		b.show(&btScreen{View: "message", Tab: "device", Title: "Not ready", Note: "Petasos is still starting."})
		return
	}
	mic := "Car Thing"
	if carMic.Load() {
		mic = "The car's"
	}
	items := []btItem{
		{Kind: "row", Icon: "device", Label: "Name", Value: h.shownName(), do: b.nameKeyboard},
		{Kind: "row", Icon: "mic", Label: "Microphone", Value: mic, do: func() {
			if err := h.setCarMic(!carMic.Load()); err != nil {
				b.message("Couldn't save that", err.Error())
				return
			}
			b.mineViewAt(1)
		}},
		{Kind: "row", Icon: "mic", Label: "Wake word", Value: map[bool]string{true: "“Hey Athena”", false: "Off"}[wakeOn.Load()],
			do: func() {
				if err := h.setWake(!wakeOn.Load()); err != nil {
					b.message("Couldn't save that", err.Error())
					return
				}
				b.mineViewAt(2)
			}},
	}
	if hasTunnel(h.dir) {
		items = append(items, btItem{Kind: "row", Icon: "device", Label: "VPN tunnel home",
			Value: map[bool]string{true: "On", false: "Off"}[vpnSetting(h.dir)], do: func() {
				if err := h.setVPN(!vpnSetting(h.dir)); err != nil {
					b.message("Couldn't save that", err.Error())
					return
				}
				b.mineViewAt(3)
			}})
	}
	// What comes from Hermes is asked for together and waited on briefly, so the tab opens even
	// when Hermes can't be reached (the tunnel home turned off, say) and its own settings, the VPN
	// among them, can still be changed.
	type got struct {
		p   devicePresets
		err error
	}
	presetsCh, versionsCh := make(chan got, 1), make(chan versions, 1)
	go func() { p, err := h.presets(); presetsCh <- got{p, err} }()
	go func() { versionsCh <- h.versions() }()
	until := time.Now().Add(hermesWait)
	var p devicePresets
	err := errors.New("Hermes didn't answer in time")
	select {
	case g := <-presetsCh:
		p, err = g.p, g.err
	case <-time.After(time.Until(until)):
	}
	v := h.localVersions()
	select {
	case v = <-versionsCh:
	case <-time.After(time.Until(until)):
	}
	note := "Name is also what Bluetooth shows this Car Thing as."
	if carMic.Load() {
		note = "The car's microphone is used while the car is connected, the Car Thing's own otherwise."
	}
	if err != nil {
		items = append(items, btItem{Kind: "header", Label: "Soft keys"},
			btItem{Kind: "note", Label: "Can't reach " + display.agent() + " for the soft keys"})
	} else {
		agent := display.agent()
		for i, name := range presetNames {
			switch i {
			case 0:
				items = append(items, btItem{Kind: "header", Label: "Soft keys"})
			case 4:
				items = append(items, btItem{Kind: "header", Label: "Second set, after the top-right button"})
			}
			i, step := i, p.Presets[name].press()
			label := "—"
			if i < len(p.Labels) && p.Labels[i] != "" {
				label = p.Labels[i]
			}
			items = append(items, btItem{Kind: "row", Icon: "key", Label: label, Detail: step.describe(agent),
				do: func() { b.softKeyView(i, p) }})
		}
		if p.Own {
			items = append(items, btItem{Kind: "row", Icon: "back", Label: "Use the shared soft keys", do: func() {
				if _, err := h.setPresets(nil); err != nil {
					b.message("Couldn't change them", friendlyHermes(err))
					return
				}
				b.mineView()
			}})
		} else {
			items = append(items, btItem{Kind: "note", Label: "Shared with your other devices until you change one"})
		}
	}
	bands, fall := meterSetting(h.dir)
	items = append(items, btItem{Kind: "header", Label: "Sound on the screen"})
	at := len(items)
	for i, row := range []struct{ label, value, which string }{
		{"Bands", fmt.Sprint(bands), "bands"},
		{"Fall-off", strings.ToUpper(fall[:1]) + fall[1:], "fall"},
	} {
		items = append(items, btItem{Kind: "row", Icon: "speaker", Label: row.label, Value: row.value, do: func() {
			if err := h.setMeter(row.which); err != nil {
				b.message("Couldn't save that", err.Error())
				return
			}
			b.mineViewAt(at + i)
		}})
	}
	items = append(items, versionItems(v, func() { b.checkForUpdate(h) })...)
	b.show(&btScreen{View: "mine", Tab: "device", Note: note, Items: items, Sel: firstRow(items, sel)})
}

func (b *bluetooth) nameKeyboard() {
	h := hereNow()
	b.keyboard("Name", "What this Car Thing is called. Renaming it starts a new chat with "+display.agent()+".",
		h.shownName(), nameMax, func(text string) {
			if strings.TrimSpace(text) == h.shownName() {
				b.mineView()
				return
			}
			if err := h.rename(text); err != nil {
				b.message("Couldn't rename it", err.Error())
				return
			}
			b.message("Renamed "+strings.TrimSpace(text), "Starting again with the new name…")
			restartSoon()
		}, b.mineView)
}

// softKeyView edits one soft key. Each change is saved at once, making the soft keys this
// device's own.
func (b *bluetooth) softKeyView(i int, p devicePresets) {
	b.mu.Lock()
	b.draft = &softKeyDraft{index: i, presets: p.Presets, step: p.Presets[presetNames[i]].press()}
	b.mu.Unlock()
	b.softKeyViewAt(-1, false, "")
}

type softKeyDraft struct {
	index   int
	presets map[string]presetGestures
	step    presetStep
}

func (b *bluetooth) softKeyViewAt(sel int, editing bool, problem string) {
	b.mu.Lock()
	d := b.draft
	b.mu.Unlock()
	if d == nil {
		b.mineView()
		return
	}
	s := d.step
	items := []btItem{{Kind: "row", Icon: "key", Label: "Does", Value: actionLabel(s.Action),
		turn: func(delta int) {
			at := 0
			for j, a := range softKeyActions {
				if a.value == d.step.Action {
					at = j
				}
			}
			at = max(0, min(len(softKeyActions)-1, at+delta))
			d.step.Action = softKeyActions[at].value
		}}}
	items[0].do = func() { b.softKeyViewAt(0, true, "") }
	if s.Action == "prompt" {
		text := s.Text
		if text == "" {
			text = "Type a question"
		}
		items = append(items, btItem{Kind: "row", Icon: "mic", Label: "Asks", Detail: truncate(text, 48),
			do: b.promptKeyboard})
	}
	if s.Action != "none" {
		label := s.Label
		if label == "" {
			label = "Automatic"
		}
		items = append(items, btItem{Kind: "row", Icon: "device", Label: "Label", Value: truncate(label, 24),
			do: b.labelKeyboard})
	}
	items = append(items, btItem{Kind: "row", Icon: "back", Label: "Back", do: b.mineView})
	note := problem
	if note == "" {
		note = "Changes are saved as you make them, for this Car Thing only."
	}
	title := fmt.Sprintf("Soft key %d", d.index+1)
	if d.index >= 4 {
		title = fmt.Sprintf("Second set, key %d", d.index-3)
	}
	b.show(&btScreen{View: "softkey", Tab: "device", Title: title, Note: note,
		Items: items, Sel: firstRow(items, sel), Editing: editing})
}

// saveSoftKey saves the soft key being edited, unless it still needs words.
func (b *bluetooth) saveSoftKey(sel int) {
	b.mu.Lock()
	d := b.draft
	b.mu.Unlock()
	h := hereNow()
	if d == nil || h == nil {
		return
	}
	if d.step.Action == "prompt" && strings.TrimSpace(d.step.Text) == "" {
		b.promptKeyboard()
		return
	}
	step := d.step
	if step.Action != "prompt" {
		step.Text = ""
	}
	if step.Action == "none" {
		step.Label = ""
	}
	all := map[string]presetGestures{}
	for name, g := range d.presets {
		all[name] = g
	}
	all[presetNames[d.index]] = presetGestures{"press": step}
	p, err := h.setPresets(all)
	if err != nil {
		b.softKeyViewAt(sel, false, "Not saved: "+friendlyHermes(err))
		return
	}
	d.presets = p.Presets
	b.softKeyViewAt(sel, false, "")
}

func (b *bluetooth) promptKeyboard() {
	b.mu.Lock()
	d := b.draft
	b.mu.Unlock()
	if d == nil {
		return
	}
	b.keyboard("Asks", "What this soft key asks "+display.agent()+".", d.step.Text, promptMax,
		func(text string) {
			d.step.Text = strings.TrimSpace(text)
			if d.step.Text == "" {
				b.softKeyViewAt(1, false, "A question needs some words.")
				return
			}
			b.saveSoftKey(1)
		}, func() { b.softKeyViewAt(1, false, "") })
}

func (b *bluetooth) labelKeyboard() {
	b.mu.Lock()
	d := b.draft
	b.mu.Unlock()
	if d == nil {
		return
	}
	at := 1
	if d.step.Action == "prompt" {
		at = 2
	}
	b.keyboard("Label", "Shown on the screen above this button. Leave it empty to make one up.", d.step.Label, labelMax,
		func(text string) {
			d.step.Label = strings.TrimSpace(text)
			b.saveSoftKey(at)
		}, func() { b.softKeyViewAt(at, false, "") })
}

// --- the on-screen keyboard ----------------------------------------------------------------

// keyboard shows the on-screen keyboard (drawn by the page) with text to edit. done gets the
// text when the page sends it; back runs if the front button cancels.
func (b *bluetooth) keyboard(title, note, text string, max int, done func(string), back func()) {
	b.keyboardFor(title, note, text, max, false, done, back)
}

// keyboardFor is keyboard with a choice of starting in capitals (plain is for addresses, paths
// and sign-ins, which mostly don't).
func (b *bluetooth) keyboardFor(title, note, text string, max int, plain bool, done func(string), back func()) {
	b.mu.Lock()
	b.kbID++
	id := b.kbID
	b.typed, b.typedBack = done, back
	tab := b.tab
	b.mu.Unlock()
	b.show(&btScreen{View: "keyboard", Tab: tab, Title: title, Note: note, Text: text, Max: max, Keyboard: id, Plain: plain})
}

// keyboardInput takes a knob event while the keyboard is up: the page moves its highlight and
// presses the highlighted key. The front button cancels.
func (b *bluetooth) keyboardInput(name, action string, delta int) {
	switch {
	case name == "knob" && action == "turn" && delta != 0:
		display.send(map[string]any{"kb": map[string]int{"turn": delta}})
	case name == "knob" && action == "press":
		display.send(map[string]any{"kb": "press"})
	case name == "front" && action == "press":
		b.mu.Lock()
		back := b.typedBack
		b.typed, b.typedBack = nil, nil
		b.mu.Unlock()
		if back != nil {
			back()
		}
	}
}

// typedText is /settings/text: the page's keyboard is done, with the text typed.
func typedText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Keyboard int    `json:"keyboard"`
		Text     string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	bt.mu.Lock()
	v, done := bt.view, bt.typed
	ok := bt.open && v != nil && v.View == "keyboard" && v.Keyboard == body.Keyboard && done != nil
	max := 0
	if ok {
		max = v.Max
		bt.typed, bt.typedBack = nil, nil
	}
	bt.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusGone)
		return
	}
	text := strings.Map(func(r rune) rune {
		if r < ' ' {
			return -1
		}
		return r
	}, body.Text)
	if max > 0 && len([]rune(text)) > max {
		text = string([]rune(text)[:max])
	}
	w.WriteHeader(http.StatusNoContent)
	go done(text)
}

// checkForUpdate is the Petasos row pressed: an update, if there is one, downloads now (its
// progress shows on the screen) rather than at the next half-hourly check.
func (b *bluetooth) checkForUpdate(h *hermes) {
	b.message("Checking for an update", "Asking "+display.agent()+" for the latest Petasos…")
	go func() {
		current, err := h.checkNow()
		switch {
		case err != nil:
			b.message("Couldn't update", friendlyHermes(err))
		case current:
			b.message("Petasos is up to date", "This is the latest version, "+version+".")
		}
	}()
}

// versionItems lists the image, this program and the plugin, at the foot of the Device tab.
// Pressing Petasos's row checks for an update.
func versionItems(v versions, check func()) []btItem {
	unknown := func(s string) string {
		if s == "" {
			return "Unknown"
		}
		return s
	}
	items := []btItem{{Kind: "header", Label: "Versions"}}
	if !virtual {
		items = append(items, btItem{Kind: "row", Icon: "device", Label: "System image", Value: unknown(v.Image)})
	}
	program := btItem{Kind: "row", Icon: "device", Label: "Petasos", Value: v.Program, do: check}
	switch {
	case v.Latest != "" && v.Latest != v.Program:
		program.Detail = "Updating to " + v.Latest
	case virtual:
		program.Detail = "Click to check for an update"
	default:
		program.Detail = "Press the knob to check for an update"
	}
	return append(items, program, btItem{Kind: "row", Icon: "device", Label: "Plugin on Hermes", Value: unknown(v.Plugin)})
}
