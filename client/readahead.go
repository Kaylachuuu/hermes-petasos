package main

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// readAhead downloads a track ahead of where it is playing, so a moment's slowness (the VPN, the
// phone changing networks) is taken from what's already here rather than heard as the speaker
// running dry. It comes as fast as the network allows until lead bytes are held, then at rate:
// the phone's internet and the car's sound share the Car Thing's one Bluetooth radio, and a
// download going flat out takes the airtime the car's sound needs, which the car hears as skips.
type readAhead struct {
	mu        sync.Mutex
	cond      *sync.Cond
	buf       bytes.Buffer // read but not yet played
	err       error        // how the download ended (io.EOF: all of it)
	abandoned bool         // no longer played: what still comes is dropped
	got       time.Time    // when the last of it came
	rate      int          // bytes a second once lead is held (or it's abandoned); 0 for no limit
	lead      int
}

// abandonedRate paces a download no longer played, which carries on to be kept, when no rate
// was set: about twice a 192 kbps song's.
const abandonedRate = 48 << 10

// pace sets the rate the download keeps to once lead bytes are held unplayed.
func (r *readAhead) pace(rate, lead int) {
	r.mu.Lock()
	r.rate, r.lead = rate, lead
	r.cond.Broadcast()
	r.mu.Unlock()
}

// wait is how long to pause after n bytes came, to keep to the rate.
func (r *readAhead) wait(n int) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	rate := r.rate
	if r.abandoned && rate == 0 {
		rate = abandonedRate
	}
	if rate <= 0 || n <= 0 || !r.abandoned && r.buf.Len() <= r.lead {
		return 0
	}
	return time.Duration(n) * time.Second / time.Duration(rate)
}

// readAheadMax is the most held unplayed, enough for a whole song to be in before it starts
// (the Music tab's Buffer before playing); the download waits beyond it.
const readAheadMax = 64 << 20

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
			r.got = time.Now()
			if err != nil {
				r.err = err
			}
			r.cond.Broadcast()
			r.mu.Unlock()
			if err != nil {
				done(err)
				return
			}
			// Not reading leaves the rest in the network's own buffers, so the phone stops sending.
			time.Sleep(r.wait(n))
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

// fill waits until at least want bytes are held, the download has ended, or nothing more has
// come for stall: a slow download is waited for as long as it keeps coming.
func (r *readAhead) fill(want int, stall time.Duration) {
	tick := time.NewTicker(min(stall/4, 250*time.Millisecond))
	defer tick.Stop()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				r.mu.Lock()
				r.cond.Broadcast()
				r.mu.Unlock()
			}
		}
	}()
	began := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	// Waiting from when it was asked for, or from when the last of it came if that's later.
	for r.buf.Len() < want && r.err == nil && time.Since(later(r.got, began)) < stall {
		r.cond.Wait()
	}
}

// later is the later of two times.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// head is a copy of up to n of the bytes held, from the start of what is unplayed.
func (r *readAhead) head(n int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf.Bytes()[:min(n, r.buf.Len())]...)
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
