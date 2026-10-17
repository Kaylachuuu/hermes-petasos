package main

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// slowReader gives its data a piece at a time, pausing before each.
type slowReader struct {
	data  []byte
	piece int
	pause time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(s.data) == 0 {
		return 0, io.EOF
	}
	time.Sleep(s.pause)
	n := copy(p[:min(len(p), s.piece)], s.data)
	s.data = s.data[n:]
	return n, nil
}

func TestReadAheadGivesTheWholeStream(t *testing.T) {
	want := bytes.Repeat([]byte("0123456789"), 5000)
	ended := make(chan error, 1)
	r := newReadAhead(&slowReader{data: want, piece: 1000, pause: time.Millisecond}, func(err error) { ended <- err })
	r.fill(20000, time.Second)
	if r.low(20000) {
		t.Error("fill came back without the cushion")
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}
	if err := <-ended; err != io.EOF {
		t.Errorf("ended with %v", err)
	}
}

func TestReadAheadCarriesOnWhenAbandoned(t *testing.T) {
	ended := make(chan error, 1)
	r := newReadAhead(&slowReader{data: make([]byte, 10000), piece: 100, pause: time.Millisecond}, func(err error) { ended <- err })
	r.abandon()
	select {
	case err := <-ended:
		if err != io.EOF {
			t.Errorf("ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the download stopped when abandoned")
	}
}

func TestReadAheadFillGivesUp(t *testing.T) {
	r := newReadAhead(&slowReader{data: make([]byte, 100), piece: 1, pause: time.Second}, func(error) {})
	defer r.abandon()
	start := time.Now()
	r.fill(1000, 100*time.Millisecond)
	if d := time.Since(start); d > time.Second {
		t.Errorf("fill waited %v", d)
	}
}
