package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Voice on the Car Thing: record from its microphones while the plugin says to listen, send the
// recording to the plugin, and play Athena's spoken reply on the Bluetooth speaker. The plugin
// does the transcribing, the chat turn and the speech; this side only moves audio.
//
// Recording uses arecord (16 kHz mono, raw PCM on stdout). The Car Thing's microphones only
// deliver 32-bit samples (16-bit is advertised but refused), so they are read as S32_LE and cut
// down to 16-bit here. Playback uses aplay on BlueALSA's
// "bluealsa" device, which plays to the most recently connected Bluetooth speaker.

const (
	sampleRate = 16000
	frameBytes = sampleRate / 50 * 2 // 20 ms of 16-bit mono
)

type voice struct {
	h       *hermes
	mic     string // ALSA capture device
	speaker string // ALSA playback device
	slow    *http.Client
	presses chan<- map[string]any // for telling the plugin a conversation ended by itself

	mu      sync.Mutex
	rec     *recording
	mode    string // "", "hold" or "conversation"
	endMs   int
	idleS   int
	player  *exec.Cmd
	turnGen int // bumped by cancel/stop so a reply that arrives late is dropped
	hushed  int // bumped by stopSpeaking, to cut short a reply shown without sound
}

func newVoice(h *hermes, presses chan<- map[string]any) *voice {
	v := &voice{h: h, mic: h.cfg.Mic, speaker: h.cfg.Speaker, presses: presses,
		slow: &http.Client{Transport: h.http.Transport, Timeout: 4 * time.Minute}}
	if v.mic == "" {
		v.mic = "hw:0,0" // the Car Thing's microphones (yocto-superbird routes them to both capture devices)
	}
	if v.speaker == "" {
		v.speaker = "bluealsa"
	}
	return v
}

// carryOut acts on one command from the plugin. It never blocks the button loop: recording,
// asking and playing run in the background.
func (v *voice) carryOut(c map[string]any) {
	raw, _ := json.Marshal(c)
	log.Printf("command %s", raw)
	switch c["do"] {
	case "listen":
		mode, _ := c["mode"].(string)
		v.listen(mode, intOf(c["end_of_speech_ms"], 1000), intOf(c["idle_end_s"], 30))
	case "send":
		v.finish(true)
	case "cancel", "listen_stop":
		v.mu.Lock()
		v.mode = ""
		v.turnGen++
		v.mu.Unlock()
		v.finish(false)
		display.setPhase("idle")
	case "stop_speaking":
		v.stopSpeaking() // the turn then ends as usual, starting the next one in conversation mode
	case "prompt":
		if text, _ := c["text"].(string); text != "" {
			v.stopSpeaking()
			display.update(func(st *screenState) { st.Phase, st.Heard, st.Reply = "thinking", text, "" })
			go v.ask("/api/plugins/petasos/ask", "application/json", mustJSON(map[string]string{"text": text}))
		}
	case "volume":
		go v.volume(intOf(c["delta"], 0))
	case "mic":
		muted, _ := c["muted"].(bool)
		display.update(func(st *screenState) { st.Muted = muted })
	case "notice":
		if text, _ := c["text"].(string); text != "" {
			display.notice(text)
		}
	case "screen":
		if raw, ok := c["presets"].([]any); ok {
			labels := make([]string, 4)
			for i := 0; i < 4 && i < len(raw); i++ {
				labels[i], _ = raw[i].(string)
			}
			display.update(func(st *screenState) { st.Presets = labels })
		}
		if name, _ := c["agent"].(string); name != "" {
			display.update(func(st *screenState) { st.Agent = name })
		}
	}
}

// --- recording ---------------------------------------------------------------------------------

type recording struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	mu     sync.Mutex
	pcm    bytes.Buffer
	done   chan struct{}
}

func (v *voice) listen(mode string, endMs, idleS int) {
	v.stopSpeaking() // no talking over Athena, and her voice shouldn't end up in the recording
	v.mu.Lock()
	v.mode, v.endMs, v.idleS = mode, endMs, idleS
	recording := v.rec != nil
	v.mu.Unlock()
	// A tap turns the push-to-talk recording already under way into a conversation; keep it,
	// so the first words aren't lost.
	if !recording {
		v.startRecording()
	}
}

func (v *voice) startRecording() {
	display.setPhase("ready")
	cmd := v.recordCommand()
	out, err := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		log.Printf("can't record from %s: %v", v.mic, err)
		display.setPhase("idle")
		display.notice("The microphone isn't working.")
		return
	}
	rec := &recording{cmd: cmd, stderr: &stderr, done: make(chan struct{})}
	v.mu.Lock()
	v.rec = rec
	mode := v.mode
	v.mu.Unlock()
	go func() {
		defer close(rec.done)
		var vad *endpointer
		r := bufio.NewReaderSize(out, frameBytes*8)
		wide := make([]byte, frameBytes*2)
		frame := make([]byte, frameBytes)
		for first := true; ; first = false {
			if _, err := io.ReadFull(r, wide); err != nil {
				return
			}
			if first { // the microphone is really recording now: tell the person to speak
				v.mu.Lock()
				current := v.rec == rec
				v.mu.Unlock()
				if current {
					display.setPhase("listening")
				}
			}
			to16(wide, frame)
			rec.mu.Lock()
			rec.pcm.Write(frame)
			rec.mu.Unlock()
			v.mu.Lock()
			conversation, endMs, idleS := v.mode == "conversation", v.endMs, v.idleS
			v.mu.Unlock()
			if !conversation {
				continue
			}
			if vad == nil {
				vad = newEndpointer(endMs, idleS)
			}
			switch vad.feed(frame) {
			case endOfSpeech:
				log.Printf("end of speech (%v)", vad)
				go v.finish(true)
				return
			case idle:
				log.Printf("no speech heard (%v)", vad)
				go v.endConversation()
				return
			}
		}
	}()
	log.Printf("listening (%s)", mode)
}

// finish stops the recording; with send, the recording goes to Athena.
func (v *voice) finish(send bool) {
	v.mu.Lock()
	rec := v.rec
	v.rec = nil
	gen := v.turnGen
	v.mu.Unlock()
	if rec == nil {
		return
	}
	_ = rec.cmd.Process.Kill()
	_ = rec.cmd.Wait()
	<-rec.done
	rec.mu.Lock()
	pcm := append([]byte(nil), rec.pcm.Bytes()...)
	rec.mu.Unlock()
	if !send {
		return
	}
	if len(pcm) < sampleRate*2/4 { // under a quarter second: a slip of the finger
		log.Printf("recording too short (%d ms); not sent", len(pcm)*1000/(sampleRate*2))
		if msg := strings.TrimSpace(rec.stderr.String()); msg != "" {
			log.Printf("the microphone (%s) said: %s", v.mic, truncate(msg, 300))
		}
		v.idleOrRelisten(gen)
		return
	}
	display.setPhase("thinking")
	go v.askWithGen(gen, "/api/plugins/petasos/voice", "audio/wav", wavFile(pcm, sampleRate))
}

func (v *voice) endConversation() {
	v.mu.Lock()
	rec := v.rec
	v.rec = nil
	v.mode = ""
	v.mu.Unlock()
	if rec != nil {
		_ = rec.cmd.Process.Kill()
		_ = rec.cmd.Wait()
	}
	log.Printf("conversation ended (quiet)")
	display.setPhase("idle")
	select {
	case v.presses <- map[string]any{"input": "device", "action": "conversation_ended"}:
	default:
	}
}

// relisten starts the next turn of a conversation, unless it was ended or cancelled meanwhile.
func (v *voice) relisten(gen int) bool {
	v.mu.Lock()
	again := v.mode == "conversation" && v.turnGen == gen && v.rec == nil
	v.mu.Unlock()
	if again {
		v.startRecording()
	}
	return again
}

// idleOrRelisten ends a turn: the next one starts in conversation mode, otherwise the screen
// goes back to rest (unless something newer has taken over it).
func (v *voice) idleOrRelisten(gen int) {
	if !v.relisten(gen) && v.current(gen) {
		v.mu.Lock()
		recording := v.rec != nil
		v.mu.Unlock()
		if !recording {
			display.setPhase("idle")
		}
	}
}

// --- asking and playing ------------------------------------------------------------------------

type answer struct {
	Heard string  `json:"heard"`
	Reply string  `json:"reply"`
	Audio *string `json:"audio"`
}

func (v *voice) ask(path, contentType string, payload []byte) {
	v.mu.Lock()
	gen := v.turnGen
	v.mu.Unlock()
	v.askWithGen(gen, path, contentType, payload)
}

func (v *voice) askWithGen(gen int, path, contentType string, payload []byte) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	data, err := v.h.exchange("POST", path+sep+"device="+url.QueryEscape(v.h.cfg.Device),
		contentType, payload, v.slow, 1<<20)
	if err != nil {
		log.Printf("asking %s failed: %v", display.agent(), err)
		if v.current(gen) {
			display.notice(display.agent() + " couldn't answer just now.")
		}
		v.idleOrRelisten(gen)
		return
	}
	var a answer
	if err := json.Unmarshal(data, &a); err != nil {
		log.Printf("unexpected reply from the plugin: %v", err)
		v.idleOrRelisten(gen)
		return
	}
	if a.Heard == "" {
		log.Printf("heard nothing")
		if v.current(gen) {
			display.notice("I didn't catch that.")
		}
		v.idleOrRelisten(gen)
		return
	}
	log.Printf("heard: %s", a.Heard)
	log.Printf("%s: %s", display.agent(), a.Reply)
	if !v.current(gen) {
		return
	}
	display.update(func(st *screenState) { st.Heard = a.Heard })
	var audio []byte
	if a.Audio != nil {
		audio, err = v.h.exchange("GET", "/api/plugins/petasos/voice/audio/"+url.PathEscape(*a.Audio),
			"", nil, v.slow, 64<<20)
		if err != nil {
			log.Printf("fetching the spoken reply failed: %v", err)
		}
	}
	if a.Reply != "" && v.current(gen) {
		v.speak(gen, a.Reply, audio)
	}
	v.idleOrRelisten(gen)
}

// speak shows the reply while it plays, or long enough to read when there's no sound for it.
func (v *voice) speak(gen int, reply string, audio []byte) {
	ms := readingMs(reply)
	if audio != nil {
		if converted, err := toSpeakerFormat(audio); err == nil {
			audio = converted
			ms = len(audio) * 1000 / (speakerRate * speakerChannels * 2)
		}
	}
	display.update(func(st *screenState) { st.Phase, st.Reply, st.ReplyMs = "speaking", reply, ms })
	if audio == nil || !display.snapshot().Links.Speaker && display.snapshot().Links.Checked {
		// No speaker to hear it on: leave the words up to read, unless stopped first.
		v.mu.Lock()
		hushed := v.hushed
		v.mu.Unlock()
		deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
		for time.Now().Before(deadline) && v.current(gen) && v.notHushedSince(hushed) {
			time.Sleep(200 * time.Millisecond)
		}
		return
	}
	v.play(audio)
}

// readingMs is roughly how long a reply takes to read: 200 words a minute, at least 5 seconds.
func readingMs(text string) int {
	return max(5000, len(strings.Fields(text))*300)
}

func (v *voice) current(gen int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.turnGen == gen
}

// play blocks until the reply has played or was stopped.
func (v *voice) play(wav []byte) {
	// BlueALSA can't resample: the Bluetooth link runs at the codec's own rate (48 kHz stereo
	// for SBC on most speakers), and asking for anything else fails. Text-to-speech comes at
	// 24 kHz mono, so convert first.
	if converted, err := toSpeakerFormat(wav); err != nil { // a no-op when speak converted it already
		log.Printf("can't read the spoken reply: %v", err)
		return
	} else {
		wav = converted
	}
	cmd := v.playCommand()
	cmd.Stdin = bytes.NewReader(wav)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		log.Printf("can't play: %v", err)
		return
	}
	v.mu.Lock()
	v.player = cmd
	v.mu.Unlock()
	err := cmd.Wait()
	v.mu.Lock()
	stopped := v.player != cmd
	if !stopped {
		v.player = nil
	}
	v.mu.Unlock()
	if err != nil && !stopped {
		log.Printf("playing on %s failed (is the speaker connected?): %v %s", v.speaker, err,
			strings.TrimSpace(stderr.String()))
	}
	if err == nil && !stopped && !virtual {
		// aplay is done once BlueALSA has the last of it, but the speaker (a car's especially)
		// is still playing its buffer. Opening the mic now would record the end of her reply
		// as the next turn, so let it finish first.
		time.Sleep(speakerTail)
	}
}

// speakerTail is how long a Bluetooth speaker goes on playing after the last of the audio was
// handed to it.
const speakerTail = 900 * time.Millisecond

func (v *voice) notHushedSince(n int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.hushed == n
}

func (v *voice) stopSpeaking() {
	v.mu.Lock()
	p := v.player
	v.player = nil
	v.hushed++
	v.mu.Unlock()
	if p != nil && p.Process != nil {
		_ = p.Process.Kill()
	}
}

// volume nudges the speaker's own volume through BlueALSA's mixer control for it.
var a2dpControl = regexp.MustCompile(`Simple mixer control '([^']*A2DP[^']*)'`)

var mixerLevel = regexp.MustCompile(`\[(\d+)%\]`)

func (v *voice) volume(delta int) {
	if delta == 0 {
		return
	}
	if virtual {
		macVolume(delta)
		return
	}
	out, err := exec.Command("amixer", "-D", v.speaker, "scontrols").Output()
	m := a2dpControl.FindSubmatch(out)
	if err != nil || m == nil {
		log.Printf("no speaker volume to change (is the speaker connected?)")
		display.notice("No speaker connected.")
		return
	}
	step := fmt.Sprintf("%d%%+", delta)
	if delta < 0 {
		step = fmt.Sprintf("%d%%-", -delta)
	}
	out, err = exec.Command("amixer", "-D", v.speaker, "sset", string(m[1]), step).CombinedOutput()
	if err != nil {
		log.Printf("changing the volume failed: %v %s", err, strings.TrimSpace(string(out)))
		return
	}
	if level := mixerLevel.FindSubmatch(out); level != nil {
		var n int
		fmt.Sscan(string(level[1]), &n)
		display.update(func(st *screenState) { st.Volume = n; st.VolumeID++ })
		volumes.remember(string(m[1]), n)
	}
}

// --- end of speech -----------------------------------------------------------------------------

type vadResult int

const (
	keepListening vadResult = iota
	endOfSpeech
	idle
)

// endpointer decides when someone has finished speaking, from loudness alone. The background
// level is the quietest moment of the last three seconds: speech always has gaps between
// words that let the background show through, so this follows road noise up or down within
// seconds whatever it does, while speech can't raise it. Frames well above it count as speech,
// and the turn ends after endMs of quiet following speech, after idleS with no speech at all, or
// after maxTurn of talking, so it can never be stuck listening.
type endpointer struct {
	endFrames, idleFrames int
	frames, quiet, voiced int
	speechFrames          int
	recent                []float64 // levels of the last window frames, as a ring
	floor, peak           float64
	heard                 bool
}

const (
	floorWindow = 150     // frames (3 s)
	maxTurn     = 30 * 50 // frames (30 s)
	minSpeech   = 400.0   // below this nothing is speech, however quiet the room
)

func newEndpointer(endMs, idleS int) *endpointer {
	return &endpointer{endFrames: max(endMs/20, 5), idleFrames: max(idleS*50, 50), floor: 300,
		recent: make([]float64, 0, floorWindow)}
}

func (e *endpointer) feed(frame []byte) vadResult {
	level := rms(frame)
	if len(e.recent) < floorWindow {
		e.recent = append(e.recent, level)
	} else {
		e.recent[e.frames%floorWindow] = level
	}
	e.frames++
	if e.frames >= 10 { // a fifth of a second is enough for a first guess
		low := math.Inf(1)
		for _, l := range e.recent {
			low = math.Min(low, l)
		}
		e.floor = math.Max(low, 30)
	}
	e.peak = math.Max(e.peak, level)
	if level > math.Max(e.floor*2.5, minSpeech) {
		e.voiced++
		e.quiet = 0
		if e.voiced >= 4 && e.frames > 10 { // 80 ms of sound, so a click isn't speech
			e.heard = true
		}
	} else {
		e.voiced = 0
		e.quiet++
	}
	if e.heard {
		e.speechFrames++
	}
	switch {
	case e.heard && (e.quiet >= e.endFrames || e.speechFrames >= maxTurn):
		return endOfSpeech
	case !e.heard && e.frames >= e.idleFrames:
		return idle
	}
	return keepListening
}

// String sums up what it heard, for the log.
func (e *endpointer) String() string {
	return fmt.Sprintf("%.1f s, background %.0f, loudest %.0f, speech threshold %.0f",
		float64(e.frames)/50, e.floor, e.peak, math.Max(e.floor*2.5, minSpeech))
}

func rms(frame []byte) float64 {
	var sum float64
	n := len(frame) / 2
	for i := 0; i < n; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(frame[2*i:])))
		sum += s * s
	}
	return math.Sqrt(sum / float64(max(n, 1)))
}

// --- helpers -----------------------------------------------------------------------------------

func wavFile(pcm []byte, rate int) []byte { return wavFileWith(pcm, rate, 1) }

func wavFileWith(pcm []byte, rate, channels int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	for _, field := range []any{uint32(16), uint16(1), uint16(channels), uint32(rate),
		uint32(rate * 2 * channels), uint16(2 * channels), uint16(16)} { // PCM, channels, rate, bytes/s, block, bits
		_ = binary.Write(&b, binary.LittleEndian, field)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// to16 keeps the top 16 bits of each 32-bit little-endian sample (the microphones' data is
// left-aligned in the 32-bit word).
func to16(wide, out []byte) {
	for i := 0; i+1 < len(out); i += 2 {
		out[i], out[i+1] = wide[2*i+2], wide[2*i+3]
	}
}

const (
	speakerRate     = 48000
	speakerChannels = 2
)

// toSpeakerFormat turns a 16-bit PCM WAV of any rate and channel count into 48 kHz stereo,
// mixing channels down and resampling by linear interpolation (plenty for speech).
func toSpeakerFormat(wav []byte) ([]byte, error) {
	channels, rate, bits, pcm, err := parseWav(wav)
	if err != nil {
		return nil, err
	}
	if bits != 16 {
		return nil, fmt.Errorf("%d-bit audio; expected 16-bit", bits)
	}
	if rate == speakerRate && channels == speakerChannels {
		return wav, nil
	}
	frames := len(pcm) / (2 * channels)
	mono := make([]float64, frames)
	for i := 0; i < frames; i++ {
		var sum float64
		for c := 0; c < channels; c++ {
			sum += float64(int16(binary.LittleEndian.Uint16(pcm[2*(i*channels+c):])))
		}
		mono[i] = sum / float64(channels)
	}
	outFrames := int(int64(frames) * speakerRate / int64(rate))
	out := make([]byte, outFrames*2*speakerChannels)
	for j := 0; j < outFrames; j++ {
		pos := float64(j) * float64(rate) / speakerRate
		i := int(pos)
		sample := mono[min(i, frames-1)]
		if i+1 < frames {
			sample += (mono[i+1] - sample) * (pos - float64(i))
		}
		value := uint16(int16(math.Max(-32768, math.Min(32767, math.Round(sample)))))
		for c := 0; c < speakerChannels; c++ {
			binary.LittleEndian.PutUint16(out[2*(j*speakerChannels+c):], value)
		}
	}
	return wavFileWith(out, speakerRate, speakerChannels), nil
}

// parseWav finds the format and the samples in a WAV file, skipping any other chunks.
func parseWav(wav []byte) (channels, rate, bits int, pcm []byte, err error) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0, 0, 0, nil, fmt.Errorf("not a WAV file")
	}
	for p := 12; p+8 <= len(wav); {
		id, size := string(wav[p:p+4]), int(binary.LittleEndian.Uint32(wav[p+4:]))
		body := wav[p+8:]
		if size > len(body) || size < 0 {
			size = len(body) // streamed WAVs leave the data size unset
		}
		switch id {
		case "fmt ":
			if size < 16 || binary.LittleEndian.Uint16(body) != 1 {
				return 0, 0, 0, nil, fmt.Errorf("not plain PCM")
			}
			channels = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			bits = int(binary.LittleEndian.Uint16(body[14:]))
		case "data":
			if channels == 0 || rate == 0 {
				return 0, 0, 0, nil, fmt.Errorf("no format before the samples")
			}
			return channels, rate, bits, body[:size], nil
		}
		p += 8 + size + size%2
	}
	return 0, 0, 0, nil, fmt.Errorf("no samples")
}

// intOf reads a number from a decoded JSON command (float64) or a local event (int).
func intOf(v any, fallback int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return fallback
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
