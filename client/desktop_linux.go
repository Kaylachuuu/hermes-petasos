//go:build linux

package main

// The Linux desktop app (a Raspberry Pi with a desktop, say): `petasos desktop` is the app, as
// petasos.exe is on Windows. It runs the petasos program behind it (virtual, restarted if it
// stops, and handed over to updates from the plugin), and shows the screen in a Chromium-family
// browser's app window, which has no tabs or address bar; closing that window quits. The page
// records the microphone itself, as in the Mac app (pagemic.go), and replies play through the
// desktop's sound server (paplay, else pw-play or aplay), run as `petasos --play`.
// linux/install.sh puts it in the desktop's menu.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// windowsApp handles the helper modes the desktop app runs itself in: --play and --inputs.
func windowsApp() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "--play":
		if err := linuxPlay(os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case "--inputs":
		_ = json.NewEncoder(os.Stdout).Encode(linuxInputs())
		os.Exit(0)
	}
	return false
}

// linuxPlay plays a WAV from stdin through the desktop's sound, returning once it has played.
func linuxPlay(in io.Reader) error {
	for _, p := range [][]string{{"paplay"}, {"pw-play", "-"}, {"aplay", "-q", "-"}} {
		if _, err := exec.LookPath(p[0]); err != nil {
			continue
		}
		cmd := exec.Command(p[0], p[1:]...)
		cmd.Stdin, cmd.Stderr = in, os.Stderr
		return cmd.Run()
	}
	return errors.New("no sound player found: install pulseaudio-utils (paplay) or alsa-utils (aplay)")
}

// linuxInputs is the microphones PulseAudio or PipeWire knows, by the names the page sees.
func linuxInputs() []micInfo {
	out, err := exec.Command("pactl", "list", "sources").Output()
	if err != nil {
		return []micInfo{}
	}
	return parsePactlSources(string(out))
}

var pactlField = regexp.MustCompile(`^\s*(Name|Description):\s*(.*)$`)

func parsePactlSources(out string) []micInfo {
	mics := []micInfo{}
	var name string
	for _, line := range strings.Split(out, "\n") {
		m := pactlField.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == "Name" {
			name = strings.TrimSpace(m[2])
			continue
		}
		if name == "" || strings.HasSuffix(name, ".monitor") { // what's playing, not a microphone
			name = ""
			continue
		}
		mics = append(mics, micInfo{ID: name, Name: strings.TrimSpace(m[2])})
		name = ""
	}
	return mics
}

// computerVolume nudges the desktop's output volume, for the knob's arrows and wheel.
func computerVolume(delta int) {
	sign := "+"
	if delta < 0 {
		sign, delta = "-", -delta
	}
	if err := exec.Command("pactl", "set-sink-volume", "@DEFAULT_SINK@", fmt.Sprintf("%s%d%%", sign, delta)).Run(); err != nil {
		log.Printf("can't change the volume: %v", err)
		return
	}
	out, err := exec.Command("pactl", "get-sink-volume", "@DEFAULT_SINK@").Output()
	if err != nil {
		return
	}
	if m := regexp.MustCompile(`(\d+)%`).FindStringSubmatch(string(out)); m != nil {
		level, _ := strconv.Atoi(m[1])
		level = min(level, 100)
		display.update(func(st *screenState) { st.Volume = level; st.VolumeID++ })
	}
}

// --- the app ----------------------------------------------------------------------------------

func desktopFolder() string {
	base, err := os.UserConfigDir() // ~/.config
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "Petasos")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// browsers are the Chromium-family browsers whose app windows can show the screen.
var browsers = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable",
	"brave-browser", "microsoft-edge", "vivaldi"}

func findBrowser() string {
	for _, b := range browsers {
		if path, err := exec.LookPath(b); err == nil {
			return path
		}
	}
	return ""
}

func runDesktop() error {
	dir := desktopFolder()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	engine := &desktopEngine{self: self, dir: dir}
	engine.start()
	defer engine.stop()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	ready := false
	for i := 0; i < 150 && !ready; i++ {
		if resp, err := http.Get("http://" + screenAddr + "/app/signin"); err == nil {
			resp.Body.Close()
			ready = true
		} else {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if !ready {
		return fmt.Errorf("Petasos didn't start; its log is %s", filepath.Join(dir, "petasos.log"))
	}
	url := "http://" + screenAddr + "/"
	browser := findBrowser()
	if browser == "" {
		// No app window to be had: a tab in the default browser, and Petasos runs until quit.
		desktopMessage("Petasos opens best in Chromium (sudo snap install chromium); for now it's in a browser tab.")
		_ = exec.Command("xdg-open", url).Start()
		<-quit
		return nil
	}
	// Its own profile, so the window is a program of its own (closing it quits Petasos) and the
	// microphone it is allowed stays allowed. A snap's browser may only write under ~/snap.
	profile := filepath.Join(dir, "window")
	if strings.HasPrefix(browser, "/snap/") {
		if home, err := os.UserHomeDir(); err == nil {
			profile = filepath.Join(home, "snap", filepath.Base(browser), "common", "petasos-window")
		}
	}
	window := exec.Command(browser, "--app="+url, "--user-data-dir="+profile, "--class=Petasos",
		"--name=Petasos", "--window-size=800,580", "--no-first-run", "--no-default-browser-check",
		"--disable-features=Translate", "--autoplay-policy=no-user-gesture-required",
		"--use-fake-ui-for-media-stream") // the screen's own page may use the microphone without asking
	if err := window.Start(); err != nil {
		return err
	}
	closed := make(chan struct{})
	go func() { _ = window.Wait(); close(closed) }()
	select {
	case <-closed:
	case <-quit:
		_ = window.Process.Signal(syscall.SIGTERM)
	}
	return nil
}

// desktopMessage tells the person something, in a dialog where the desktop has one.
func desktopMessage(text string) {
	for _, c := range [][]string{{"kdialog", "--title", "Petasos", "--msgbox", text},
		{"zenity", "--info", "--title=Petasos", "--text=" + text}, {"notify-send", "Petasos", text}} {
		if _, err := exec.LookPath(c[0]); err == nil {
			_ = exec.Command(c[0], c[1:]...).Start()
			return
		}
	}
	log.Print(text)
}

// desktopEngine is the petasos program behind the window, started again if it stops (after
// signing in, or an update).
type desktopEngine struct {
	self, dir string
	mu        sync.Mutex
	cmd       *exec.Cmd
	quitting  bool
}

func (e *desktopEngine) start() {
	logFile, _ := os.OpenFile(filepath.Join(e.dir, "petasos.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	cmd := exec.Command(e.self, "-dir", e.dir, "-audio", e.self, "virtual") // it hands over to an update itself
	var out io.Writer = &bytes.Buffer{}
	if logFile != nil {
		out = logFile
	}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		desktopMessage("Couldn't start Petasos: " + err.Error())
		return
	}
	e.mu.Lock()
	e.cmd = cmd
	e.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		time.Sleep(time.Second)
		e.mu.Lock()
		again := !e.quitting && e.cmd == cmd
		e.mu.Unlock()
		if again {
			e.start()
		}
	}()
}

func (e *desktopEngine) stop() {
	e.mu.Lock()
	e.quitting = true
	cmd := e.cmd
	e.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
		}
	}
}
