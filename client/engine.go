package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// The app's copy of this program updates itself from the plugin as the Car Thing's does, but
// the copy inside the app can't be replaced (on a Mac it is signed as part of Petasos.app), so
// updates go in DIR/engine/, beside the app's settings, and the newer of the two runs: the
// app's own copy hands over to a newer download as it starts, and a newer app (after a
// rebuild) simply wins.

// engineName is the file an update to version v is saved as. Each version gets its own name,
// since Windows won't replace a program that is running.
func engineName(v string) string {
	name := "petasos-" + v
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// newerEngine is the downloaded copy in dir/engine, when it is newer than this one.
func newerEngine(dir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "engine", "VERSION"))
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(b))
	if !versionNewer(v, version) {
		return "", false
	}
	path := filepath.Join(dir, "engine", engineName(v))
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

// handOver runs the downloaded copy in this one's place when it is newer. On Windows the app
// itself starts the newer copy (app_windows.go), since a process there can't be replaced.
func handOver(dir string) {
	path, ok := newerEngine(dir)
	if !ok || runtime.GOOS == "windows" {
		return
	}
	log.Printf("petasos %s handing over to the updated copy, %s", version, path)
	if err := execReplace(path, os.Args); err != nil {
		log.Printf("can't run the updated copy: %v", err)
	}
}

// installEngine records the update just saved as dir/engine/engineName(v) as the one to run,
// and clears out older downloads.
func installEngine(dir, v string) error {
	engines := filepath.Join(dir, "engine")
	staged := filepath.Join(engines, "VERSION.new")
	if err := os.WriteFile(staged, []byte(v+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(staged, filepath.Join(engines, "VERSION")); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(engines, "petasos-*"))
	for _, f := range old {
		if filepath.Base(f) != engineName(v) {
			os.Remove(f) // the one running now stays until next time on Windows
		}
	}
	return nil
}

// versionNewer says whether version a is later than b, comparing them number by number
// ("0.10.0" is later than "0.9.3"). A part that isn't a number counts as 0.
func versionNewer(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}
