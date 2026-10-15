package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Bluetooth on the screen: the link icons, and the Bluetooth screens for pairing, choosing and
// forgetting devices without a computer. They are the first tab of the settings (settings.go):
// holding the mic button for two seconds opens them; the knob moves and chooses, the front
// button goes back.
//
// Pairing needs an agent, the program BlueZ asks to confirm a pairing code; this registers one
// over D-Bus (godbus), shows the code on the screen and answers with the knob. A phone pairing
// from its own Bluetooth settings gets the same screen even when the Bluetooth screens are shut.

const (
	agentPath   = dbus.ObjectPath("/petasos/agent")
	adapterPath = dbus.ObjectPath("/org/bluez/hci0")
)

// btScreen is what the page draws while the Bluetooth screens are open.
type btScreen struct {
	View    string   `json:"view"`            // list, device, scan, pair, message, screen
	Tab     string   `json:"tab"`             // the settings tab it belongs to: bluetooth or screen
	Editing bool     `json:"editing"`         // the highlighted row's value is being turned
	Meter   int      `json:"meter,omitempty"` // a level, 1–100, lit along the band at the bottom like the volume
	Title   string   `json:"title,omitempty"`
	Note    string   `json:"note,omitempty"`
	Items   []btItem `json:"items,omitempty"`
	Sel     int      `json:"sel"`            // index into Items of the highlighted row
	Code    string   `json:"code,omitempty"` // the pairing code, in the pair view
	Asking  bool     `json:"asking"`         // the pair view waits for the knob

	// The keyboard view (device.go): the text being edited, how long it may be, and which
	// keyboard this is, so the page starts afresh for each.
	Text     string `json:"text,omitempty"`
	Max      int    `json:"max,omitempty"`
	Keyboard int    `json:"keyboard,omitempty"`
	Plain    bool   `json:"plain,omitempty"` // no capital to start: addresses, paths, sign-ins
}

type btItem struct {
	Kind   string `json:"kind"` // header, row
	Icon   string `json:"icon,omitempty"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Hot    bool   `json:"hot,omitempty"`   // shown in orange: the speaker Athena uses, the phone's hotspot
	Value  string `json:"value,omitempty"` // a setting's current value

	do   func()
	turn func(delta int) // changes a setting's value
}

type btDev struct {
	Path                       dbus.ObjectPath
	Address, Name, Icon        string
	Paired, Connected, Trusted bool
	Phone, Speaker, NetUp      bool
	RSSI                       int16
	HasRSSI                    bool
}

type bluetooth struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	open    bool
	view    *btScreen
	focus   dbus.ObjectPath // the device the device view is about
	pairing dbus.ObjectPath // the device Pair was called on from the scan view, while it runs
	stop    chan struct{}   // ends the scan view's refreshing
	confirm chan bool       // the knob's answer to a pairing code
	tab     string          // the settings tab last shown, opened again next time
	name    string          // what other devices see the Car Thing as: its name (device.go)

	draft     *softKeyDraft // the soft key being edited
	kbID      int           // the latest on-screen keyboard
	music     *musicDraft   // the Music tab's unsaved edits (musictab.go)
	typed     func(string)  // gets the keyboard's text
	typedBack func()        // runs when the keyboard is cancelled
}

var bt = &bluetooth{}

// start connects to the system bus and registers the pairing agent, retrying until BlueZ is up.
func (b *bluetooth) start() {
	for {
		conn, err := dbus.SystemBus()
		if err == nil {
			err = conn.Export(agent{b}, agentPath, "org.bluez.Agent1")
		}
		if err == nil {
			mgr := conn.Object("org.bluez", "/org/bluez")
			err = mgr.Call("org.bluez.AgentManager1.RegisterAgent", 0, agentPath, "DisplayYesNo").Err
			if err == nil {
				err = mgr.Call("org.bluez.AgentManager1.RequestDefaultAgent", 0, agentPath).Err
			}
		}
		if err == nil {
			b.mu.Lock()
			b.conn = conn
			b.mu.Unlock()
			log.Printf("Bluetooth pairing agent ready")
			b.nameAdapter()
			routeSCOOverHCI()
			setSpeakerClass()
			go b.watchLinks()
			return
		}
		log.Printf("Bluetooth pairing agent not ready (%v); trying again in 10s", err)
		time.Sleep(10 * time.Second)
	}
}

// nameAdapter makes sure other devices (a phone, a car) see the Car Thing by its name. BlueZ
// keeps the adapter's name in /var/lib/bluetooth, where an earlier system (Nocturne) may have
// left its own.
func (b *bluetooth) nameAdapter() {
	b.mu.Lock()
	conn, btName := b.conn, b.name
	b.mu.Unlock()
	if btName == "" {
		btName = "Car Thing"
	}
	obj := conn.Object("org.bluez", adapterPath)
	v, err := obj.GetProperty("org.bluez.Adapter1.Alias")
	if err == nil && v.Value() == btName {
		return
	}
	if err := obj.Call("org.freedesktop.DBus.Properties.Set", 0, "org.bluez.Adapter1", "Alias",
		dbus.MakeVariant(btName)).Err; err != nil {
		log.Printf("can't name the Bluetooth adapter %s: %v", btName, err)
		return
	}
	log.Printf("Bluetooth name set to %s (was %v)", btName, v.Value())
}

// devices lists every device BlueZ knows, paired or merely seen.
func (b *bluetooth) devices() ([]btDev, error) {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn == nil {
		return nil, fmt.Errorf("Bluetooth isn't ready")
	}
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := conn.Object("org.bluez", "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs); err != nil {
		return nil, err
	}
	var out []btDev
	for path, ifaces := range objs {
		props, ok := ifaces["org.bluez.Device1"]
		if !ok {
			continue
		}
		d := btDev{Path: path}
		get := func(name string, into any) {
			if v, ok := props[name]; ok {
				_ = v.Store(into)
			}
		}
		get("Address", &d.Address)
		get("Alias", &d.Name)
		get("Icon", &d.Icon)
		get("Paired", &d.Paired)
		get("Connected", &d.Connected)
		get("Trusted", &d.Trusted)
		if v, ok := props["RSSI"]; ok {
			d.HasRSSI = v.Store(&d.RSSI) == nil
		}
		var uuids []string
		get("UUIDs", &uuids)
		for _, u := range uuids {
			d.Phone = d.Phone || strings.EqualFold(u, napUUID)
			d.Speaker = d.Speaker || strings.EqualFold(u, audioSinkUUID)
		}
		// Phone means any device that can share internet: a phone's hotspot, or a computer.
		if strings.HasPrefix(d.Icon, "phone") || strings.HasPrefix(d.Icon, "computer") {
			d.Phone = true
		}
		if net, ok := ifaces["org.bluez.Network1"]; ok {
			if v, ok := net["Connected"]; ok {
				_ = v.Store(&d.NetUp)
			}
		}
		if d.Name == "" {
			d.Name = d.Address
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// watchLinks keeps the link icons current.
func (b *bluetooth) watchLinks() {
	for ; ; time.Sleep(5 * time.Second) {
		devs, err := b.devices()
		if err != nil {
			continue
		}
		var l links
		for _, d := range devs {
			if !d.Paired {
				continue
			}
			if d.Phone {
				l.PhonePaired = true
			}
			if d.Speaker {
				l.SpeakerKnown = true
				if d.Connected && !l.Speaker {
					l.Speaker, l.SpeakerName = true, d.Name
				} else if l.SpeakerName == "" {
					l.SpeakerName = d.Name
				}
			}
		}
		l.Phone = tethered()
		display.update(func(st *screenState) {
			l.Home, l.Checked = st.Links.Home, true
			st.Links = l
		})
		b.mu.Lock()
		refresh := b.open && b.view != nil && (b.view.View == "list" || b.view.View == "device")
		b.mu.Unlock()
		if refresh {
			b.redraw()
		}
	}
}

// --- the screens -------------------------------------------------------------------------------

// handle takes a button or knob event while the Bluetooth screens are open. It reports false
// when they are shut, so the event goes to the plugin as usual.
func (b *bluetooth) handle(ev map[string]any) bool {
	b.mu.Lock()
	open, view := b.open, b.view
	confirm := b.confirm
	b.mu.Unlock()
	if !open || view == nil {
		return false
	}
	name, _ := ev["input"].(string)
	action, _ := ev["action"].(string)
	if view.View == "keyboard" {
		b.keyboardInput(name, action, intOf(ev["delta"], 0))
		return true
	}
	if view.Editing {
		switch {
		case name == "knob" && action == "turn":
			b.adjust(intOf(ev["delta"], 0))
		case (name == "knob" || name == "front") && action == "press":
			b.doneEditing()
		}
		return true
	}
	switch {
	case name == "preset-1" && action == "press" && view.View != "pair":
		b.stopScan()
		b.listView()
	case name == "preset-2" && action == "press" && view.View != "pair":
		b.stopScan()
		b.screenView()
	case name == "preset-3" && action == "press" && view.View != "pair":
		b.stopScan()
		go b.mineView() // asks Hermes for the soft keys
	case name == "preset-4" && action == "press" && view.View != "pair":
		b.stopScan()
		go b.musicView() // asks Hermes for the music settings
	case name == "knob" && action == "turn":
		b.move(intOf(ev["delta"], 0))
	case name == "knob" && action == "press":
		if view.View == "pair" {
			if confirm != nil && view.Asking {
				select {
				case confirm <- true:
				default:
				}
			}
			return true
		}
		b.choose()
	case name == "front" && action == "press":
		if view.View == "pair" && confirm != nil {
			select {
			case confirm <- false:
			default:
			}
			return true
		}
		b.back()
	}
	return true // the spare presets, mute and releases do nothing here
}

func (b *bluetooth) show(v *btScreen) {
	if v.Tab == "" {
		v.Tab = "bluetooth"
		if v.View == "screen" {
			v.Tab = "screen"
		}
	}
	b.mu.Lock()
	b.view = v
	if v.View != "pair" && v.View != "message" {
		b.tab = v.Tab
	}
	open := b.open
	b.mu.Unlock()
	if !open {
		return
	}
	display.update(func(st *screenState) { st.BT = v })
}

func (b *bluetooth) openScreens() {
	b.mu.Lock()
	b.open = true
	tab := b.tab
	b.mu.Unlock()
	log.Printf("settings open")
	switch tab {
	case "screen":
		b.screenView()
	case "device":
		b.mineView()
	case "music":
		go b.musicView()
	default:
		b.listView()
	}
}

func (b *bluetooth) close() {
	b.stopScan()
	backlight.showDim(0)
	b.mu.Lock()
	b.open, b.view = false, nil
	b.mu.Unlock()
	display.update(func(st *screenState) { st.BT = nil })
}

func (b *bluetooth) move(delta int) {
	b.mu.Lock()
	v := b.view
	b.mu.Unlock()
	if v == nil || delta == 0 {
		return
	}
	n := *v
	step := 1
	if delta < 0 {
		step = -1
	}
	for i := n.Sel + step; i >= 0 && i < len(n.Items); i += step {
		if n.Items[i].Kind == "row" {
			n.Sel = i
			break
		}
	}
	b.show(&n)
}

func (b *bluetooth) choose() {
	b.mu.Lock()
	v := b.view
	b.mu.Unlock()
	if v == nil || v.Sel < 0 || v.Sel >= len(v.Items) || v.Items[v.Sel].do == nil {
		return
	}
	go v.Items[v.Sel].do()
}

func (b *bluetooth) back() {
	b.mu.Lock()
	v := b.view
	b.mu.Unlock()
	if v == nil {
		return
	}
	switch v.View {
	case "list", "screen", "mine", "music":
		b.close()
	case "softkey":
		go b.mineView()
	case "scan":
		b.stopScan()
		b.listView()
	default:
		if v.Tab == "device" {
			go b.mineView()
			return
		}
		if v.Tab == "music" {
			go b.musicView()
			return
		}
		b.listView()
	}
}

// redraw rebuilds the current list or device view, keeping the highlighted row where it can.
func (b *bluetooth) redraw() {
	b.mu.Lock()
	v := b.view
	b.mu.Unlock()
	if v == nil {
		return
	}
	switch v.View {
	case "list":
		b.listViewAt(v.Sel)
	case "device":
		b.deviceViewAt(v.Sel)
	}
}

func firstRow(items []btItem, from int) int {
	if from >= 0 && from < len(items) && items[from].Kind == "row" {
		return from
	}
	for i, it := range items {
		if it.Kind == "row" {
			return i
		}
	}
	return 0
}

func (b *bluetooth) listView() { b.listViewAt(-1) }

func (b *bluetooth) listViewAt(sel int) {
	devs, err := b.devices()
	if err != nil {
		b.message("Bluetooth isn't ready", err.Error())
		return
	}
	items := []btItem{{Kind: "header", Label: "Internet"}}
	phones, speakers := 0, 0
	first := netFirst.get()
	for _, d := range devs {
		if !d.Paired || !d.Phone {
			continue
		}
		d := d
		detail := "Paired"
		if d.NetUp {
			detail = "Online through it"
		} else if d.Connected {
			detail = "Connected"
		}
		if strings.EqualFold(d.Address, first) {
			detail += " · first"
		}
		items = append(items, btItem{Kind: "row", Icon: netIcon(d), Label: d.Name, Detail: detail, Hot: d.NetUp,
			do: func() { b.deviceView(d.Path) }})
		phones++
	}
	if phones == 0 {
		items = append(items, btItem{Kind: "note", Label: "No phone or computer paired yet"})
	}
	items = append(items, btItem{Kind: "header", Label: "Sound"})
	for _, d := range devs {
		if !d.Paired || !d.Speaker || d.Phone {
			continue
		}
		d := d
		detail := "Paired · not connected"
		if d.Connected {
			detail = "Playing " + display.agent()
		}
		items = append(items, btItem{Kind: "row", Icon: "speaker", Label: d.Name, Detail: detail, Hot: d.Connected,
			do: func() { b.deviceView(d.Path) }})
		speakers++
	}
	if speakers == 0 {
		items = append(items, btItem{Kind: "note", Label: "No speaker paired yet"})
	}
	items = append(items,
		btItem{Kind: "row", Icon: "plus", Label: "Pair a new device", do: b.scanView},
		btItem{Kind: "row", Icon: "close", Label: "Close", do: b.close})
	b.show(&btScreen{View: "list", Items: items, Sel: firstRow(items, sel)})
}

func (b *bluetooth) deviceView(path dbus.ObjectPath) {
	b.mu.Lock()
	b.focus = path
	b.mu.Unlock()
	b.deviceViewAt(-1)
}

func (b *bluetooth) deviceViewAt(sel int) {
	b.mu.Lock()
	path := b.focus
	b.mu.Unlock()
	devs, err := b.devices()
	if err != nil {
		b.message("Bluetooth isn't ready", err.Error())
		return
	}
	var d *btDev
	for i := range devs {
		if devs[i].Path == path {
			d = &devs[i]
		}
	}
	if d == nil || !d.Paired {
		b.listView()
		return
	}
	var items []btItem
	note := "Paired"
	if d.Phone {
		first := strings.EqualFold(d.Address, netFirst.get())
		switch {
		case d.NetUp && first:
			note = "Online through it. It goes first whenever it's nearby and sharing."
		case d.NetUp:
			note = "Online through it."
		case first:
			note = "Paired. It goes first whenever it's nearby and sharing."
		default:
			note = "Paired. The Car Thing gets online through it on its own when nothing else is sharing."
		}
		if !d.NetUp {
			items = append(items, btItem{Kind: "row", Icon: netIcon(*d), Label: "Use its internet now",
				do: func() { b.act(d.Name, "Getting online", b.useNetwork(d.Path)) }})
		}
		if !first {
			address := d.Address
			items = append(items, btItem{Kind: "row", Icon: "plus", Label: "Try this one first",
				do: func() { netFirst.set(address); b.deviceViewAt(-1) }})
		}
	} else if d.Speaker {
		if d.Connected {
			note = display.agent() + " speaks here"
			items = append(items, btItem{Kind: "row", Icon: "close", Label: "Disconnect",
				do: func() { b.act(d.Name, "Disconnecting", b.call(d.Path, "org.bluez.Device1.Disconnect")) }})
		} else {
			items = append(items, btItem{Kind: "row", Icon: "speaker", Label: "Use for " + display.agent(),
				do: func() { b.act(d.Name, "Connecting", b.useSpeaker(d.Path)) }})
		}
	}
	items = append(items,
		btItem{Kind: "row", Icon: "forget", Label: "Forget this device",
			do: func() { b.act(d.Name, "Forgetting", b.forget(d.Path)) }},
		btItem{Kind: "row", Icon: "back", Label: "Back", do: b.listView})
	b.show(&btScreen{View: "device", Title: d.Name, Note: note, Items: items, Sel: firstRow(items, sel)})
}

// act shows a working message, runs the step, then says how it went and returns to the list.
func (b *bluetooth) act(name, doing string, step func() error) {
	b.show(&btScreen{View: "message", Title: name, Note: doing + "…"})
	if err := step(); err != nil {
		log.Printf("Bluetooth: %s %s failed: %v", strings.ToLower(doing), name, err)
		b.show(&btScreen{View: "message", Title: name, Note: "That didn't work: " + friendly(err)})
		time.Sleep(4 * time.Second)
	}
	if b.isOpen() {
		b.listView()
	}
}

func (b *bluetooth) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

func (b *bluetooth) message(title, note string) {
	b.mu.Lock()
	tab := b.tab
	b.mu.Unlock()
	if tab != "device" && tab != "music" {
		tab = "bluetooth"
	}
	b.show(&btScreen{View: "message", Tab: tab, Title: title, Note: note})
}

func friendly(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Page Timeout"), strings.Contains(msg, "Host is down"), strings.Contains(msg, "not available"):
		return "the device didn't answer. Is it on and nearby?"
	case strings.Contains(msg, "Authentication"), strings.Contains(msg, "Rejected"), strings.Contains(msg, "Canceled"):
		return "the pairing was declined."
	}
	return truncate(msg, 120)
}

func (b *bluetooth) call(path dbus.ObjectPath, method string, args ...any) func() error {
	return func() error {
		b.mu.Lock()
		conn := b.conn
		b.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		return conn.Object("org.bluez", path).CallWithContext(ctx, method, 0, args...).Err
	}
}

func (b *bluetooth) connectHotspot(path dbus.ObjectPath) func() error {
	return b.call(path, "org.bluez.Network1.Connect", "nap")
}

// useNetwork gets online through one device, letting go of the one in use, and makes it the one
// tried first from now on.
func (b *bluetooth) useNetwork(path dbus.ObjectPath) func() error {
	return func() error {
		devs, err := b.devices()
		if err != nil {
			return err
		}
		for _, d := range devs {
			if d.Path == path {
				netFirst.set(d.Address)
			} else if d.NetUp {
				_ = b.call(d.Path, "org.bluez.Network1.Disconnect")()
			}
		}
		return b.connectHotspot(path)()
	}
}

// netIcon is a laptop for a computer, otherwise a phone.
func netIcon(d btDev) string {
	if strings.HasPrefix(d.Icon, "computer") {
		return "laptop"
	}
	return "phone"
}

// useSpeaker connects one speaker and lets go of any other, since BlueALSA plays to the one most
// recently connected.
func (b *bluetooth) useSpeaker(path dbus.ObjectPath) func() error {
	return func() error {
		devs, err := b.devices()
		if err != nil {
			return err
		}
		for _, d := range devs {
			if d.Path != path && d.Speaker && !d.Phone && d.Connected {
				_ = b.call(d.Path, "org.bluez.Device1.Disconnect")()
			}
		}
		return b.call(path, "org.bluez.Device1.Connect")()
	}
}

func (b *bluetooth) forget(path dbus.ObjectPath) func() error {
	return b.call(adapterPath, "org.bluez.Adapter1.RemoveDevice", path)
}

// --- pairing -----------------------------------------------------------------------------------

func (b *bluetooth) setAdapter(prop string, value any) {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn != nil {
		_ = conn.Object("org.bluez", adapterPath).Call("org.freedesktop.DBus.Properties.Set", 0,
			"org.bluez.Adapter1", prop, dbus.MakeVariant(value)).Err
	}
}

func (b *bluetooth) scanView() {
	b.stopScan()
	stop := make(chan struct{})
	b.mu.Lock()
	b.stop = stop
	b.mu.Unlock()
	// Discoverable too, so a phone can find "Petasos" in its own Bluetooth settings.
	b.setAdapter("Pairable", true)
	b.setAdapter("DiscoverableTimeout", uint32(180))
	b.setAdapter("Discoverable", true)
	if err := b.call(adapterPath, "org.bluez.Adapter1.StartDiscovery")(); err != nil && !strings.Contains(err.Error(), "InProgress") {
		log.Printf("Bluetooth: can't start looking for devices: %v", err)
	}
	sel := -1
	for {
		b.scanList(sel)
		select {
		case <-stop:
			return
		case <-time.After(2 * time.Second):
		}
		b.mu.Lock()
		if b.view != nil && b.view.View == "scan" {
			sel = b.view.Sel
		}
		b.mu.Unlock()
	}
}

func (b *bluetooth) scanList(sel int) {
	devs, err := b.devices()
	if err != nil {
		return
	}
	// Devices nearby that aren't paired yet, strongest signal first; ones with no name are
	// usually not something to pair with.
	var found []btDev
	for _, d := range devs {
		if !d.Paired && d.HasRSSI && d.Name != d.Address {
			found = append(found, d)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].RSSI > found[j].RSSI })
	var items []btItem
	for _, d := range found {
		d := d
		icon := "device"
		if d.Phone {
			icon = netIcon(d)
		} else if d.Speaker || strings.HasPrefix(d.Icon, "audio") {
			icon = "speaker"
		}
		items = append(items, btItem{Kind: "row", Icon: icon, Label: d.Name, do: func() { b.pair(d) }})
	}
	if len(items) == 0 {
		items = append(items, btItem{Kind: "note", Label: "Nothing found yet"})
	}
	items = append(items, btItem{Kind: "row", Icon: "back", Label: "Back", do: func() { b.stopScan(); b.listView() }})
	b.mu.Lock()
	still := b.view == nil || b.view.View == "scan" || b.view.View == "list"
	b.mu.Unlock()
	if still {
		b.show(&btScreen{View: "scan", Title: "Looking for devices",
			Note:  "Put a speaker in pairing mode, or pick Petasos in a phone's Bluetooth settings.",
			Items: items, Sel: firstRow(items, sel)})
	}
}

func (b *bluetooth) stopScan() {
	b.mu.Lock()
	stop := b.stop
	b.stop = nil
	b.mu.Unlock()
	if stop != nil {
		close(stop)
		_ = b.call(adapterPath, "org.bluez.Adapter1.StopDiscovery")()
		b.setAdapter("Discoverable", false)
	}
}

// pair pairs with a device found by the scan; the agent below shows the code if there is one.
func (b *bluetooth) pair(d btDev) {
	b.stopScan()
	b.show(&btScreen{View: "pair", Title: d.Name, Note: "Pairing…"})
	b.mu.Lock()
	b.pairing = d.Path
	b.mu.Unlock()
	err := b.call(d.Path, "org.bluez.Device1.Pair")()
	b.mu.Lock()
	b.pairing = ""
	b.mu.Unlock()
	if err != nil && !strings.Contains(err.Error(), "AlreadyExists") {
		log.Printf("Bluetooth: pairing with %s failed: %v", d.Name, err)
		b.show(&btScreen{View: "message", Title: d.Name, Note: "Not paired: " + friendly(err)})
		time.Sleep(4 * time.Second)
		if b.isOpen() {
			b.listView()
		}
		return
	}
	b.afterPairing(d.Path, d.Name)
}

func (b *bluetooth) afterPairing(path dbus.ObjectPath, name string) {
	log.Printf("Bluetooth: paired with %s", name)
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	_ = conn.Object("org.bluez", path).Call("org.freedesktop.DBus.Properties.Set", 0,
		"org.bluez.Device1", "Trusted", dbus.MakeVariant(true)).Err
	// What it is only shows once its services are known, a moment after pairing.
	time.Sleep(2 * time.Second)
	var d *btDev
	if devs, err := b.devices(); err == nil {
		for i := range devs {
			if devs[i].Path == path {
				d = &devs[i]
			}
		}
	}
	note := "Paired"
	switch {
	case d != nil && d.Phone && netIcon(*d) == "laptop":
		note = "Paired. Share its internet over Bluetooth PAN and the Car Thing connects on its own."
		_ = b.connectHotspot(path)()
	case d != nil && d.Phone:
		note = "Paired. Turn on Personal Hotspot and the Car Thing connects on its own."
		_ = b.connectHotspot(path)()
	case d != nil && d.Speaker:
		note = "Paired. " + display.agent() + " speaks here now."
		if err := b.useSpeaker(path)(); err != nil {
			note = "Paired, but it didn't connect: " + friendly(err)
		}
	}
	b.show(&btScreen{View: "message", Title: name, Note: note})
	time.Sleep(3 * time.Second)
	if b.isOpen() {
		b.listView()
	}
}

// agent is the BlueZ pairing agent. BlueZ calls its methods over D-Bus, each in its own goroutine.
type agent struct{ b *bluetooth }

func (a agent) name(path dbus.ObjectPath) string {
	if devs, err := a.b.devices(); err == nil {
		for _, d := range devs {
			if d.Path == path {
				return d.Name
			}
		}
	}
	return "a device"
}

var rejected = dbus.NewError("org.bluez.Error.Rejected", []any{"Declined on the Car Thing"})

// ask shows a code and waits for the knob (yes) or the front button (no).
func (a agent) ask(path dbus.ObjectPath, code, note string) bool {
	b := a.b
	answer := make(chan bool, 1)
	b.mu.Lock()
	if b.confirm != nil { // one pairing at a time
		b.mu.Unlock()
		return false
	}
	b.confirm = answer
	wasOpen := b.open
	b.open = true // an incoming pairing opens the screens
	b.mu.Unlock()
	b.show(&btScreen{View: "pair", Title: a.name(path), Code: code, Note: note, Asking: true})
	ok := false
	select {
	case ok = <-answer:
	case <-time.After(60 * time.Second):
	}
	b.mu.Lock()
	b.confirm = nil
	b.mu.Unlock()
	if ok {
		b.show(&btScreen{View: "pair", Title: a.name(path), Code: code, Note: "Pairing…"})
	} else if !wasOpen {
		b.close()
	} else {
		b.listView()
	}
	return ok
}

func (a agent) Release() *dbus.Error { return nil }

func (a agent) RequestConfirmation(path dbus.ObjectPath, passkey uint32) *dbus.Error {
	code := fmt.Sprintf("%06d", passkey)
	if !a.ask(path, code[:3]+" "+code[3:], "If the other device shows the same code, press the knob, and tap Pair on it.") {
		return rejected
	}
	go a.watchPaired(path)
	return nil
}

func (a agent) RequestAuthorization(path dbus.ObjectPath) *dbus.Error {
	if !a.ask(path, "", "Press the knob to pair, or the front button to decline.") {
		return rejected
	}
	go a.watchPaired(path)
	return nil
}

// AuthorizeService lets paired devices use their services (a phone's hotspot, audio).
func (a agent) AuthorizeService(path dbus.ObjectPath, uuid string) *dbus.Error { return nil }

func (a agent) RequestPinCode(path dbus.ObjectPath) (string, *dbus.Error) {
	return "0000", nil // older speakers with a fixed PIN almost always use 0000
}

func (a agent) RequestPasskey(path dbus.ObjectPath) (uint32, *dbus.Error) { return 0, nil }

func (a agent) DisplayPinCode(path dbus.ObjectPath, pin string) *dbus.Error {
	a.b.show(&btScreen{View: "pair", Title: a.name(path), Code: pin, Note: "Type this code on the other device."})
	return nil
}

func (a agent) DisplayPasskey(path dbus.ObjectPath, passkey uint32, entered uint16) *dbus.Error {
	code := fmt.Sprintf("%06d", passkey)
	a.b.show(&btScreen{View: "pair", Title: a.name(path), Code: code[:3] + " " + code[3:], Note: "Type this code on the other device."})
	return nil
}

func (a agent) Cancel() *dbus.Error {
	a.b.mu.Lock()
	answer := a.b.confirm
	a.b.mu.Unlock()
	if answer != nil {
		select {
		case answer <- false:
		default:
		}
	}
	return nil
}

// watchPaired finishes a pairing the other device started (a phone pairing from its settings):
// once BlueZ marks it paired, trust it and connect.
func (a agent) watchPaired(path dbus.ObjectPath) {
	b := a.b
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second)
		devs, err := b.devices()
		if err != nil {
			continue
		}
		for _, d := range devs {
			if d.Path == path && d.Paired {
				// When the pairing began here, pair() carries on once Device1.Pair returns.
				if !b.pairingFromHere(path) {
					b.afterPairing(path, d.Name)
				}
				return
			}
		}
	}
}

// pairingFromHere reports whether Device1.Pair from the scan view is under way for this device,
// in which case pair() finishes the job.
func (b *bluetooth) pairingFromHere(path dbus.ObjectPath) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pairing == path
}
