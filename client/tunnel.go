package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// The Mac app's VPN: a WireGuard tunnel inside the program itself (tunnel_wireguard.go), which
// carries only Petasos's own connections to Hermes. It needs no administrator rights and leaves
// the rest of the Mac alone. vpn.json in the app's folder says whether it is on, and when:
// "away" uses it only while Hermes can't be reached directly (at home it can), "always" for
// every connection. The WireGuard config is wireguard.conf beside it.
//
// The Car Thing has its own system tunnel (wireguard.go) and never routes through this.

type vpnSettings struct {
	On   bool   `json:"on"`
	When string `json:"when"` // "away" (the default) or "always"
}

var route struct {
	tunnel func(ctx context.Context, network, addr string) (net.Conn, error)
	use    atomic.Bool
}

// dialHermes is how every connection to Hermes is made: through the tunnel while it's in use,
// otherwise directly.
func dialHermes(ctx context.Context, network, addr string) (net.Conn, error) {
	if route.tunnel != nil && route.use.Load() {
		return route.tunnel(ctx, network, addr)
	}
	return (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
}

// startVPN brings the tunnel up when vpn.json turns it on, and keeps choosing between it and
// the direct way.
func (h *hermes) startVPN() {
	var set vpnSettings
	if err := readJSON(filepath.Join(h.dir, "vpn.json"), &set); err != nil || !set.On {
		return
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, "wireguard.conf"))
	if err == nil {
		var cfg *wgConfig
		if cfg, err = parseWireGuard(raw); err == nil {
			route.tunnel, err = newTunnel(cfg)
		}
	}
	if err != nil {
		log.Printf("VPN not started: %v", err)
		app.mu.Lock()
		app.vpnError = err.Error()
		app.mu.Unlock()
		display.notice(fmt.Sprintf("The VPN didn't start: %v", err))
		return
	}
	log.Printf("VPN ready (WireGuard, %s)", map[bool]string{true: "always", false: "when away"}[set.When == "always"])
	if set.When == "always" {
		h.useTunnel(true)
		return
	}
	h.chooseRoute() // the first choice now, before anything connects
	go func() {
		for {
			time.Sleep(15 * time.Second)
			h.chooseRoute()
		}
	}()
}

// chooseRoute uses the tunnel only while Hermes doesn't answer directly.
func (h *hermes) chooseRoute() {
	u, err := url.Parse(h.cfg.Server)
	if err != nil {
		return
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), map[bool]string{true: "443", false: "80"}[u.Scheme == "https"])
	}
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err == nil {
		c.Close()
	}
	h.useTunnel(err != nil)
}

func (h *hermes) useTunnel(on bool) {
	if route.use.Swap(on) == on {
		return
	}
	log.Printf("reaching Hermes %s", map[bool]string{true: "through the VPN", false: "directly"}[on])
	if t, ok := h.http.Transport.(*http.Transport); ok {
		t.CloseIdleConnections() // so nothing keeps using the old way
	}
	display.update(func(st *screenState) { st.Links.Tunnel = on })
}

// checkVPNConfig is for the app's settings window (petasos vpncheck FILE): whether the WireGuard
// config in path would work.
func checkVPNConfig(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cfg, err := parseWireGuard(raw)
	if err != nil {
		return err
	}
	_, err = cfg.uapi()
	return err
}
