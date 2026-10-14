package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The app's own settings (the Mac and Windows apps alike), on the page at /settings.html: this
// computer's name, which tells its chat apart from the Car Thing's and other computers', and
// the VPN. Saving restarts the program, which the app then starts again, so a change takes
// effect the same way a fresh start would.

// appDir is the app's folder (config.json, tokens.json and the rest); signInNeeded is set while
// it has no sign-in yet, when the program serves only the sign-in page.
var (
	appDir       string
	signInNeeded bool
)

var app struct {
	mu       sync.Mutex
	h        *hermes
	vpnError string
}

type appSettingsView struct {
	Name      string `json:"name"`
	Device    string `json:"device"`
	Server    string `json:"server"`
	VPN       bool   `json:"vpn"`
	When      string `json:"when"`
	HasConfig bool   `json:"has_config"`
	Endpoint  string `json:"endpoint,omitempty"`
	Address   string `json:"address,omitempty"`
	Route     string `json:"route"` // "direct" or "vpn"
	VPNError  string `json:"vpn_error,omitempty"`
}

type appSettingsChange struct {
	Name      string  `json:"name"`
	VPN       bool    `json:"vpn"`
	When      string  `json:"when"`
	WireGuard *string `json:"wireguard,omitempty"` // a new config's text; absent keeps the old one
}

func (h *hermes) servesAppSettings() {
	app.mu.Lock()
	app.h = h
	app.mu.Unlock()
}

func appSettings(w http.ResponseWriter, r *http.Request) {
	app.mu.Lock()
	h := app.h
	app.mu.Unlock()
	if h == nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSONResponse(w, http.StatusOK, h.appSettingsView())
	case http.MethodPost:
		var c appSettingsChange
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&c); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "That wasn't a settings change."})
			return
		}
		if problem := h.saveAppSettings(c); problem != "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": problem})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]bool{"restarting": true})
		go func() {
			time.Sleep(300 * time.Millisecond)
			log.Printf("settings changed; restarting")
			os.Exit(0) // the app starts it again
		}()
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *hermes) appSettingsView() appSettingsView {
	var set vpnSettings
	_ = readJSON(filepath.Join(h.dir, "vpn.json"), &set)
	v := appSettingsView{Name: h.cfg.Name, Device: h.cfg.Device, Server: h.cfg.Server, VPN: set.On, When: set.When,
		Route: map[bool]string{true: "vpn", false: "direct"}[route.use.Load()]}
	if v.Name == "" {
		v.Name = titleFromDevice(h.cfg.Device)
	}
	if v.When != "always" {
		v.When = "away"
	}
	if raw, err := os.ReadFile(filepath.Join(h.dir, "wireguard.conf")); err == nil {
		v.HasConfig = true
		if cfg, err := parseWireGuard(raw); err == nil {
			v.Address = strings.Join(cfg.Addresses, ", ")
			if len(cfg.Peers) > 0 {
				v.Endpoint = cfg.Peers[0].Endpoint
			}
		}
	}
	app.mu.Lock()
	v.VPNError = app.vpnError
	app.mu.Unlock()
	return v
}

// saveAppSettings checks and saves a change, returning what's wrong with it if anything is.
func (h *hermes) saveAppSettings(c appSettingsChange) string {
	name := strings.TrimSpace(c.Name)
	device := deviceID(name)
	if device == "" {
		return "Give this computer a name, such as Office Mac."
	}
	if device == "car-thing" {
		return "Car Thing is the Car Thing's own name; pick another for this computer."
	}
	confPath := filepath.Join(h.dir, "wireguard.conf")
	if c.WireGuard != nil {
		cfg, err := parseWireGuard([]byte(*c.WireGuard))
		if err == nil {
			_, err = cfg.uapi()
		}
		if err != nil {
			return "That WireGuard config won't work: " + err.Error() + "."
		}
	} else if _, err := os.Stat(confPath); c.VPN && err != nil {
		return "Import a WireGuard config first."
	}
	when := c.When
	if when != "always" {
		when = "away"
	}
	if c.WireGuard != nil {
		if err := os.WriteFile(confPath+".tmp", []byte(*c.WireGuard), 0o600); err != nil {
			return "Couldn't save the config: " + err.Error()
		}
		if err := os.Rename(confPath+".tmp", confPath); err != nil {
			return "Couldn't save the config: " + err.Error()
		}
	}
	if err := writeJSON(filepath.Join(h.dir, "vpn.json"), vpnSettings{On: c.VPN, When: when}, 0o644); err != nil {
		return "Couldn't save the VPN setting: " + err.Error()
	}
	cfg := h.cfg
	cfg.Name, cfg.Device = name, device
	if err := writeJSON(filepath.Join(h.dir, "config.json"), cfg, 0o644); err != nil {
		return "Couldn't save the name: " + err.Error()
	}
	return ""
}

// deviceID turns a name such as "Kayla's MacBook" into the id the plugin keeps its chat under,
// "kaylas-macbook".
func deviceID(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		case r == '\'' || r == '’':
		default:
			dash = true
		}
	}
	id := b.String()
	if len(id) > 48 {
		id = strings.TrimRight(id[:48], "-")
	}
	return id
}

func titleFromDevice(device string) string {
	words := strings.FieldsFunc(device, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func writeJSONResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// appSignIn is the sign-in page's: GET fills it in, POST signs in and restarts the program.
func appSignIn(w http.ResponseWriter, r *http.Request) {
	if !virtual || appDir == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var cfg config
		_ = readJSON(filepath.Join(appDir, "config.json"), &cfg)
		if cfg.Server == "" {
			cfg.Server = "http://10.0.0.20:9119"
		}
		writeJSONResponse(w, http.StatusOK, map[string]any{"server": cfg.Server, "username": cfg.Username, "needed": signInNeeded})
	case http.MethodPost:
		var in struct{ Server, Username, Password string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Server) == "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "Give the Hermes address, your username and password."})
			return
		}
		if err := signIn(appDir, strings.TrimSpace(in.Server), strings.TrimSpace(in.Username), strings.NewReader(in.Password+"\n")); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]bool{"restarting": true})
		go func() {
			time.Sleep(300 * time.Millisecond)
			log.Printf("signed in; restarting")
			os.Exit(0) // the app starts it again, signed in
		}()
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
