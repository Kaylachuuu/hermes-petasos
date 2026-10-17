package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testConf = `[Interface]
PrivateKey = %s
Address = 10.8.0.5/32

[Peer]
PublicKey = c2VydmVyIHB1YmxpYyBrZXkgZm9yIHRoZSB0ZXN0cz0=
AllowedIPs = 10.0.0.0/24
Endpoint = home.example.net:51820
`

func TestWireGuardKeysAndConfigsRoundTrip(t *testing.T) {
	priv, err := newWireGuardKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := publicKey(priv)
	if err != nil || len(pub) != 44 || pub == priv {
		t.Fatalf("public key %q: %v", pub, err)
	}
	// RFC 7748's test vector: Alice's keys.
	if p, _ := publicKey("dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo="); p != "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=" {
		t.Errorf("Alice's public key came out %s", p)
	}
	cfg, err := parseWireGuard([]byte(strings.Replace(testConf, "%s", priv, 1)))
	if err != nil {
		t.Fatal(err)
	}
	again, err := parseWireGuard([]byte(cfg.quick()))
	if err != nil || again.PrivateKey != priv || again.Peers[0].Endpoint != "home.example.net:51820" || again.checkKeys() != nil {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	if endpointOf(cfg.quick()) != "home.example.net:51820" {
		t.Error("no endpoint")
	}
}

// The installer's wireguard.conf becomes "Home", which each phone not yet set uses; a phone
// set to another, or to none, follows its own setting.
func TestEachPhoneChoosesItsVPN(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wireguard.conf"), []byte(strings.Replace(testConf, "%s", "x", 1)), 0o600)
	if live := liveVPNs(dir); len(live) != 1 || live[0].ID != "home" || live[0].Name != "Home" {
		t.Fatalf("migrated %+v", live)
	}
	if st, _ := os.Stat(filepath.Join(dir, vpnsFile)); st.Mode().Perm() != 0o600 {
		t.Errorf("vpns.json is %v", st.Mode())
	}
	const phone = "aa:bb:cc:dd:ee:ff"
	if p := vpnForPhone(dir, phone); !p.On || p.Config != "home" {
		t.Fatalf("a phone not set: %+v", p)
	}
	storeVPN(dir, vpnConfig{ID: "away", Name: "Away", Conf: "x"})
	setVPNForPhone(dir, phone, phoneVPN{On: true, Config: "away"})
	phoneNow.Store(strings.ToUpper(phone))
	defer phoneNow.Store("")
	if c, ok := tunnelChoice(dir); !ok || c.ID != "away" {
		t.Fatalf("chose %+v", c)
	}
	setVPNForPhone(dir, phone, phoneVPN{On: false, Config: "away"})
	if _, ok := tunnelChoice(dir); ok {
		t.Fatal("a phone set to no VPN still uses one")
	}
	// Turned off before each phone had its own: a phone not set has none.
	off := false
	writeJSON(filepath.Join(dir, ownSettingsFile), ownSettings{VPN: &off}, 0o644)
	if p := vpnForPhone(dir, "11:22:33:44:55:66"); p.On {
		t.Fatal("the old off switch was lost")
	}
}

// Configs sync with Hermes both ways, the latest change to each winning, deletions included.
func TestVPNConfigsSyncWithHermes(t *testing.T) {
	var mu sync.Mutex
	remote := map[string]vpnConfig{
		"office": {ID: "office", Name: "Office", Conf: "office conf", Updated: 5000},
		"home":   {ID: "home", Name: "Home (old)", Conf: "old", Updated: 1000},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/api/plugins/petasos/vpn/")
		switch r.Method {
		case "GET":
			if r.URL.Query().Get("keys") != "1" {
				t.Error("a device asks for the keys")
			}
			var list []vpnConfig
			for _, c := range remote {
				list = append(list, c)
			}
			json.NewEncoder(w).Encode(map[string]any{"configs": list})
		case "PUT":
			var c vpnConfig
			json.NewDecoder(r.Body).Decode(&c)
			remote[id] = c
			w.Write([]byte("{}"))
		case "DELETE":
			remote[id] = vpnConfig{ID: id, Deleted: true, Updated: 9000}
			w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	h := &hermes{dir: dir, cfg: config{Server: srv.URL}, http: srv.Client()}
	saveVPNs(dir, []vpnConfig{
		{ID: "home", Name: "Home", Conf: "new", Updated: 2000},
		{ID: "phone", Name: "Phone", Updated: 3000, Deleted: true},
	})
	h.syncVPNs()
	live := liveVPNs(dir)
	if len(live) != 2 || live[0].Name != "Home" || live[0].Conf != "new" || live[1].Name != "Office" {
		t.Fatalf("here: %+v", live)
	}
	mu.Lock()
	defer mu.Unlock()
	if remote["home"].Conf != "new" || !remote["phone"].Deleted {
		t.Fatalf("on Hermes: %+v", remote)
	}
}

// Logging out is holding the knob on Status; letting go early does nothing.
func TestNetworkTabLogsOutOnAHold(t *testing.T) {
	dir := t.TempDir()
	writeJSON(filepath.Join(dir, "tokens.json"), tokens{AccessToken: "a", RefreshToken: "r"}, 0o600)
	h := &hermes{dir: dir, cfg: config{Server: "http://10.0.0.20:9119", Username: "kayla"}, tokens: tokens{AccessToken: "a", RefreshToken: "r"}}
	h.servesDevice()
	defer func() { here.h = nil }()
	saved, savedRestart := bt, restartSoon
	restarted := false
	restartSoon = func() { restarted = true }
	defer func() { bt, restartSoon = saved, savedRestart }()
	bt = &bluetooth{open: true}
	defer display.update(func(st *screenState) { st.BT = nil })
	view := func() btScreen {
		bt.mu.Lock()
		defer bt.mu.Unlock()
		return *bt.view
	}
	bt.networkView()
	v := view()
	if !v.Items[1].Off || !v.Items[2].Off || v.Items[v.Sel].Label != "Status" || !strings.Contains(v.Items[v.Sel].Detail, "10.0.0.20:9119 as kayla") {
		t.Fatalf("logged in: %+v", v)
	}
	ev := func(action string) { bt.handle(map[string]any{"input": "knob", "action": action}) }
	ev("press")
	if !view().Holding {
		t.Fatal("no hold shown")
	}
	time.Sleep(100 * time.Millisecond)
	ev("release")
	time.Sleep(50 * time.Millisecond)
	if view().Holding || !h.signedIn() {
		t.Fatal("letting go early should do nothing")
	}
	if testing.Short() {
		return
	}
	ev("press")
	time.Sleep(logoutHold + 300*time.Millisecond)
	if h.signedIn() || !restarted {
		t.Fatal("holding didn't log out")
	}
	if _, err := os.Stat(filepath.Join(dir, "tokens.json")); err == nil {
		t.Fatal("tokens.json is still there")
	}
}
