package main

import "testing"

func TestMgmtReply(t *testing.T) {
	// Command Complete for Set Device Class on controller 0: status 0, then the class.
	ok := []byte{0x01, 0x00, 0x00, 0x00, 0x06, 0x00, 0x0e, 0x00, 0x00, 0x14, 0x04, 0x20}
	if status, mine := mgmtReply(ok, 0x000e); !mine || status != 0 {
		t.Errorf("Command Complete: got %d, %v", status, mine)
	}
	failed := []byte{0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x0e, 0x00, 0x0d}
	if status, mine := mgmtReply(failed, 0x000e); !mine || status != 0x0d {
		t.Errorf("Command Status: got %d, %v", status, mine)
	}
	other := []byte{0x01, 0x00, 0x00, 0x00, 0x03, 0x00, 0x05, 0x00, 0x00}
	if _, mine := mgmtReply(other, 0x000e); mine {
		t.Error("another command's reply was taken for ours")
	}
	if _, mine := mgmtReply([]byte{0x01, 0x00}, 0x000e); mine {
		t.Error("a short event was read")
	}
}
