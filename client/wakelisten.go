package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/binary"
	"io"
	"log"
	"math"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Listening for "Hey Athena" (wakeword.go spots it). While nothing else is going on, and the
// mic isn't muted, the microphone stays open and every 80 ms of it is scored. On the wake word,
// a chime plays and the plugin is told, which starts a conversation as the conversation button
// does; the listener lets go of the microphone first, and picks it up again once things are
// quiet. On the Car Thing it always uses the Car Thing's own microphones: listening through the
// car's would keep a hands-free call open the whole time.

//go:embed wakeword/hey-athena.bin
var heyAthenaModel []byte

// wakeOn is the setting: listen for the wake word or not.
var wakeOn atomic.Bool

// wakeThreshold is the score that counts as hearing it, wakeSteps how many 80 ms steps in a
// row must reach it: a lone step over the line is far more often something else.
const (
	wakeThreshold = 0.5
	wakeSteps     = 2
)

type wakeListener struct {
	mu    sync.Mutex
	src   micSource
	done  chan struct{} // closed when the current listen has let go of the microphone
	model *wakeModel
	v     *voice
}

var wake = &wakeListener{}

// start begins listening in the background, whenever wakeOn and things are quiet.
func (w *wakeListener) start(v *voice) {
	m, err := loadWakeModel(heyAthenaModel)
	if err != nil {
		log.Printf("no wake word: %v", err)
		return
	}
	w.mu.Lock()
	w.model, w.v = m, v
	w.mu.Unlock()
	go w.loop()
}

// quiet is whether the wake word may listen now.
func (w *wakeListener) quiet() bool {
	if !wakeOn.Load() {
		return false
	}
	st := display.snapshot()
	if st.Phase != "idle" || st.Muted || st.Page != "" { // only on the home page: music sets it off
		return false
	}
	w.v.mu.Lock()
	busy := w.v.rec != nil
	w.v.mu.Unlock()
	return !busy
}

func (w *wakeListener) loop() {
	for {
		if !w.quiet() {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if heard := w.listen(); heard {
			w.woken()
		} else {
			time.Sleep(time.Second) // the mic closed or failed: not straight back
		}
	}
}

// listen keeps the microphone open until the wake word is heard (true) or it should stop.
func (w *wakeListener) listen() bool {
	src, out, err := w.openMic()
	if err != nil {
		log.Printf("wake word: can't open the microphone: %v", err)
		time.Sleep(10 * time.Second)
		return false
	}
	done := make(chan struct{})
	w.mu.Lock()
	w.src, w.done = src, done
	w.mu.Unlock()
	defer func() {
		src.stop()
		_ = src.wait()
		w.mu.Lock()
		w.src = nil
		w.mu.Unlock()
		close(done)
	}()
	spotter := newWakeSpotter(w.model)
	r := bufio.NewReaderSize(out, frameBytes*8)
	wide := make([]byte, frameBytes*2)
	frame := make([]byte, frameBytes)
	pcm := make([]int16, frameBytes/2)
	run := 0
	for {
		if _, err := io.ReadFull(r, wide); err != nil {
			return false
		}
		if !w.quiet() {
			return false
		}
		to16(wide, frame)
		for i := range pcm {
			pcm[i] = int16(binary.LittleEndian.Uint16(frame[2*i:]))
		}
		for _, score := range spotter.write(pcm) {
			if score < wakeThreshold {
				run = 0
				continue
			}
			if run++; run >= wakeSteps {
				log.Printf("wake word heard (%.2f)", score)
				return true
			}
		}
	}
}

// openMic opens the microphone for listening: the Car Thing's own, or the computer's.
func (w *wakeListener) openMic() (micSource, io.Reader, error) {
	if virtual {
		return w.v.openMic()
	}
	cmd := exec.Command("arecord", "-q", "-D", w.v.mic, "-f", "S32_LE", "-r", "16000", "-c", "1", "-t", "raw")
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return nil, nil, err
	}
	return cmdSource{cmd: cmd, stderr: new(bytes.Buffer)}, out, nil
}

// release lets go of the microphone now, for a recording about to start.
func (w *wakeListener) release() {
	w.mu.Lock()
	src, done := w.src, w.done
	w.mu.Unlock()
	if src != nil {
		src.kill()
		<-done
	}
}

// woken chimes and asks the plugin for a conversation. The music pauses for it as it does
// for Athena; if no conversation starts (the mic is muted on Hermes, say), it carries on.
func (w *wakeListener) woken() {
	display.setPhase("ready")
	library.freeSpeaker()
	music.freeSpeaker()
	w.v.chime()
	select {
	case w.v.presses <- map[string]any{"input": "device", "action": "wake"}:
	default:
	}
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		w.v.mu.Lock()
		started := w.v.rec != nil
		w.v.mu.Unlock()
		if started {
			return
		}
	}
	if display.snapshot().Phase == "ready" {
		display.setPhase("idle")
	}
}

// chime is two short rising notes, to say the wake word was heard.
func (v *voice) chime() {
	const rate = speakerRate
	var pcm []byte
	for _, note := range []struct {
		f   float64
		dur float64
	}{{880, 0.09}, {1318.5, 0.16}} {
		n := int(note.dur * rate)
		for i := 0; i < n; i++ {
			t := float64(i) / rate
			env := math.Min(1, t/0.005) * math.Exp(-t*18)
			s := int16(9000 * env * math.Sin(2*math.Pi*note.f*t))
			for c := 0; c < speakerChannels; c++ {
				pcm = binary.LittleEndian.AppendUint16(pcm, uint16(s))
			}
		}
	}
	cmd := v.playCommand()
	in, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_, _ = in.Write(wavFileWith(pcm, rate, speakerChannels))
	in.Close()
	_ = cmd.Wait()
}
