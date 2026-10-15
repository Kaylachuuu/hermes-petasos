package main

import (
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Music from the phone. The Car Thing is a Bluetooth speaker to the phone (BlueALSA's A2DP
// sink), so choosing it as the phone's audio output sends whatever the phone plays to it, and
// petasos passes that on to the car (bluealsa-aplay, from the phone's stream to the car's).
// Over AVRCP the Car Thing is also the phone's remote: it shows what is playing, and the knob,
// the presets and the car's own buttons can play, pause and skip (the plugin's music_* actions).
//
// BlueALSA lets one program at a time play to the car, so the music steps aside for Athena:
// the phone is paused as soon as she starts listening, the relay stops before she speaks, and
// both come back once she is done (only if it was the Car Thing that paused it).

const (
	a2dpSinkUUID     = "0000110b-0000-1000-8000-00805f9b34fb" // our side of the phone's stream
	musicRefresh     = 2 * time.Second
	mediaPlayerIface = "org.bluez.MediaPlayer1"
)

// nowPlaying is what the screen shows of the phone's music.
type nowPlaying struct {
	Title   string `json:"title,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Playing bool   `json:"playing"`
	Source  string `json:"source"` // the phone's name
}

type phoneMusic struct {
	mu         sync.Mutex
	player     dbus.ObjectPath // the phone's AVRCP player, when there is one
	status     string          // playing, paused, stopped
	phone      string          // the address of the phone streaming to us, when one is
	relay      *relay          // bluealsa-aplay, passing the phone's stream to the car
	held       bool            // Athena has the speaker (from listening until idle again)
	pausedByUs bool            // the phone was playing when Athena took over
}

var music = &phoneMusic{}

type relay struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// watch follows the phone's player and stream, keeping the relay running while the phone
// streams and Athena isn't using the speaker.
func (m *phoneMusic) watch() {
	for ; ; time.Sleep(musicRefresh) {
		m.refresh()
	}
}

func (m *phoneMusic) refresh() {
	bt.mu.Lock()
	conn := bt.conn
	bt.mu.Unlock()
	if conn == nil {
		return
	}
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := conn.Object("org.bluez", "/").Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objs); err != nil {
		return
	}
	var (
		player             dbus.ObjectPath
		status, phone, car string
		np                 *nowPlaying
	)
	address := func(dev dbus.ObjectPath) (string, string, bool) { // address, name, is a speaker
		props := objs[dev]["org.bluez.Device1"]
		var addr, name string
		var uuids []string
		if v, ok := props["Address"]; ok {
			_ = v.Store(&addr)
		}
		if v, ok := props["Alias"]; ok {
			_ = v.Store(&name)
		}
		if v, ok := props["UUIDs"]; ok {
			_ = v.Store(&uuids)
		}
		speaker := false
		for _, u := range uuids {
			speaker = speaker || strings.EqualFold(u, audioSinkUUID)
		}
		return addr, name, speaker
	}
	for path, ifaces := range objs {
		if dev, ok := ifaces["org.bluez.Device1"]; ok {
			var connected bool
			if v, ok := dev["Connected"]; ok {
				_ = v.Store(&connected)
			}
			if addr, _, speaker := address(path); connected && speaker && car == "" {
				car = addr
			}
		}
		if t, ok := ifaces["org.bluez.MediaTransport1"]; ok {
			var uuid string
			var dev dbus.ObjectPath
			if v, ok := t["UUID"]; ok {
				_ = v.Store(&uuid)
			}
			if v, ok := t["Device"]; ok {
				_ = v.Store(&dev)
			}
			if strings.EqualFold(uuid, a2dpSinkUUID) { // something streams to us: the phone
				phone, _, _ = address(dev)
			}
		}
		if p, ok := ifaces[mediaPlayerIface]; ok && player == "" {
			var dev dbus.ObjectPath
			if v, ok := p["Device"]; ok {
				_ = v.Store(&dev)
			}
			if _, _, speaker := address(dev); speaker {
				continue // a car's own player, not the phone's
			}
			player = path
			np = &nowPlaying{}
			_, np.Source, _ = address(dev)
			if v, ok := p["Status"]; ok {
				_ = v.Store(&status)
			}
			np.Playing = status == "playing"
			var track map[string]dbus.Variant
			if v, ok := p["Track"]; ok {
				_ = v.Store(&track)
			}
			if v, ok := track["Title"]; ok {
				_ = v.Store(&np.Title)
			}
			if v, ok := track["Artist"]; ok {
				_ = v.Store(&np.Artist)
			}
		}
	}

	m.mu.Lock()
	m.player, m.status, m.phone = player, status, phone
	start := phone != "" && car != "" && !m.held && m.relay == nil
	stop := m.relay != nil && phone == ""
	m.mu.Unlock()
	if stop {
		m.stopRelay()
	}
	if start {
		m.startRelay(phone, car)
	}
	display.update(func(st *screenState) {
		if np == nil || (np.Title == "" && !np.Playing) {
			st.Music = nil
		} else {
			st.Music = np
		}
	})
}

func (m *phoneMusic) startRelay(phone, car string) {
	cmd := exec.Command("bluealsa-aplay", "--pcm=bluealsa:DEV="+car+",PROFILE=a2dp", phone)
	if err := cmd.Start(); err != nil {
		log.Printf("can't pass the phone's music on to the car: %v", err)
		return
	}
	log.Printf("passing music from %s on to %s", phone, car)
	r := &relay{cmd: cmd, done: make(chan struct{})}
	m.mu.Lock()
	m.relay = r
	m.mu.Unlock()
	go func() {
		err := cmd.Wait()
		close(r.done)
		m.mu.Lock()
		if m.relay == r {
			m.relay = nil
			log.Printf("the music relay stopped: %v", err)
		}
		m.mu.Unlock()
	}()
}

// stopRelay stops passing music on and waits until the car's stream is free.
func (m *phoneMusic) stopRelay() {
	m.mu.Lock()
	r := m.relay
	m.relay = nil
	m.mu.Unlock()
	if r == nil {
		return
	}
	_ = r.cmd.Process.Kill()
	select {
	case <-r.done:
	case <-time.After(time.Second):
	}
}

// phaseChanged follows Athena: listening, thinking or speaking pauses the phone; idle again
// brings it back if the Car Thing paused it.
func (m *phoneMusic) phaseChanged(phase string) {
	if virtual {
		return
	}
	m.mu.Lock()
	switch {
	case phase == "idle" && m.held:
		m.held = false
		resume := m.pausedByUs
		m.pausedByUs = false
		player := m.player
		m.mu.Unlock()
		if resume {
			go m.call(player, "Play")
		}
		go m.refresh() // restarts the relay
		return
	case phase != "idle" && !m.held:
		m.held = true
		if m.status == "playing" {
			m.pausedByUs = true
			player := m.player
			m.mu.Unlock()
			go m.call(player, "Pause")
			return
		}
	}
	m.mu.Unlock()
}

// freeSpeaker is called just before Athena plays to the car: the relay must let go of it.
func (m *phoneMusic) freeSpeaker() {
	if virtual {
		return
	}
	m.mu.Lock()
	m.held = true
	m.mu.Unlock()
	m.stopRelay()
}

// control carries out the plugin's music command: toggle, next or previous.
func (m *phoneMusic) control(op string) {
	if virtual {
		display.notice("Music plays from the Car Thing, not here.")
		return
	}
	m.mu.Lock()
	player, status := m.player, m.status
	if op == "toggle" || op == "play" || op == "pause" {
		m.pausedByUs = false // a deliberate choice stands, even after Athena
	}
	m.mu.Unlock()
	if player == "" {
		display.notice("No music from a phone. Choose " + bt.shownName() + " as its speaker.")
		return
	}
	method := map[string]string{"next": "Next", "previous": "Previous", "play": "Play", "pause": "Pause"}[op]
	if op == "toggle" {
		method = "Play"
		if status == "playing" {
			method = "Pause"
		}
	}
	if method == "" {
		return
	}
	m.call(player, method)
	time.Sleep(300 * time.Millisecond)
	m.refresh()
}

func (m *phoneMusic) call(player dbus.ObjectPath, method string) { musicCall(player, method) }

// musicCall asks the phone's player to Play, Pause, Next or Previous.
var musicCall = func(player dbus.ObjectPath, method string) {
	bt.mu.Lock()
	conn := bt.conn
	bt.mu.Unlock()
	if conn == nil || player == "" {
		return
	}
	if err := conn.Object("org.bluez", player).Call(mediaPlayerIface+"."+method, 0).Err; err != nil {
		log.Printf("music %s: %v", method, err)
	}
}

// shownName is what the Car Thing is called, as the phone lists it among its speakers.
func (b *bluetooth) shownName() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.name == "" {
		return "Car Thing"
	}
	return b.name
}
