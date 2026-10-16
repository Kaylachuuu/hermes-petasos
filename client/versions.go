package main

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// versions are the three that make up a Petasos: the system image flashed onto the Car Thing,
// this program (which updates itself from the plugin) and the plugin running on Hermes.
type versions struct {
	Image   string `json:"image,omitempty"`  // "0.2.0, built 2026-10-15"; empty in the apps
	Program string `json:"program"`          // this program
	Latest  string `json:"latest,omitempty"` // the program the plugin has, when it could be asked
	Plugin  string `json:"plugin,omitempty"` // the plugin, when it could be asked and says
}

// osRelease is where the image keeps its version (VERSION_ID) and build day (BUILD_ID).
var osRelease = "/etc/os-release"

// hermesWait is how long a settings tab waits on Hermes before showing what it has.
var hermesWait = 3 * time.Second

// localVersions are the versions known without asking Hermes.
func (h *hermes) localVersions() versions {
	v := versions{Program: version}
	if !virtual {
		v.Image = imageVersion(osRelease)
	}
	return v
}

func (h *hermes) versions() versions {
	v := h.localVersions()
	var latest clientInfo
	if h.call("GET", "/api/plugins/petasos/client?os="+runtime.GOOS+"&arch="+runtime.GOARCH, nil, &latest) == nil {
		v.Latest, v.Plugin = latest.Version, latest.Plugin
	}
	return v
}

func imageVersion(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, val, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			fields[k] = strings.Trim(val, `"'`)
		}
	}
	out := fields["VERSION_ID"]
	if b := fields["BUILD_ID"]; len(b) == 8 && out != "" {
		out += ", built " + b[:4] + "-" + b[4:6] + "-" + b[6:]
	}
	return out
}
