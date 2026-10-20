package main

import (
	"log"
	"os"
	"strconv"
	"strings"
)

// The USB cable's network (the NCM gadget) takes 32 KB blocks of memory for each packet from the
// computer, in an interrupt, where the kernel can't make room. With the kernel's small default
// reserve (about 2.6 MB here), after an hour or so of use memory is too broken up and those
// packets are dropped: the computer still sees the Car Thing but can't reach it. A bigger reserve
// keeps blocks free for them.

const (
	minFreePath = "/proc/sys/vm/min_free_kbytes"
	minFreeKB   = 16384
)

func keepMemoryReserve() {
	if virtual {
		return
	}
	data, err := os.ReadFile(minFreePath)
	if err != nil {
		return
	}
	if now, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && now >= minFreeKB {
		return
	}
	if err := os.WriteFile(minFreePath, []byte(strconv.Itoa(minFreeKB)), 0o644); err != nil {
		log.Printf("couldn't keep a memory reserve for the USB cable: %v", err)
		return
	}
	log.Printf("memory reserve for the USB cable set to %d KB", minFreeKB)
}
