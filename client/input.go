package main

import (
	"encoding/binary"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The Car Thing's controls, as confirmed on hardware (README, step 2): every button is on
// gpio-keys-polled, the knob's turns on rotary.
const (
	buttonsDevice = "gpio-keys-polled"
	knobDevice    = "rotary"

	evKey    = 1
	evRel    = 2
	evAbs    = 3
	relHWhee = 6

	eviocgrab = 0x40044590 // _IOW('E', 0x90, int)
)

var keyNames = map[uint16]string{
	2: "preset-1", 3: "preset-2", 4: "preset-3", 5: "preset-4", // KEY_1..KEY_4
	50: "mute",  // KEY_M, the top-right button
	1:  "front", // KEY_ESC, the front button
	28: "knob",  // KEY_ENTER, pressing the knob

	// A car's steering wheel media buttons, as BlueZ passes them on from AVRCP.
	164: "car-play", 200: "car-play", 201: "car-play", // KEY_PLAYPAUSE, KEY_PLAYCD, KEY_PAUSECD
	163: "car-next", 165: "car-previous", // KEY_NEXTSONG, KEY_PREVIOUSSONG
	// Holding skip back or ahead, as a Ford SYNC sends it: rewind and fast forward.
	168: "car-rewind", 208: "car-forward", // KEY_REWIND, KEY_FASTFORWARD
}

// carSeekAt is when a car's skip button was last held (Unix ms). A car sends play as it is let
// go, to carry on from where the seek took it, which carPlayer then leaves alone.
var carSeekAt atomic.Int64

// watchInputs finds the button and knob devices by name and reports their events, reopening
// them if they go away. With grab, Nocturne's own screen stops seeing these presses.
func watchInputs(grab bool, out chan<- map[string]any) {
	for _, name := range []string{buttonsDevice, knobDevice} {
		go func(name string) {
			for {
				path := findInput(name)
				if path == "" {
					log.Printf("no input device named %q; looking again in 10s", name)
					time.Sleep(10 * time.Second)
					continue
				}
				readEventFile(path, name, out, grab)
				time.Sleep(2 * time.Second)
			}
		}(name)
	}
}

// watchCarButtons reads the media buttons of whatever is connected over Bluetooth: while the
// Car Thing plays to a car, BlueZ turns its steering wheel buttons (AVRCP) into an input device
// named after the car (see isMediaRemote). Those come and go with the connection.
func watchCarButtons(out chan<- map[string]any) {
	reading := map[string]bool{}
	var mu sync.Mutex
	for ; ; time.Sleep(5 * time.Second) {
		paths, _ := filepath.Glob("/sys/class/input/event*/device/name")
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil || !isMediaRemote(strings.TrimSpace(string(data))) {
				continue
			}
			dev := "/dev/input/" + filepath.Base(filepath.Dir(filepath.Dir(p)))
			mu.Lock()
			busy := reading[dev]
			reading[dev] = true
			mu.Unlock()
			if busy {
				continue
			}
			go func(dev, name string) {
				readEventFile(dev, name, out)
				mu.Lock()
				delete(reading, dev)
				mu.Unlock()
			}(dev, strings.TrimSpace(string(data)))
		}
	}
}

// isMediaRemote is BlueZ's naming for a connected device's media buttons: "(AVRCP)" for classic
// Bluetooth, "(MCS)" for LE Audio.
func isMediaRemote(name string) bool {
	return strings.HasSuffix(name, "(AVRCP)") || strings.HasSuffix(name, "(MCS)")
}

// watchTouch wakes the screen when the touchscreen is touched. Touches only wake it: nothing on
// the screen is meant to be tapped, and the kiosk browser still gets them as usual.
func watchTouch() {
	for {
		path := findInputLike("tlsc6x", "touch")
		if path == "" {
			log.Printf("no touchscreen found; looking again in 60s")
			time.Sleep(60 * time.Second)
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			log.Printf("opening the touchscreen: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}
		log.Printf("reading touchscreen (%s)", path)
		buf := make([]byte, 24)
		for {
			if _, err := io.ReadFull(f, buf); err != nil {
				break
			}
			typ := binary.LittleEndian.Uint16(buf[16:18])
			if typ == evKey || typ == evAbs {
				backlight.wake()
			}
		}
		f.Close()
		time.Sleep(2 * time.Second)
	}
}

// findInputLike finds the first input device whose name contains any of the words, ignoring case.
func findInputLike(words ...string) string {
	paths, _ := filepath.Glob("/sys/class/input/event*/device/name")
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(string(data)))
		for _, w := range words {
			if strings.Contains(name, w) {
				return "/dev/input/" + filepath.Base(filepath.Dir(filepath.Dir(p)))
			}
		}
	}
	return ""
}

func findInput(name string) string {
	paths, _ := filepath.Glob("/sys/class/input/event*/device/name")
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil && strings.TrimSpace(string(data)) == name {
			return "/dev/input/" + filepath.Base(filepath.Dir(filepath.Dir(p)))
		}
	}
	return ""
}

// readEventFile reads 24-byte input_event structs (64-bit: 16-byte timestamp, u16 type, u16
// code, s32 value) until the file ends or fails.
func readEventFile(path, label string, out chan<- map[string]any, grab ...bool) {
	f, err := os.Open(path)
	if err != nil {
		log.Printf("opening %s: %v", path, err)
		return
	}
	defer f.Close()
	if len(grab) > 0 && grab[0] {
		if err := grabInput(f); err != nil {
			log.Printf("couldn't take %s from Nocturne's screen (%v); it will see these presses too", label, err)
		}
	}
	log.Printf("reading %s (%s)", label, path)
	buf := make([]byte, 24)
	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			if err != io.EOF {
				log.Printf("reading %s: %v", label, err)
			}
			return
		}
		sec := int64(binary.LittleEndian.Uint64(buf[0:8]))
		usec := int64(binary.LittleEndian.Uint64(buf[8:16]))
		typ := binary.LittleEndian.Uint16(buf[16:18])
		code := binary.LittleEndian.Uint16(buf[18:20])
		value := int32(binary.LittleEndian.Uint32(buf[20:24]))
		if ev := translate(typ, code, value, sec*1000+usec/1000); ev != nil {
			out <- ev
		}
	}
}

// translate turns one raw event into what the plugin expects, or nil for anything it doesn't
// need (sync markers, key repeats, unknown codes).
func translate(typ, code uint16, value int32, ms int64) map[string]any {
	switch typ {
	case evKey:
		name, ok := keyNames[code]
		if !ok && value == 1 {
			log.Printf("a key with no name: %d", code) // a car's other buttons, say
		}
		if !ok || (value != 0 && value != 1) {
			return nil
		}
		if name == "car-rewind" || name == "car-forward" {
			carSeekAt.Store(time.Now().UnixMilli())
		}
		action := "release"
		if value == 1 {
			action = "press"
		}
		return map[string]any{"input": name, "action": action, "t": ms}
	case evRel:
		if code != relHWhee || value == 0 {
			return nil
		}
		return map[string]any{"input": "knob", "action": "turn", "delta": int(value), "t": ms}
	}
	return nil
}

// btHold is how long the mic button is held to open the settings.
const btHold = 2 * time.Second

// filterInputs sits between the controls and the plugin. Every event first wakes the screen
// (backlight.go). While the settings are open it keeps every press for them. It also holds back
// the mic button's press until the button is let go, since holding it opens the settings instead
// of muting.
func filterInputs(in <-chan map[string]any, out chan<- map[string]any) {
	var muteDown map[string]any
	var opened bool
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case ev := <-in:
			if backlight.touched(ev) {
				continue // it only woke the screen
			}
			name, _ := ev["input"].(string)
			action, _ := ev["action"].(string)
			if name == "mute" {
				switch action {
				case "press":
					muteDown, opened = ev, false
					timer.Reset(btHold)
					continue
				case "release":
					timer.Stop()
					if muteDown == nil || opened {
						muteDown, opened = nil, false
						continue // the hold opened the Bluetooth screens
					}
					press := muteDown
					muteDown = nil
					if bt.handle(press) {
						continue
					}
					out <- press
					out <- ev
					continue
				}
			}
			if bt.handle(ev) {
				continue
			}
			out <- ev
		case <-timer.C:
			if muteDown != nil && !virtual { // the Mac has its own Bluetooth and screen settings
				opened = true
				if bt.isOpen() {
					bt.close()
				} else {
					bt.openScreens()
				}
			}
		}
	}
}
