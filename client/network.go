package main

import (
	"net/url"
	"strings"
)

// networkView is the Network tab: the server this Car Thing signs in to, and its tunnel home.
func (b *bluetooth) networkView() { b.networkViewAt(-1) }

func (b *bluetooth) networkViewAt(sel int) {
	h := hereNow()
	if h == nil {
		b.show(&btScreen{View: "message", Tab: "network", Title: "Not ready", Note: "Petasos is still starting."})
		return
	}
	server := h.cfg.Server
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		server = u.Host
	}
	status := "Signed in"
	if name := strings.TrimSpace(h.cfg.Username); name != "" {
		status = "Signed in as " + name
	}
	items := []btItem{{Kind: "header", Label: "Server"},
		{Kind: "row", Icon: "home", Label: "Address", Value: server},
		{Kind: "row", Icon: "key", Label: "Status", Value: status},
		{Kind: "header", Label: "VPN"}}
	if hasTunnel(h.dir) {
		items = append(items, btItem{Kind: "row", Icon: "shield", Label: "Tunnel home", Check: checked(vpnSetting(h.dir)),
			do: func() {
				if err := h.setVPN(!vpnSetting(h.dir)); err != nil {
					b.message("Couldn't save that", err.Error())
					return
				}
				b.networkViewAt(len(items) - 1)
			}})
	} else {
		items = append(items, btItem{Kind: "note", Label: "No tunnel home set up"})
	}
	b.show(&btScreen{View: "network", Tab: "network", Items: items, Sel: firstRow(items, sel),
		Note: "Signing in from here, and more than one VPN, come in the next update."})
}
