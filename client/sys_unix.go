//go:build !windows

package main

import (
	"os"
	"syscall"
)

// grabInput takes an input device for this program alone (EVIOCGRAB).
func grabInput(f *os.File) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgrab, 1); errno != 0 {
		return errno
	}
	return nil
}

// execReplace runs path in this process's place, as after an update.
func execReplace(path string, argv []string) error {
	return syscall.Exec(path, argv, os.Environ())
}
