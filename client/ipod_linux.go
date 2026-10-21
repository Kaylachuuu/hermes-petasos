package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ipodMount is where an iPod (or a USB drive) plugged into the Car Thing is mounted, read-only.
const ipodMount = "/run/petasos/ipod"

// ipodFormats are the disk formats tried: an iPod set up on Windows is FAT32, on a Mac HFS+
// (mounted read-only by Linux anyway while it has a journal); a drive may be exFAT.
var ipodFormats = []string{"vfat", "hfsplus", "exfat"}

// watchIPod mounts a USB drive as it is plugged in and reads its music, and lets it go as it
// is pulled out.
func watchIPod() {
	_ = unix.Unmount(ipodMount, unix.MNT_DETACH) // one left from before a restart
	disk := ""
	tries := map[string]int{} // a disk with nothing to mount is tried a few times, as it settles
	for {
		disks, _ := filepath.Glob("/sys/block/sd*")
		sort.Strings(disks)
		if disk != "" && !contains(disks, disk) {
			log.Printf("ipod: %s was unplugged", filepath.Base(disk))
			_ = unix.Unmount(ipodMount, unix.MNT_DETACH)
			disk = ""
			ipodState.mu.Lock()
			ipodState.root, ipodState.lib, ipodState.problem = "", nil, ""
			ipodState.mu.Unlock()
			if library.playingFrom(ipodSource) {
				library.control("stop")
			}
		}
		for d := range tries {
			if !contains(disks, d) {
				delete(tries, d)
			}
		}
		if disk == "" {
			for _, d := range disks {
				if tries[d] >= 5 {
					continue
				}
				tries[d]++
				if mountIPod(d) {
					disk = d
					go readMountedIPod()
					break
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// mountIPod mounts the first of a disk's partitions (or the disk itself) holding a format it
// knows.
func mountIPod(disk string) bool {
	name := filepath.Base(disk)
	parts, _ := filepath.Glob(filepath.Join(disk, name+"*"))
	sort.Strings(parts)
	devs := []string{}
	for _, p := range parts {
		devs = append(devs, "/dev/"+filepath.Base(p))
	}
	devs = append(devs, "/dev/"+name)
	if err := os.MkdirAll(ipodMount, 0o755); err != nil {
		log.Printf("ipod: %v", err)
		return false
	}
	for _, dev := range devs {
		for _, kind := range ipodFormats {
			opts := ""
			if kind == "vfat" || kind == "exfat" {
				opts = "utf8"
				if kind == "exfat" {
					opts = "iocharset=utf8"
				}
			}
			err := unix.Mount(dev, ipodMount, kind, unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, opts)
			if err == nil {
				log.Printf("ipod: mounted %s (%s) read-only", dev, kind)
				return true
			}
		}
	}
	return false
}

func readMountedIPod() {
	ipodState.mu.Lock()
	ipodState.reading, ipodState.problem = true, ""
	ipodState.mu.Unlock()
	started := time.Now()
	lib, err := readIPod(ipodMount)
	ipodState.mu.Lock()
	ipodState.reading = false
	if err != nil {
		ipodState.problem = "Can't read the iPod: " + err.Error()
		log.Printf("ipod: %v", err)
	} else {
		ipodState.root, ipodState.lib = ipodMount, lib
		log.Printf("ipod: %q, %d songs and %d playlists from its %s (%d protected), in %v", lib.Name,
			len(lib.Tracks), len(lib.Playlists), lib.From, lib.Protected, time.Since(started).Round(time.Millisecond))
	}
	ipodState.mu.Unlock()
	if err == nil {
		name := strings.TrimSpace(lib.Name)
		if name == "" {
			name = "The iPod"
		}
		display.notice(name + " is plugged in.")
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
