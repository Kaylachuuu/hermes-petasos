package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

// bluetoothReport is what the Car Thing offers over Bluetooth (its class and profiles), what each
// device paired with it offers and whether it's connected, and BlueALSA's connections (a car's
// hands-free shows its features there once it has set up): for Send log, to see why a car does
// or doesn't take the Car Thing for a phone. bluetoothctl prints nothing without a terminal.
func bluetoothReport() string {
	conn, err := dbus.SystemBus()
	if err != nil {
		return err.Error() + "\n"
	}
	var b strings.Builder
	for _, service := range []string{"org.bluez", bluealsaService} {
		root := dbus.ObjectPath("/")
		if service == bluealsaService {
			root = "/org/bluealsa"
		}
		var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
		if err := conn.Object(service, root).Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs); err != nil {
			fmt.Fprintf(&b, "%s: %v\n", service, err)
			continue
		}
		paths := make([]string, 0, len(objs))
		for p := range objs {
			paths = append(paths, string(p))
		}
		sort.Strings(paths)
		for _, p := range paths {
			for iface, props := range objs[dbus.ObjectPath(p)] {
				if !strings.HasPrefix(iface, "org.bluez.Adapter1") && !strings.HasPrefix(iface, "org.bluez.Device1") &&
					!strings.HasPrefix(iface, "org.bluealsa.") {
					continue
				}
				fmt.Fprintf(&b, "%s %s\n", p, iface)
				keys := make([]string, 0, len(props))
				for k := range props {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(&b, "  %s: %s\n", k, btValue(k, props[k].Value()))
				}
			}
		}
	}
	return b.String()
}

// btValue shows a property: a class in hex, profiles by name.
func btValue(key string, v any) string {
	switch x := v.(type) {
	case uint32:
		if key == "Class" || key == "Features" {
			return fmt.Sprintf("0x%06x", x)
		}
	case []string:
		if key == "UUIDs" {
			names := make([]string, len(x))
			for i, u := range x {
				names[i] = u
				if len(u) >= 8 {
					if n, ok := btProfiles[strings.ToLower(u[4:8])]; ok {
						names[i] = n
					}
				}
			}
			return strings.Join(names, ", ")
		}
	}
	return fmt.Sprint(v)
}

var btProfiles = map[string]string{
	"1108": "HSP", "1112": "HSP-AG", "111e": "HFP-HF", "111f": "HFP-AG", "110a": "A2DP-source", "110b": "A2DP-sink",
	"110c": "AVRCP-target", "110e": "AVRCP", "110f": "AVRCP-controller", "112e": "PBAP-client", "112f": "PBAP-server",
	"1132": "MAP-server", "1133": "MAP-notify", "1134": "MAP", "1115": "PANU", "1116": "NAP", "1200": "PnP",
	"1800": "GAP", "1801": "GATT", "180a": "DeviceInfo", "1105": "OPP", "1106": "FTP", "1101": "SPP",
}
