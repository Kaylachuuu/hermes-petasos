package main

import (
	"log"

	"golang.org/x/sys/unix"
)

// routeSCOOverHCI asks the Car Thing's Broadcom Bluetooth chip to carry call audio (SCO, the
// car's microphone when it is chosen) over the same serial link as everything else. Broadcom
// chips send it to a separate PCM bus by default, which the Car Thing doesn't wire up, so
// without this a call opens but no sound arrives.
//
// It is the vendor command Write_SCO_PCM_Int_Param (OGF 0x3f, OCF 0x1c): routing 1 (HCI),
// then the PCM settings, unused once routed over HCI.
func routeSCOOverHCI() {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		log.Printf("can't route call audio over HCI: %v", err)
		return
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: 0, Channel: unix.HCI_CHANNEL_RAW}); err != nil {
		log.Printf("can't route call audio over HCI: %v", err)
		return
	}
	const opcode = 0x3f<<10 | 0x1c
	cmd := []byte{0x01, opcode & 0xff, opcode >> 8, 5, 0x01, 0x02, 0x00, 0x01, 0x01}
	if _, err := unix.Write(fd, cmd); err != nil {
		log.Printf("can't route call audio over HCI: %v", err)
		return
	}
	log.Printf("call audio routed over HCI")
}
