package main

import (
	"encoding/binary"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The Car Thing's controls, as confirmed on hardware (README, step 2): every button is on
// gpio-keys-polled, the knob's turns on rotary.
const (
	buttonsDevice = "gpio-keys-polled"
	knobDevice    = "rotary"

	evKey    = 1
	evRel    = 2
	relHWhee = 6

	eviocgrab = 0x40044590 // _IOW('E', 0x90, int)
)

var keyNames = map[uint16]string{
	2: "preset-1", 3: "preset-2", 4: "preset-3", 5: "preset-4", // KEY_1..KEY_4
	50: "mute",  // KEY_M, the top-right button
	1:  "front", // KEY_ESC, the front button
	28: "knob",  // KEY_ENTER, pressing the knob
}

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
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgrab, 1); errno != 0 {
			log.Printf("couldn't take %s from Nocturne's screen (%v); it will see these presses too", label, errno)
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
		if !ok || (value != 0 && value != 1) {
			return nil
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

// btHold is how long the mic button is held to open the Bluetooth screens.
const btHold = 2 * time.Second

// filterInputs sits between the controls and the plugin. While the Bluetooth screens are open
// it keeps every press for them. It also holds back the mic button's press until the button is
// let go, since holding it opens the Bluetooth screens instead of muting.
func filterInputs(in <-chan map[string]any, out chan<- map[string]any) {
	var muteDown map[string]any
	var opened bool
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case ev := <-in:
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
			if muteDown != nil {
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
