package main

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConversationKeepsGoing(t *testing.T) {
	dir := t.TempDir()
	loud := make([]byte, 16000/2*4)
	for i := 0; i < len(loud)/4; i++ {
		s := int32(8000 << 16)
		if i%2 == 1 {
			s = -s
		}
		binary.LittleEndian.PutUint32(loud[4*i:], uint32(s))
	}
	// A moment of quiet, as when the mic opens, then half a second of speech.
	os.WriteFile(filepath.Join(dir, "loud.raw"), append(make([]byte, 16000*4*4/10), loud...), 0o644)
	helper := filepath.Join(dir, "audio")
	os.WriteFile(helper, []byte("#!/bin/sh\ncase \"$1\" in\n--record) cat "+dir+"/loud.raw; exec cat /dev/zero ;;\n--play) cat >/dev/null; sleep 0.2 ;;\nesac\n"), 0o755)

	var asks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/plugins/petasos/voice/audio/"):
			w.Write(wavFile(make([]byte, 4800), 24000))
		case r.URL.Path == "/api/plugins/petasos/voice":
			asks.Add(1)
			w.Write([]byte(`{"heard":"hi","reply":"hello","audio":"a1"}`))
		}
	}))
	defer srv.Close()

	oldV, oldA := virtual, audioHelper
	virtual, audioHelper = true, helper
	defer func() { virtual, audioHelper = oldV, oldA }()
	defer recordWithHelper()()
	h := &hermes{dir: dir, cfg: config{Server: srv.URL, Device: "test"}, http: srv.Client()}
	v := newVoice(h, make(chan map[string]any, 8))
	v.carryOut(map[string]any{"do": "listen", "mode": "conversation", "end_of_speech_ms": 300, "idle_end_s": 30})
	deadline := time.Now().Add(10 * time.Second)
	for asks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	v.carryOut(map[string]any{"do": "cancel"})
	if n := asks.Load(); n < 3 {
		t.Fatalf("only %d rounds of the conversation", n)
	}
}

// A recorder that gives up before any audio leaves the screen idle with a notice, rather than
// "Getting ready…" for good.
func TestMicThatFailsIsReported(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "audio")
	os.WriteFile(helper, []byte("#!/bin/sh\necho 'no microphone to record from' >&2\nexit 1\n"), 0o755)
	oldV, oldA := virtual, audioHelper
	virtual, audioHelper = true, helper
	defer func() { virtual, audioHelper = oldV, oldA }()
	defer recordWithHelper()()
	h := &hermes{dir: dir, cfg: config{Device: "test"}, http: http.DefaultClient}
	v := newVoice(h, make(chan map[string]any, 8))
	display.mu.Lock()
	before := display.state.NoticeID
	display.mu.Unlock()
	v.carryOut(map[string]any{"do": "listen", "mode": "hold"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		display.mu.Lock()
		phase, notices := display.state.Phase, display.state.NoticeID
		display.mu.Unlock()
		if phase == "idle" && notices > before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("still waiting on a microphone that gave up")
}

// A recorder that starts but never gives audio is stopped and reported too.
func TestMicThatHangsIsReported(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "audio")
	os.WriteFile(helper, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755)
	oldV, oldA, oldT := virtual, audioHelper, micTimeout
	virtual, audioHelper, micTimeout = true, helper, 300*time.Millisecond
	defer func() { virtual, audioHelper, micTimeout = oldV, oldA, oldT }()
	defer recordWithHelper()()
	h := &hermes{dir: dir, cfg: config{Device: "test"}, http: http.DefaultClient}
	v := newVoice(h, make(chan map[string]any, 8))
	display.mu.Lock()
	before := display.state.NoticeID
	display.mu.Unlock()
	v.carryOut(map[string]any{"do": "listen", "mode": "hold"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		display.mu.Lock()
		phase, notices := display.state.Phase, display.state.NoticeID
		display.mu.Unlock()
		if phase == "idle" && notices > before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("still waiting on a microphone that never started")
}

// recordWithHelper has the app record with its helper program rather than its page, as the
// Windows app does; it returns the undo.
func recordWithHelper() func() {
	old := pageMicOn
	pageMicOn = func() bool { return false }
	return func() { pageMicOn = old }
}
