package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScreenStreamsState(t *testing.T) {
	s := &screen{state: screenState{Phase: "idle", Presets: []string{"", "", "", ""}}, subs: map[chan []byte]struct{}{}}
	srv := httptest.NewServer(http.HandlerFunc(s.events))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	next := func() screenState {
		for lines.Scan() {
			if data, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				var st screenState
				if err := json.Unmarshal([]byte(data), &st); err != nil {
					t.Fatal(err)
				}
				return st
			}
		}
		t.Fatal("stream ended")
		return screenState{}
	}
	if st := next(); st.Phase != "idle" {
		t.Fatalf("first state: %+v", st)
	}
	s.setPhase("listening")
	if st := next(); st.Phase != "listening" {
		t.Fatalf("after setPhase: %+v", st)
	}
}

func TestScreenPageIsEmbedded(t *testing.T) {
	for _, name := range []string{"screen/index.html", "screen/fonts/caladea.woff2", "screen/fonts/source-sans-3.woff2"} {
		if _, err := screenFiles.ReadFile(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFrames(t *testing.T) {
	for _, n := range []int{5, 300, 70000} {
		payload := []byte(strings.Repeat("x", n))
		frame := wsFrame(payload)
		// Unmask as a server would, then read it back with the client's reader.
		head := 2
		switch {
		case n >= 1<<16:
			head += 8
		case n >= 126:
			head += 2
		}
		mask := frame[head : head+4]
		unmasked := append([]byte{frame[0], frame[1] &^ 0x80}, frame[2:head]...)
		for i, b := range frame[head+4:] {
			unmasked = append(unmasked, b^mask[i%4])
		}
		got, err := wsRead(bufio.NewReader(strings.NewReader(string(unmasked))))
		if err != nil || string(got) != string(payload) {
			t.Fatalf("%d bytes: err %v, got %d bytes", n, err, len(got))
		}
	}
}
