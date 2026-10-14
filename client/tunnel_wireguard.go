//go:build darwin || windows || tunnel

package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// newTunnel starts a userspace WireGuard tunnel with its own network stack and returns how to
// dial through it. Only macOS builds carry it (the Car Thing uses the kernel's WireGuard).
func newTunnel(cfg *wgConfig) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	var addrs, dns []netip.Addr
	for _, a := range cfg.Addresses {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			addr, err2 := netip.ParseAddr(a)
			if err2 != nil {
				return nil, fmt.Errorf("Address %s: %v", a, err)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		addrs = append(addrs, p.Addr())
	}
	for _, d := range cfg.DNS {
		if a, err := netip.ParseAddr(d); err == nil {
			dns = append(dns, a)
		}
	}
	mtu := 1420
	if cfg.MTU != "" {
		if n, err := strconv.Atoi(cfg.MTU); err == nil {
			mtu = n
		}
	}
	uapi, err := cfg.uapi()
	if err != nil {
		return nil, err
	}
	tdev, tnet, err := netstack.CreateNetTUN(addrs, dns, mtu)
	if err != nil {
		return nil, err
	}
	dev := device.NewDevice(tdev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, "wireguard: "))
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("WireGuard didn't accept the config: %v", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return tnet.DialContext, nil
}
