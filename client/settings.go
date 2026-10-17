package main

import "fmt"

// The settings on the Car Thing itself. Holding the mic button opens them. The preset buttons
// become tabs while they are open, in two sets the top-right button swaps between
// (settingsTabs): Device, Display, Bluetooth and Network, then Music and Soft keys.
// This is the Display tab: the light's brightness, dimming and time-out, and how the top meander
// shows the sound. A choice is changed by pressing the knob, turning it, and pressing again; the
// front button puts it back.

type choice struct {
	value int
	label string
}

var (
	brightnessChoices = []choice{{0, "Auto"}, {10, "10%"}, {20, "20%"}, {30, "30%"}, {40, "40%"},
		{50, "50%"}, {60, "60%"}, {70, "70%"}, {80, "80%"}, {90, "90%"}, {100, "100%"}}
	waitChoices     = []choice{{0, "Off"}, {15, "15 seconds"}, {30, "30 seconds"}, {45, "45 seconds"}, {60, "60 seconds"}}
	dimAfterChoices = waitChoices[1:] // never dimming is the Dim screen box
	dimToChoices    = []choice{{10, "10%"}, {20, "20%"}, {30, "30%"}, {40, "40%"}, {50, "50%"},
		{60, "60%"}, {70, "70%"}}
	offAfterChoices = waitChoices
)

// labelFor is the choice's label for a value, or the nearest choice's.
func labelFor(choices []choice, value int) string {
	return choices[nearest(choices, value)].label
}

func nearest(choices []choice, value int) int {
	best := 0
	for i, c := range choices {
		if abs(c.value-value) < abs(choices[best].value-value) {
			best = i
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// stepChoice moves a value delta choices along, stopping at the ends.
func stepChoice(choices []choice, value, delta int) int {
	i := max(0, min(len(choices)-1, nearest(choices, value)+delta))
	return choices[i].value
}

// screenView is the Display tab; editing is whether the highlighted row is being changed.
func (b *bluetooth) screenView() { b.screenViewAt(-1, false) }

func (b *bluetooth) screenViewAt(sel int, editing bool) {
	s := backlight.settings()
	auto := "Auto"
	if !backlight.hasSensor() {
		auto = "Auto (no light sensor)"
	}
	bright := labelFor(brightnessChoices, s.Brightness)
	if s.Brightness == 0 {
		bright = auto
	}
	setting := func(icon, label, value string, choices []choice, get func(lightSettings) int,
		put func(*lightSettings, int)) btItem {
		return btItem{Kind: "row", Icon: icon, Label: label, Value: value,
			turn: func(delta int) {
				s := backlight.settings()
				put(&s, stepChoice(choices, get(s), delta))
				backlight.change(s)
			}}
	}
	dir := ""
	if h := hereNow(); h != nil {
		dir = h.dir
	}
	m := meterSetting(dir)
	meterRow := func(label, value, which string) btItem {
		return btItem{Kind: "row", Icon: "speaker", Label: label, Value: value,
			turn: func(delta int) {
				if err := stepMeter(dir, which, delta); err != nil {
					b.message("Couldn't save that", err.Error())
				}
			}}
	}
	dimRow := func(it btItem) btItem { it.Off = s.NoDim; return it }
	items := []btItem{
		setting("sun", "Brightness", bright, brightnessChoices,
			func(s lightSettings) int { return s.Brightness }, func(s *lightSettings, v int) { s.Brightness = v }),
		{Kind: "row", Icon: "moon", Label: "Dim screen", Check: checked(!s.NoDim), do: func() {
			s := backlight.settings()
			s.NoDim = !s.NoDim
			backlight.change(s)
			b.screenViewAt(1, false)
		}},
		dimRow(setting("moon", "Dim after", labelFor(dimAfterChoices, s.DimAfter), dimAfterChoices,
			func(s lightSettings) int { return s.DimAfter }, func(s *lightSettings, v int) { s.DimAfter = v })),
		dimRow(setting("moon", "Dimmed brightness", labelFor(dimToChoices, s.DimTo), dimToChoices,
			func(s lightSettings) int { return s.DimTo }, func(s *lightSettings, v int) { s.DimTo = v; backlight.showDim(v) })),
		setting("power", "Turn off after", labelFor(offAfterChoices, s.OffAfter), offAfterChoices,
			func(s lightSettings) int { return s.OffAfter }, func(s *lightSettings, v int) { s.OffAfter = v }),
		{Kind: "header", Label: "Audio visualization"},
		meterRow("Bands", fmt.Sprint(m.Bands), "bands"),
		meterRow("Response", speedLabel(m.Rise), "rise"),
		meterRow("Decay", speedLabel(m.Fall), "fall"),
		{Kind: "row", Icon: "speaker", Label: "Peaks", Check: checked(m.Peaks), do: func() {
			if err := stepMeter(dir, "peaks", 0); err != nil {
				b.message("Couldn't save that", err.Error())
				return
			}
			b.screenViewAt(9, false)
		}},
	}
	for i := range items {
		if items[i].turn != nil {
			i := i
			items[i].do = func() {
				// The front button puts the settings back as they were before the change.
				light, meter := backlight.settings(), meterSetting(dir)
				b.mu.Lock()
				b.undo = func() {
					backlight.change(light)
					if dir != "" {
						_ = setMeterTo(dir, meter)
					}
				}
				b.mu.Unlock()
				b.screenViewAt(i, true)
			}
		}
	}
	sel = firstRow(items, sel)
	note := "Any button wakes a dark screen. A preset pressed while it is dark only wakes it."
	if items[sel].Icon == "speaker" {
		note = "How the band at the top shows the sound as it plays."
	}
	// The band at the top shows the level of the Brightness or Dimmed brightness row while one
	// of them is highlighted; the other rows are words only.
	meter := 0
	switch items[sel].Label {
	case "Brightness":
		meter = s.Brightness
		if meter == 0 {
			meter = backlight.percent()
		}
	case "Dimmed brightness":
		meter = s.DimTo
	}
	b.show(&btScreen{View: "screen", Note: note, Items: items, Sel: sel,
		Editing: editing && items[sel].turn != nil, Meter: meter})
	if editing && items[sel].Label == "Dimmed brightness" {
		backlight.showDim(s.DimTo)
	} else {
		backlight.showDim(0)
	}
}

// adjust turns the value of the row being edited.
func (b *bluetooth) adjust(delta int) {
	b.mu.Lock()
	v := b.view
	b.mu.Unlock()
	if v == nil || !v.Editing || v.Sel < 0 || v.Sel >= len(v.Items) || v.Items[v.Sel].turn == nil || delta == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	v.Items[v.Sel].turn(step)
	switch v.View {
	case "softkey":
		b.softKeyViewAt(v.Sel, true, "")
	case "device", "peek":
		b.deviceViewEdit(v.Sel, true)
	default:
		b.screenViewAt(v.Sel, true)
	}
}

// cancelEditing leaves the row being changed over the home screen, putting its value back.
func (b *bluetooth) cancelEditing() {
	b.mu.Lock()
	v, undo := b.view, b.undo
	b.undo = nil
	b.mu.Unlock()
	stopBeeps()
	if undo != nil {
		undo()
	}
	switch {
	case v == nil:
	case v.View == "screen":
		b.screenViewAt(v.Sel, false)
	default:
		b.deviceViewEdit(v.Sel, false)
	}
}

// doneEditing leaves the row being edited, keeping its value.
func (b *bluetooth) doneEditing() {
	b.mu.Lock()
	v := b.view
	b.mu.Unlock()
	switch {
	case v == nil:
	case v.View == "softkey":
		go b.saveSoftKey(v.Sel)
	case v.View == "device" || v.View == "peek":
		b.mu.Lock()
		b.undo = nil
		b.mu.Unlock()
		stopBeeps()
		b.deviceViewEdit(v.Sel, false)
	default:
		b.mu.Lock()
		b.undo = nil
		b.mu.Unlock()
		b.screenViewAt(v.Sel, false)
	}
}
