package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The virtual Petasos: this same program on a Mac, inside the Petasos app (mac/). It has no
// buttons, Bluetooth or backlight of its own. The app's window shows the screen page, whose
// on-screen controls and keyboard shortcuts post presses to /input; the app's own executable
// records and plays the audio (-audio), since macOS has no arecord or aplay. Hermes sees it as
// another device ("mac" unless config.json says otherwise), with the same buttons and presets.
//
//	petasos -dir DIR signin SERVER [USERNAME]    password on stdin; writes config.json and tokens.json
//	petasos -dir DIR -audio APP virtual          runs the virtual Petasos

var (
	virtual     bool   // running as the virtual Petasos
	audioHelper string // the app's executable, which records (--record RATE) and plays (--play)
)

// recordCommand is the recorder: raw 32-bit little-endian mono samples at sampleRate on stdout.
func (v *voice) recordCommand() *exec.Cmd {
	if virtual {
		args := []string{"--record", strconv.Itoa(sampleRate)}
		if micChoice != "" {
			args = append(args, micChoice)
		}
		return exec.Command(audioHelper, args...)
	}
	mic := v.mic
	if carMic.Load() && display.snapshot().Links.Speaker {
		mic = carMicDevice
	}
	return exec.Command("arecord", "-q", "-D", mic, "-f", "S32_LE", "-r", fmt.Sprint(sampleRate),
		"-c", "1", "-t", "raw")
}

// playCommand plays a WAV file from stdin and returns once it has played.
func (v *voice) playCommand() *exec.Cmd {
	if virtual {
		return exec.Command(audioHelper, "--play")
	}
	return exec.Command("aplay", "-q", "-D", v.speaker, "-")
}

// macVolume nudges the Mac's own output volume, for the knob on the virtual Petasos.
func macVolume(delta int) {
	if runtime.GOOS != "darwin" {
		computerVolume(delta) // Windows's, or the Linux desktop's
		return
	}
	out, err := exec.Command("osascript", "-e", "output volume of (get volume settings)").Output()
	level, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || convErr != nil {
		log.Printf("can't read the Mac's volume: %v", err)
		return
	}
	level = max(0, min(100, level+delta))
	if err := exec.Command("osascript", "-e", fmt.Sprintf("set volume output volume %d", level)).Run(); err != nil {
		log.Printf("can't set the Mac's volume: %v", err)
		return
	}
	display.update(func(st *screenState) { st.Volume = level; st.VolumeID++ })
}

// startVirtual is run's Mac half: everything but the hardware.
func (h *hermes) startVirtual(raw chan<- map[string]any) {
	display.update(func(st *screenState) {
		st.Virtual = true
		st.PageMic = pageMicOn()
		st.OnTop = loadWindowSettings(h.dir).OnTop
		st.NoPin = runtime.GOOS == "linux" // a browser's app window can't keep itself on top
		// No phone or speaker to watch: Phone stands for whether the Mac is online, and the
		// Mac has its own speakers.
		st.Links = links{Phone: true, Speaker: true, PhonePaired: true, SpeakerKnown: true, Checked: true}
	})
	display.mu.Lock()
	display.inputs = raw
	display.mu.Unlock()
	micChoice = loadAudioSettings(h.dir).Mic
	h.servesAppSettings()
	h.startVPN()
	go h.watchHome()
	go watchInternet()
}

// watchInternet checks every 20 seconds that the Mac is online, for the network icon.
func watchInternet() {
	for ; ; time.Sleep(20 * time.Second) {
		ok := false
		for _, addr := range []string{"captive.apple.com:80", "1.1.1.1:443"} {
			if c, err := net.DialTimeout("tcp", addr, 5*time.Second); err == nil {
				c.Close()
				ok = true
				break
			}
		}
		display.update(func(st *screenState) { st.Links.Phone = ok })
	}
}

// pagePress is a press from the page's on-screen controls or keyboard shortcuts.
func (s *screen) pagePress(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	inputs := s.inputs
	s.mu.Unlock()
	if r.Method != http.MethodPost || inputs == nil {
		http.Error(w, "not here", http.StatusNotFound)
		return
	}
	var ev struct {
		Input  string `json:"input"`
		Action string `json:"action"`
		Delta  int    `json:"delta"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&ev); err != nil {
		http.Error(w, "bad press", http.StatusBadRequest)
		return
	}
	known := map[string]bool{"preset-1": true, "preset-2": true, "preset-3": true, "preset-4": true,
		"mute": true, "front": true, "knob": true}
	if !known[ev.Input] || (ev.Action != "press" && ev.Action != "release" && ev.Action != "turn") {
		http.Error(w, "unknown control", http.StatusBadRequest)
		return
	}
	out := map[string]any{"input": ev.Input, "action": ev.Action, "t": time.Now().UnixMilli()}
	if ev.Action == "turn" {
		out["delta"] = ev.Delta
	}
	select {
	case inputs <- out:
	default:
	}
	w.WriteHeader(http.StatusNoContent)
}

// signIn signs in to Hermes with a password, as the Car Thing's installer does, and saves the
// server address and the sign-in for the virtual Petasos.
func signIn(dir, server, username string, password io.Reader) error {
	server = strings.TrimRight(server, "/")
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		server = "http://" + server
	}
	pass, err := bufio.NewReader(password).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	pass = strings.TrimRight(pass, "\r\n")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(server + "/api/auth/providers")
	if err != nil {
		return fmt.Errorf("can't reach %s: %w", server, err)
	}
	var listed struct {
		Providers []struct {
			Name             string `json:"name"`
			SupportsPassword bool   `json:"supports_password"`
		} `json:"providers"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&listed)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("%s doesn't look like a Hermes dashboard: %w", server, err)
	}
	provider := ""
	for _, p := range listed.Providers {
		if p.SupportsPassword {
			provider = p.Name
			break
		}
	}
	if provider == "" {
		return errors.New("this Hermes has no password sign-in turned on")
	}
	body, _ := json.Marshal(map[string]string{"provider": provider, "username": username, "password": pass})
	resp, err = client.Post(server+"/auth/password-login", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Hermes didn't accept that username and password (HTTP %d)", resp.StatusCode)
	}
	var t tokens
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "hermes_session_at":
			t.AccessToken = strings.Trim(c.Value, `"`)
		case "hermes_session_rt":
			t.RefreshToken = strings.Trim(c.Value, `"`)
		}
	}
	if t.AccessToken == "" || t.RefreshToken == "" {
		return errors.New("signed in, but Hermes gave no renewable sign-in")
	}
	t.Provider = provider
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Signing in again keeps this computer's name.
	var cfg config
	_ = readJSON(filepath.Join(dir, "config.json"), &cfg)
	cfg.Server, cfg.Username = server, username
	if cfg.Device == "" {
		cfg.Device = defaultComputerID()
	}
	if err := writeJSON(filepath.Join(dir, "config.json"), cfg, 0o644); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "tokens.json"), t, 0o600)
}

// defaultComputerID names a newly signed-in computer until its settings give it a name.
func defaultComputerID() string {
	switch runtime.GOOS {
	case "windows":
		return "pc"
	case "linux":
		return "linux"
	}
	return "mac"
}
