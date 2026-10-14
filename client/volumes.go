package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Each speaker comes back at full volume whenever it connects (after a power cycle, say), so the
// level last set for it is kept in volume.json, by its BlueALSA mixer control, and set again
// when it shows up.
type volumeMemory struct {
	mu     sync.Mutex
	file   string
	levels map[string]int
}

var volumes = &volumeMemory{levels: map[string]int{}}

func (m *volumeMemory) load(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.file = filepath.Join(dir, "volume.json")
	if data, err := os.ReadFile(m.file); err == nil {
		_ = json.Unmarshal(data, &m.levels)
	}
}

func (m *volumeMemory) remember(control string, level int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.levels[control] == level || m.file == "" {
		return
	}
	m.levels[control] = level
	if err := writeJSON(m.file, m.levels, 0o644); err != nil {
		log.Printf("can't save the volume: %v", err)
	}
}

func (m *volumeMemory) level(control string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.levels[control]
	return n, ok
}

// keepVolumes puts each speaker back to its remembered level when it connects.
func (v *voice) keepVolumes() {
	present := map[string]bool{}
	for ; ; time.Sleep(3 * time.Second) {
		out, err := exec.Command("amixer", "-D", v.speaker, "scontrols").Output()
		if err != nil {
			out = nil // no speaker connected: they all count as new when they come back
		}
		now := map[string]bool{}
		for _, m := range a2dpControl.FindAllSubmatch(out, -1) {
			control := string(m[1])
			now[control] = true
			if present[control] {
				continue
			}
			if n, ok := volumes.level(control); ok {
				out, err := exec.Command("amixer", "-D", v.speaker, "sset", control, fmt.Sprintf("%d%%", n)).CombinedOutput()
				if err != nil {
					log.Printf("can't restore the volume of %s: %v %s", control, err, strings.TrimSpace(string(out)))
				} else {
					log.Printf("%s back to %d%%", control, n)
					display.update(func(st *screenState) { st.Volume = n })
				}
			}
		}
		present = now
	}
}
