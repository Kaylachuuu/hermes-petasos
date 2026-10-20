//go:build linux

package main

import (
	"syscall"
	"time"
)

func setSystemClock(t time.Time) error {
	tv := syscall.NsecToTimeval(t.UnixNano())
	return syscall.Settimeofday(&tv)
}
