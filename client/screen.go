package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// The Car Thing's screen. The image's kiosk browser shows a page this program serves on
// loopback (kiosk.go points it here), so the screen's look ships with petasos's own updates.
// The page holds no logic of its own beyond drawing: everything it shows is the state below,
// pushed to it as server-sent events whenever something changes.

const screenAddr = "127.0.0.1:8187"

//go:embed screen
var screenFiles embed.FS

// screenState is everything the page draws. Field names are the page's.
type screenState struct {
	Version  string      `json:"version"`            // petasos's version: a page from an older one reloads
	Phase    string      `json:"phase"`              // idle, ready, listening, thinking, speaking
	Heard    string      `json:"heard,omitempty"`    // what Hermes heard, or the preset's prompt
	Reply    string      `json:"reply,omitempty"`    // Athena's reply, while it plays
	ReplyMs  int         `json:"reply_ms,omitempty"` // how long the reply plays, for scrolling long ones
	Notice   string      `json:"notice,omitempty"`   // a passing message, such as "The mic is off."
	NoticeID int         `json:"notice_id,omitempty"`
	Volume   int         `json:"volume"`    // the speaker's level, 0 to 100, or -1 when unknown
	VolumeID int         `json:"volume_id"` // bumped on every change, so the page lights the meander
	Muted    bool        `json:"muted"`
	Presets  []string    `json:"presets"`
	Agent    string      `json:"agent"` // who the Car Thing talks to, from the plugin's agent_name
	Links    links       `json:"links"`
	BT       *btScreen   `json:"bt,omitempty"`       // the Bluetooth screens, when open
	Virtual  bool        `json:"virtual,omitempty"`  // the virtual Petasos on a Mac: the page draws its own controls
	PageMic  bool        `json:"page_mic,omitempty"` // the page itself records (pagemic.go)
	MicOpen  bool        `json:"mic_open,omitempty"` // ...and should be recording now
	MicID    int         `json:"mic_id,omitempty"`   // which recording, for its posts to /mic
	MicName  string      `json:"mic_name,omitempty"` // the microphone chosen in settings, by name
	OnTop    bool        `json:"on_top,omitempty"`   // the app keeps its window above others (the pin)
	Music    *nowPlaying `json:"music,omitempty"`    // what the phone plays through the Car Thing (music.go)
	Page     string      `json:"page,omitempty"`     // "music" while the music page is open (musicpage.go)
	Hold     *holdShown  `json:"hold,omitempty"`     // a control held down, and what letting go would do (gestures.go)
}

type links struct {
	Phone        bool   `json:"phone"`
	Home         bool   `json:"home"`
	Speaker      bool   `json:"speaker"`
	SpeakerName  string `json:"speaker_name,omitempty"` // the paired speaker, connected or not
	PhonePaired  bool   `json:"phone_paired"`
	SpeakerKnown bool   `json:"speaker_paired"`
	Checked      bool   `json:"checked"`          // false until the first round of checks, so nothing flashes red at boot
	Tunnel       bool   `json:"tunnel,omitempty"` // the Mac app is reaching Hermes through its VPN
}

type screen struct {
	mu     sync.Mutex
	state  screenState
	subs   map[chan []byte]struct{}
	inputs chan<- map[string]any // where the page's own controls go, on the virtual Petasos
}

var display = &screen{
	state: screenState{Phase: "idle", Volume: -1, Presets: []string{"", "", "", ""}, Agent: "Hermes"},
	subs:  map[chan []byte]struct{}{},
}

// update changes the state and sends it to every open page.
func (s *screen) update(change func(*screenState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.state)
	raw, err := json.Marshal(s.state)
	if err != nil {
		return
	}
	for ch := range s.subs {
		select {
		case ch <- raw:
		default: // a stalled page gets the next one
		}
	}
}

// send sends a message that isn't part of the state to every open page, such as the knob
// turning while the on-screen keyboard is up.
func (s *screen) send(message any) {
	raw, err := json.Marshal(message)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- raw:
		default:
		}
	}
}

// micLevel sends the microphone's level (0 to 1) to every open page, for the bottom band while
// listening. It isn't part of the state, so it stays out of what a page gets as it connects.
func (s *screen) micLevel(level float64) {
	raw := []byte(fmt.Sprintf(`{"mic":%.2f}`, level))
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		if len(ch) > 0 {
			continue // a page that's behind skips a level, keeping room for the state
		}
		select {
		case ch <- raw:
		default:
		}
	}
}

func (s *screen) snapshot() screenState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *screen) setPhase(phase string) {
	music.phaseChanged(phase)
	library.phaseChanged(phase)
	s.update(func(st *screenState) {
		st.Phase = phase
		if phase == "idle" || phase == "ready" || phase == "listening" {
			st.Heard, st.Reply, st.ReplyMs = "", "", 0
		}
	})
}

// agent is the agent's name, for messages on the screen.
func (s *screen) agent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Agent
}

func (s *screen) notice(text string) {
	s.update(func(st *screenState) { st.Notice = text; st.NoticeID++ })
}

func (s *screen) serve() {
	s.update(func(st *screenState) { st.Version = version })
	files, _ := fs.Sub(screenFiles, "screen")
	mux := http.NewServeMux()
	pages := http.FileServer(http.FS(files))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store") // a new petasos brings a new page
		if signInNeeded && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
			http.Redirect(w, r, "/signin.html", http.StatusFound)
			return
		}
		pages.ServeHTTP(w, r)
	})
	mux.HandleFunc("/events", s.events)
	mux.HandleFunc("/input", s.pagePress)
	mux.HandleFunc("/mic", pagePost)
	mux.HandleFunc("/settings/text", typedText)
	mux.HandleFunc("/app/window", appWindow)
	mux.HandleFunc("/app/settings", appSettings)
	mux.HandleFunc("/app/softkeys", appSoftKeys)
	mux.HandleFunc("/app/music", appMusic)
	mux.HandleFunc("/app/soundcloud", appSoundCloud)
	mux.HandleFunc("/app/signin", appSignIn)
	mux.HandleFunc("/music/browse", musicBrowse)
	mux.HandleFunc("/music/play", musicPlay)
	mux.HandleFunc("/music/control", musicControlPost)
	mux.HandleFunc("/music/page", musicPagePost)
	l, err := net.Listen("tcp", screenAddr)
	if err != nil {
		log.Printf("can't serve the screen: %v", err)
		return
	}
	log.Print(http.Serve(l, mux))
}

func (s *screen) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan []byte, 8)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	first, _ := json.Marshal(s.state)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	send := func(raw []byte) bool {
		if _, err := w.Write(append(append([]byte("data: "), raw...), '\n', '\n')); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send(first) {
		return
	}
	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case raw := <-ch:
			if !send(raw) {
				return
			}
		case <-keepAlive.C:
			if _, err := w.Write([]byte(": still here\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
