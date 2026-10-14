// Petasos device program: runs on the Car Thing, reads its buttons and knob, and asks the petasos
// plugin on Hermes what each press should do.
//
// It holds no button mapping of its own; the plugin decides. It also keeps itself up to date: on
// start, and every half hour, it asks the plugin which version is current and replaces itself
// when that differs from its own.
//
//	petasos run        the normal mode, started at boot
//	petasos check      sign-in and connection check, then exit
//	petasos version    print the version
//
// Everything lives in one folder (default /var/lib/petasos): this program, config.json
// ({"server": "http://10.0.0.20:9119"}) and tokens.json (written by the installer, then kept
// fresh here as Hermes rotates them).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

var version = "dev" // set at build time from VERSION

type config struct {
	Server string `json:"server"`
	Device string `json:"device,omitempty"` // name the plugin keeps state under; default "car-thing"
	Grab   *bool  `json:"grab,omitempty"`   // take the buttons from Nocturne's own screen; default true
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Provider     string `json:"provider,omitempty"`
}

var errSignedOut = errors.New("Hermes no longer accepts this device's sign-in; run the installer again")

type hermes struct {
	dir    string
	cfg    config
	http   *http.Client
	mu     sync.Mutex
	tokens tokens
}

func main() {
	log.SetFlags(0)
	dir := flag.String("dir", "/var/lib/petasos", "folder holding config.json and tokens.json")
	events := flag.String("events", "", "read raw input events from this file instead of the Car Thing's buttons (testing)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: petasos [-dir DIR] [run|check|version]")
		flag.PrintDefaults()
	}
	flag.Parse()
	cmd := flag.Arg(0)
	if cmd == "" {
		cmd = "run"
	}
	if cmd == "version" {
		fmt.Println(version)
		return
	}
	h, err := load(*dir)
	if err != nil {
		log.Fatal(err)
	}
	switch cmd {
	case "check":
		if err := h.check(); err != nil {
			log.Fatal(err)
		}
	case "run":
		h.run(*events)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func load(dir string) (*hermes, error) {
	h := &hermes{dir: dir, http: &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{Proxy: nil, MaxIdleConns: 2, IdleConnTimeout: 90 * time.Second},
	}}
	if err := readJSON(filepath.Join(dir, "config.json"), &h.cfg); err != nil {
		return nil, fmt.Errorf("reading config.json: %w", err)
	}
	h.cfg.Server = strings.TrimRight(h.cfg.Server, "/")
	if h.cfg.Server == "" {
		return nil, errors.New("config.json has no server address")
	}
	if h.cfg.Device == "" {
		h.cfg.Device = "car-thing"
	}
	if err := readJSON(filepath.Join(dir, "tokens.json"), &h.tokens); err != nil {
		return nil, fmt.Errorf("reading tokens.json: %w", err)
	}
	return h, nil
}

// --- talking to Hermes ---------------------------------------------------------------------

// call sends one JSON request with the access token, renewing the token once if it has expired.
func (h *hermes) call(method, path string, body, out any) error {
	for attempt := 0; ; attempt++ {
		status, data, err := h.send(method, path, body)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			if err := h.refresh(); err != nil {
				return err
			}
			continue
		}
		if status/100 != 2 {
			return fmt.Errorf("%s %s: HTTP %d: %s", method, path, status, truncate(string(data), 200))
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

func (h *hermes) send(method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.cfg.Server+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h.mu.Lock()
	token := h.tokens.AccessToken
	h.mu.Unlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, err
}

// refresh swaps the refresh token for a new pair. Hermes rotates the refresh token every time,
// so the new one is saved before anything else happens.
func (h *hermes) refresh() error {
	h.mu.Lock()
	body := map[string]string{"refresh_token": h.tokens.RefreshToken, "provider": h.tokens.Provider}
	h.mu.Unlock()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", h.cfg.Server+"/auth/native/refresh", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
		return errSignedOut
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("renewing the sign-in: HTTP %d", resp.StatusCode)
	}
	var fresh tokens
	if err := json.NewDecoder(resp.Body).Decode(&fresh); err != nil || fresh.AccessToken == "" {
		return fmt.Errorf("renewing the sign-in: unexpected reply")
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = body["refresh_token"]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tokens = fresh
	return writeJSON(filepath.Join(h.dir, "tokens.json"), fresh, 0o600)
}

func (h *hermes) input(ev map[string]any) ([]map[string]any, error) {
	ev["device"] = h.cfg.Device
	var reply struct {
		Commands []map[string]any `json:"commands"`
	}
	err := h.call("POST", "/api/plugins/petasos/input", ev, &reply)
	return reply.Commands, err
}

func (h *hermes) check() error {
	fmt.Printf("petasos %s (%s/%s), server %s\n", version, runtime.GOOS, runtime.GOARCH, h.cfg.Server)
	if _, err := h.input(map[string]any{"input": "device", "action": "hello"}); err != nil {
		return fmt.Errorf("reaching the petasos plugin: %w", err)
	}
	fmt.Println("signed in, and the petasos plugin answered")
	var latest clientInfo
	if err := h.call("GET", "/api/plugins/petasos/client", nil, &latest); err != nil {
		return fmt.Errorf("asking for the latest version: %w", err)
	}
	fmt.Printf("latest version on the server: %s\n", latest.Version)
	return nil
}

// --- self-update ---------------------------------------------------------------------------

type clientInfo struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// update replaces this program with the server's version when they differ, then restarts in
// place. It returns only when there was nothing to do or the update failed.
func (h *hermes) update() error {
	var latest clientInfo
	if err := h.call("GET", "/api/plugins/petasos/client?arch="+runtime.GOARCH, nil, &latest); err != nil {
		return err
	}
	if latest.Version == "" || latest.Version == version {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	log.Printf("updating from %s to %s", version, latest.Version)
	req, err := http.NewRequest("GET", h.cfg.Server+"/api/plugins/petasos/client/download?arch="+runtime.GOARCH, nil)
	if err != nil {
		return err
	}
	h.mu.Lock()
	req.Header.Set("Authorization", "Bearer "+h.tokens.AccessToken)
	h.mu.Unlock()
	download := &http.Client{Transport: h.http.Transport, Timeout: 5 * time.Minute}
	resp, err := download.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	staged := self + ".new"
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, 64<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != latest.Size || hex.EncodeToString(sum.Sum(nil)) != latest.SHA256) {
		err = errors.New("the download doesn't match the server's checksum")
	}
	if err == nil {
		err = os.Rename(staged, self)
	}
	if err != nil {
		os.Remove(staged)
		return err
	}
	log.Printf("updated to %s; restarting", latest.Version)
	return syscall.Exec(self, os.Args, os.Environ())
}

// --- the main loop -------------------------------------------------------------------------

func (h *hermes) run(eventsFile string) {
	log.Printf("petasos %s starting, server %s", version, h.cfg.Server)
	presses := make(chan map[string]any, 64)
	grab := h.cfg.Grab == nil || *h.cfg.Grab
	if eventsFile != "" {
		go readEventFile(eventsFile, "test", presses)
	} else {
		go watchInputs(grab, presses)
	}

	online := false
	lastUpdate := time.Time{}
	for {
		if !online {
			if err := h.connect(&lastUpdate); err != nil {
				log.Printf("can't reach Hermes yet: %v", err)
				wait := 10 * time.Second
				if errors.Is(err, errSignedOut) {
					wait = 10 * time.Minute
				}
				drainFor(presses, wait)
				continue
			}
			online = true
			log.Printf("connected")
		}
		select {
		case ev := <-presses:
			commands, err := h.input(ev)
			if err != nil {
				log.Printf("lost Hermes: %v", err)
				online = false
				continue
			}
			for _, c := range commands {
				h.carryOut(c)
			}
		case <-time.After(time.Minute):
			if time.Since(lastUpdate) > 30*time.Minute {
				lastUpdate = time.Now()
				if err := h.update(); err != nil {
					log.Printf("update check failed: %v", err)
				}
			}
		}
	}
}

// connect checks for an update, then says hello so the plugin forgets any half-finished press
// from before the connection dropped.
func (h *hermes) connect(lastUpdate *time.Time) error {
	if err := h.update(); err != nil {
		if errors.Is(err, errSignedOut) {
			return err
		}
		log.Printf("update check failed: %v", err)
	}
	*lastUpdate = time.Now()
	_, err := h.input(map[string]any{"input": "device", "action": "hello"})
	return err
}

// carryOut acts on one command from the plugin. Sound isn't wired up yet, so for now every
// command is logged; the voice step fills these in.
func (h *hermes) carryOut(c map[string]any) {
	raw, _ := json.Marshal(c)
	log.Printf("command %s", raw)
}

// drainFor waits, throwing away presses made while Hermes can't be reached: replaying them
// later would surprise more than help.
func drainFor(presses <-chan map[string]any, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case <-presses:
		case <-deadline:
			return
		}
	}
}

// --- files ---------------------------------------------------------------------------------

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v any, mode os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
