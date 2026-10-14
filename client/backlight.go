package main

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The screen's light: how bright it is, when it dims and when it turns off. The settings are
// changed on the Car Thing itself (the Screen settings, settings.go) and kept in screen.json
// next to config.json.
//
// Auto brightness follows the ambient light sensor the way the BSP's superbird-als daemon does:
// the median of the sensor's raw clear count, on a log curve, eased toward a little each tick.
// Any button or knob, and anything happening on the screen (a voice turn, a notice, the volume,
// the settings), counts as activity and brings the light back.

const (
	backlightDir = "/sys/class/backlight/backlight"
	alsDir       = "/sys/bus/iio/devices/iio:device0"

	lightTick  = 200 * time.Millisecond
	lightFloor = 16 // the lowest backlight step that still lights the panel (superbird-als's minimum)
)

// lightSettings is screen.json. Brightness 0 means auto; seconds of 0 mean never.
type lightSettings struct {
	Brightness int `json:"brightness"` // percent, or 0 for auto
	DimAfter   int `json:"dim_after"`  // seconds without activity before dimming
	DimTo      int `json:"dim_to"`     // percent of the normal brightness while dimmed
	OffAfter   int `json:"off_after"`  // seconds without activity before the screen turns off
}

var defaultLight = lightSettings{Brightness: 0, DimAfter: 60, DimTo: 30, OffAfter: 0}

func (s lightSettings) valid() lightSettings {
	clamp := func(v, lo, hi int) int { return max(lo, min(hi, v)) }
	s.Brightness = clamp(s.Brightness, 0, 100)
	s.DimTo = clamp(s.DimTo, 5, 100)
	// Times are kept to the choices the Screen tab offers, so it always shows the one in use.
	s.DimAfter = dimAfterChoices[nearest(dimAfterChoices, s.DimAfter)].value
	s.OffAfter = offAfterChoices[nearest(offAfterChoices, s.OffAfter)].value
	return s
}

type light struct {
	mu       sync.Mutex
	file     string
	set      lightSettings
	active   time.Time // the last activity
	preview  int       // a dim level shown while it is being chosen, or 0
	sensor   bool      // the light sensor answered
	dark     bool      // the screen is off
	level    int       // the brightness now, in percent of the steps that light the panel
	swallow  map[string]bool
	lastSeen [2]int // the screen's notice and volume ids, to notice them changing
}

var backlight = &light{set: defaultLight, active: time.Now(), swallow: map[string]bool{}}

// start loads screen.json and runs the light until the program ends. Without a backlight (a
// test machine) it only keeps the settings.
func (l *light) start(dir string) {
	l.mu.Lock()
	l.file = filepath.Join(dir, "screen.json")
	if raw, err := os.ReadFile(l.file); err == nil {
		s := defaultLight
		if json.Unmarshal(raw, &s) == nil {
			l.set = s.valid()
		}
	}
	l.mu.Unlock()
	maxLevel := readInt(backlightDir + "/max_brightness")
	if maxLevel <= 0 {
		log.Printf("no backlight to control")
		return
	}
	// The BSP's own light daemon would fight over the backlight.
	_ = exec.Command("systemctl", "stop", "superbird-als").Run()
	l.mu.Lock()
	l.sensor = startSensor()
	l.mu.Unlock()
	if !l.sensor {
		log.Printf("no ambient light sensor; auto brightness stays at full")
	}
	current := readInt(backlightDir + "/actual_brightness")
	if current < 0 || current > maxLevel {
		current = maxLevel
	}
	var samples []int
	for ; ; time.Sleep(lightTick) {
		if raw := readInt(alsDir + "/in_intensity0_raw"); raw >= 0 {
			samples = append(samples, raw)
			if len(samples) > 11 {
				samples = samples[1:]
			}
		}
		target := l.target(maxLevel, median(samples))
		next := current
		if target == 0 || current == 0 {
			next = target // off and on are immediate
		} else if target != current {
			diff := target - current
			step := max(1, int(math.Round(math.Abs(float64(diff))*0.15)))
			if diff > 0 {
				next = current + min(step, diff)
			} else {
				next = current - min(step, -diff)
			}
		}
		if next != current && writeInt(backlightDir+"/brightness", next) == nil {
			current = next
		}
		if current > 0 {
			l.mu.Lock()
			l.level = max(1, (current-lightFloor)*100/(maxLevel-lightFloor))
			l.mu.Unlock()
		}
	}
}

// target is the backlight step the screen should be at now.
func (l *light) target(maxLevel, ambient int) int {
	st := display.snapshot()
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if st.Phase != "idle" || st.BT != nil || st.NoticeID != l.lastSeen[0] || st.VolumeID != l.lastSeen[1] {
		l.active = now
		l.lastSeen = [2]int{st.NoticeID, st.VolumeID}
	}
	idle := now.Sub(l.active)
	s := l.set
	if s.OffAfter > 0 && idle >= time.Duration(s.OffAfter)*time.Second {
		l.dark = true
		return 0
	}
	l.dark = false
	level := maxLevel
	switch {
	case s.Brightness > 0:
		level = percentLevel(s.Brightness, maxLevel)
	case l.sensor && ambient >= 0:
		level = ambientLevel(ambient, maxLevel)
	}
	dimTo := 0
	if l.preview > 0 {
		dimTo = l.preview
	} else if s.DimAfter > 0 && idle >= time.Duration(s.DimAfter)*time.Second {
		dimTo = s.DimTo
	}
	if dimTo > 0 {
		level = lightFloor + (level-lightFloor)*dimTo/100
	}
	return max(lightFloor, min(maxLevel, level))
}

// percentLevel spreads 1–100% over the steps that light the panel.
func percentLevel(pct, maxLevel int) int {
	return lightFloor + (maxLevel-lightFloor)*pct/100
}

// ambientLevel is superbird-als's curve: dark rooms (a raw count of 3 or less) get the floor,
// direct daylight (about 1500) full brightness, on a log scale between.
func ambientLevel(raw, maxLevel int) int {
	if raw <= 3 {
		return lightFloor
	}
	ratio := math.Log10(1+float64(raw)) / math.Log10(1+1500.0)
	ratio = max(0, min(1, ratio))
	return lightFloor + int(float64(maxLevel-lightFloor)*ratio+0.5)
}

// startSensor puts the TMD2772 where room light reads tens of counts and daylight thousands
// (its driver starts at its least sensitive), and reports whether it is there.
func startSensor() bool {
	if _, err := os.Stat(alsDir + "/in_intensity0_raw"); err != nil {
		return false
	}
	_ = os.WriteFile(alsDir+"/in_intensity0_integration_time", []byte("0.100000\n"), 0)
	_ = os.WriteFile(alsDir+"/in_intensity0_calibscale", []byte("16\n"), 0)
	return true
}

// median ignores the driver's occasional zero reads and single spikes; -1 when there are no
// readings.
func median(samples []int) int {
	if len(samples) == 0 {
		return -1
	}
	s := append([]int(nil), samples...)
	sort.Ints(s)
	return s[len(s)/2]
}

// touched is called for every button and knob event. It reports whether the event only woke
// the screen and should go no further: a preset pressed while the screen is off, since its
// label can't be seen. Its release is kept back too.
func (l *light) touched(ev map[string]any) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active = time.Now()
	name, _ := ev["input"].(string)
	action, _ := ev["action"].(string)
	if !strings.HasPrefix(name, "preset-") {
		return false
	}
	switch action {
	case "press":
		if l.dark {
			l.dark = false
			l.swallow[name] = true
			return true
		}
	case "release":
		if l.swallow[name] {
			delete(l.swallow, name)
			return true
		}
	}
	return false
}

func (l *light) settings() lightSettings {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.set
}

// percent is the screen's brightness now, for showing auto brightness; 100 until known.
func (l *light) percent() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.level == 0 {
		return 100
	}
	return l.level
}

func (l *light) hasSensor() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sensor
}

// change applies new settings at once and saves them.
func (l *light) change(s lightSettings) {
	s = s.valid()
	l.mu.Lock()
	l.set = s
	l.active = time.Now()
	file := l.file
	l.mu.Unlock()
	if file == "" {
		return
	}
	raw, _ := json.MarshalIndent(s, "", "  ")
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err == nil {
		err = os.Rename(tmp, file)
		if err != nil {
			log.Printf("saving the screen settings: %v", err)
		}
	} else {
		log.Printf("saving the screen settings: %v", err)
	}
}

// showDim previews a dim level (a percent) while it is chosen; 0 ends the preview.
func (l *light) showDim(pct int) {
	l.mu.Lock()
	l.preview = pct
	l.mu.Unlock()
}

func readInt(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return -1
	}
	return n
}

func writeInt(path string, v int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(v)+"\n"), 0)
}
