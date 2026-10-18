package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// The car's call button. To the car's hands-free, the Car Thing is a phone (BlueALSA's audio
// gateway), and a car asks a phone for its voice assistant with AT+BVRA=1, which BlueALSA leaves
// to whoever opens the connection's AT channel (org.bluealsa.RFCOMM1.Open). carCalls opens it for
// each car and answers what BlueALSA doesn't: voice recognition starts a conversation (car-call),
// at once handing the car back its media so Athena is heard; anything else gets the ERROR
// BlueALSA would have given. The car only asks once the image's BlueALSA says the Car Thing has
// voice recognition (image/meta-petasos/recipes-multimedia/bluealsa).

const bluealsaService = "org.bluealsa"

// bluealsaAnswers are the car's commands BlueALSA answers itself (ba-rfcomm.c's handlers); the
// rest come to us to answer.
var bluealsaAnswers = []string{"AT+CIND=?", "AT+CIND?", "AT+CMER=", "AT+BIA=", "AT+BRSF=", "AT+NREC=",
	"AT+VGM=", "AT+VGS=", "AT+BTRH?", "AT+BCC", "AT+BCS=", "AT+BAC=", "AT+ANDROID=", "AT+IPHONEACCEV=", "AT+XAPL="}

// carCallAnswer is what to send the car for one AT message, and whether it is the call button.
func carCallAnswer(msg string) (reply string, call bool) {
	at := strings.ToUpper(strings.TrimSpace(msg))
	if !strings.HasPrefix(at, "AT") {
		return "", false // a response, which BlueALSA deals with
	}
	for _, own := range bluealsaAnswers {
		if at == own || (strings.HasSuffix(own, "=") && strings.HasPrefix(at, own)) {
			return "", false
		}
	}
	switch at {
	case "AT+BVRA=1", "AT+CKPD=200", "AT+BLDN": // voice recognition; HSP's button; redial, for cars without
		return "\r\nOK\r\n\r\n+BVRA: 0\r\n", true
	case "AT+BVRA=0":
		return "\r\nOK\r\n", false
	}
	return "\r\nERROR\r\n", false
}

type carCallLine struct {
	mu   sync.Mutex
	open map[dbus.ObjectPath]bool
	out  chan<- map[string]any
}

var carCalls = &carCallLine{open: map[dbus.ObjectPath]bool{}}

// watch opens the AT channel of each car connected as a hands-free.
func (c *carCallLine) watch(out chan<- map[string]any) {
	c.out = out
	for ; ; time.Sleep(5 * time.Second) {
		conn, err := dbus.SystemBus()
		if err != nil {
			continue
		}
		var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
		if conn.Object(bluealsaService, "/org/bluealsa").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects) != nil {
			continue
		}
		for path, ifaces := range objects {
			if _, ok := ifaces["org.bluealsa.RFCOMM1"]; !ok {
				continue
			}
			c.mu.Lock()
			busy := c.open[path]
			c.open[path] = true
			c.mu.Unlock()
			if !busy {
				go c.serve(conn, path)
			}
		}
	}
}

func (c *carCallLine) serve(conn *dbus.Conn, path dbus.ObjectPath) {
	defer func() {
		c.mu.Lock()
		delete(c.open, path)
		c.mu.Unlock()
	}()
	var fd dbus.UnixFD
	if err := conn.Object(bluealsaService, path).Call("org.bluealsa.RFCOMM1.Open", 0).Store(&fd); err != nil {
		log.Printf("couldn't take the car's call button (%s): %v", path, err)
		time.Sleep(30 * time.Second) // someone else has it; don't keep asking
		return
	}
	f := os.NewFile(uintptr(fd), "rfcomm")
	defer f.Close()
	log.Printf("taking the car's call button (%s)", path)
	buf := make([]byte, 512)
	for {
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			return
		}
		reply, call := carCallAnswer(string(buf[:n]))
		if reply != "" {
			if _, err := f.Write([]byte(reply)); err != nil {
				return
			}
		}
		if call {
			log.Printf("the car's call button")
			ms := time.Now().UnixMilli()
			c.out <- map[string]any{"input": "car-call", "action": "press", "t": ms}
			c.out <- map[string]any{"input": "car-call", "action": "release", "t": ms}
		}
	}
}
