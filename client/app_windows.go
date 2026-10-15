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
	w.SetSize(420, 286, webview2.HintMin)
	hwnd := uintptr(w.Window())
	shapeWindow(hwnd)
	w.Bind("petasosWindow", func(what string) { windowAction(hwnd, what) })
	go keepOnTop(w, dir)
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

// The window has no title bar, like a gadget: the page draws minimize and close buttons and
// is dragged by its background (windowAction). The inside keeps the Car Thing's shape, as on
// the Mac: its screen plus the controls under it.
const aspectW, aspectH = 800, 544

var (
	windowProc     uintptr // the window's own procedure, which ours passes everything on to
	shapeProc      = syscall.NewCallback(shapeWindowProc)
	callWindowProc = user32.NewProc("CallWindowProcW")
	getWindowRect  = user32.NewProc("GetWindowRect")
	getClientRect  = user32.NewProc("GetClientRect")
	setWindowPos   = user32.NewProc("SetWindowPos")
)

type winRect struct{ Left, Top, Right, Bottom int32 }

// shapeWindow steps in front of the window's procedure (shapeWindowProc), then sizes the
// window so its inside is aspectW by aspectH now that the title bar is gone.
func shapeWindow(hwnd uintptr) {
	const gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC (-4)
	windowProc, _, _ = user32.NewProc("SetWindowLongPtrW").Call(hwnd, gwlpWndProc, shapeProc)
	const noMove, noZOrder, noActivate, frameChanged = 0x2, 0x4, 0x10, 0x20
	setWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x1|noMove|noZOrder|noActivate|frameChanged) // 0x1: SWP_NOSIZE
	fw, fh := frame(hwnd)
	setWindowPos.Call(hwnd, 0, 0, 0, uintptr(aspectW+fw), uintptr(aspectH+fh), noMove|noZOrder|noActivate)
}

// frame is how much wider and taller the window is than its inside: its resizing borders.
func frame(hwnd uintptr) (int32, int32) {
	var win, client winRect
	getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&win)))
	getClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	return (win.Right - win.Left) - (client.Right - client.Left), (win.Bottom - win.Top) - (client.Bottom - client.Top)
}

func shapeWindowProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	const wmNCCalcSize, wmSizing = 0x0083, 0x0214
	switch {
	case msg == wmNCCalcSize && wParam == 1:
		// The window keeps its borders (for resizing, snapping and its shadow) but not its title
		// bar: the inside reaches the top, where the title bar was. A maximized window's frame
		// hangs off the screen, so its top is brought back in by the same amount.
		r := rectAt(lParam)
		top := r.Top
		ret, _, _ := callWindowProc.Call(windowProc, hwnd, msg, wParam, lParam)
		r.Top = top
		if zoomed, _, _ := user32.NewProc("IsZoomed").Call(hwnd); zoomed != 0 {
			r.Top += int32(systemMetric(33) + systemMetric(92)) // SM_CYSIZEFRAME + SM_CXPADDEDBORDER
		}
		return ret
	case msg == wmSizing && lParam != 0:
		keepShape(hwnd, wParam, rectAt(lParam))
	}
	r, _, _ := callWindowProc.Call(windowProc, hwnd, msg, wParam, lParam)
	return r
}

// rectAt is the RECT a window message points to (for WM_NCCALCSIZE, the first of its three).
func rectAt(lParam uintptr) *winRect {
	return (*winRect)(unsafe.Add(unsafe.Pointer(nil), lParam))
}

func systemMetric(i uintptr) uintptr {
	v, _, _ := user32.NewProc("GetSystemMetrics").Call(i)
	return v
}

// keepShape changes the proposed window rectangle r so the inside keeps aspectW:aspectH,
// moving the edge that isn't being dragged.
func keepShape(hwnd, edge uintptr, r *winRect) {
	fw, fh := frame(hwnd)
	w, h := r.Right-r.Left-fw, r.Bottom-r.Top-fh
	const top, topLeft, topRight, bottom = 3, 4, 5, 6 // WMSZ_*
	switch edge {
	case top, bottom: // dragging the top or bottom: the width follows
		r.Right = r.Left + h*aspectW/aspectH + fw
	case topLeft, topRight: // a top corner: the top follows the width
		r.Top = r.Bottom - w*aspectH/aspectW - fh
	default: // a side or a bottom corner: the bottom follows the width
		r.Bottom = r.Top + w*aspectH/aspectW + fh
	}
}

// windowAction is the page's petasosWindow(what): "drag" (the mouse went down on the page's
// background, so the window moves with it as if by its title bar), "minimize" or "close".
func windowAction(hwnd uintptr, what string) {
	post := user32.NewProc("PostMessageW")
	switch what {
	case "drag":
		user32.NewProc("ReleaseCapture").Call()
		post.Call(hwnd, 0x00A1, 2, 0) // WM_NCLBUTTONDOWN on HTCAPTION
	case "minimize":
		user32.NewProc("ShowWindow").Call(hwnd, 6) // SW_MINIMIZE
	case "close":
		post.Call(hwnd, 0x0010, 0, 0) // WM_CLOSE
	}
}

// keepOnTop keeps the window above other windows while the pin under the screen says so
// (window.json), checking every half second.
func keepOnTop(w webview2.WebView, dir string) {
	applied := false
	for first := true; ; first = false {
		on := loadWindowSettings(dir).OnTop
		if first || on != applied {
			applied = on
			after := uintptr(^uintptr(1)) // HWND_NOTOPMOST (-2)
			if on {
				after = ^uintptr(0) // HWND_TOPMOST (-1)
			}
			w.Dispatch(func() {
				// SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE
				user32.NewProc("SetWindowPos").Call(uintptr(w.Window()), after, 0, 0, 0, 0, 0x1|0x2|0x10)
			})
		}
		time.Sleep(500 * time.Millisecond)
	}
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
