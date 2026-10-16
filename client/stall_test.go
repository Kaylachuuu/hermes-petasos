package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStalledDownloadGivesUp(t *testing.T) {
	old := updateStall
	updateStall = 200 * time.Millisecond
	defer func() { updateStall = old }()
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 3; i++ { // data keeps coming for longer than updateStall
			w.Write([]byte("chunk"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
		select { // then stops
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall := time.AfterFunc(updateStall, cancel)
	defer stall.Stop()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	got, err := io.ReadAll(&stallReader{resp.Body, stall})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("read ended with %v, ctx %v", err, ctx.Err())
	}
	if string(got) != "chunkchunkchunk" {
		t.Errorf("got %q before the stall", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v to give up", d)
	}
}
