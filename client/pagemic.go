package main

import (
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net/http"
	"runtime"
	"strconv"
	"sync"
)

// The Mac app's microphone is its own window: the screen page captures it with the browser's
// getUserMedia, as the Hermes desktop app does, and posts the audio here. Apple's audio engine,
// which the helper used before, stalls when AirPods switch into call mode as recording starts;
// the browser's capture copes with that.
//
// The page opens the microphone while the state says mic_open, and posts 16-bit mono samples
// at its own rate to /mic?id=N&rate=R, one request at a time; /mic answers 410 once that
// recording is over. A failure to open it is posted as /mic?id=N&error=....

// pageMicOn says whether recordings come from the page: the Mac app's own window.
var pageMicOn = func() bool { return virtual && runtime.GOOS == "darwin" }

var pageMic pageMics

type pageMics struct {
	mu   sync.Mutex
	cur  *pageMicSource
	next int
}

type pageMicSource struct {
	id   int
	r    *io.PipeReader
	w    *io.PipeWriter
	once sync.Once
	done chan struct{}

	mu   sync.Mutex
	why  string
	pos  float64 // where the next sample at sampleRate falls in carry
	keep []int16 // input samples not yet used
}

// open starts a recording from the page.
func (p *pageMics) open() (micSource, io.Reader, error) {
	r, w := io.Pipe()
	p.mu.Lock()
	if p.cur != nil {
		p.cur.close()
	}
	p.next++
	src := &pageMicSource{id: p.next, r: r, w: w, done: make(chan struct{})}
	p.cur = src
	p.mu.Unlock()
	display.update(func(st *screenState) {
		st.MicOpen, st.MicID, st.MicName = true, src.id, loadAudioSettings(appDir).MicName
	})
	return src, r, nil
}

func (s *pageMicSource) close() {
	s.once.Do(func() {
		s.w.Close()
		close(s.done)
		pageMic.mu.Lock()
		if pageMic.cur == s {
			pageMic.cur = nil
		}
		pageMic.mu.Unlock()
		display.update(func(st *screenState) {
			if st.MicID == s.id {
				st.MicOpen = false
			}
		})
	})
}

func (s *pageMicSource) stop()       { s.close() }
func (s *pageMicSource) kill()       { s.close() }
func (s *pageMicSource) wait() error { <-s.done; return nil }
func (s *pageMicSource) problem() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.why == "" {
		return "the app's window sent no audio"
	}
	return s.why
}

// convert turns the page's samples at rate into what a recorder gives: 32-bit samples at
// sampleRate, each the average of the input samples it stands for.
func (s *pageMicSource) convert(in []int16, rate int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := append(s.keep, in...)
	step := float64(rate) / sampleRate
	var out []byte
	for s.pos+step <= float64(len(all)) {
		from, to := int(s.pos), int(s.pos+step)
		if to <= from {
			to = from + 1
		}
		var sum int
		for _, v := range all[from:to] {
			sum += int(v)
		}
		out = binary.LittleEndian.AppendUint32(out, uint32(int32(sum/(to-from))<<16))
		s.pos += step
	}
	used := int(s.pos)
	s.keep = append(s.keep[:0:0], all[used:]...)
	s.pos -= float64(used)
	return out
}

// pagePost is /mic: a piece of the recording, or the page's report that it couldn't record.
func pagePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	pageMic.mu.Lock()
	src := pageMic.cur
	pageMic.mu.Unlock()
	if src == nil || src.id != id {
		w.WriteHeader(http.StatusGone) // that recording is over
		return
	}
	if why := r.URL.Query().Get("error"); why != "" {
		log.Printf("the app's window couldn't open the microphone: %s", truncate(why, 300))
		src.mu.Lock()
		src.why = why
		src.mu.Unlock()
		src.close()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	rate, _ := strconv.Atoi(r.URL.Query().Get("rate"))
	if rate < 8000 || rate > 192000 {
		http.Error(w, "bad rate", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return
	}
	samples := make([]int16, len(body)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(body[2*i:]))
	}
	if _, err := src.w.Write(src.convert(samples, rate)); err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			w.WriteHeader(http.StatusGone)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
