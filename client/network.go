package main

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The Network tab: the server this Car Thing signs in to, and the VPN configurations it can
// reach home through. Logging in takes the address, username and password typed here; logging
// out is holding the knob on the Status row for logoutHold, so it isn't done by a slip.
// The configurations open in screens of their own, under a "VPN" tab.

const logoutHold = 3 * time.Second

// signInDraft is what's typed for logging in. The password is only ever held here.
type signInDraft struct {
	server, username, password string
}

// vpnDraft is a configuration being added or changed, kept until Save.
type vpnDraft struct {
	id, name string
	cfg      wgConfig
	isNew    bool
	problem  string
}

func (h *hermes) signedIn() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tokens.RefreshToken != ""
}

// serverShown is the server's address as people write it: 10.0.0.20:9119.
func serverShown(server string) string {
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		return u.Host
	}
	return server
}

func (b *bluetooth) networkView() { b.networkViewAt(-1) }

func (b *bluetooth) networkViewAt(sel int) {
	h := hereNow()
	if h == nil {
		b.show(&btScreen{View: "message", Tab: "network", Title: "Not ready", Note: "Petasos is still starting."})
		return
	}
	b.mu.Lock()
	if b.signin == nil {
		b.signin = &signInDraft{server: serverShown(h.cfg.Server), username: h.cfg.Username}
	}
	d := b.signin
	b.mu.Unlock()
	in := h.signedIn()
	field := func(icon, label, value string, row int, secret bool, set func(string)) btItem {
		shown := value
		if shown == "" {
			shown = "—"
		} else if secret {
			shown = strings.Repeat("•", min(len([]rune(value)), 12))
		}
		return btItem{Kind: "row", Icon: icon, Label: label, Value: truncate(shown, 28), Off: in, do: func() {
			done := func(text string) {
				set(strings.TrimSpace(text))
				b.networkViewAt(row)
			}
			if secret {
				b.keyboardSecret(label, "", 200, done, func() { b.networkViewAt(row) })
				return
			}
			b.keyboardFor(label, "", value, 200, true, done, func() { b.networkViewAt(row) })
		}}
	}
	items := []btItem{{Kind: "header", Label: "Server"},
		field("home", "Address", d.server, 1, false, func(s string) { d.server = s }),
		field("device", "Username", d.username, 2, false, func(s string) { d.username = s }),
		field("key", "Password", d.password, 3, true, func(s string) { d.password = s })}
	if in {
		who := ""
		if h.cfg.Username != "" {
			who = " as " + h.cfg.Username
		}
		items = append(items, btItem{Kind: "row", Icon: "power", Label: "Status", Value: "Logged in",
			Detail: "Logged into " + serverShown(h.cfg.Server) + who + ". Hold the knob for 3 seconds to log out.",
			Hold:   true, hold: func() { b.logOut(h) }})
	} else {
		ready := d.server != "" && d.username != "" && d.password != ""
		items = append(items, btItem{Kind: "row", Icon: "power", Label: "Status", Value: "Logged out",
			Detail: "Press the knob to log in", Off: !ready, do: func() { b.logIn(h, d) }})
	}
	live := liveVPNs(h.dir)
	items = append(items, btItem{Kind: "header", Label: "VPN"},
		btItem{Kind: "row", Icon: "shield", Label: "VPN configurations", Value: fmt.Sprint(len(live)), do: b.vpnListView})
	note := "Each phone chooses its VPN on its page in the Bluetooth tab."
	if !in {
		note = "Logged out: " + display.agent() + " can't be reached until you log in."
	}
	b.show(&btScreen{View: "network", Tab: "network", Items: items, Sel: firstRow(items, sel), Note: note})
}

// logIn signs in with what was typed, then starts again signed in.
func (b *bluetooth) logIn(h *hermes, d *signInDraft) {
	b.show(&btScreen{View: "message", Tab: "network", Title: "Logging in", Note: "Asking " + serverShown(d.server) + "…"})
	if err := signIn(h.dir, d.server, d.username, strings.NewReader(d.password+"\n")); err != nil {
		log.Printf("logging in: %v", err)
		b.show(&btScreen{View: "message", Tab: "network", Title: "Not logged in", Note: friendlySignIn(err)})
		time.Sleep(4 * time.Second)
		if b.isOpen() {
			b.networkViewAt(4)
		}
		return
	}
	d.password = ""
	b.message("Logged in", "Starting again, logged in as "+d.username+"…")
	restartSoon()
}

func friendlySignIn(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "didn't accept"):
		return "Wrong username or password."
	case strings.Contains(msg, "can't reach"):
		return "Can't reach that address. Is the VPN or the phone's internet on?"
	}
	return truncate(msg, 160)
}

// logOut forgets this device's sign-in, then starts again logged out.
func (b *bluetooth) logOut(h *hermes) {
	if err := os.Remove(filepath.Join(h.dir, "tokens.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		b.message("Couldn't log out", err.Error())
		return
	}
	h.mu.Lock()
	h.tokens = tokens{}
	h.mu.Unlock()
	b.message("Logged out", "Starting again, logged out…")
	restartSoon()
}

// --- the VPN configurations --------------------------------------------------------------

func (b *bluetooth) vpnListView() { b.vpnListViewAt(-1) }

func (b *bluetooth) vpnListViewAt(sel int) {
	h := hereNow()
	if h == nil {
		return
	}
	var items []btItem
	for _, c := range liveVPNs(h.dir) {
		id, detail := c.ID, endpointOf(c.Conf)
		if users := vpnUsers(h.dir, c.ID); len(users) > 0 {
			detail = strings.TrimPrefix(detail+" · used by "+strings.Join(users, ", "), " · ")
		}
		items = append(items, btItem{Kind: "row", Icon: "shield", Label: c.Name, Detail: truncate(detail, 48),
			do: func() { b.vpnEdit(id) }})
	}
	if len(items) == 0 {
		items = append(items, btItem{Kind: "note", Label: "None yet"})
	}
	items = append(items, btItem{Kind: "row", Icon: "plus", Label: "Add new VPN configuration", do: func() { b.vpnEdit("") }},
		btItem{Kind: "row", Icon: "back", Label: "Back", do: b.networkView})
	b.show(&btScreen{View: "vpns", Tab: "network", Sub: "VPN", Items: items, Sel: firstRow(items, sel),
		Note: "Added on the Hermes dashboard, on a computer, or here. Each phone chooses one in the Bluetooth tab."})
}

// vpnEdit opens a configuration, or a new one with a key of its own, to change.
func (b *bluetooth) vpnEdit(id string) {
	h := hereNow()
	if h == nil {
		return
	}
	d := &vpnDraft{id: id, isNew: id == ""}
	if c, ok := findVPN(h.dir, id); ok {
		cfg, err := parseWireGuard([]byte(c.Conf))
		if err != nil {
			cfg = &wgConfig{}
			d.problem = "This configuration doesn't work as it is: " + err.Error()
		}
		d.name, d.cfg = c.Name, *cfg
	} else {
		key, err := newWireGuardKey()
		if err != nil {
			b.message("Couldn't make a key", err.Error())
			return
		}
		d.cfg = wgConfig{PrivateKey: key, Peers: []wgPeer{{AllowedIPs: "10.0.0.0/24", Keepalive: "25"}}}
	}
	if len(d.cfg.Peers) == 0 {
		d.cfg.Peers = []wgPeer{{}}
	}
	b.mu.Lock()
	b.vpn = d
	b.mu.Unlock()
	b.vpnEditViewAt(-1)
}

func (b *bluetooth) vpnEditViewAt(sel int) {
	b.mu.Lock()
	d := b.vpn
	b.mu.Unlock()
	h := hereNow()
	if d == nil || h == nil {
		b.vpnListView()
		return
	}
	peer := &d.cfg.Peers[0]
	var items []btItem
	text := func(icon, label string, value *string, max int) {
		row := len(items)
		shown := *value
		if shown == "" {
			shown = "—"
		}
		items = append(items, btItem{Kind: "row", Icon: icon, Label: label, Value: truncate(shown, 26), do: func() {
			b.keyboardFor(label, "", *value, max, true, func(text string) {
				*value = strings.TrimSpace(text)
				b.vpnEditViewAt(row)
			}, func() { b.vpnEditViewAt(row) })
		}})
	}
	list := func(icon, label string, values *[]string) {
		joined := strings.Join(*values, ", ")
		row := len(items)
		text(icon, label, &joined, 120)
		items[row].do = func() {
			b.keyboardFor(label, "Separate more than one with commas.", joined, 120, true, func(text string) {
				*values = nil
				for _, v := range strings.Split(text, ",") {
					if v = strings.TrimSpace(v); v != "" {
						*values = append(*values, v)
					}
				}
				b.vpnEditViewAt(row)
			}, func() { b.vpnEditViewAt(row) })
		}
	}
	text("shield", "Name", &d.name, 40)
	list("home", "Address", &d.cfg.Addresses)
	list("home", "DNS", &d.cfg.DNS)
	items = append(items, btItem{Kind: "row", Icon: "key", Label: "Private key", Value: map[bool]string{true: "Set", false: "None"}[d.cfg.PrivateKey != ""],
		do: b.vpnKeyView})
	if pub, err := publicKey(d.cfg.PrivateKey); err == nil {
		items = append(items, btItem{Kind: "row", Icon: "key", Label: "Public key", Detail: pub})
	}
	text("key", "Server public key", &peer.PublicKey, 44)
	text("network", "Endpoint", &peer.Endpoint, 120)
	text("network", "Allowed IPs", &peer.AllowedIPs, 200)
	text("network", "Keepalive", &peer.Keepalive, 5)
	items = append(items, btItem{Kind: "row", Icon: "plus", Label: "Save", do: b.vpnSave})
	if !d.isNew {
		items = append(items, btItem{Kind: "row", Icon: "forget", Label: "Delete", do: b.vpnDeleteView})
	}
	items = append(items, btItem{Kind: "row", Icon: "back", Label: "Back", do: b.vpnListView})
	title := d.name
	if title == "" {
		title = "New VPN configuration"
	}
	note := d.problem
	if note == "" {
		note = "Nothing is kept until you choose Save. Give the server your public key."
	}
	b.show(&btScreen{View: "vpn", Tab: "network", Sub: "VPN", Title: title, Note: note, Items: items, Sel: firstRow(items, sel)})
}

// vpnKeyView offers a new private key, or typing one in.
func (b *bluetooth) vpnKeyView() {
	b.mu.Lock()
	d := b.vpn
	b.mu.Unlock()
	if d == nil {
		return
	}
	back := func() { b.vpnEditViewAt(3) }
	items := []btItem{
		{Kind: "row", Icon: "plus", Label: "Make a new key", do: func() {
			if key, err := newWireGuardKey(); err == nil {
				d.cfg.PrivateKey = key
			}
			back()
		}},
		{Kind: "row", Icon: "key", Label: "Type a key", do: func() {
			b.keyboardFor("Private key", "", "", 44, true, func(text string) {
				d.cfg.PrivateKey = strings.TrimSpace(text)
				if _, err := publicKey(d.cfg.PrivateKey); err != nil {
					d.problem = "That private key isn't a WireGuard key."
				}
				back()
			}, back)
		}},
		{Kind: "row", Icon: "back", Label: "Back", do: back}}
	b.show(&btScreen{View: "vpnkey", Tab: "network", Sub: "VPN", Title: "Private key", Items: items,
		Note: "A new key means giving the server the new public key."})
}

// vpnSave checks the configuration as the tunnel would, then keeps it.
func (b *bluetooth) vpnSave() {
	b.mu.Lock()
	d := b.vpn
	b.mu.Unlock()
	h := hereNow()
	if d == nil || h == nil {
		return
	}
	d.problem = ""
	if strings.TrimSpace(d.name) == "" {
		d.problem = "Give it a name."
	} else if cfg, err := parseWireGuard([]byte(d.cfg.quick())); err != nil {
		d.problem = "Not saved: " + err.Error() + "."
	} else if err := cfg.homeOnly(); err != nil {
		d.problem = "Not saved: " + err.Error() + "."
	} else if err := cfg.checkKeys(); err != nil {
		d.problem = "Not saved: " + err.Error() + "."
	}
	if d.problem != "" {
		b.vpnEditViewAt(-1)
		return
	}
	if d.isNew {
		d.id = newVPNID(h.dir, d.name)
	}
	if err := storeVPN(h.dir, vpnConfig{ID: d.id, Name: strings.TrimSpace(d.name), Conf: d.cfg.quick()}); err != nil {
		d.problem = "Not saved: " + err.Error()
		b.vpnEditViewAt(-1)
		return
	}
	b.vpnListView()
}

func (b *bluetooth) vpnDeleteView() {
	b.mu.Lock()
	d := b.vpn
	b.mu.Unlock()
	h := hereNow()
	if d == nil || h == nil {
		return
	}
	items := []btItem{
		{Kind: "row", Icon: "forget", Label: "Delete " + d.name, do: func() {
			if err := deleteVPN(h.dir, d.id); err != nil {
				d.problem = "Not deleted: " + err.Error()
				b.vpnEditViewAt(-1)
				return
			}
			b.vpnListView()
		}},
		{Kind: "row", Icon: "back", Label: "Keep it", do: func() { b.vpnEditViewAt(-1) }}}
	b.show(&btScreen{View: "vpnkey", Tab: "network", Sub: "VPN", Title: "Delete " + d.name + "?", Items: items, Sel: 1,
		Note: "It's deleted from Hermes and your other devices too."})
}
