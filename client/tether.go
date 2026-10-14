package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Getting online: the Car Thing reaches the internet through the phone's hotspot over Bluetooth
// (BlueZ's network access point profile, "nap"). Pairing is done once by hand with bluetoothctl;
// from then on this keeps the link up, reconnecting whenever it drops and the phone is in range
// with its hotspot on. systemd-networkd takes the bnep interface BlueZ creates and gets an
// address from the phone (50-petasos-pan.network in the image).
//
// It talks to BlueZ through busctl, which the image already has, so the program stays free of
// D-Bus libraries.

const napUUID = "00001116-0000-1000-8000-00805f9b34fb"

type btDevice struct {
	Path, Address, Name string
	Paired, NAP         bool
}

// keepTethered runs for the life of the program. prefer is the phone's Bluetooth address from
// config.json; when empty, any paired device offering a hotspot will do.
func keepTethered(prefer string) {
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
		phones, err := hotspotPhones(out, prefer)
		if err != nil {
			problem("can't read the Bluetooth device list: %v", err)
			continue
		}
		if len(phones) == 0 {
			problem("no paired phone with a hotspot; pair one with bluetoothctl (see the README)")
			continue
		}
		var failures []string
		for _, p := range phones {
			if _, err := busctl(30*time.Second, "call", "org.bluez", p.Path, "org.bluez.Network1",
				"Connect", "s", "nap"); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %v", p.Name, err))
				continue
			}
			log.Printf("connected to %s's hotspot over Bluetooth", p.Name)
			lastProblem = ""
			break
		}
		if len(failures) == len(phones) {
			problem("can't reach the phone's hotspot (is it nearby, with Personal Hotspot on?): %s",
				strings.Join(failures, "; "))
		}
	}
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

// hotspotPhones picks the paired devices offering a hotspot out of busctl's JSON for BlueZ's
// GetManagedObjects, the preferred address first (or only, when it is among them).
func hotspotPhones(raw []byte, prefer string) ([]btDevice, error) {
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
		var uuids []string
		_ = json.Unmarshal(props["UUIDs"].Data, &uuids)
		for _, u := range uuids {
			d.NAP = d.NAP || strings.EqualFold(u, napUUID)
		}
		if d.Name == "" {
			d.Name = d.Address
		}
		if d.Paired && d.NAP {
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
