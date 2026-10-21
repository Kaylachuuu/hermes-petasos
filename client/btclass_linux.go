package main

import (
	"encoding/binary"
	"log"
	"time"

	"golang.org/x/sys/unix"
)

// The Class of Device the Car Thing gives itself: Audio/Video, Loudspeaker. A phone decides from
// it whether a device is a speaker. BlueZ's own default is "uncategorized", and an iPhone then
// never offers the Car Thing as somewhere to play its music, though its A2DP sink is there
// (BlueALSA's -p a2dp-sink). The phone reads the class when it pairs, so a phone paired before
// has to forget the Car Thing and pair again to see it.
//
// A car may offer its hands-free (the call button, its phone screens) only to a device that says
// it is a phone, so the Bluetooth tab can make it Phone, Smartphone instead (Class in
// device.json). A phone paired while it was a speaker keeps it as one.
const (
	classMajorAudio       = 0x04
	classMinorLoudspeaker = 0x05 << 2 // the minor class sits above two format bits
	classMajorPhone       = 0x02
	classMinorSmartphone  = 0x03 << 2
)

// setSpeakerClass sets the Car Thing's Class of Device, as the Bluetooth tab chooses, through the
// kernel's Bluetooth management interface (Set Device Class, opcode 0x000e, on controller 0), as
// btmgmt's "class 4 5" does. It changes no connection. bluetoothd sets the class only when it
// starts, so it holds until then.
func setSpeakerClass() {
	major, minor, name := byte(classMajorAudio), byte(classMinorLoudspeaker), "loudspeaker"
	if h := hereNow(); h != nil && loadOwnSettings(h.dir).Class == "phone" {
		major, minor, name = classMajorPhone, classMinorSmartphone, "phone"
	}
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		log.Printf("can't set the Bluetooth class: %v", err)
		return
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: 0xffff, Channel: unix.HCI_CHANNEL_CONTROL}); err != nil {
		log.Printf("can't set the Bluetooth class: %v", err)
		return
	}
	const opcode = 0x000e
	cmd := []byte{opcode, 0, 0, 0, 2, 0, major, minor} // opcode, index 0, length 2
	if _, err := unix.Write(fd, cmd); err != nil {
		log.Printf("can't set the Bluetooth class: %v", err)
		return
	}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2})
	buf := make([]byte, 512)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		n, err := unix.Read(fd, buf)
		if err != nil {
			log.Printf("no answer setting the Bluetooth class: %v", err)
			return
		}
		if status, ok := mgmtReply(buf[:n], opcode); ok {
			if status != 0 {
				log.Printf("can't set the Bluetooth class: management status %d", status)
			} else {
				log.Printf("Bluetooth class set to %s", name)
			}
			return
		}
	}
}

// mgmtReply reads a management event: the status, when it is the Command Complete (0x0001) or
// Command Status (0x0002) for the command opcode.
func mgmtReply(ev []byte, opcode uint16) (byte, bool) {
	if len(ev) < 9 {
		return 0, false
	}
	code := binary.LittleEndian.Uint16(ev)
	if (code != 0x0001 && code != 0x0002) || binary.LittleEndian.Uint16(ev[6:]) != opcode {
		return 0, false
	}
	return ev[8], true
}
