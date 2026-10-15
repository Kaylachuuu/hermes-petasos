package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
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
	Art     string `json:"art,omitempty"` // a picture for the music page, when the source has one
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

	// When the track playing reached the speaker, and from which frame, for where it is now.
	began      time.Time
	beganFrame int

	repeat  string     // "one" (this track again), "all" (the queue again) or "" (stop at the end)
	shuffle bool       // the queue is in a random order
	order   []libTrack // the queue as it came, for when shuffle is turned off

	// The last track's whole MP3, so a song on repeat, or carrying on after Athena, plays
	// from memory instead of being downloaded again.
	cacheKey  string
	cacheData []byte
}

// libraryCacheMax is the most kept of one track: about 20 minutes at 192 kbps.
const libraryCacheMax = 32 << 20

func trackKey(t libTrack) string { return t.Source + "\x00" + t.ID }

// cached is the track's MP3 when it is the one kept.
func (l *libraryPlayer) cached(t libTrack) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cacheKey == trackKey(t) {
		return l.cacheData
	}
	return nil
}

func (l *libraryPlayer) keep(t libTrack, data []byte) {
	l.mu.Lock()
	l.cacheKey, l.cacheData = trackKey(t), data
	l.mu.Unlock()
}

// capture keeps what is read from a stream, up to libraryCacheMax.
type capture struct {
	buf  bytes.Buffer
	over bool
}

func (c *capture) Write(p []byte) (int, error) {
	if !c.over {
		if c.buf.Len()+len(p) > libraryCacheMax {
			c.over, c.buf = true, bytes.Buffer{}
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
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

// start plays the queue the plugin sent: {"op":"play","title":...,"tracks":[...],"repeat":...,
// "shuffle":...}. The plugin has already shuffled a queue asked for shuffled.
func (l *libraryPlayer) start(title string, tracks []libTrack, repeat string, shuffle bool) {
	l.startAt(title, tracks, 0, repeat, shuffle)
}

// startAt plays a queue from one of its tracks: a song picked on the music page, with the rest
// of its list after it.
func (l *libraryPlayer) startAt(title string, tracks []libTrack, at int, repeat string, shuffle bool) {
	if len(tracks) == 0 {
		return
	}
	at = max(0, min(at, len(tracks)-1))
	music.yieldToLibrary()
	l.halt()
	l.mu.Lock()
	l.queue, l.pos, l.frame, l.title, l.want, l.failed = tracks, at, 0, title, "playing", 0
	l.order, l.shuffle, l.repeat = tracks, shuffle, repeatMode(repeat)
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
		if l.positionLocked() < 10*libraryRate { // further in, back to the start of this one instead
			l.pos = max(0, l.pos-1)
		}
		l.frame, restart = 0, true
	case "stop":
		l.want, l.queue = "", nil
	case "repeat_one", "repeat_all", "repeat_off":
		l.repeat = repeatMode(strings.TrimPrefix(op, "repeat_"))
	case "shuffle_on":
		if !l.shuffle && l.pos < len(l.queue) {
			// The song playing stays first; the rest come in a random order.
			rest := append([]libTrack(nil), l.queue[l.pos+1:]...)
			rand.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
			l.queue = append(append([]libTrack(nil), l.queue[:l.pos+1]...), rest...)
		}
		l.shuffle = true
	case "shuffle_off":
		if l.shuffle && l.pos < len(l.queue) && len(l.order) > 0 {
			// Back to the order it came in, carrying on from the song playing.
			now := l.queue[l.pos]
			for i, t := range l.order {
				if t == now {
					l.queue, l.pos = l.order, i
					break
				}
			}
		}
		l.shuffle = false
	}
	if l.pos >= len(l.queue) && l.repeat != "" && len(l.queue) > 0 && op == "next" {
		l.pos = 0 // repeating: past the end is the start again
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

// positionLocked is how far into the track playing it is, in frames.
func (l *libraryPlayer) positionLocked() int {
	if l.run != nil && !l.began.IsZero() {
		return l.beganFrame + int(time.Since(l.began).Seconds()*libraryRate)
	}
	return l.frame
}

// reached marks the track as having reached the speaker, from a frame.
func (l *libraryPlayer) reached(r *libRun, from int) {
	l.mu.Lock()
	if l.run == r {
		l.began, l.beganFrame = time.Now(), from
	}
	l.mu.Unlock()
	l.show()
}

// repeatMode is "one", "all" or "" from what the plugin or Athena said.
func repeatMode(s string) string {
	switch s {
	case "one", "all":
		return s
	}
	return ""
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
		source := map[string]string{"navidrome": "Navidrome", "soundcloud": "SoundCloud"}[t.Source]
		if source == "" {
			source = "Music"
		}
		np = &nowPlaying{Title: t.Title, Artist: t.Artist, Playing: l.want == "playing", Source: source,
			Art: t.Art, Seconds: t.Seconds, Position: float64(l.positionLocked()) / libraryRate,
			At: time.Now().UnixMilli(), Moving: l.run != nil && !l.began.IsZero(),
			Library: true, Repeat: l.repeat, Shuffle: l.shuffle}
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
		l.began = time.Time{}
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
		if l.repeat != "one" || err != nil {
			l.pos++
		}
		l.frame = 0
		if l.pos >= len(l.queue) && l.repeat == "all" && l.failed < len(l.queue) {
			l.pos = 0
			if l.shuffle { // a new order each time round
				q := append([]libTrack(nil), l.queue...)
				rand.Shuffle(len(q), func(i, j int) { q[i], q[j] = q[j], q[i] })
				l.queue = q
			}
		}
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
	var src io.Reader
	if data := l.cached(t); data != nil {
		src = bytes.NewReader(data)
	} else {
		body, err := h.openStream("/api/plugins/petasos/music/stream?source=" + url.QueryEscape(t.Source) +
			"&id=" + url.QueryEscape(t.ID))
		if err != nil {
			return from, err
		}
		got := &capture{}
		tee := io.TeeReader(body, got)
		src = tee
		finished := make(chan bool, 1) // true: the whole track was read
		defer func() {
			select {
			case whole := <-finished:
				body.Close()
				if whole && !got.over {
					l.keep(t, got.buf.Bytes())
				}
			default:
				// Stopped partway (Athena, say): fetch the rest meanwhile, so carrying on, or the
				// track coming round again, plays from memory.
				go func() {
					_, err := io.Copy(io.Discard, tee)
					body.Close()
					if err == nil && !got.over {
						l.keep(t, got.buf.Bytes())
					}
				}()
			}
		}()
		src = &endWatch{r: tee, done: finished}
	}
	dec, err := mp3.NewDecoder(src)
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
	l.reached(r, from)
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
	l.reached(r, from)
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

// soundCloudRedirect is where SoundCloud sends the app's window back to after you sign in. It is
// the same for the Mac and Windows apps, and is what the SoundCloud app must list as its
// redirect URI.
const soundCloudRedirect = "http://" + screenAddr + "/app/soundcloud"

// appSoundCloud signs in to your SoundCloud account from the app's Settings. A POST with ?start
// opens SoundCloud's sign-in in your own browser (the app's window can't open the pop-ups that
// signing in with Google or Apple needs); SoundCloud sends the browser back here with a code,
// which the plugin on Hermes turns into your tokens (the app's secret never leaves Hermes), and
// the browser tab says how it went. A POST with ?disconnect signs out.
func appSoundCloud(w http.ResponseWriter, r *http.Request) {
	h := hereNow()
	if h == nil || !virtual {
		http.NotFound(w, r)
		return
	}
	back := func(problem string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		title, text := "Signed in to SoundCloud", "You can close this tab and go back to Petasos."
		if problem != "" {
			title, text = "Couldn't sign in to SoundCloud", problem
		}
		fmt.Fprintf(w, soundCloudDonePage, html.EscapeString(title), html.EscapeString(title), html.EscapeString(text))
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && q.Has("disconnect"):
		if _, err := h.exchange("POST", "/api/plugins/petasos/music/soundcloud/disconnect", "application/json", []byte("{}"), h.http, 1<<16); err != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]string{"account": ""})
	case q.Get("error") != "":
		back("SoundCloud said: " + firstNonEmpty(q.Get("error_description"), q.Get("error")))
	case q.Get("code") != "":
		body, _ := json.Marshal(map[string]string{"code": q.Get("code"), "state": q.Get("state")})
		if _, err := h.exchange("POST", "/api/plugins/petasos/music/soundcloud/connect", "application/json", body, h.http, 1<<16); err != nil {
			back(friendlyHermes(err))
			return
		}
		back("")
	case r.Method == http.MethodPost && q.Has("start"):
		to, err := soundCloudSignInPage(h)
		if err == nil {
			err = openInBrowser(to)
		}
		if err != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": friendlyHermes(err)})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]bool{"opened": true})
	default:
		to, err := soundCloudSignInPage(h)
		if err != nil {
			back(friendlyHermes(err))
			return
		}
		http.Redirect(w, r, to, http.StatusFound)
	}
}

// soundCloudSignInPage asks the plugin for SoundCloud's sign-in page, with a new PKCE verifier
// kept on Hermes.
func soundCloudSignInPage(h *hermes) (string, error) {
	body, _ := json.Marshal(map[string]string{"redirect_uri": soundCloudRedirect})
	data, err := h.exchange("POST", "/api/plugins/petasos/music/soundcloud/authorize", "application/json", body, h.http, 1<<16)
	var out struct {
		URL string `json:"url"`
	}
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	if err != nil || !strings.HasPrefix(out.URL, "https://") {
		return "", firstErr(err, fmt.Errorf("no sign-in page"))
	}
	return out.URL, nil
}

// openInBrowser opens a page in your default browser; a variable so tests can watch it.
var openInBrowser = func(page string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", page).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", page).Run()
	default:
		return exec.Command("xdg-open", page).Run()
	}
}

const soundCloudDonePage = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>%s</title>
<style>body{margin:0;min-height:100vh;display:grid;place-content:center;text-align:center;background:#1a1410;color:#c9b48a;font:17px/1.5 -apple-system,"Segoe UI",sans-serif;padding:24px}h1{color:#d97a3a;font-weight:500;font-size:24px}</style>
<h1>%s</h1><p>%s</p>`

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// endWatch says when its reader reaches the end, so the whole of a track can be kept.
type endWatch struct {
	r    io.Reader
	done chan bool
	sent bool
}

func (e *endWatch) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !e.sent {
		e.sent = true
		e.done <- err == io.EOF
	}
	return n, err
}
