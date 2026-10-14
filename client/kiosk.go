package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Pointing the kiosk at the screen. The image's chromium kiosk starts on whatever page the image
// was built with, and keeps its DevTools endpoint open on loopback port 9223. This checks every
// few seconds which page it shows and, when it isn't ours (a fresh boot, or the kiosk restarted),
// tells it to load the screen. So the screen works on an image built before it existed, and
// changes to it never need a reflash.

const (
	devtools  = "http://127.0.0.1:9223"
	screenURL = "http://" + screenAddr + "/"
)

func keepKioskOnScreen() {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	lastProblem := ""
	for ; ; time.Sleep(5 * time.Second) {
		err := pointKiosk(client)
		if err == nil {
			lastProblem = ""
			continue
		}
		if msg := err.Error(); msg != lastProblem {
			log.Printf("can't show the screen yet: %v", err)
			lastProblem = msg
		}
	}
}

func pointKiosk(client *http.Client) error {
	resp, err := client.Get(devtools + "/json/list")
	if err != nil {
		return fmt.Errorf("the kiosk browser isn't up (%v)", err)
	}
	defer resp.Body.Close()
	var targets []struct {
		Type, URL            string
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return fmt.Errorf("the kiosk browser's page list: %v", err)
	}
	for _, t := range targets {
		if t.Type != "page" {
			continue
		}
		if strings.HasPrefix(t.URL, screenURL) {
			return nil
		}
		if t.WebSocketDebuggerURL == "" {
			return fmt.Errorf("the kiosk page has no DevTools connection")
		}
		if err := devtoolsCall(t.WebSocketDebuggerURL, "Page.navigate", map[string]string{"url": screenURL}); err != nil {
			return fmt.Errorf("telling the kiosk to show the screen: %v", err)
		}
		log.Printf("screen shown on the kiosk")
		return nil
	}
	return fmt.Errorf("the kiosk browser has no page open")
}

// devtoolsCall sends one DevTools command over a bare-bones WebSocket and waits for its answer.
// Just enough of RFC 6455 for a loopback connection: one masked text frame out, unmasked text
// frames back.
func devtoolsCall(wsURL, method string, params any) error {
	u, err := url.Parse(wsURL)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", u.Host, 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host,
		base64.StdEncoding.EncodeToString(key))
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("DevTools refused the connection: HTTP %d", resp.StatusCode)
	}
	msg, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": params})
	if _, err := conn.Write(wsFrame(msg)); err != nil {
		return err
	}
	for {
		payload, err := wsRead(r)
		if err != nil {
			return err
		}
		var reply struct {
			ID    int `json:"id"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &reply) == nil && reply.ID == 1 {
			if reply.Error != nil {
				return fmt.Errorf("%s", reply.Error.Message)
			}
			return nil
		}
	}
}

func wsFrame(payload []byte) []byte {
	frame := []byte{0x81} // final fragment, text
	n := len(payload)
	switch {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	case n < 1<<16:
		frame = append(frame, 0x80|126, byte(n>>8), byte(n))
	default:
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(n))
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	frame = append(frame, mask...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	return frame
}

func wsRead(r *bufio.Reader) ([]byte, error) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, err
	}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		n = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		n = binary.BigEndian.Uint64(ext)
	}
	if n > 4<<20 {
		return nil, fmt.Errorf("DevTools frame too large")
	}
	var mask []byte
	if head[1]&0x80 != 0 {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(r, mask); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	for i := range payload {
		if mask != nil {
			payload[i] ^= mask[i%4]
		}
	}
	if head[0]&0x0f == 0x8 {
		return nil, fmt.Errorf("DevTools closed the connection")
	}
	return payload, nil
}
