package main

import (
	"encoding/binary"
	"fmt"
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
	if ev := linkEvent(pkt[6:]); ev != "" {
		out = append(out, who+" "+ev)
	}
	for _, run := range strings.FieldsFunc(string(pkt[6:]), func(r rune) bool { return r < 0x20 || r > 0x7e }) {
		if m := atLine.FindString(run); m != "" {
			out = append(out, who+" "+m)
		}
	}
	return out
}

// linkEvent is what sets up a hands-free connection, below its AT commands: a channel asked for
// (SDP, the lookup of what a device offers, or RFCOMM, which hands-free runs over), and RFCOMM's
// own opening and closing of a channel. ACL data: a 4-byte header, then L2CAP's (length, channel).
func linkEvent(acl []byte) string {
	if len(acl) < 8 {
		return ""
	}
	cid := binary.LittleEndian.Uint16(acl[6:])
	l2 := acl[8:]
	if cid == 1 { // signalling: a Connection Request (code 2) names the protocol (PSM)
		if len(l2) >= 8 && l2[0] == 0x02 {
			psm := binary.LittleEndian.Uint16(l2[4:])
			name := map[uint16]string{1: "SDP (looking up what it offers)", 3: "RFCOMM", 0x17: "AVCTP", 0x19: "AVDTP"}[psm]
			if name == "" {
				name = fmt.Sprintf("PSM 0x%x", psm)
			}
			return "asks for a channel: " + name
		}
		return ""
	}
	// An RFCOMM frame with no data: address (DLCI above the two low bits), control, length 0.
	if cid < 0x40 || len(l2) < 4 || l2[0]&1 == 0 || l2[2] != 0x01 {
		return ""
	}
	kind := map[byte]string{0x2f: "opens", 0x63: "accepts", 0x0f: "refuses", 0x43: "closes"}[l2[1]&^0x10]
	if kind == "" {
		return ""
	}
	dlci := l2[0] >> 2
	if dlci == 0 {
		return "RFCOMM " + kind + " the link"
	}
	return fmt.Sprintf("RFCOMM %s channel %d", kind, dlci>>1)
}
