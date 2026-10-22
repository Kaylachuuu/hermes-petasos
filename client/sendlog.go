package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Sending the log to Hermes, for troubleshooting when the USB cable (and so ssh) isn't working:
// petasos's own log, plus the state of the USB port and the services around it. The plugin saves
// it as petasos/logs/<device>.log in the Hermes home.

const sentLogLimit = 384 << 10 // a Bluetooth hotspot can be slow

// logChecks are what the Car Thing reports besides its log; each runs for at most a few seconds.
var logChecks = [][]string{
	{"superbird-usb-role", "status"},
	{"sh", "-c", "ls /sys/class/udc; cat /sys/kernel/config/usb_gadget/*/UDC; cat /sys/class/usb_role/*/role; cat /var/lib/superbird-usb-role/boot-role"},
	{"ip", "-br", "addr"},
	{"networkctl", "--no-pager"},
	{"systemctl", "status", "--no-pager", "--lines=0", "android-tools-adbd", "superbird-usb-gadget", "superbird-usb-role", "usbmuxd", "bluealsa"},
	{"sh", "-c", "journalctl -b --no-pager -u android-tools-adbd -u superbird-usb-gadget -u superbird-usb-role -u usbmuxd -u bluealsa -u bluetooth -u systemd-networkd | grep -v 'PCM overrun' | tail -n 200"},
	// What the Car Thing offers over Bluetooth (its class and profiles), and what each device
	// paired with it offers and has connected: why a car does or doesn't take it for a phone.
	{"sh", "-c", "bluetoothctl show; for d in $(bluetoothctl devices | cut -d' ' -f2); do bluetoothctl info $d; done"},
	{"sh", "-c", "ls /sys/block; grep -E ' /run/petasos' /proc/mounts; cat /proc/filesystems | grep -E 'fat|hfs'; command -v ffmpeg"},
	{"sh", "-c", "dmesg | tail -n 200"},
}

// logReport puts the checks and the end of the log together.
func logReport(dir string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "petasos %s, %s\n", version, time.Now().Format(time.RFC3339))
	if !virtual {
		for _, c := range logChecks {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			out, err := exec.CommandContext(ctx, c[0], c[1:]...).CombinedOutput()
			cancel()
			fmt.Fprintf(&b, "\n=== %s\n%s", strings.Join(c, " "), out)
			if err != nil {
				fmt.Fprintf(&b, "(%v)\n", err)
			}
		}
	}
	var logs []byte
	for _, name := range []string{"petasos.log.1", "petasos.log"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			logs = append(logs, data...)
		}
	}
	if room := sentLogLimit - b.Len(); len(logs) > room && room > 0 {
		logs = logs[len(logs)-room:]
	}
	b.WriteString("\n=== petasos.log\n")
	b.Write(logs)
	return b.Bytes()
}

// sendLog sends the report to Hermes and says where it went.
func (b *bluetooth) sendLog(h *hermes) {
	b.message("Sending the log", "Sending it to "+display.agent()+"…")
	go func() {
		slow := &http.Client{Transport: h.http.Transport, Timeout: 3 * time.Minute}
		data, err := h.exchange("POST", "/api/plugins/petasos/log?device="+url.QueryEscape(h.cfg.Device), "text/plain",
			logReport(h.dir), slow, 1<<16)
		if err != nil {
			b.message("Couldn't send the log", friendlyHermes(err))
			return
		}
		var sent struct {
			Path string `json:"path"`
		}
		json.Unmarshal(data, &sent)
		b.message("Log sent", "It's on "+display.agent()+" at "+sent.Path+".")
	}()
}
