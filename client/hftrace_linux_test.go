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
	if got := hfLines(pkt(3, "AT+BRSF=1")); got != nil {
		t.Fatalf("an event isn't ACL data: %q", got)
	}
}
