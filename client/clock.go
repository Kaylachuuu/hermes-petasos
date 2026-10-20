package main

import (
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

// The Car Thing has no battery-backed clock, and the image keeps no network time (its
// superbird-clock only moves the time forward to when it last ran), so after a power cycle its
// clock is behind. Hermes's answers carry the time (their Date header); a Car Thing more than a
// minute off takes it. superbird-clock's own tick then keeps it for the next boot.

const clockSlack = time.Minute

var clockSetOnce atomic.Bool

func followHermesClock(resp *http.Response) {
	if virtual || resp == nil {
		return
	}
	at, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return
	}
	off := at.Sub(time.Now())
	if off < clockSlack && off > -clockSlack {
		return
	}
	if err := setSystemClock(at); err != nil {
		if !clockSetOnce.Swap(true) {
			log.Printf("couldn't set the clock from Hermes: %v", err)
		}
		return
	}
	log.Printf("clock set from Hermes (it was %s off)", off.Round(time.Second))
}
