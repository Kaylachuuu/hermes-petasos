package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/curve25519"
)

// The VPN configurations: WireGuard configs in wg-quick form, kept in vpns.json (only its owner
// may read it: it holds private keys) so the tunnel home works when Hermes can't be reached.
// Hermes keeps them too (the plugin's vpn.py), so one added on its dashboard reaches every
// device; syncVPNs brings the two together, the latest change to each winning, a deletion
// included. Each phone the Car Thing gets online through says whether to use one, and which
// (its page in the Bluetooth tab).

const vpnsFile = "vpns.json"

type vpnConfig struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Conf    string `json:"conf"`
	Updated int64  `json:"updated"` // when it last changed, in ms
	Deleted bool   `json:"deleted,omitempty"`
}

// phoneVPN is whether to use a VPN while online through one phone, and which.
type phoneVPN struct {
	On     bool   `json:"on"`
	Config string `json:"config,omitempty"`
}

var vpnMu sync.Mutex // loading and saving vpns.json

// loadVPNs is every config, deletions included. The installer's wireguard.conf, from before
// there could be several, becomes the first one, "Home".
func loadVPNs(dir string) []vpnConfig {
	vpnMu.Lock()
	defer vpnMu.Unlock()
	var file struct {
		Configs []vpnConfig `json:"configs"`
	}
	if err := readJSON(filepath.Join(dir, vpnsFile), &file); err == nil {
		return file.Configs
	}
	raw, err := os.ReadFile(filepath.Join(dir, "wireguard.conf"))
	if err != nil {
		return nil
	}
	updated := int64(1) // any change made elsewhere since wins
	if st, err := os.Stat(filepath.Join(dir, "wireguard.conf")); err == nil {
		updated = st.ModTime().UnixMilli()
	}
	home := []vpnConfig{{ID: "home", Name: "Home", Conf: string(raw), Updated: updated}}
	if err := writeJSON(filepath.Join(dir, vpnsFile), map[string]any{"configs": home}, 0o600); err != nil {
		log.Printf("can't keep the VPN configurations: %v", err)
	}
	return home
}

func saveVPNs(dir string, configs []vpnConfig) error {
	vpnMu.Lock()
	defer vpnMu.Unlock()
	sort.Slice(configs, func(i, j int) bool { return strings.ToLower(configs[i].Name) < strings.ToLower(configs[j].Name) })
	return writeJSON(filepath.Join(dir, vpnsFile), map[string]any{"configs": configs}, 0o600)
}

// liveVPNs is the configs not deleted, by name.
func liveVPNs(dir string) []vpnConfig {
	var out []vpnConfig
	for _, c := range loadVPNs(dir) {
		if !c.Deleted {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func findVPN(dir, id string) (vpnConfig, bool) {
	for _, c := range liveVPNs(dir) {
		if c.ID == id {
			return c, true
		}
	}
	return vpnConfig{}, false
}

// storeVPN adds or changes one config, as changed now, and sends it to Hermes.
func storeVPN(dir string, c vpnConfig) error {
	c.Updated = time.Now().UnixMilli()
	all := loadVPNs(dir)
	found := false
	for i := range all {
		if all[i].ID == c.ID {
			all[i], found = c, true
		}
	}
	if !found {
		all = append(all, c)
	}
	if err := saveVPNs(dir, all); err != nil {
		return err
	}
	vpnChanged(dir)
	return nil
}

// deleteVPN deletes a config, unless a phone uses it.
func deleteVPN(dir, id string) error {
	if users := vpnUsers(dir, id); len(users) > 0 {
		return fmt.Errorf("%s uses it. Choose another for it first.", users[0])
	}
	c, ok := findVPN(dir, id)
	if !ok {
		return nil
	}
	c.Deleted, c.Conf = true, ""
	return storeVPN(dir, c)
}

// vpnChanged follows a change on this device: the tunnel, and Hermes's copy.
func vpnChanged(dir string) {
	go setUpTunnel(dir)
	if h := hereNow(); h != nil {
		go h.syncVPNs()
	}
}

// newVPNID is an id for a new config from its name, unique among the configs here.
func newVPNID(dir, name string) string {
	base := deviceID(name)
	if base == "" {
		base = "vpn"
	}
	base = truncate(base, 32)
	taken := map[string]bool{}
	for _, c := range loadVPNs(dir) {
		taken[c.ID] = true
	}
	id := base
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// --- which one is used ---------------------------------------------------------------------

// phoneNow is the Bluetooth address of the phone the Car Thing is online through, or "".
var phoneNow atomic.Value

func phoneAddress() string {
	a, _ := phoneNow.Load().(string)
	return a
}

// vpnForPhone is a phone's VPN setting. A phone not set uses the one choice there was before
// each phone had its own: on unless turned off, with the first config (Home, if there is one).
func vpnForPhone(dir, address string) phoneVPN {
	own := loadOwnSettings(dir)
	if p, ok := own.Phones[strings.ToUpper(address)]; ok {
		return p
	}
	p := phoneVPN{On: own.VPN == nil || *own.VPN}
	if live := liveVPNs(dir); len(live) > 0 {
		p.Config = live[0].ID
		if _, ok := findVPN(dir, "home"); ok {
			p.Config = "home"
		}
	} else {
		p.On = false
	}
	return p
}

func setVPNForPhone(dir, address string, p phoneVPN) error {
	own := loadOwnSettings(dir)
	if own.Phones == nil {
		own.Phones = map[string]phoneVPN{}
	}
	own.Phones[strings.ToUpper(address)] = p
	if err := writeJSON(filepath.Join(dir, ownSettingsFile), own, 0o644); err != nil {
		return err
	}
	if strings.EqualFold(address, phoneAddress()) {
		go setUpTunnel(dir)
	}
	return nil
}

// tunnelChoice is the config to use now, for the phone the Car Thing is online through.
func tunnelChoice(dir string) (vpnConfig, bool) {
	p := vpnForPhone(dir, phoneAddress())
	if !p.On {
		return vpnConfig{}, false
	}
	return findVPN(dir, p.Config)
}

// vpnUsers names the paired phones set to use a config.
func vpnUsers(dir, id string) []string {
	var names []string
	own := loadOwnSettings(dir)
	devs, _ := bt.devices()
	for _, d := range devs {
		if p, ok := own.Phones[strings.ToUpper(d.Address)]; ok && d.Paired && d.Phone && p.On && p.Config == id {
			names = append(names, d.Name)
		}
	}
	return names
}

// --- keys ----------------------------------------------------------------------------------

// newWireGuardKey makes a private key, so the only long thing to type on the Car Thing is the
// server's public key.
func newWireGuardKey() (string, error) {
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		return "", err
	}
	k[0] &= 248
	k[31] = k[31]&127 | 64
	return base64.StdEncoding.EncodeToString(k[:]), nil
}

// publicKey is the public key that goes with a private one, for the server's config.
func publicKey(private string) (string, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(private))
	if err != nil || len(k) != 32 {
		return "", errors.New("that isn't a WireGuard key")
	}
	pub, err := curve25519.X25519(k, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// checkKeys is whether the keys are WireGuard keys, before the tunnel is asked to use them.
func (c *wgConfig) checkKeys() error {
	key := func(name, b64 string) error {
		if k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64)); err != nil || len(k) != 32 {
			return fmt.Errorf("the %s isn't a WireGuard key", name)
		}
		return nil
	}
	if err := key("private key", c.PrivateKey); err != nil {
		return err
	}
	for _, p := range c.Peers {
		if err := key("server public key", p.PublicKey); err != nil {
			return err
		}
		if p.PresharedKey != "" {
			if err := key("preshared key", p.PresharedKey); err != nil {
				return err
			}
		}
	}
	return nil
}

// quick renders the config in wg-quick form again.
func (c *wgConfig) quick() string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", c.PrivateKey)
	if len(c.Addresses) > 0 {
		fmt.Fprintf(&b, "Address = %s\n", strings.Join(c.Addresses, ", "))
	}
	if len(c.DNS) > 0 {
		fmt.Fprintf(&b, "DNS = %s\n", strings.Join(c.DNS, ", "))
	}
	if c.ListenPort != "" {
		fmt.Fprintf(&b, "ListenPort = %s\n", c.ListenPort)
	}
	if c.MTU != "" {
		fmt.Fprintf(&b, "MTU = %s\n", c.MTU)
	}
	for _, p := range c.Peers {
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\n", p.PublicKey)
		if p.PresharedKey != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", p.PresharedKey)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", p.AllowedIPs)
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.Keepalive != "" {
			fmt.Fprintf(&b, "PersistentKeepalive = %s\n", p.Keepalive)
		}
	}
	return b.String()
}

var endpointLine = regexp.MustCompile(`(?mi)^\s*Endpoint\s*=\s*(\S+)`)

// endpointOf is where a config's tunnel goes, for its row.
func endpointOf(conf string) string {
	if m := endpointLine.FindStringSubmatch(conf); m != nil {
		return m[1]
	}
	return ""
}

// --- with Hermes ---------------------------------------------------------------------------

var syncingVPNs sync.Mutex

// syncVPNs brings this device's configs and Hermes's together: for each, the latest change
// wins, a deletion included. A plugin from before there were configs on Hermes is left be.
func (h *hermes) syncVPNs() {
	syncingVPNs.Lock()
	defer syncingVPNs.Unlock()
	var remote struct {
		Configs []vpnConfig `json:"configs"`
	}
	if err := h.call("GET", "/api/plugins/petasos/vpn?keys=1", nil, &remote); err != nil {
		if !strings.Contains(err.Error(), "HTTP 404") {
			log.Printf("can't sync the VPN configurations: %v", err)
		}
		return
	}
	local := loadVPNs(h.dir)
	merged := map[string]vpnConfig{}
	for _, c := range remote.Configs {
		merged[c.ID] = c
	}
	changed := false
	for _, c := range local {
		r, ok := merged[c.ID]
		switch {
		case ok && r.Updated >= c.Updated:
			changed = changed || r != c
			continue
		case c.Deleted:
			err := h.call("DELETE", fmt.Sprintf("/api/plugins/petasos/vpn/%s?updated=%d", url.PathEscape(c.ID), c.Updated), nil, nil)
			logSync(c, err)
		default:
			err := h.call("PUT", "/api/plugins/petasos/vpn/"+url.PathEscape(c.ID), c, nil)
			logSync(c, err)
		}
		merged[c.ID] = c
	}
	if len(merged) != len(local) {
		changed = true
	}
	if !changed {
		return
	}
	all := make([]vpnConfig, 0, len(merged))
	for _, c := range merged {
		all = append(all, c)
	}
	if err := saveVPNs(h.dir, all); err != nil {
		log.Printf("can't keep the VPN configurations: %v", err)
		return
	}
	log.Printf("VPN configurations synced with Hermes")
	go setUpTunnel(h.dir)
}

func logSync(c vpnConfig, err error) {
	if err != nil {
		log.Printf("can't send the VPN configuration %q to Hermes: %v", c.Name, err)
	}
}

// keepVPNsSynced syncs now and then, for configs added on the dashboard.
func (h *hermes) keepVPNsSynced() {
	for ; ; time.Sleep(15 * time.Minute) {
		h.syncVPNs()
	}
}
