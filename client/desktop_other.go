//go:build !linux

package main

import "errors"

// runDesktop is the Linux desktop app's (desktop_linux.go); the Mac and Windows apps have their own.
func runDesktop() error {
	return errors.New("petasos desktop is for Linux; the Mac and Windows apps are their own")
}
