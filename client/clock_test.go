package main

import (
	"net/http"
	"testing"
	"time"
)

func TestAClockCloseToHermesIsLeftAlone(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Date": {time.Now().UTC().Format(http.TimeFormat)}}}
	before := time.Now()
	followHermesClock(resp) // within the slack: no change attempted
	if time.Since(before) > time.Second {
		t.Fatal("took too long")
	}
	followHermesClock(&http.Response{Header: http.Header{}}) // no Date: nothing
}
