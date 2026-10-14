package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Getting online: the Car Thing reaches the internet through a phone's hotspot, or a computer
// sharing its connection, over Bluetooth (BlueZ's network access point profile, "nap"). Devices
// are paired on the Bluetooth tab; this keeps the link up, reconnecting whenever it drops, trying
// the device chosen to go first (network.json) before the others. systemd-networkd takes the bnep interface BlueZ creates and gets an
// address from the phone (50-petasos-pan.network in the image).
//
// It talks to BlueZ through busctl, which the image already has, so the program stays free of
// D-Bus libraries.

const (
	napUUID       = "00001116-0000-1000-8000-00805f9b34fb" // a phone's hotspot
	audioSinkUUID = "0000110b-0000-1000-8000-00805f9b34fb" // a speaker, headphones or car stereo
)

type btDevice struct {
	Path, Address, Name, Icon string
	Paired, Connected, Offers bool
}

// netFirst is the device to get online through first, chosen on the Bluetooth tab and kept in
// network.json next to config.json.
var netFirst = &firstChoice{}

type firstChoice struct {
	mu    sync.Mutex
	file  string
	First string `json:"first"` // a Bluetooth address
}

func (c *firstChoice) load(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.file = filepath.Join(dir, "network.json")
	if raw, err := os.ReadFile(c.file); err == nil {
		_ = json.Unmarshal(raw, c)
	}
}

func (c *firstChoice) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.First
}

func (c *firstChoice) set(address string) {
	c.mu.Lock()
	c.First = address
	file := c.file
	raw, _ := json.Marshal(c)
	c.mu.Unlock()
	if file != "" {
		if err := os.WriteFile(file, append(raw, '\n'), 0o644); err != nil {
			log.Printf("saving the first network: %v", err)
		}
	}
}

// keepTethered runs for the life of the program. pinned is a Bluetooth address from config.json
// to use and no other; when empty, every paired device sharing internet is tried, the one chosen
// to go first before the rest.
func keepTethered(pinned string) {
	lastProblem := ""
	problem := func(format string, args ...any) {
		// The phone is often just out of range or its hotspot off; say so once, not every round.
		if msg := fmt.Sprintf(format, args...); msg != lastProblem {
			log.Print(msg)
			lastProblem = msg
		}
	}
	for ; ; time.Sleep(15 * time.Second) {
		if tethered() {
			lastProblem = ""
			continue
		}
		if _, err := busctl(10*time.Second, "set-property", "org.bluez", "/org/bluez/hci0",
			"org.bluez.Adapter1", "Powered", "b", "true"); err != nil {
			problem("Bluetooth isn't ready: %v", err)
			continue
		}
		out, err := busctl(10*time.Second, "--json=short", "call", "org.bluez", "/",
			"org.freedesktop.DBus.ObjectManager", "GetManagedObjects")
		if err != nil {
			problem("can't list Bluetooth devices: %v", err)
			continue
		}
		phones, err := pairedOffering(out, napUUID, pinned)
		if pinned == "" {
			phones = firstOf(phones, netFirst.get())
		}
		if err != nil {
			problem("can't read the Bluetooth device list: %v", err)
			continue
		}
		if len(phones) == 0 {
			problem("no paired phone or computer sharing internet; pair one on the Bluetooth tab")
			continue
		}
		var failures []string
		for _, p := range phones {
			if _, err := busctl(30*time.Second, "call", "org.bluez", p.Path, "org.bluez.Network1",
				"Connect", "s", "nap"); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", p.Name, err))
				continue
			}
			log.Printf("online through %s over Bluetooth", p.Name)
			lastProblem = ""
			break
		}
		if len(failures) == len(phones) {
			problem("can't get online over Bluetooth (is a phone nearby with its hotspot on, or a computer sharing?): %s",
				strings.Join(failures, "; "))
		}
	}
}

// firstOf moves the device with the given address to the front.
func firstOf(devs []btDevice, address string) []btDevice {
	for i, d := range devs {
		if address != "" && strings.EqualFold(d.Address, address) {
			out := append([]btDevice{d}, devs[:i]...)
			return append(out, devs[i+1:]...)
		}
	}
	return devs
}

// tethered reports whether a Bluetooth network link exists already.
func tethered() bool {
	links, _ := filepath.Glob("/sys/class/net/bnep*")
	return len(links) > 0
}

func busctl(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "busctl", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// pairedOffering picks the paired devices offering a service (by UUID) out of busctl's JSON for
// BlueZ's GetManagedObjects, the preferred address first (or only, when it is among them).
func pairedOffering(raw []byte, uuid, prefer string) ([]btDevice, error) {
	type variant struct {
		Data json.RawMessage `json:"data"`
	}
	var reply struct {
		Data []map[string]map[string]map[string]variant `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, err
	}
	if len(reply.Data) != 1 {
		return nil, fmt.Errorf("unexpected reply shape")
	}
	var found []btDevice
	for path, ifaces := range reply.Data[0] {
		props, ok := ifaces["org.bluez.Device1"]
		if !ok {
			continue
		}
		d := btDevice{Path: path}
		_ = json.Unmarshal(props["Address"].Data, &d.Address)
		_ = json.Unmarshal(props["Alias"].Data, &d.Name)
		_ = json.Unmarshal(props["Paired"].Data, &d.Paired)
		_ = json.Unmarshal(props["Connected"].Data, &d.Connected)
		_ = json.Unmarshal(props["Icon"].Data, &d.Icon)
		var uuids []string
		_ = json.Unmarshal(props["UUIDs"].Data, &uuids)
		for _, u := range uuids {
			d.Offers = d.Offers || strings.EqualFold(u, uuid)
		}
		if d.Name == "" {
			d.Name = d.Address
		}
		// A computer may share its internet without having said so when it was paired.
		if uuid == napUUID && strings.HasPrefix(d.Icon, "computer") {
			d.Offers = true
		}
		if d.Paired && d.Offers {
			found = append(found, d)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Address < found[j].Address })
	if prefer != "" {
		for _, d := range found {
			if strings.EqualFold(d.Address, prefer) {
				return []btDevice{d}, nil
			}
		}
	}
	return found, nil
}

// keepSpeakerConnected reconnects a paired Bluetooth speaker (or headphones, or a car stereo)
// whenever none is connected, so Athena's voice has somewhere to go after the speaker was
// switched off or out of range. It checks less often than the hotspot: a speaker that's off
// costs a few seconds of Bluetooth paging on every try.
func keepSpeakerConnected() {
	lastProblem := ""
	for ; ; time.Sleep(30 * time.Second) {
		out, err := busctl(10*time.Second, "--json=short", "call", "org.bluez", "/",
			"org.freedesktop.DBus.ObjectManager", "GetManagedObjects")
		if err != nil {
			continue // keepTethered reports Bluetooth trouble
		}
		speakers, err := pairedOffering(out, audioSinkUUID, "")
		if err != nil || len(speakers) == 0 {
			continue
		}
		connected := false
		for _, s := range speakers {
			connected = connected || s.Connected
		}
		if connected {
			lastProblem = ""
			continue
		}
		for _, s := range speakers {
			if _, err := busctl(20*time.Second, "call", "org.bluez", s.Path, "org.bluez.Device1", "Connect"); err == nil {
				log.Printf("connected to %s for sound", s.Name)
				lastProblem = ""
				break
			} else if msg := fmt.Sprintf("can't reach %s for sound (is it on?)", s.Name); msg != lastProblem {
				log.Print(msg)
				lastProblem = msg
			}
		}
	}
}
