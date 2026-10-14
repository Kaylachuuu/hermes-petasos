package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// The tunnel home: the phone's hotspot reaches the internet but not the home network (phones
// don't send hotspot traffic through their own VPN), so the Car Thing has a WireGuard peer of its
// own. The installer copies the peer's config, in the usual wg-quick format, to
// <dir>/wireguard.conf; this turns it into a systemd-networkd tunnel under /run (the system
// partition is read-only) and has networkd load it. networkd then keeps the tunnel up and
// re-resolves the endpoint by itself as the Bluetooth link comes and goes.

const (
	wgName    = "wg-petasos"
	wgNetDev  = "/run/systemd/network/60-petasos-wg.netdev"
	wgNetwork = "/run/systemd/network/60-petasos-wg.network"
)

type wgPeer struct{ PublicKey, PresharedKey, AllowedIPs, Endpoint, Keepalive string }

type wgConfig struct {
	PrivateKey, ListenPort, MTU string
	Addresses                   []string
	Peers                       []wgPeer
}

// setUpTunnel brings up the tunnel when <dir>/wireguard.conf exists. Problems are logged and
// leave the Car Thing without a tunnel rather than stopping the program.
func setUpTunnel(dir string) {
	raw, err := os.ReadFile(filepath.Join(dir, "wireguard.conf"))
	if os.IsNotExist(err) {
		log.Printf("no wireguard.conf, so no tunnel home; the installer's --wireguard option adds one")
		return
	}
	if err != nil {
		log.Printf("can't read wireguard.conf: %v", err)
		return
	}
	cfg, err := parseWireGuard(raw)
	if err != nil {
		log.Printf("wireguard.conf: %v; no tunnel home", err)
		return
	}
	netdev, network := cfg.networkd()
	changed, err := writeIfChanged(wgNetDev, netdev, 0o640)
	if err == nil {
		var c bool
		c, err = writeIfChanged(wgNetwork, network, 0o644)
		changed = changed || c
	}
	if err != nil {
		log.Printf("can't set up the tunnel home: %v", err)
		return
	}
	// networkd reads the private key as its own user, so the netdev file belongs to its group.
	if g, err := user.LookupGroup("systemd-network"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			_ = os.Chown(wgNetDev, 0, gid)
		}
	}
	if !changed {
		return
	}
	if out, err := exec.Command("networkctl", "reload").CombinedOutput(); err != nil {
		log.Printf("networkctl reload failed: %v: %s", err, strings.TrimSpace(string(out)))
		return
	}
	log.Printf("tunnel home set up (%s, %s)", wgName, strings.Join(cfg.Addresses, ", "))
}

// parseWireGuard reads a wg-quick style config: [Interface] with PrivateKey and Address, then one
// or more [Peer] sections. Keys it doesn't use (DNS, PostUp and the like) are ignored.
func parseWireGuard(raw []byte) (*wgConfig, error) {
	cfg := &wgConfig{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[] "))
			if section == "peer" {
				cfg.Peers = append(cfg.Peers, wgPeer{})
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d isn't key = value", n)
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch section {
		case "interface":
			switch key {
			case "privatekey":
				cfg.PrivateKey = value
			case "address":
				for _, a := range strings.Split(value, ",") {
					if a = strings.TrimSpace(a); a != "" {
						cfg.Addresses = append(cfg.Addresses, a)
					}
				}
			case "listenport":
				cfg.ListenPort = value
			case "mtu":
				cfg.MTU = value
			}
		case "peer":
			p := &cfg.Peers[len(cfg.Peers)-1]
			switch key {
			case "publickey":
				p.PublicKey = value
			case "presharedkey":
				p.PresharedKey = value
			case "allowedips":
				p.AllowedIPs = value
			case "endpoint":
				p.Endpoint = value
			case "persistentkeepalive":
				p.Keepalive = value
			}
		}
	}
	switch {
	case cfg.PrivateKey == "":
		return nil, fmt.Errorf("no PrivateKey under [Interface]")
	case len(cfg.Addresses) == 0:
		return nil, fmt.Errorf("no Address under [Interface]")
	case len(cfg.Peers) == 0:
		return nil, fmt.Errorf("no [Peer]")
	}
	for i, p := range cfg.Peers {
		if p.PublicKey == "" || p.AllowedIPs == "" {
			return nil, fmt.Errorf("peer %d needs PublicKey and AllowedIPs", i+1)
		}
		for _, ip := range strings.Split(p.AllowedIPs, ",") {
			// Sending everything home would also send the tunnel's own traffic into itself; the
			// phone carries ordinary internet traffic anyway.
			if ip = strings.TrimSpace(ip); ip == "0.0.0.0/0" || ip == "::/0" {
				return nil, fmt.Errorf("AllowedIPs sends everything home (%s); list only the home network, such as 10.0.0.0/24", ip)
			}
		}
	}
	return cfg, nil
}

// networkd renders the .netdev and .network files.
func (c *wgConfig) networkd() (netdev, network []byte) {
	var d bytes.Buffer
	fmt.Fprintf(&d, "# Written by petasos from %s; changes here are lost on restart.\n", "wireguard.conf")
	fmt.Fprintf(&d, "[NetDev]\nName=%s\nKind=wireguard\n", wgName)
	if c.MTU != "" {
		fmt.Fprintf(&d, "MTUBytes=%s\n", c.MTU)
	}
	fmt.Fprintf(&d, "\n[WireGuard]\nPrivateKey=%s\nRouteTable=main\n", c.PrivateKey)
	if c.ListenPort != "" {
		fmt.Fprintf(&d, "ListenPort=%s\n", c.ListenPort)
	}
	for _, p := range c.Peers {
		fmt.Fprintf(&d, "\n[WireGuardPeer]\nPublicKey=%s\nAllowedIPs=%s\n", p.PublicKey, p.AllowedIPs)
		if p.PresharedKey != "" {
			fmt.Fprintf(&d, "PresharedKey=%s\n", p.PresharedKey)
		}
		if p.Endpoint != "" {
			fmt.Fprintf(&d, "Endpoint=%s\n", p.Endpoint)
		}
		if p.Keepalive != "" {
			fmt.Fprintf(&d, "PersistentKeepalive=%s\n", p.Keepalive)
		}
	}
	var n bytes.Buffer
	fmt.Fprintf(&n, "# Written by petasos from wireguard.conf.\n[Match]\nName=%s\n\n[Network]\n", wgName)
	for _, a := range c.Addresses {
		fmt.Fprintf(&n, "Address=%s\n", a)
	}
	return d.Bytes(), n.Bytes()
}

func writeIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
