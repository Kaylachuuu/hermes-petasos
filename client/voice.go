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
// Recording uses arecord (16 kHz mono, raw PCM on stdout); playback uses aplay on BlueALSA's
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
}

func newVoice(h *hermes, presses chan<- map[string]any) *voice {
	v := &voice{h: h, mic: h.cfg.Mic, speaker: h.cfg.Speaker, presses: presses,
		slow: &http.Client{Transport: h.http.Transport, Timeout: 4 * time.Minute}}
	if v.mic == "" {
		v.mic = "plughw:0,0" // the Car Thing's microphones (yocto-superbird routes them to both capture devices)
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
	case "stop_speaking":
		v.stopSpeaking()
	case "prompt":
		if text, _ := c["text"].(string); text != "" {
			v.stopSpeaking()
			go v.ask("/api/plugins/petasos/ask", "application/json", mustJSON(map[string]string{"text": text}))
		}
	case "volume":
		go v.volume(intOf(c["delta"], 0))
	case "mic", "notice":
		// Shown on the screen once it's live; the plugin already keeps the mic state.
	}
}

// --- recording ---------------------------------------------------------------------------------

type recording struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	pcm  bytes.Buffer
	done chan struct{}
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
	cmd := exec.Command("arecord", "-q", "-D", v.mic, "-f", "S16_LE", "-r", fmt.Sprint(sampleRate),
		"-c", "1", "-t", "raw")
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		log.Printf("can't record from %s: %v", v.mic, err)
		return
	}
	rec := &recording{cmd: cmd, done: make(chan struct{})}
	v.mu.Lock()
	v.rec = rec
	mode := v.mode
	v.mu.Unlock()
	go func() {
		defer close(rec.done)
		var vad *endpointer
		r := bufio.NewReaderSize(out, frameBytes*4)
		frame := make([]byte, frameBytes)
		for {
			if _, err := io.ReadFull(r, frame); err != nil {
				return
			}
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
				go v.finish(true)
				return
			case idle:
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
		log.Printf("recording too short; not sent")
		v.relisten(gen)
		return
	}
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
	select {
	case v.presses <- map[string]any{"input": "device", "action": "conversation_ended"}:
	default:
	}
}

// relisten starts the next turn of a conversation, unless it was ended or cancelled meanwhile.
func (v *voice) relisten(gen int) {
	v.mu.Lock()
	again := v.mode == "conversation" && v.turnGen == gen && v.rec == nil
	v.mu.Unlock()
	if again {
		v.startRecording()
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
		log.Printf("asking Athena failed: %v", err)
		v.relisten(gen)
		return
	}
	var a answer
	if err := json.Unmarshal(data, &a); err != nil {
		log.Printf("unexpected reply from the plugin: %v", err)
		return
	}
	if a.Heard == "" {
		log.Printf("heard nothing")
		v.relisten(gen)
		return
	}
	log.Printf("heard: %s", a.Heard)
	log.Printf("Athena: %s", a.Reply)
	if a.Audio != nil {
		audio, err := v.h.exchange("GET", "/api/plugins/petasos/voice/audio/"+url.PathEscape(*a.Audio),
			"", nil, v.slow, 64<<20)
		if err != nil {
			log.Printf("fetching the spoken reply failed: %v", err)
		} else if v.current(gen) {
			v.play(audio)
		}
	}
	v.relisten(gen)
}

func (v *voice) current(gen int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.turnGen == gen
}

// play blocks until the reply has played or was stopped.
func (v *voice) play(wav []byte) {
	cmd := exec.Command("aplay", "-q", "-D", v.speaker, "-")
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
}

func (v *voice) stopSpeaking() {
	v.mu.Lock()
	p := v.player
	v.player = nil
	v.mu.Unlock()
	if p != nil && p.Process != nil {
		_ = p.Process.Kill()
	}
}

// volume nudges the speaker's own volume through BlueALSA's mixer control for it.
var a2dpControl = regexp.MustCompile(`Simple mixer control '([^']*A2DP[^']*)'`)

func (v *voice) volume(delta int) {
	if delta == 0 {
		return
	}
	out, err := exec.Command("amixer", "-D", v.speaker, "scontrols").Output()
	m := a2dpControl.FindSubmatch(out)
	if err != nil || m == nil {
		log.Printf("no speaker volume to change (is the speaker connected?)")
		return
	}
	step := fmt.Sprintf("%d%%+", delta)
	if delta < 0 {
		step = fmt.Sprintf("%d%%-", -delta)
	}
	if out, err := exec.Command("amixer", "-q", "-D", v.speaker, "sset", string(m[1]), step).CombinedOutput(); err != nil {
		log.Printf("changing the volume failed: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// --- end of speech -----------------------------------------------------------------------------

type vadResult int

const (
	keepListening vadResult = iota
	endOfSpeech
	idle
)

// endpointer decides when someone has finished speaking, from loudness alone. It tracks the
// background level as a floor that drops at once to anything quieter and creeps up slowly
// otherwise (so steady road noise is learned within seconds, while speech barely moves it),
// counts frames well above that floor as speech, and ends the turn after endMs of quiet following
// speech, or after idleS with no speech at all.
type endpointer struct {
	endFrames, idleFrames int
	frames, quiet, voiced int
	floor                 float64
	heard                 bool
}

func newEndpointer(endMs, idleS int) *endpointer {
	return &endpointer{endFrames: max(endMs/20, 5), idleFrames: max(idleS*50, 50), floor: 300}
}

func (e *endpointer) feed(frame []byte) vadResult {
	level := rms(frame)
	e.frames++
	if level < e.floor {
		e.floor = math.Max(level, 30)
	} else {
		e.floor *= 1.003
	}
	if level > math.Max(e.floor*3, 400) {
		e.voiced++
		e.quiet = 0
		if e.voiced >= 4 { // 80 ms of sound, so a click isn't speech
			e.heard = true
		}
	} else {
		e.voiced = 0
		e.quiet++
	}
	switch {
	case e.heard && e.quiet >= e.endFrames:
		return endOfSpeech
	case !e.heard && e.frames >= e.idleFrames:
		return idle
	}
	return keepListening
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

func wavFile(pcm []byte, rate int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	for _, field := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2),
		uint16(2), uint16(16)} { // PCM, mono, rate, bytes per second, block size, bits
		_ = binary.Write(&b, binary.LittleEndian, field)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

func intOf(v any, fallback int) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return fallback
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
