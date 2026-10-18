package main

import "testing"

// The car's call button starts Athena; what BlueALSA answers is left to it, and anything else
// gets the ERROR it would have given.
func TestCarCallAnswers(t *testing.T) {
	for msg, want := range map[string]struct {
		reply string
		call  bool
	}{
		"AT+BVRA=1\r":       {"\r\nOK\r\n\r\n+BVRA: 0\r\n", true},
		"AT+BVRA=0\r":       {"\r\nOK\r\n", false},
		"AT+CKPD=200\r":     {"\r\nOK\r\n\r\n+BVRA: 0\r\n", true},
		"AT+CMER=3,0,0,1\r": {"", false},
		"AT+CIND=?\r":       {"", false},
		"AT+BCC\r":          {"", false},
		"AT+CLCC\r":         {"\r\nERROR\r\n", false},
		"AT+COPS=3,0\r":     {"\r\nERROR\r\n", false},
		"\r\nOK\r\n":        {"", false},
	} {
		if reply, call := carCallAnswer(msg); reply != want.reply || call != want.call {
			t.Errorf("%q: got %q %v", msg, reply, call)
		}
	}
}
