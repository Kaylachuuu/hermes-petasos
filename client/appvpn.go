package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The app's Network and Display tabs (settings.html), beyond what appSettings saves with a
// restart: the VPN configurations, which a computer shares with the Car Thing and Hermes
// (vpn.go); this computer's own sound bands, screensaver and buffering, which take effect at
// once; and logging out.

// computerVPN is vpn.json: whether this computer uses a VPN, which, and when. One turned on
// before there could be several uses the installer's config, "Home".
func computerVPN(dir string) vpnSettings {
	var set vpnSettings
	_ = readJSON(filepath.Join(dir, "vpn.json"), &set)
	if set.When != "always" {
		set.When = "away"
	}
	if set.On && set.Config == "" {
		if _, ok := findVPN(dir, "home"); ok {
			set.Config = "home"
		}
	}
	return set
}

// appVPN is one configuration as the settings page shows it: never with its private key.
type appVPN struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Endpoint  string `json:"endpoint"`
	PublicKey string `json:"public_key"`
	Conf      string `json:"conf"`
	Used      bool   `json:"used"`
}

var privateKeyLine = regexp.MustCompile(`(?mi)^\s*PrivateKey\s*=.*$\n?`)

func appVPNs(dir string) []appVPN {
	set := computerVPN(dir)
	out := []appVPN{}
	for _, c := range liveVPNs(dir) {
		v := appVPN{ID: c.ID, Name: c.Name, Endpoint: endpointOf(c.Conf), Conf: privateKeyLine.ReplaceAllString(c.Conf, ""),
			Used: set.On && set.Config == c.ID}
		if cfg, err := parseWireGuard([]byte(c.Conf)); err == nil {
			v.PublicKey, _ = publicKey(cfg.PrivateKey)
		}
		out = append(out, v)
	}
	return out
}

// saveAppVPN checks and keeps a configuration typed, pasted or imported on the settings page.
// One without a private key keeps the one it had, or, new, gets a new one, whose public key the
// page then shows for the server.
func saveAppVPN(dir, id, name, conf string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("Give it a name.")
	}
	if len(name) > 40 || len(conf) > 8<<10 {
		return "", errors.New("That's too long.")
	}
	if !privateKeyLine.MatchString(conf) {
		key := ""
		if old, ok := findVPN(dir, id); ok && id != "" {
			if cfg, err := parseWireGuard([]byte(old.Conf)); err == nil {
				key = cfg.PrivateKey
			}
		}
		if key == "" {
			var err error
			if key, err = newWireGuardKey(); err != nil {
				return "", err
			}
		}
		conf = interfaceLine.ReplaceAllString(conf, "${1}\nPrivateKey = "+key)
	}
	cfg, err := parseWireGuard([]byte(conf))
	if err == nil {
		err = cfg.checkKeys()
	}
	if err == nil {
		_, err = cfg.uapi()
	}
	if err != nil {
		return "", errors.New("That configuration won't work: " + err.Error() + ".")
	}
	if id == "" {
		id = newVPNID(dir, name)
	} else if _, ok := findVPN(dir, id); !ok {
		return "", errors.New("That configuration was deleted meanwhile.")
	}
	return id, storeVPN(dir, vpnConfig{ID: id, Name: name, Conf: cfg.quick()})
}

var interfaceLine = regexp.MustCompile(`(?mi)^(\s*\[Interface\]\s*)$`)

// appVPNsHandler is /app/vpns: GET lists the configurations, POST adds or changes one
// ({id, name, conf}; no id adds), DELETE ?id= deletes one. Changing the one this computer uses
// restarts the program, so its tunnel uses the change.
func appVPNsHandler(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if h == nil || !virtual {
		http.NotFound(w, r)
		return
	}
	restart := false
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var in struct{ ID, Name, Conf string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "That wasn't a VPN configuration."})
			return
		}
		id, err := saveAppVPN(h.dir, in.ID, in.Name, in.Conf)
		if err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		set := computerVPN(h.dir)
		restart = set.On && set.Config == id
	case http.MethodDelete:
		if err := deleteVPN(h.dir, r.URL.Query().Get("id")); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"configs": appVPNs(h.dir), "restarting": restart})
	if restart {
		go func() {
			time.Sleep(300 * time.Millisecond)
			log.Printf("the VPN configuration in use changed; restarting")
			os.Exit(0) // the app starts it again
		}()
	}
}

// appPrefs is the computer's own settings that take effect at once, as the Car Thing's do.
type appPrefs struct {
	Bands      int    `json:"bands"`
	Rise       string `json:"rise"`
	Fall       string `json:"fall"`
	Peaks      bool   `json:"peaks"`
	Saver      string `json:"saver"`
	SaverAfter int    `json:"saver_after"`
	Buffer     int    `json:"buffer"`
}

// appPrefsHandler is /app/prefs: GET and PUT the settings above; POST ?preview shows the
// screensaver until the next key or click, ?wake counts as activity (the mouse moving over the
// window), ?logout forgets the sign-in and starts again, at the sign-in page.
func appPrefsHandler(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if h == nil || !virtual {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && q.Has("preview"):
		backlight.trySaver()
	case r.Method == http.MethodPost && q.Has("wake"):
		backlight.wake()
	case r.Method == http.MethodPost && q.Has("logout"):
		if err := os.Remove(filepath.Join(h.dir, "tokens.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]bool{"restarting": true})
		go func() {
			time.Sleep(300 * time.Millisecond)
			log.Printf("logged out; restarting")
			os.Exit(0) // the app starts it again, at its sign-in page
		}()
		return
	case r.Method == http.MethodPut:
		var p appPrefs
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&p); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "That wasn't a settings change."})
			return
		}
		if err := setAppPrefs(h.dir, p); err != nil {
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "Couldn't save that: " + err.Error()})
			return
		}
	case r.Method != http.MethodGet:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSONResponse(w, http.StatusOK, getAppPrefs(h.dir))
}

func getAppPrefs(dir string) appPrefs {
	m, s := meterSetting(dir), backlight.settings()
	return appPrefs{Bands: m.Bands, Rise: m.Rise, Fall: m.Fall, Peaks: m.Peaks, Saver: s.Saver, SaverAfter: s.SaverAfter,
		Buffer: bufferSetting(dir)}
}

func setAppPrefs(dir string, p appPrefs) error {
	m := meterSetting(dir)
	if slices.Contains(meterBandChoices, p.Bands) {
		m.Bands = p.Bands
	}
	if speedIndex(p.Rise) >= 0 {
		m.Rise = p.Rise
	}
	if speedIndex(p.Fall) >= 0 {
		m.Fall = p.Fall
	}
	m.Peaks = p.Peaks
	if err := setMeterTo(dir, m); err != nil {
		return err
	}
	s := backlight.settings()
	s.Saver, s.SaverAfter = p.Saver, p.SaverAfter
	backlight.change(s)
	return setBufferTo(dir, p.Buffer)
}
