package main

import (
	"embed"
	"encoding/json"
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
	Version  string    `json:"version"`            // petasos's version: a page from an older one reloads
	Phase    string    `json:"phase"`              // idle, ready, listening, thinking, speaking
	Heard    string    `json:"heard,omitempty"`    // what Hermes heard, or the preset's prompt
	Reply    string    `json:"reply,omitempty"`    // Athena's reply, while it plays
	ReplyMs  int       `json:"reply_ms,omitempty"` // how long the reply plays, for scrolling long ones
	Notice   string    `json:"notice,omitempty"`   // a passing message, such as "The mic is off."
	NoticeID int       `json:"notice_id,omitempty"`
	Volume   int       `json:"volume"`    // the speaker's level, 0 to 100, or -1 when unknown
	VolumeID int       `json:"volume_id"` // bumped on every change, so the page lights the meander
	Muted    bool      `json:"muted"`
	Presets  []string  `json:"presets"`
	Agent    string    `json:"agent"` // who the Car Thing talks to, from the plugin's agent_name
	Links    links     `json:"links"`
	BT       *btScreen `json:"bt,omitempty"` // the Bluetooth screens, when open
}

type links struct {
	Phone        bool   `json:"phone"`
	Home         bool   `json:"home"`
	Speaker      bool   `json:"speaker"`
	SpeakerName  string `json:"speaker_name,omitempty"` // the paired speaker, connected or not
	PhonePaired  bool   `json:"phone_paired"`
	SpeakerKnown bool   `json:"speaker_paired"`
	Checked      bool   `json:"checked"` // false until the first round of checks, so nothing flashes red at boot
}

type screen struct {
	mu    sync.Mutex
	state screenState
	subs  map[chan []byte]struct{}
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

func (s *screen) snapshot() screenState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *screen) setPhase(phase string) {
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
	mux.Handle("/", http.FileServer(http.FS(files)))
	mux.HandleFunc("/events", s.events)
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
