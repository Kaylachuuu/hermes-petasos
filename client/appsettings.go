package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The app's own settings (the Mac, Windows and Linux apps alike), on the page at /settings.html,
// in the Car Thing's tabs less what a computer does itself (Bluetooth, the screen's light). Here
// are those saved with a restart: this computer's name, which tells its chat apart from the Car
// Thing's and other computers', the microphone (mics.go), the wake word and the VPN. Saving
// restarts the program, which the app then starts again, so a change takes effect the same way a
// fresh start would. appvpn.go has the rest.

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
	Name      string    `json:"name"`
	Device    string    `json:"device"`
	Server    string    `json:"server"`
	Username  string    `json:"username"`
	VPN       bool      `json:"vpn"`
	When      string    `json:"when"`
	Config    string    `json:"config"` // the VPN configuration used
	Configs   []appVPN  `json:"configs"`
	Route     string    `json:"route"` // "direct" or "vpn"
	VPNError  string    `json:"vpn_error,omitempty"`
	Mic       string    `json:"mic"`      // "" for the computer's own choice
	MicName   string    `json:"mic_name"` // the chosen one's name
	Mics      []micInfo `json:"mics"`     // the microphones connected now
	MicsError string    `json:"mics_error,omitempty"`
	Versions  versions  `json:"versions"`
	Wake      bool      `json:"wake"` // listen for "Hey Athena"
}

type appSettingsChange struct {
	Name    string  `json:"name"`
	VPN     bool    `json:"vpn"`
	When    string  `json:"when"`
	Config  string  `json:"config"`        // which VPN configuration
	Mic     *string `json:"mic,omitempty"` // absent keeps the microphone as it is
	MicName string  `json:"mic_name,omitempty"`
	Wake    *bool   `json:"wake,omitempty"`
}

func (h *hermes) servesAppSettings() {
	app.mu.Lock()
	app.h = h
	app.mu.Unlock()
}

// appUpdate is the settings page's "Check for updates": {"current": true} when there was
// nothing newer. An update restarts petasos, so the page loses its answer and waits.
func appUpdate(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if r.Method != http.MethodPost || h == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	current, err := h.checkNow()
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"current": current, "version": version})
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
	set := computerVPN(h.dir)
	v := appSettingsView{Name: h.cfg.Name, Device: h.cfg.Device, Server: h.cfg.Server, Username: h.cfg.Username,
		VPN: set.On, When: set.When, Config: set.Config, Configs: appVPNs(h.dir),
		Route: map[bool]string{true: "vpn", false: "direct"}[route.use.Load()]}
	if v.Name == "" {
		v.Name = titleFromDevice(h.cfg.Device)
	}
	app.mu.Lock()
	v.VPNError = app.vpnError
	app.mu.Unlock()
	a := loadAudioSettings(h.dir)
	v.Mic, v.MicName, v.Wake = a.Mic, a.MicName, a.Wake
	mics, err := listMics()
	v.Mics = mics
	v.Versions = h.versions()
	if err != nil {
		v.Mics = []micInfo{}
		v.MicsError = err.Error()
	}
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
	if _, ok := findVPN(h.dir, c.Config); c.VPN && !ok {
		if len(liveVPNs(h.dir)) == 0 {
			return "Add a VPN configuration first."
		}
		return "Choose which VPN configuration to use."
	}
	when := c.When
	if when != "always" {
		when = "away"
	}
	if err := writeJSON(filepath.Join(h.dir, "vpn.json"), vpnSettings{On: c.VPN, When: when, Config: c.Config}, 0o644); err != nil {
		return "Couldn't save the VPN setting: " + err.Error()
	}
	if c.Mic != nil || c.Wake != nil {
		a := loadAudioSettings(h.dir)
		if c.Mic != nil {
			a.Mic, a.MicName = *c.Mic, strings.TrimSpace(c.MicName)
			if a.Mic == "" {
				a.MicName = ""
			}
		}
		if c.Wake != nil {
			a.Wake = *c.Wake
		}
		if err := writeJSON(filepath.Join(h.dir, "audio.json"), a, 0o644); err != nil {
			return "Couldn't save the microphone: " + err.Error()
		}
	}
	if err := h.carryPresets(h.cfg.Device, device, name); err != nil {
		log.Printf("renaming: %v", err) // the soft keys stay under the old name; the rename goes ahead
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

// windowSettings is window.json, set by the pin under the screen and read by the Mac and
// Windows apps themselves: they keep the window above other windows while on_top is set.
type windowSettings struct {
	OnTop    bool `json:"on_top"`
	TitleBar bool `json:"title_bar,omitempty"` // the window's own title bar, which the page otherwise stands in for
}

func loadWindowSettings(dir string) windowSettings {
	var w windowSettings
	_ = readJSON(filepath.Join(dir, "window.json"), &w)
	return w
}

// appWindow is /app/window: the pin under the screen, and the Display tab's title bar. A POST
// changes what it names; GET says what's set. It takes effect without a restart; the apps check
// window.json every half second.
func appWindow(w http.ResponseWriter, r *http.Request) {
	if appDir == "" || (r.Method != http.MethodPost && r.Method != http.MethodGet) {
		http.NotFound(w, r)
		return
	}
	set := loadWindowSettings(appDir)
	if r.Method == http.MethodGet {
		writeJSONResponse(w, http.StatusOK, set)
		return
	}
	var change struct {
		OnTop    *bool `json:"on_top"`
		TitleBar *bool `json:"title_bar"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&change); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "That wasn't a window setting."})
		return
	}
	if change.OnTop != nil {
		set.OnTop = *change.OnTop
	}
	if change.TitleBar != nil {
		set.TitleBar = *change.TitleBar
	}
	if err := writeJSON(filepath.Join(appDir, "window.json"), set, 0o644); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	display.update(func(st *screenState) { st.OnTop, st.TitleBar = set.OnTop, set.TitleBar })
	writeJSONResponse(w, http.StatusOK, set)
}

// spotifyRedirect is where Spotify sends the browser back to after you sign in, and what the
// Spotify app must list as its redirect URI. Spotify takes only 127.0.0.1 for this, not
// localhost.
const spotifyRedirect = "http://" + screenAddr + "/app/spotify"

// appSpotify signs in to your Spotify account from the app's Settings, as appSoundCloud does
// for SoundCloud: a POST with ?start opens Spotify's sign-in in your own browser, Spotify sends
// the browser back here with a code, which the plugin on Hermes turns into your tokens, and the
// browser tab says how it went. A POST with ?disconnect signs out. The Car Thing uses the
// sign-in to have the Spotify app on your phone play what you choose on its music page.
func appSpotify(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if h == nil || !virtual {
		http.NotFound(w, r)
		return
	}
	back := func(problem string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		title, text := "Signed in to Spotify", "You can close this tab and go back to Petasos."
		if problem != "" {
			title, text = "Couldn't sign in to Spotify", problem
		}
		fmt.Fprintf(w, soundCloudDonePage, html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && q.Has("disconnect"):
		if _, err := h.exchange("POST", "/api/plugins/petasos/music/spotify/disconnect", "application/json", []byte("{}"), h.http, 1<<16); err != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]string{"account": ""})
	case q.Get("error") != "":
		back("Spotify said: " + firstNonEmpty(q.Get("error_description"), q.Get("error")))
	case q.Get("code") != "":
		body, _ := json.Marshal(map[string]string{"code": q.Get("code"), "state": q.Get("state")})
		if _, err := h.exchange("POST", "/api/plugins/petasos/music/spotify/connect", "application/json", body, h.http, 1<<16); err != nil {
			back(friendlyHermes(err))
			return
		}
		back("")
	case r.Method == http.MethodPost && q.Has("start"):
		to, err := spotifySignInPage(h)
		if err == nil {
			err = openInBrowser(to)
		}
		if err != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]bool{"opened": true})
	default:
		to, err := spotifySignInPage(h)
		if err != nil {
			back(friendlyHermes(err))
			return
		}
		http.Redirect(w, r, to, http.StatusFound)
	}
}

// spotifySignInPage asks the plugin for Spotify's sign-in page, with a new PKCE verifier kept
// on Hermes.
func spotifySignInPage(h *hermes) (string, error) {
	body, _ := json.Marshal(map[string]string{"redirect_uri": spotifyRedirect})
	data, err := h.exchange("POST", "/api/plugins/petasos/music/spotify/authorize", "application/json", body, h.http, 1<<16)
	var out struct {
		URL string `json:"url"`
	}
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	if err != nil || !strings.HasPrefix(out.URL, "https://") {
		return "", firstErr(err, fmt.Errorf("no sign-in page"))
	}
	return out.URL, nil
}
