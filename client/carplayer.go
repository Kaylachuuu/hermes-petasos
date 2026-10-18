package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// The car sees the Car Thing as a music player (AVRCP): what's playing, the song's title, artist
// and length for the car's screen, and its play, pause and skip buttons. BlueZ takes these from a
// media player registered with it (org.bluez.Media1, in MPRIS's terms), which carPlayer is.
// The buttons go the way the car's media buttons always have, as car-play, car-next and
// car-previous, but play and pause only when they'd change something: many cars send play as
// they connect, which starts what was paused and leaves playing music alone.

const (
	carPlayerPath  = dbus.ObjectPath("/org/petasos/player")
	mprisPlayer    = "org.mpris.MediaPlayer2.Player"
	carPlayerEvery = time.Second
)

type carPlayer struct {
	mu     sync.Mutex
	out    chan<- map[string]any
	props  *prop.Properties
	shown  string  // what the car was last told, to tell it only what changed
	tracks int     // a new track ID for each song
	moving bool    // whether the position was moving on
	pos    float64 // the position last told, in seconds, at told
	told   time.Time
}

var car = &carPlayer{}

// start registers the player with BlueZ, retrying until it's up, then keeps the car told.
func (c *carPlayer) start(out chan<- map[string]any) {
	c.out = out
	for {
		err := c.register()
		if err == nil {
			break
		}
		log.Printf("couldn't show the music on a car's screen yet (%v); trying again in 10s", err)
		time.Sleep(10 * time.Second)
	}
	log.Printf("the music shows on a car's screen")
	for ; ; time.Sleep(carPlayerEvery) {
		c.update(display.snapshot().Music, time.Now())
	}
}

func (c *carPlayer) register() error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.props == nil {
		if err := conn.Export(carControls{c}, carPlayerPath, mprisPlayer); err != nil {
			c.mu.Unlock()
			return err
		}
		c.props, err = prop.Export(conn, carPlayerPath, prop.Map{mprisPlayer: carPlayerProps()})
		if err != nil {
			c.mu.Unlock()
			return err
		}
	}
	all, _ := c.props.GetAll(mprisPlayer)
	c.mu.Unlock()
	return conn.Object("org.bluez", adapterPath).Call("org.bluez.Media1.RegisterPlayer", 0, carPlayerPath, all).Err
}

func carPlayerProps() map[string]*prop.Prop {
	p := func(v any) *prop.Prop { return &prop.Prop{Value: v, Emit: prop.EmitTrue} }
	return map[string]*prop.Prop{
		"PlaybackStatus": p("Stopped"),
		"LoopStatus":     p("None"),
		"Shuffle":        p(false),
		"Rate":           p(1.0),
		"MinimumRate":    p(1.0),
		"MaximumRate":    p(1.0),
		"Position":       p(int64(0)),
		"Metadata":       p(map[string]dbus.Variant{}),
		"Volume":         p(1.0),
		"CanGoNext":      p(true),
		"CanGoPrevious":  p(true),
		"CanPlay":        p(true),
		"CanPause":       p(true),
		"CanSeek":        p(false),
		"CanControl":     p(true),
	}
}

// update tells the car what changed in what's playing.
func (c *carPlayer) update(m *nowPlaying, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.props == nil {
		return
	}
	status, song := "Stopped", ""
	if m != nil {
		status, song = "Paused", fmt.Sprintf("%s\x00%s\x00%d\x00%s", m.Title, m.Artist, m.Seconds, m.Source)
		if m.Playing {
			status = "Playing"
		}
	}
	if song != c.shown {
		c.shown = song
		meta := map[string]dbus.Variant{}
		if m != nil {
			c.tracks++
			meta["mpris:trackid"] = dbus.MakeVariant(dbus.ObjectPath(fmt.Sprintf("/org/petasos/track/%d", c.tracks)))
			meta["xesam:title"] = dbus.MakeVariant(m.Title)
			meta["xesam:artist"] = dbus.MakeVariant([]string{m.Artist})
			meta["xesam:album"] = dbus.MakeVariant(m.Source)
			if m.Seconds > 0 {
				meta["mpris:length"] = dbus.MakeVariant(int64(m.Seconds) * 1e6)
			}
		}
		c.props.SetMust(mprisPlayer, "Metadata", meta)
		c.told = time.Time{} // and the position, from the start
	}
	if v, _ := c.props.Get(mprisPlayer, "PlaybackStatus"); v.Value() != status {
		c.props.SetMust(mprisPlayer, "PlaybackStatus", status)
	}
	if m == nil || m.At == 0 {
		return
	}
	pos := m.Position
	if m.Moving {
		pos += now.Sub(time.UnixMilli(m.At)).Seconds()
	}
	expected := c.pos
	if c.moving {
		expected += now.Sub(c.told).Seconds()
	}
	if c.told.IsZero() || m.Moving != c.moving || pos-expected > 2 || expected-pos > 2 {
		c.props.SetMust(mprisPlayer, "Position", int64(pos*1e6))
		c.pos, c.moving, c.told = pos, m.Moving, now
	}
}

// press passes a car button on as the car's media buttons always came.
func (c *carPlayer) press(name string) {
	if c.out == nil {
		return
	}
	ms := time.Now().UnixMilli()
	c.out <- map[string]any{"input": name, "action": "press", "t": ms}
	c.out <- map[string]any{"input": name, "action": "release", "t": ms}
}

func (c *carPlayer) playing() bool {
	m := display.snapshot().Music
	return m != nil && m.Playing
}

// carControls is the player's methods, which BlueZ calls for the car's buttons.
type carControls struct{ c *carPlayer }

func (p carControls) Play() *dbus.Error {
	if !p.c.playing() {
		p.c.press("car-play")
	}
	return nil
}

func (p carControls) Pause() *dbus.Error {
	if p.c.playing() {
		p.c.press("car-play")
	}
	return nil
}

func (p carControls) Stop() *dbus.Error { return p.Pause() }

func (p carControls) PlayPause() *dbus.Error {
	p.c.press("car-play")
	return nil
}

func (p carControls) Next() *dbus.Error {
	p.c.press("car-next")
	return nil
}

func (p carControls) Previous() *dbus.Error {
	p.c.press("car-previous")
	return nil
}
