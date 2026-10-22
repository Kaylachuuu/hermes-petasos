package main

import (
	"encoding/binary"
	"testing"
)

// The AT lines in an RFCOMM frame, read straight from a monitor packet.
func TestHFLines(t *testing.T) {
	pkt := func(op uint16, data string) []byte {
		b := binary.LittleEndian.AppendUint16(nil, op)
		b = binary.LittleEndian.AppendUint16(b, 0)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(data)))
		return append(b, data...)
	}
	got := hfLines(pkt(monACLRx, "\x02\x20\x13\x00\x0f\x00\x41\x00\x0b\xef\x1bAT+BRSF=959\r\xa2"))
	if len(got) != 1 || got[0] != "→ Car Thing AT+BRSF=959" {
		t.Fatalf("%q", got)
	}
	got = hfLines(pkt(monACLTx, "\x02\x20\x13\x00\x0f\x00\x41\x00\x0b\xef\x1b\r\n+BRSF: 3874\r\n\r\nOK\r\n\x9a"))
	if len(got) != 2 || got[0] != "Car Thing → +BRSF: 3874" || got[1] != "Car Thing → OK" {
		t.Fatalf("%q", got)
	}
	got = hfLines(pkt(monACLRx, "\x02\x20\x13\x00\x0f\x00\x41\x00\x0b\xef)AT+CIND=?\r"))
	if len(got) != 1 || got[0] != "→ Car Thing AT+CIND=?" {
		t.Fatalf("after a printable length: %q", got)
	}
	acl := func(cid uint16, l2 string) string {
		b := []byte{0x02, 0x20, 0, 0, byte(len(l2)), 0, byte(cid), byte(cid >> 8)}
		return string(append(b, l2...))
	}
	got = hfLines(pkt(monACLRx, acl(1, "\x02\x05\x04\x00\x03\x00\x40\x00")))
	if len(got) != 1 || got[0] != "→ Car Thing asks for a channel: RFCOMM" {
		t.Fatalf("an L2CAP request: %q", got)
	}
	got = hfLines(pkt(monACLTx, acl(0x41, "\x0b\x3f\x01\x5c"))) // SABM on DLCI 2: channel 1
	if len(got) != 1 || got[0] != "Car Thing → RFCOMM opens channel 1" {
		t.Fatalf("an RFCOMM SABM: %q", got)
	}
	if got := hfLines(pkt(3, "AT+BRSF=1")); got != nil {
		t.Fatalf("an event isn't ACL data: %q", got)
	}
}
