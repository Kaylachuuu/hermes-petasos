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
	for _, q := range phoneAnswers {
		if at == q.ask || (strings.HasSuffix(q.ask, "=") && strings.HasPrefix(at, q.ask)) {
			if q.info != "" {
				return "\r\n" + q.info + "\r\n\r\nOK\r\n", false
			}
			return "\r\nOK\r\n", false
		}
	}
	return "\r\nERROR\r\n", false
}

// phoneAnswers are what a phone with no call going answers the questions a car asks as it
// connects (a Ford SYNC asks all of these). Some cars take an ERROR to one as the phone not
// being fit for hands-free, and stop offering its voice assistant.
// networkSignal is a phone network's indicators: service (the first in BlueALSA's AT+CIND list) on,
// and signal (the fifth) at 5 of 5.
const networkSignal = "\r\n+CIEV: 1,1\r\n\r\n+CIEV: 5,5\r\n"

var phoneAnswers = []struct{ ask, info string }{
	{"AT+CHLD=?", "+CHLD: (0,1,2,3)"},
	{"AT+COPS=", ""},
	{"AT+COPS?", `+COPS: 0,0,"Petasos"`},
	{"AT+CLCC", ""}, // no calls
	// An iPhone, as a car may hand its voice button only to one (Siri Eyes Free), with Siri
	// there to talk to when the car asks Apple's way (APLSIRI).
	{"AT+CGMI", "Apple Inc."},
	{"AT+CGMM", "iPhone"},
	{"AT+APLSIRI?", "+APLSIRI: 1"},
	{"AT+CGMR", version},
	{"AT+CNUM", ""},
	{"AT+CLIP=", ""},
	{"AT+CCWA=", ""},
	{"AT+CMEE=", ""},
	{"AT+NREC=", ""},
}

type carCallLine struct {
	mu   sync.Mutex
	open map[dbus.ObjectPath]bool
	out  chan<- map[string]any
}

var carCalls = &carCallLine{open: map[dbus.ObjectPath]bool{}}

// watch opens the AT channel of each car connected as a hands-free. The car asks its questions
// in the first second, and BlueALSA answers ERROR to any it asks before the channel is open, so
// a new one is opened as BlueALSA announces it; a look every few seconds catches any missed.
func (c *carCallLine) watch(out chan<- map[string]any) {
	c.out = out
	go c.watchAdded()
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

// watchAdded opens a car's AT channel the moment BlueALSA announces it.
func (c *carCallLine) watchAdded() {
	for ; ; time.Sleep(5 * time.Second) {
		conn, err := dbus.ConnectSystemBus() // its own, so its signals come to it alone
		if err != nil {
			continue
		}
		if err := conn.AddMatchSignal(dbus.WithMatchSender(bluealsaService),
			dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"), dbus.WithMatchMember("InterfacesAdded")); err != nil {
			conn.Close()
			continue
		}
		signals := make(chan *dbus.Signal, 16)
		conn.Signal(signals)
		for s := range signals {
			if len(s.Body) < 2 {
				continue
			}
			path, _ := s.Body[0].(dbus.ObjectPath)
			ifaces, _ := s.Body[1].(map[string]map[string]dbus.Variant)
			if _, ok := ifaces["org.bluealsa.RFCOMM1"]; !ok || path == "" {
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
		conn.Close() // the bus went away; join it again
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
	var signal *time.Timer
	var mu sync.Mutex // the reply here and the signal below both write
	signalled := false
	for {
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			return
		}
		reply, call := carCallAnswer(string(buf[:n]))
		if reply != "" && !call {
			log.Printf("the car asked %q", strings.TrimSpace(string(buf[:n]))) // what its buttons send, if not BVRA
		}
		if reply != "" {
			mu.Lock()
			_, err := f.Write([]byte(reply))
			mu.Unlock()
			if err != nil {
				return
			}
		}
		// BlueALSA tells the car there's no phone network, and a Ford SYNC 2 then passes no
		// button on, so the Car Thing says it has service, at full signal. The car asks for the
		// indicators again during its setup, so this waits until it has been quiet a moment. Only
		// once: the car asks for the network's name on hearing it, which mustn't start it again.
		if signal == nil {
			signal = time.AfterFunc(2*time.Second, func() {
				mu.Lock()
				defer mu.Unlock()
				signalled = true
				if _, err := f.Write([]byte(networkSignal)); err == nil {
					log.Printf("told the car the Car Thing has phone service")
				}
			})
			defer signal.Stop()
		} else {
			mu.Lock()
			if !signalled {
				signal.Reset(2 * time.Second)
			}
			mu.Unlock()
		}
		if call {
			log.Printf("the car's call button")
			ms := time.Now().UnixMilli()
			c.out <- map[string]any{"input": "car-call", "action": "press", "t": ms}
			c.out <- map[string]any{"input": "car-call", "action": "release", "t": ms}
		}
	}
}
