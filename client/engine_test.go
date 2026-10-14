package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVersionNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.10.0", "0.9.3", true}, {"0.9.3", "0.10.0", false}, {"0.10.0", "0.10.0", false},
		{"1.0", "0.99.9", true}, {"0.10.1", "0.10", true}, {"0.10.0", "dev", true},
	} {
		if got := versionNewer(c.a, c.b); got != c.want {
			t.Errorf("versionNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// The app's copy downloads a newer program beside its settings, and only a newer one.
func TestAppUpdatesBesideItsSettings(t *testing.T) {
	program := []byte("the new program")
	sum := sha256.Sum256(program)
	served, servedOS := "", runtime.GOOS
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("os") != runtime.GOOS || r.URL.Query().Get("arch") != runtime.GOARCH {
			http.Error(w, "wrong build", 400)
			return
		}
		switch r.URL.Path {
		case "/api/plugins/petasos/client":
			fmt.Fprintf(w, `{"version":%q,"os":%q,"sha256":%q,"size":%d}`, served, servedOS, hex.EncodeToString(sum[:]), len(program))
		case "/api/plugins/petasos/client/download":
			w.Write(program)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	oldV, oldDir, oldVersion, oldRestart := virtual, appDir, version, restartAs
	defer func() { virtual, appDir, version, restartAs = oldV, oldDir, oldVersion, oldRestart }()
	var restarted string
	virtual, appDir, version = true, dir, "0.10.0"
	restartAs = func(path string) error { restarted = path; return nil }
	h := &hermes{dir: dir, cfg: config{Server: srv.URL}, http: srv.Client()}

	for _, skip := range []struct{ version, os string }{{"0.9.9", runtime.GOOS}, {"0.11.0", ""}} {
		served, servedOS = skip.version, skip.os
		if err := h.update(); err != nil || restarted != "" {
			t.Fatalf("%v: updated (%q, %v)", skip, restarted, err)
		}
	}

	served, servedOS = "0.11.0", runtime.GOOS
	if err := h.update(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "engine", engineName("0.11.0"))
	if restarted != want {
		t.Fatalf("restarted as %q, want %q", restarted, want)
	}
	if b, _ := os.ReadFile(want); string(b) != string(program) {
		t.Fatalf("saved %q", b)
	}
	if got, ok := newerEngine(dir); !ok || got != want {
		t.Fatalf("newerEngine = %q, %v", got, ok)
	}
	// Once that copy is running it doesn't hand over to itself, and a newer app wins.
	version = "0.11.0"
	if _, ok := newerEngine(dir); ok {
		t.Fatal("handed over to itself")
	}
	version = "0.12.0"
	if _, ok := newerEngine(dir); ok {
		t.Fatal("a newer app handed over to an older download")
	}

	// The next update clears out the last one.
	version, restarted, served = "0.11.0", "", "0.12.1"
	if err := h.update(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("the old download is still there: %v", err)
	}
}
