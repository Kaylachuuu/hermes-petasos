package main

// The Windows app: one petasos.exe that is the window (run with no arguments), the petasos
// program behind it (virtual, started by the window), and the recorder and player that program
// uses (--record RATE, --play), as the Mac app's own executable is for it. The window is a
// WebView2 showing the same screen, settings and sign-in pages as the Mac app.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

var (
	winmm  = syscall.NewLazyDLL("winmm.dll")
	user32 = syscall.NewLazyDLL("user32.dll")
)

// windowsApp handles the Windows-only ways the program is run, and says whether it did.
func windowsApp() bool {
	args := os.Args[1:]
	switch {
	case len(args) >= 1 && args[0] == "--record":
		rate := 16000
		if len(args) >= 2 {
			if n, err := strconv.Atoi(args[1]); err == nil {
				rate = n
			}
		}
		mic := ""
		if len(args) >= 3 {
			mic = args[2]
		}
		if err := winRecord(rate, mic); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case len(args) >= 1 && args[0] == "--inputs":
		_ = json.NewEncoder(os.Stdout).Encode(winInputs())
		os.Exit(0)
	case len(args) >= 1 && args[0] == "--play":
		if err := winPlay(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	case len(args) == 0:
		runWindow()
		return true
	}
	return false
}

// --- the window ----------------------------------------------------------------------------

func appFolder() string {
	base, err := os.UserConfigDir() // %APPDATA%
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "Petasos")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func runWindow() {
	dir := appFolder()
	self, err := os.Executable()
	if err != nil {
		winMessage("Petasos", "Can't find this program's own file: "+err.Error())
		return
	}
	engine := &winEngine{self: self, dir: dir}
	engine.start()
	defer engine.stop()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(dir, "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title: "Petasos", Width: 800, Height: 544, IconId: 1, Center: true,
		},
	})
	if w == nil {
		winMessage("Petasos", "Petasos needs Microsoft Edge WebView2, which comes with Windows 10 and 11. "+
			"Install the WebView2 Runtime from microsoft.com/edge/webview2 and start Petasos again.")
		return
	}
	defer w.Destroy()
	w.SetSize(800, 544, webview2.HintNone) // the inside of the window: the screen and its controls
	w.SetSize(560, 381, webview2.HintMin)
	w.SetHtml(`<html><body style="margin:0;background:#1f1b17"></body></html>`)
	go func() {
		// Show the screen once the program behind it answers.
		for i := 0; i < 150; i++ {
			if resp, err := http.Get("http://" + screenAddr + "/app/signin"); err == nil {
				resp.Body.Close()
				w.Dispatch(func() { w.Navigate("http://" + screenAddr + "/") })
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		w.Dispatch(func() {
			w.SetHtml(`<html><body style="margin:0;background:#1f1b17;color:#c9b48a;font:16px sans-serif;padding:32px">` +
				`Petasos didn't start. Its log is in ` + filepath.Join(dir, "petasos.log") + `.</body></html>`)
		})
	}()
	w.Run()
}

// winEngine keeps the petasos program running behind the window: saving settings or signing
// in restarts it, as on the Mac.
type winEngine struct {
	self, dir string
	mu        sync.Mutex
	cmd       *exec.Cmd
	quitting  bool
}

func (e *winEngine) start() {
	logFile, _ := os.OpenFile(filepath.Join(e.dir, "petasos.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	engine := e.self
	if newer, ok := newerEngine(e.dir); ok {
		engine = newer // an update from the plugin (engine.go)
	}
	cmd := exec.Command(engine, "-dir", e.dir, "-audio", e.self, "virtual")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		winMessage("Petasos", "Couldn't start Petasos: "+err.Error())
		return
	}
	e.mu.Lock()
	e.cmd = cmd
	e.mu.Unlock()
	go func() {
		cmd.Wait()
		if logFile != nil {
			logFile.Close()
		}
		time.Sleep(time.Second)
		e.mu.Lock()
		again := !e.quitting && e.cmd == cmd
		e.mu.Unlock()
		if again {
			e.start() // it stopped on its own: start it again
		}
	}()
}

func (e *winEngine) stop() {
	e.mu.Lock()
	e.quitting = true
	cmd := e.cmd
	e.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func winMessage(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	user32.NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x40) // MB_ICONINFORMATION
}

// --- volume --------------------------------------------------------------------------------

// computerVolume nudges Windows's own volume with the volume keys, 2% a press; Windows shows
// its own volume display as it does for the keyboard's keys.
func computerVolume(delta int) {
	key := uintptr(0xAF) // VK_VOLUME_UP
	if delta < 0 {
		key, delta = 0xAE, -delta // VK_VOLUME_DOWN
	}
	press := user32.NewProc("keybd_event")
	for i := 0; i < max(1, delta/2); i++ {
		press.Call(key, 0, 0, 0)
		press.Call(key, 0, 2, 0) // KEYEVENTF_KEYUP
	}
}

// --- recording and playing -----------------------------------------------------------------

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

type waveHdr struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

const (
	waveMapper = 0xFFFFFFFF
	whdrDone   = 1
)

// waveInCaps is WAVEINCAPSW: a microphone's name, among other things.
type waveInCaps struct {
	Mid, Pid      uint16
	DriverVersion uint32
	Name          [32]uint16
	Formats       uint32
	Channels      uint16
	Reserved      uint16
}

// winInputs lists the microphones, for the settings page. Windows numbers them afresh as they
// come and go, so each is known by its name.
func winInputs() []micInfo {
	mics := []micInfo{}
	n, _, _ := winmm.NewProc("waveInGetNumDevs").Call()
	caps := winmm.NewProc("waveInGetDevCapsW")
	for i := uintptr(0); i < n; i++ {
		var c waveInCaps
		if r, _, _ := caps.Call(i, uintptr(unsafe.Pointer(&c)), unsafe.Sizeof(c)); r != 0 {
			continue
		}
		name := syscall.UTF16ToString(c.Name[:])
		mics = append(mics, micInfo{ID: name, Name: name})
	}
	return mics
}

// winRecord writes the microphone named mic (or the default one) to stdout as 32-bit
// little-endian mono samples at rate (what arecord gives on the Car Thing), until stdout is
// closed or it is stopped.
func winRecord(rate int, mic string) error {
	format := waveFormatEx{FormatTag: 1, Channels: 1, SamplesPerSec: uint32(rate),
		AvgBytesPerSec: uint32(rate * 2), BlockAlign: 2, BitsPerSample: 16}
	device := uintptr(waveMapper)
	if mic != "" {
		found := false
		for i, m := range winInputs() {
			if m.ID == mic {
				device, found = uintptr(i), true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "%s isn't connected; recording from the default microphone\n", mic)
		}
	}
	var h uintptr
	if r, _, _ := winmm.NewProc("waveInOpen").Call(uintptr(unsafe.Pointer(&h)), device,
		uintptr(unsafe.Pointer(&format)), 0, 0, 0); r != 0 {
		return fmt.Errorf("no microphone to record from (waveInOpen %d)", r)
	}
	defer winmm.NewProc("waveInClose").Call(h)
	defer winmm.NewProc("waveInReset").Call(h)
	prepare, add := winmm.NewProc("waveInPrepareHeader"), winmm.NewProc("waveInAddBuffer")
	const buffers = 8
	frame := rate / 20 * 2 // 50 ms of 16-bit samples
	bufs := make([][]byte, buffers)
	hdrs := make([]waveHdr, buffers)
	for i := range hdrs {
		bufs[i] = make([]byte, frame)
		hdrs[i] = waveHdr{Data: uintptr(unsafe.Pointer(&bufs[i][0])), BufferLength: uint32(frame)}
		prepare.Call(h, uintptr(unsafe.Pointer(&hdrs[i])), unsafe.Sizeof(hdrs[i]))
		add.Call(h, uintptr(unsafe.Pointer(&hdrs[i])), unsafe.Sizeof(hdrs[i]))
	}
	if r, _, _ := winmm.NewProc("waveInStart").Call(h); r != 0 {
		return fmt.Errorf("the microphone didn't start (waveInStart %d)", r)
	}
	out := make([]byte, 0, frame*2)
	for next := 0; ; next = (next + 1) % buffers {
		hdr := &hdrs[next]
		for hdr.Flags&whdrDone == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		out = out[:0]
		n := int(hdr.BytesRecorded) / 2
		for i := 0; i < n; i++ {
			s := int32(int16(binary.LittleEndian.Uint16(bufs[next][2*i:]))) << 16 // left-aligned, as the Car Thing's
			out = binary.LittleEndian.AppendUint32(out, uint32(s))
		}
		if _, err := os.Stdout.Write(out); err != nil {
			return nil // the program stopped listening
		}
		hdr.Flags &^= whdrDone
		hdr.BytesRecorded = 0
		add.Call(h, uintptr(unsafe.Pointer(hdr)), unsafe.Sizeof(*hdr))
	}
}

// winPlay plays a WAV file from stdin on the default speakers, returning when it has played.
func winPlay() error {
	wav, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	if len(wav) < 44 {
		return errors.New("no audio to play")
	}
	// SND_MEMORY | SND_NODEFAULT, and synchronous: it returns once played (or when this process is stopped).
	if r, _, _ := winmm.NewProc("PlaySoundW").Call(uintptr(unsafe.Pointer(&wav[0])), 0, 0x4|0x2); r == 0 {
		return errors.New("Windows couldn't play the reply")
	}
	return nil
}
