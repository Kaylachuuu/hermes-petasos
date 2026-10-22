package main

import (
	"encoding/binary"
	"log"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// traceHandsFree logs the hands-free conversation between a car and the Car Thing, both sides,
// from the kernel's Bluetooth monitor (what btmon reads): the AT commands the car sends and what
// BlueALSA and petasos answer. petasos sees only the commands BlueALSA passes on, and when a car
// won't take the Car Thing for a phone, the log shows how far the two get.
func traceHandsFree() {
	for ; ; time.Sleep(30 * time.Second) {
		fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
		if err != nil {
			log.Printf("hands-free trace: %v", err)
			return
		}
		if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: 0xffff, Channel: unix.HCI_CHANNEL_MONITOR}); err != nil {
			log.Printf("hands-free trace: %v", err)
			unix.Close(fd)
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := unix.Read(fd, buf)
			if err != nil {
				log.Printf("hands-free trace stopped: %v", err)
				break
			}
			for _, line := range hfLines(buf[:n]) {
				log.Printf("hf: %s", line)
			}
		}
		unix.Close(fd)
	}
}

// The monitor's packets: a header (opcode, controller, length), then the packet; ACL data going
// out is opcode 4, coming in 5.
const (
	monACLTx = 4
	monACLRx = 5
)

// atLine is a command or an answer, found anywhere in a run of text: the frame's length byte
// before it may be printable.
var atLine = regexp.MustCompile(`AT[+A-Z&].*|\+[A-Z]+:.*|^OK$|^ERROR$|^RING$`)

// hfLines finds the AT lines in a monitor packet: the text in an RFCOMM frame, which needs no
// decoding to read.
func hfLines(pkt []byte) []string {
	if len(pkt) < 6 || len(pkt) > 6+300 { // AT frames are short; the music's packets aren't
		return nil
	}
	op := binary.LittleEndian.Uint16(pkt)
	if op != monACLTx && op != monACLRx {
		return nil
	}
	who := map[uint16]string{monACLTx: "Car Thing →", monACLRx: "→ Car Thing"}[op]
	var out []string
	for _, run := range strings.FieldsFunc(string(pkt[6:]), func(r rune) bool { return r < 0x20 || r > 0x7e }) {
		if m := atLine.FindString(run); m != "" {
			out = append(out, who+" "+m)
		}
	}
	return out
}
