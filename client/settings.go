package main

// The settings on the Car Thing itself. Holding the mic button opens them. The preset buttons
// become tabs while they are open: the first for Bluetooth (the screens in bluetooth.go), the
// second for Screen, the light's brightness, dimming and time-out; the third for this Car Thing
// itself, its name, microphone and soft keys (device.go); the fourth is spare.
// A Screen row is changed by pressing the knob, turning it, and pressing again.

type choice struct {
	value int
	label string
}

var (
	brightnessChoices = []choice{{0, "Auto"}, {10, "10%"}, {20, "20%"}, {30, "30%"}, {40, "40%"},
		{50, "50%"}, {60, "60%"}, {70, "70%"}, {80, "80%"}, {90, "90%"}, {100, "100%"}}
	waitChoices     = []choice{{0, "Off"}, {15, "15 seconds"}, {30, "30 seconds"}, {45, "45 seconds"}, {60, "60 seconds"}}
	dimAfterChoices = waitChoices
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

// screenView lists the light's settings; editing is the row being changed, or -1.
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
	items := []btItem{
		setting("sun", "Brightness", bright, brightnessChoices,
			func(s lightSettings) int { return s.Brightness }, func(s *lightSettings, v int) { s.Brightness = v }),
		setting("moon", "Dim after", labelFor(dimAfterChoices, s.DimAfter), dimAfterChoices,
			func(s lightSettings) int { return s.DimAfter }, func(s *lightSettings, v int) { s.DimAfter = v }),
		setting("moon", "Dim to", labelFor(dimToChoices, s.DimTo), dimToChoices,
			func(s lightSettings) int { return s.DimTo }, func(s *lightSettings, v int) { s.DimTo = v; backlight.showDim(v) }),
		setting("power", "Turn off after", labelFor(offAfterChoices, s.OffAfter), offAfterChoices,
			func(s lightSettings) int { return s.OffAfter }, func(s *lightSettings, v int) { s.OffAfter = v }),
	}
	for i := range items {
		if items[i].turn != nil {
			i := i
			items[i].do = func() { b.screenViewAt(i, true) }
		}
	}
	sel = firstRow(items, sel)
	note := "Any button wakes a dark screen. A preset pressed while it is dark only wakes it."
	// The band at the bottom shows the level of the Brightness or Dim to row while one of them
	// is highlighted; the other rows are times, shown as words only.
	meter := 0
	switch items[sel].Label {
	case "Brightness":
		meter = s.Brightness
		if meter == 0 {
			meter = backlight.percent()
		}
	case "Dim to":
		meter = s.DimTo
	}
	b.show(&btScreen{View: "screen", Note: note, Items: items, Sel: sel,
		Editing: editing && items[sel].turn != nil, Meter: meter})
	if editing && items[sel].Label == "Dim to" {
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
	case "device":
		b.deviceViewEdit(v.Sel, true)
	default:
		b.screenViewAt(v.Sel, true)
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
	case v.View == "device":
		b.deviceViewEdit(v.Sel, false)
	default:
		b.screenViewAt(v.Sel, false)
	}
}
