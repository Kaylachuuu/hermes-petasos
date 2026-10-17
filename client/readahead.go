package main

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// readAhead downloads a track as fast as the network allows, ahead of where it is playing, so a
// moment's slowness (the VPN, the phone changing networks) is taken from what's already here
// rather than heard as the speaker running dry.
type readAhead struct {
	mu        sync.Mutex
	cond      *sync.Cond
	buf       bytes.Buffer // read but not yet played
	err       error        // how the download ended (io.EOF: all of it)
	abandoned bool         // no longer played: what still comes is dropped
}

// readAheadMax is the most held unplayed; the download waits beyond it.
const readAheadMax = 16 << 20

// newReadAhead starts reading src to its end; done is called with how that went.
func newReadAhead(src io.Reader, done func(error)) *readAhead {
	r := &readAhead{}
	r.cond = sync.NewCond(&r.mu)
	go func() {
		chunk := make([]byte, 32<<10)
		for {
			n, err := src.Read(chunk)
			r.mu.Lock()
			for !r.abandoned && r.buf.Len() > readAheadMax {
				r.cond.Wait()
			}
			if !r.abandoned {
				r.buf.Write(chunk[:n])
			}
			if err != nil {
				r.err = err
			}
			r.cond.Broadcast()
			r.mu.Unlock()
			if err != nil {
				done(err)
				return
			}
		}
	}()
	return r
}

func (r *readAhead) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.buf.Len() == 0 && r.err == nil {
		r.cond.Wait()
	}
	if r.buf.Len() > 0 {
		n, _ := r.buf.Read(p)
		r.cond.Broadcast()
		return n, nil
	}
	return 0, r.err
}

// fill waits until at least want bytes are held, the download has ended, or wait has passed.
func (r *readAhead) fill(want int, wait time.Duration) {
	timer := time.AfterFunc(wait, func() {
		r.mu.Lock()
		r.cond.Broadcast()
		r.mu.Unlock()
	})
	defer timer.Stop()
	until := time.Now().Add(wait)
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.buf.Len() < want && r.err == nil && time.Now().Before(until) {
		r.cond.Wait()
	}
}

// low is whether fewer than want bytes are held with more still to come.
func (r *readAhead) low(want int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err == nil && r.buf.Len() < want
}

// abandon drops what's held: the track stopped playing. The download carries on to its end.
func (r *readAhead) abandon() {
	r.mu.Lock()
	r.abandoned = true
	r.buf = bytes.Buffer{}
	r.cond.Broadcast()
	r.mu.Unlock()
}
