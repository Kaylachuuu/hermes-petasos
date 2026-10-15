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
// ({"server": "http://10.0.0.20:9119"}), tokens.json (written by the installer, then kept
// fresh here as Hermes rotates them) and, optionally, wireguard.conf for the tunnel home.
// It gets online through the phone's hotspot over Bluetooth (tether.go) and reaches the home
// network through WireGuard (wireguard.go).
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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var version = "dev" // set at build time from VERSION

type config struct {
	Server   string `json:"server"`
	Device   string `json:"device,omitempty"`   // name the plugin keeps state under; default "car-thing"
	Name     string `json:"name,omitempty"`     // the app's own name for this computer, shown in its settings
	Username string `json:"username,omitempty"` // who the app signed in as, to fill in its sign-in page
	Grab     *bool  `json:"grab,omitempty"`     // take the buttons from Nocturne's own screen; default true
	Phone    string `json:"phone,omitempty"`    // Bluetooth address of the phone to tether to; default any paired one
	Mic      string `json:"mic,omitempty"`      // ALSA device to record from (32-bit samples); default hw:0,0; "car" for the car's (device.go)
	Speaker  string `json:"speaker,omitempty"`  // ALSA device to play on; default bluealsa (the connected Bluetooth speaker)
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
	if windowsApp() { // petasos.exe run as the Windows app, or as its recorder or player
		return
	}
	dir := flag.String("dir", "/var/lib/petasos", "folder holding config.json and tokens.json")
	events := flag.String("events", "", "read raw input events from this file instead of the Car Thing's buttons (testing)")
	flag.StringVar(&audioHelper, "audio", "", "the virtual Petasos's recorder and player (the Mac app's executable)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: petasos [-dir DIR] [run|check|version|virtual|signin SERVER [USERNAME]|vpncheck FILE]")
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
	switch cmd {
	case "check":
		h, err := load(*dir)
		if err != nil {
			log.Fatal(err)
		}
		if err := h.check(); err != nil {
			log.Fatal(err)
		}
	case "signin": // the virtual Petasos's sign-in; the password comes on stdin
		if flag.NArg() < 2 {
			flag.Usage()
			os.Exit(2)
		}
		if err := signIn(*dir, flag.Arg(1), flag.Arg(2), os.Stdin); err != nil {
			log.Fatal(err)
		}
		fmt.Println("signed in")
	case "vpncheck": // the Mac app's settings, before saving a WireGuard config
		if err := checkVPNConfig(flag.Arg(1)); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("ok")
	case "virtual":
		if audioHelper == "" {
			log.Fatal("the virtual Petasos needs -audio, the app that records and plays")
		}
		virtual = true
		appDir = *dir
		handOver(*dir)
		h, err := load(*dir)
		if err != nil {
			log.Printf("not signed in yet (%v); the app shows its sign-in page", err)
			signInNeeded = true
			display.serve()
			return
		}
		h.run("")
	case "run":
		keepLog(*dir)
		adopt(*dir)
		h, err := load(*dir)
		for err != nil {
			// A fresh image starts this before the installer has given it a server and sign-in.
			log.Printf("waiting for setup (%v); run install_device.py from a computer", err)
			time.Sleep(30 * time.Second)
			h, err = load(*dir)
		}
		h.run(*events)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func load(dir string) (*hermes, error) {
	h := &hermes{dir: dir, http: &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{Proxy: nil, MaxIdleConns: 2, IdleConnTimeout: 90 * time.Second,
			DialContext: dialHermes},
	}}
	if err := readJSON(filepath.Join(dir, "config.json"), &h.cfg); err != nil {
		return nil, fmt.Errorf("reading config.json: %w", err)
	}
	h.cfg.applyOwnSettings(dir)
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
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = raw
	}
	data, err := h.exchange(method, path, "application/json", payload, h.http, 1<<20)
	if err == nil && out != nil {
		return json.Unmarshal(data, out)
	}
	return err
}

// exchange sends one request of any kind with the access token, renewing the token once if it
// has expired, and returns the reply body (up to limit bytes) for a 2xx status.
func (h *hermes) exchange(method, path, contentType string, payload []byte, client *http.Client, limit int64) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		status, data, err := h.send(method, path, contentType, payload, client, limit)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			if err := h.refresh(); err != nil {
				return nil, err
			}
			continue
		}
		if status/100 != 2 {
			return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, status, truncate(string(data), 200))
		}
		return data, nil
	}
}

func (h *hermes) send(method, path, contentType string, payload []byte, client *http.Client, limit int64) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, h.cfg.Server+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", contentType)
	}
	h.mu.Lock()
	token := h.tokens.AccessToken
	h.mu.Unlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
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

// adopt hands over to the copy in dir, which is the one that updates itself. The petasos image
// starts the read-only copy in /usr/lib/petasos; on first boot, or when a reflash brings a newer
// one, it copies itself into dir first. It returns only if it should keep running as it is.
func adopt(dir string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	self, _ = filepath.EvalSymlinks(self)
	live := filepath.Join(dir, "petasos")
	if self == live {
		return
	}
	if out, err := exec.Command(live, "version").Output(); err == nil && !newer(version, strings.TrimSpace(string(out))) {
		log.Printf("handing over to %s", live)
		log.Print(execReplace(live, append([]string{live}, os.Args[1:]...)))
		return
	}
	if err := copyFile(self, live+".new"); err == nil && os.Rename(live+".new", live) == nil {
		log.Printf("installed %s as %s", version, live)
		log.Print(execReplace(live, append([]string{live}, os.Args[1:]...)))
		return
	}
	log.Printf("couldn't install into %s; running from %s", dir, self)
}

// newer reports whether version a is newer than b, comparing dotted numbers ("0.10.0" > "0.9.1").
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type clientInfo struct {
	Version string `json:"version"`
	OS      string `json:"os"` // which system the build is for; older plugins only had Linux
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// update replaces this program with the server's version when they differ, then restarts in
// place. It returns only when there was nothing to do or the update failed. The app's copy
// saves the update beside its settings instead (engine.go), and only ever moves forward.
func (h *hermes) update() error {
	build := "os=" + runtime.GOOS + "&arch=" + runtime.GOARCH
	var latest clientInfo
	if err := h.call("GET", "/api/plugins/petasos/client?"+build, nil, &latest); err != nil {
		return err
	}
	if latest.Version == "" || latest.Version == version {
		return nil
	}
	var target string
	if virtual {
		if latest.OS != runtime.GOOS || !versionNewer(latest.Version, version) {
			return nil
		}
		if err := os.MkdirAll(filepath.Join(appDir, "engine"), 0o755); err != nil {
			return err
		}
		target = filepath.Join(appDir, "engine", engineName(latest.Version))
	} else {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		if target, err = filepath.EvalSymlinks(self); err != nil {
			return err
		}
	}
	log.Printf("updating from %s to %s", version, latest.Version)
	req, err := http.NewRequest("GET", h.cfg.Server+"/api/plugins/petasos/client/download?"+build, nil)
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
	staged := target + ".new"
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
		err = os.Rename(staged, target)
	}
	if err == nil && virtual {
		err = installEngine(appDir, latest.Version)
	}
	if err != nil {
		os.Remove(staged)
		return err
	}
	log.Printf("updated to %s; restarting", latest.Version)
	return restartAs(target)
}

// restartAs runs the updated program in this one's place.
var restartAs = func(path string) error {
	if virtual && runtime.GOOS == "windows" {
		os.Exit(0) // the app starts the new copy
	}
	return execReplace(path, os.Args)
}

// --- the main loop -------------------------------------------------------------------------

func (h *hermes) run(eventsFile string) {
	log.Printf("petasos %s starting, server %s", version, h.cfg.Server)
	presses := make(chan map[string]any, 64)
	raw := make(chan map[string]any, 64)
	grab := h.cfg.Grab == nil || *h.cfg.Grab
	h.servesDevice()
	bt.name = h.shownName()
	carMic.Store(h.cfg.Mic == "car")
	go display.serve()
	go backlight.start(h.dir)
	netFirst.load(h.dir)
	go filterInputs(raw, presses)
	if virtual {
		h.startVirtual(raw)
	} else if eventsFile != "" {
		go readEventFile(eventsFile, "test", raw)
	} else {
		go watchInputs(grab, raw)
		go watchTouch()
		go watchCarButtons(raw)
		setUpTunnel(h.dir)
		go keepTethered(h.cfg.Phone)
		go keepSpeakerConnected()
		go keepKioskOnScreen()
		go bt.start()
		go music.watch()
		go h.watchHome()
	}

	v := newVoice(h, presses)
	library.attach(h, v)
	go h.pollCommands(v)
	if !virtual {
		volumes.load(h.dir)
		go v.keepVolumes()
	}
	online := false
	lastUpdate := time.Time{}
	for {
		if !online {
			commands, err := h.connect(&lastUpdate)
			if err != nil {
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
			setHome(true)
			for _, c := range commands {
				v.carryOut(c)
			}
		}
		select {
		case ev := <-presses:
			commands, err := h.input(ev)
			if err != nil {
				log.Printf("lost Hermes: %v", err)
				online = false
				setHome(false)
				continue
			}
			for _, c := range commands {
				v.carryOut(c)
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
func (h *hermes) connect(lastUpdate *time.Time) ([]map[string]any, error) {
	if err := h.update(); err != nil {
		if errors.Is(err, errSignedOut) {
			return nil, err
		}
		log.Printf("update check failed: %v", err)
	}
	*lastUpdate = time.Now()
	display.update(func(st *screenState) { st.Muted = false }) // hello resets the mic on the plugin too
	return h.input(map[string]any{"input": "device", "action": "hello"})
}

// watchHome checks every 20 seconds that Hermes answers, for the home icon on the screen. Any
// answer will do, even "signed out": it never renews the sign-in itself, since two renewals at
// once would race for Hermes's single-use refresh token.
func (h *hermes) watchHome() {
	quick := &http.Client{Transport: h.http.Transport, Timeout: 8 * time.Second}
	for ; ; time.Sleep(20 * time.Second) {
		_, _, err := h.send("GET", "/api/plugins/petasos/status", "", nil, quick, 1<<16)
		setHome(err == nil)
	}
}

func setHome(ok bool) {
	display.update(func(st *screenState) { st.Links.Home = ok })
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
