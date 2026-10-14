//go:build !darwin && !tunnel

package main

import (
	"context"
	"errors"
	"net"
)

// newTunnel: only the Mac app has a tunnel of its own; the Car Thing uses the kernel's WireGuard.
func newTunnel(*wgConfig) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	return nil, errors.New("this build has no VPN of its own")
}
