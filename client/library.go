package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"

	mp3 "github.com/hajimehoshi/go-mp3"
)

// The home library: music Athena puts on from Navidrome or the music folders on Hermes (the
// plugin's play_music tool). The plugin sends the queue, and each track comes from its
// /music/stream route as MP3, decoded here and played on the speaker: the car's, through
// BlueALSA, on the Car Thing; the computer's in the apps.
//
// It shares the speaker with Athena like the phone's music does: it stops as soon as she
// starts listening and carries on from where it was once she is done. Stopping and carrying on
// opens the track again and skips what was already heard, so nothing is held open meanwhile.

type libTrack struct {
	Source  string `json:"source"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Artist  string `json:"artist"`
	Album   string `json:"album"`
	Seconds int    `json:"seconds"`
}

const libraryRate = speakerRate // 48 kHz stereo, what BlueALSA needs (see toSpeakerFormat)

type libraryPlayer struct {
	mu     sync.Mutex
	h      *hermes
	v      *voice
	queue  []libTrack
	pos    int    // the track playing
	frame  int    // how far into it, in frames at libraryRate
	title  string // what Athena called it: "Rumours by Fleetwood Mac"
	want   string // "playing", "paused" or "" (nothing queued)
	held   bool   // Athena has the speaker
	run    *libRun
	failed int // tracks in a row that wouldn't play
}

type libRun struct {
	stop chan struct{}
	done chan struct{}
}

var library = &libraryPlayer{}

func (l *libraryPlayer) attach(h *hermes, v *voice) {
	l.mu.Lock()
	l.h, l.v = h, v
	l.mu.Unlock()
}

// active is whether the library has the music: something is playing or paused.
func (l *libraryPlayer) active() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.want != ""
}

// start plays the queue the plugin sent: {"op":"play","title":...,"tracks":[...]}.
func (l *libraryPlayer) start(title string, tracks []libTrack) {
	if len(tracks) == 0 {
		return
	}
	music.yieldToLibrary()
	l.halt()
	l.mu.Lock()
	l.queue, l.pos, l.frame, l.title, l.want, l.failed = tracks, 0, 0, title, "playing", 0
	l.held = display.snapshot().Phase != "idle" // it waits for Athena to finish her reply
	l.mu.Unlock()
	log.Printf("playing %s (%d songs)", title, len(tracks))
	l.kick()
}

// control is pause, resume, toggle, play, next, previous or stop, from Athena or a button.
func (l *libraryPlayer) control(op string) {
	l.mu.Lock()
	restart := false
	switch op {
	case "pause":
		if l.want != "" {
			l.want = "paused"
		}
	case "resume", "play":
		if l.want != "" {
			l.want = "playing"
		}
	case "toggle":
		if l.want == "playing" {
			l.want = "paused"
		} else if l.want != "" {
			l.want = "playing"
		}
	case "next":
		l.pos, l.frame, restart = l.pos+1, 0, true
	case "previous":
		if l.frame < 3*libraryRate { // a few seconds in, back to the start of this one instead
			l.pos = max(0, l.pos-1)
		}
		l.frame, restart = 0, true
	case "stop":
		l.want, l.queue = "", nil
	}
	if l.pos >= len(l.queue) {
		l.want, l.queue = "", nil
	}
	l.mu.Unlock()
	if restart {
		l.halt()
	}
	l.kick()
}

// phaseChanged follows Athena: the music stops while she listens, thinks and speaks.
func (l *libraryPlayer) phaseChanged(phase string) {
	l.mu.Lock()
	held := phase != "idle"
	changed := l.held != held
	l.held = held
	l.mu.Unlock()
	if changed {
		go l.kick()
	}
}

// freeSpeaker is called just before Athena plays: the music must have let go of the speaker.
func (l *libraryPlayer) freeSpeaker() {
	l.mu.Lock()
	l.held = true
	l.mu.Unlock()
	l.kick()
}

// kick starts or stops playing to match what is wanted, and shows it.
func (l *libraryPlayer) kick() {
	l.mu.Lock()
	should := l.want == "playing" && !l.held && l.pos < len(l.queue)
	if should && l.run == nil {
		r := &libRun{stop: make(chan struct{}), done: make(chan struct{})}
		l.run = r
		go l.play(r)
	}
	stop := !should && l.run != nil
	l.mu.Unlock()
	if stop {
		l.halt()
	}
	l.show()
}

// halt stops the run playing, if any, and waits for it to let go of the speaker.
func (l *libraryPlayer) halt() {
	l.mu.Lock()
	r := l.run
	l.run = nil
	l.mu.Unlock()
	if r != nil {
		close(r.stop)
		<-r.done
	}
}

func (l *libraryPlayer) show() {
	l.mu.Lock()
	var np *nowPlaying
	if l.want != "" && l.pos < len(l.queue) {
		t := l.queue[l.pos]
		source := "Music"
		if t.Source == "navidrome" {
			source = "Navidrome"
		}
		np = &nowPlaying{Title: t.Title, Artist: t.Artist, Playing: l.want == "playing", Source: source}
	}
	l.mu.Unlock()
	display.update(func(st *screenState) { st.Music = np })
}

// play plays the queue from where it is until it ends or is stopped.
func (l *libraryPlayer) play(r *libRun) {
	defer close(r.done)
	for {
		l.mu.Lock()
		if l.run != r || l.pos >= len(l.queue) {
			l.mu.Unlock()
			return
		}
		t, from := l.queue[l.pos], l.frame
		l.mu.Unlock()
		l.show()

		reached, err := l.playTrack(r, t, from)

		l.mu.Lock()
		if l.run != r { // stopped: carry on from here next time
			if l.queue != nil && l.pos < len(l.queue) && l.queue[l.pos] == t && l.frame == from {
				l.frame = reached
			}
			l.mu.Unlock()
			return
		}
		if err != nil {
			log.Printf("can't play %q: %v", t.Title, err)
			l.failed++
		} else {
			l.failed = 0
		}
		l.pos, l.frame = l.pos+1, 0
		over := l.pos >= len(l.queue) || l.failed >= 3
		if over {
			l.want, l.queue, l.run = "", nil, nil
		}
		failed := l.failed
		l.mu.Unlock()
		if over {
			if failed >= 3 {
				display.notice("The music wouldn't play.")
			}
			l.show()
			return
		}
	}
}

// playTrack plays one track from a frame on, returning the frame it got to.
func (l *libraryPlayer) playTrack(r *libRun, t libTrack, from int) (int, error) {
	l.mu.Lock()
	h, v := l.h, l.v
	l.mu.Unlock()
	if h == nil || v == nil {
		return from, fmt.Errorf("not connected")
	}
	body, err := h.openStream("/api/plugins/petasos/music/stream?source=" + url.QueryEscape(t.Source) +
		"&id=" + url.QueryEscape(t.ID))
	if err != nil {
		return from, err
	}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() { // closing the stream ends a read waiting on the network
		select {
		case <-r.stop:
			body.Close()
		case <-stopped:
			body.Close()
		}
	}()
	dec, err := mp3.NewDecoder(body)
	if err != nil {
		return from, err
	}
	pcm := resampleStereo(dec, dec.SampleRate(), libraryRate)
	if _, err := io.CopyN(io.Discard, pcm, int64(from)*4); err != nil {
		return from, err
	}
	if virtual {
		return l.playWhole(r, pcm, from)
	}
	cmd := exec.Command("aplay", "-q", "-t", "raw", "-f", "S16_LE", "-r", fmt.Sprint(libraryRate), "-c", "2",
		"-D", v.speaker, "-")
	in, err := cmd.StdinPipe()
	if err != nil {
		return from, err
	}
	if err := cmd.Start(); err != nil {
		return from, err
	}
	frame := from
	buf := make([]byte, 4096*4)
	for {
		select {
		case <-r.stop:
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return frame, nil
		default:
		}
		n, readErr := io.ReadFull(pcm, buf)
		n -= n % 4
		if n > 0 {
			if _, err := in.Write(buf[:n]); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return frame, fmt.Errorf("the speaker stopped: %v", err)
			}
			frame += n / 4
		}
		if readErr != nil {
			in.Close()
			err := cmd.Wait()
			select {
			case <-r.stop:
				return frame, nil
			default:
			}
			if readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
				return frame, readErr
			}
			return frame, err
		}
	}
}

// playWhole plays the rest of a track through the app's player, which takes a whole WAV.
func (l *libraryPlayer) playWhole(r *libRun, pcm io.Reader, from int) (int, error) {
	data, err := io.ReadAll(pcm)
	select {
	case <-r.stop:
		return from, nil
	default:
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return from, err
	}
	data = data[:len(data)-len(data)%4]
	cmd := l.v.playCommand()
	cmd.Stdin = bytes.NewReader(wavFileWith(data, libraryRate, 2))
	if err := cmd.Start(); err != nil {
		return from, err
	}
	began := time.Now()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-r.stop:
		_ = cmd.Process.Kill()
		<-done
		return min(from+int(time.Since(began).Seconds()*libraryRate), from+len(data)/4), nil
	case err := <-done:
		return from + len(data)/4, err
	}
}

// resampleStereo turns 16-bit stereo at one rate into 16-bit stereo at another.
func resampleStereo(src io.Reader, from, to int) io.Reader {
	if from == to || from <= 0 {
		return src
	}
	return &resampler{src: src, step: float64(from) / float64(to)}
}

type resampler struct {
	src    io.Reader
	step   float64
	pos    float64     // where the next output frame falls, between have[0] and have[1]
	have   [2][2]int16 // the two input frames around it
	primed bool
	in     []byte
	err    error
	ended  bool
}

func (r *resampler) next() bool { // moves one input frame on
	if len(r.in) < 4 {
		if r.err != nil {
			return false
		}
		buf := make([]byte, 16*1024)
		n, err := io.ReadAtLeast(r.src, buf, 4)
		r.in = append(r.in, buf[:n]...)
		r.err = err
		if len(r.in) < 4 {
			return false
		}
	}
	r.have[0] = r.have[1]
	r.have[1] = [2]int16{int16(binary.LittleEndian.Uint16(r.in[0:])), int16(binary.LittleEndian.Uint16(r.in[2:]))}
	r.in = r.in[4:]
	return true
}

func (r *resampler) Read(p []byte) (int, error) {
	if !r.primed {
		r.primed = true
		if !r.next() || !r.next() {
			r.ended = true
		}
	}
	n := 0
	for n+4 <= len(p) && !r.ended {
		for r.pos >= 1 {
			if !r.next() {
				r.ended = true
				break
			}
			r.pos--
		}
		if r.ended {
			break
		}
		for c := 0; c < 2; c++ {
			a, b := float64(r.have[0][c]), float64(r.have[1][c])
			binary.LittleEndian.PutUint16(p[n+2*c:], uint16(int16(a+(b-a)*r.pos)))
		}
		n += 4
		r.pos += r.step
	}
	if n == 0 && r.ended {
		if r.err != nil && r.err != io.ErrUnexpectedEOF {
			return 0, r.err
		}
		return 0, io.EOF
	}
	return n, nil
}

// openStream starts a GET whose body is read as it arrives (a song), renewing the sign-in once.
func (h *hermes) openStream(path string) (io.ReadCloser, error) {
	client := &http.Client{Transport: h.http.Transport} // no time limit: a song takes minutes
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("GET", h.cfg.Server+path, nil)
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		token := h.tokens.AccessToken
		h.mu.Unlock()
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			if err := h.refresh(); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode/100 != 2 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 200))
		}
		return resp.Body, nil
	}
}

// pollCommands picks up what Athena left for this device between turns: music she put on from
// another chat, say. Commands from a turn on the device itself come with her reply instead.
func (h *hermes) pollCommands(v *voice) {
	wait := 4 * time.Second
	for ; ; time.Sleep(wait) {
		var out struct {
			Commands []map[string]any `json:"commands"`
		}
		err := h.call("GET", "/api/plugins/petasos/devices/"+url.PathEscape(h.cfg.Device)+"/commands", nil, &out)
		if err != nil {
			wait = time.Minute // offline, or a plugin from before music
			continue
		}
		wait = 4 * time.Second
		for _, c := range out.Commands {
			v.carryOut(c)
		}
	}
}

// appMusic is the app's Settings page reading and saving the music sources, which live on
// Hermes (the plugin's music.json): a headless Hermes has no dashboard page to set them on.
// Saving also checks them, which reads the folders' songs the first time, so it may take a while.
func appMusic(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if h == nil || !virtual {
		http.NotFound(w, r)
		return
	}
	slow := &http.Client{Transport: h.http.Transport, Timeout: 10 * time.Minute}
	var out map[string]any
	switch r.Method {
	case http.MethodGet:
		data, err := h.exchange("GET", "/api/plugins/petasos/music/settings", "", nil, slow, 1<<20)
		if err == nil {
			err = json.Unmarshal(data, &out)
		}
		if err != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
			return
		}
	case http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil || !json.Valid(body) {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "That wasn't a music settings change."})
			return
		}
		var settings, check any
		data, err := h.exchange("PUT", "/api/plugins/petasos/music/settings", "application/json", body, slow, 1<<20)
		if err == nil {
			err = json.Unmarshal(data, &settings)
		}
		if err == nil {
			data, err = h.exchange("POST", "/api/plugins/petasos/music/check", "application/json", []byte("{}"), slow, 1<<20)
		}
		if err == nil {
			err = json.Unmarshal(data, &check)
		}
		if err != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
			return
		}
		out = map[string]any{"settings": settings, "check": check}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSONResponse(w, http.StatusOK, out)
}
