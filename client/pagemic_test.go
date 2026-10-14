package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The Mac app's window records: each turn opens the page's microphone, the page posts its
// audio at its own rate, and a conversation goes round more than once.
func TestPageMicConversation(t *testing.T) {
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
	dir := t.TempDir()
	helper := dir + "/audio"
	writeFile(t, helper, "#!/bin/sh\ncat >/dev/null; sleep 0.1\n")
	oldV, oldA, oldOn, oldDir := virtual, audioHelper, pageMicOn, appDir
	virtual, audioHelper, appDir = true, helper, dir
	pageMicOn = func() bool { return true }
	defer func() { virtual, audioHelper, pageMicOn, appDir = oldV, oldA, oldOn, oldDir }()

	// The page: whenever the state opens the microphone, post 0.4 s of quiet, 0.5 s of speech
	// and then quiet, at 48 kHz, 100 ms at a time, until told the recording is over.
	page := httptest.NewServer(http.HandlerFunc(pagePost))
	defer page.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		seen := 0
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			st := display.snapshot()
			if !st.MicOpen || st.MicID == seen {
				continue
			}
			seen = st.MicID
			for chunk := 0; chunk < 100; chunk++ {
				var b bytes.Buffer
				for i := 0; i < 4800; i++ {
					v := int16(0)
					if chunk >= 4 && chunk < 9 {
						v = 8000
						if i%2 == 1 {
							v = -8000
						}
					}
					binary.Write(&b, binary.LittleEndian, v)
				}
				resp, err := http.Post(fmt.Sprintf("%s/mic?id=%d&rate=48000", page.URL, seen), "application/octet-stream", &b)
				if err != nil {
					return
				}
				resp.Body.Close()
				if resp.StatusCode == http.StatusGone {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	h := &hermes{dir: dir, cfg: config{Server: srv.URL, Device: "test"}, http: srv.Client()}
	v := newVoice(h, make(chan map[string]any, 8))
	v.carryOut(map[string]any{"do": "listen", "mode": "conversation", "end_of_speech_ms": 300, "idle_end_s": 30})
	deadline := time.Now().Add(15 * time.Second)
	for asks.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	v.carryOut(map[string]any{"do": "cancel"})
	if n := asks.Load(); n < 2 {
		t.Fatalf("only %d rounds of the conversation", n)
	}
}

// The page's samples at 48 kHz come out as a third as many 32-bit samples, averaged.
func TestPageMicConverts(t *testing.T) {
	s := &pageMicSource{}
	out := s.convert([]int16{3, 6, 9, 30, 30, 30, 1}, 48000)
	if len(out) != 8 {
		t.Fatalf("%d bytes", len(out))
	}
	if a, b := int32(binary.LittleEndian.Uint32(out))>>16, int32(binary.LittleEndian.Uint32(out[4:]))>>16; a != 6 || b != 30 {
		t.Fatalf("got %d, %d", a, b)
	}
	if len(s.keep) != 1 {
		t.Fatalf("kept %v", s.keep)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}
