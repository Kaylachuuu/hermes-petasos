package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The Music tab of the Car Thing's settings (the fourth preset): the music sources on Hermes,
// the same ones the apps' Settings and Hermes Desktop set (the plugin's music.json). Each value
// is typed on the on-screen keyboard into a draft, which "Save and check" sends to Hermes. The
// SoundCloud and Spotify account sign-ins need a browser, so they are only shown here.

type musicSettings struct {
	Navidrome struct {
		URL         string `json:"url"`
		Username    string `json:"username"`
		HasPassword bool   `json:"has_password"`
	} `json:"navidrome"`
	Folders    []string `json:"folders"`
	SoundCloud struct {
		ClientID  string `json:"client_id"`
		HasSecret bool   `json:"has_secret"`
		Account   string `json:"account"`
	} `json:"soundcloud"`
	Spotify struct {
		ClientID string `json:"client_id"`
		Account  string `json:"account"`
	} `json:"spotify"`
	Quality string `json:"quality"` // high, normal, low or lowest; empty from an older plugin
	FFmpeg  bool   `json:"ffmpeg"`
}

// musicDraft is the tab's unsaved edits on top of what Hermes has.
type musicDraft struct {
	saved    musicSettings
	url      string
	username string
	password string // "" keeps the saved one
	folders  []string
	scID     string
	scSecret string // "" keeps the saved one
	spID     string
	quality  string
	changed  bool
}

// qualities are the streaming qualities, in the order a press steps through them, with what the
// row shows for each.
var qualities = []struct{ name, shown string }{
	{"high", "High, 192 kbps"}, {"normal", "Normal, 128 kbps"}, {"low", "Low, 96 kbps"}, {"lowest", "Lowest, 64 kbps"}}

func (h *hermes) musicSettings() (musicSettings, error) {
	var s musicSettings
	err := h.call("GET", "/api/plugins/petasos/music/settings", nil, &s)
	return s, err
}

// musicView loads the settings from Hermes afresh and shows them.
func (b *bluetooth) musicView() {
	h := hereNow()
	if h == nil {
		b.show(&btScreen{View: "message", Tab: "music", Title: "Not ready", Note: "Petasos is still starting."})
		return
	}
	s, err := h.musicSettings()
	if err != nil {
		b.show(&btScreen{View: "music", Tab: "music", Note: "Can't reach " + display.agent() + " for the music settings (its Petasos plugin may need updating)."})
		return
	}
	d := &musicDraft{saved: s, url: s.Navidrome.URL, username: s.Navidrome.Username,
		folders: append([]string(nil), s.Folders...), scID: s.SoundCloud.ClientID,
		spID: s.Spotify.ClientID, quality: s.Quality}
	b.mu.Lock()
	b.music = d
	b.mu.Unlock()
	b.musicDraftView(-1)
}

func (b *bluetooth) musicEdits() *musicDraft {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.music
}

func orNone(s, none string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return s
}

// musicDraftView shows the draft without asking Hermes again.
func (b *bluetooth) musicDraftView(sel int) {
	d := b.musicEdits()
	if d == nil {
		b.musicView()
		return
	}
	edit := func(title, note string, value *string, max int, row int) func() {
		return func() {
			b.keyboardFor(title, note, *value, max, true, func(text string) {
				if text != *value {
					*value, d.changed = strings.TrimSpace(text), true
				}
				b.musicDraftView(row)
			}, func() { b.musicDraftView(row) })
		}
	}
	secret := func(typed string, saved bool) string {
		switch {
		case typed != "":
			return "New, not saved yet"
		case saved:
			return "Saved"
		}
		return "None"
	}
	var items []btItem
	if d.quality != "" { // an older plugin has no qualities
		shown, next := d.quality, qualities[0].name
		for i, q := range qualities {
			if q.name == d.quality {
				shown, next = q.shown, qualities[(i+1)%len(qualities)].name
			}
		}
		items = append(items, btItem{Kind: "header", Label: "Streaming"},
			btItem{Kind: "row", Icon: "music", Label: "Quality", Value: shown, do: func() {
				d.quality, d.changed = next, true
				b.musicDraftView(1)
			}},
			btItem{Kind: "note", Label: "Lower stutters less on a slow connection"})
	}
	items = append(items, btItem{Kind: "header", Label: "Navidrome"})
	items = append(items,
		btItem{Kind: "row", Icon: "device", Label: "Address", Value: truncate(orNone(d.url, "Not used"), 28),
			do: edit("Navidrome address", "As Hermes reaches it, like http://localhost:4533. Empty to not use Navidrome.", &d.url, 200, len(items))},
		btItem{Kind: "row", Icon: "device", Label: "Username", Value: truncate(orNone(d.username, "—"), 24),
			do: edit("Navidrome username", "Your Navidrome account, not your Hermes sign-in.", &d.username, 100, len(items)+1)},
		btItem{Kind: "row", Icon: "key", Label: "Password", Value: secret(d.password, d.saved.Navidrome.HasPassword),
			do: edit("Navidrome password", "Shown as you type it.", &d.password, 200, len(items)+2)},
		btItem{Kind: "header", Label: "Folders"})
	for i := range d.folders {
		i, row := i, len(items)
		items = append(items, btItem{Kind: "row", Icon: "device", Label: truncate(d.folders[i], 32), do: func() {
			b.keyboardFor("Music folder", "A full path on Hermes. Empty it to remove the folder.", d.folders[i], 300, true, func(text string) {
				text = strings.TrimSpace(text)
				if text == "" {
					d.folders = append(d.folders[:i], d.folders[i+1:]...)
				} else {
					d.folders[i] = text
				}
				d.changed = true
				b.musicDraftView(row)
			}, func() { b.musicDraftView(row) })
		}})
	}
	addRow := len(items)
	items = append(items, btItem{Kind: "row", Icon: "plus", Label: "Add a folder", do: func() {
		b.keyboardFor("Music folder", "A full path on Hermes, like /srv/music. A network share works once it's mounted there.", "/", 300, true, func(text string) {
			if text = strings.TrimSpace(text); text != "" && text != "/" {
				d.folders, d.changed = append(d.folders, text), true
			}
			b.musicDraftView(addRow)
		}, func() { b.musicDraftView(addRow) })
	}})
	items = append(items, btItem{Kind: "header", Label: "SoundCloud"})
	items = append(items,
		btItem{Kind: "row", Icon: "device", Label: "Client ID", Value: truncate(orNone(d.scID, "Not used"), 24),
			do: edit("SoundCloud client ID", "From the app you registered at soundcloud.com/you/apps. Empty to not use SoundCloud.", &d.scID, 200, len(items))},
		btItem{Kind: "row", Icon: "key", Label: "Client secret", Value: secret(d.scSecret, d.saved.SoundCloud.HasSecret),
			do: edit("SoundCloud client secret", "Shown as you type it.", &d.scSecret, 200, len(items)+1)})
	account := "Sign in from the Petasos app on a computer"
	if d.saved.SoundCloud.Account != "" {
		account = "Signed in as " + d.saved.SoundCloud.Account
	}
	items = append(items, btItem{Kind: "note", Label: account})
	items = append(items, btItem{Kind: "header", Label: "Spotify"},
		btItem{Kind: "row", Icon: "device", Label: "Client ID", Value: truncate(orNone(d.spID, "Not used"), 24),
			do: edit("Spotify client ID", "From the app you created at developer.spotify.com/dashboard. Empty to not use Spotify.", &d.spID, 200, len(items)+1)})
	account = "Sign in from the Petasos app on a computer"
	if d.saved.Spotify.Account != "" {
		account = "Signed in as " + d.saved.Spotify.Account + ". Plays on your phone; needs Premium."
	}
	items = append(items, btItem{Kind: "note", Label: account})
	save := "Check the music"
	if d.changed {
		save = "Save and check"
	}
	items = append(items,
		btItem{Kind: "row", Icon: "music", Label: save, do: b.saveMusic})
	note := "What " + display.agent() + " can play when you ask. Kept on Hermes, shared with your other devices."
	if d.changed {
		note = "Not saved yet: choose Save and check."
	}
	b.show(&btScreen{View: "music", Tab: "music", Note: note, Items: items, Sel: firstRow(items, sel)})
}

// saveMusic sends the draft to Hermes, then shows what the check found.
func (b *bluetooth) saveMusic() {
	d, h := b.musicEdits(), hereNow()
	if d == nil || h == nil {
		return
	}
	b.show(&btScreen{View: "message", Tab: "music", Title: "Checking the music", Note: "This can take a while the first time a big folder is read."})
	slow := &http.Client{Transport: h.http.Transport, Timeout: 10 * time.Minute}
	if d.changed {
		settings := map[string]any{
			"navidrome":  map[string]string{"url": d.url, "username": d.username, "password": d.password},
			"folders":    d.folders,
			"soundcloud": map[string]string{"client_id": d.scID, "client_secret": d.scSecret},
			"spotify":    map[string]string{"client_id": d.spID},
		}
		if d.quality != "" {
			settings["quality"] = d.quality
		}
		body, _ := json.Marshal(settings)
		if _, err := h.exchange("PUT", "/api/plugins/petasos/music/settings", "application/json", body, slow, 1<<20); err != nil {
			b.musicMessage("Couldn't save that", friendlyHermes(err))
			return
		}
	}
	data, err := h.exchange("POST", "/api/plugins/petasos/music/check", "application/json", []byte("{}"), slow, 1<<20)
	var c struct {
		Navidrome *struct {
			OK      bool   `json:"ok"`
			Problem string `json:"problem"`
		} `json:"navidrome"`
		SoundCloud *struct {
			OK      bool   `json:"ok"`
			FFmpeg  bool   `json:"ffmpeg"`
			Problem string `json:"problem"`
		} `json:"soundcloud"`
		Spotify *struct {
			OK      bool   `json:"ok"`
			Account string `json:"account"`
			Problem string `json:"problem"`
		} `json:"spotify"`
		Folders []struct {
			Path    string `json:"path"`
			OK      bool   `json:"ok"`
			Songs   int    `json:"songs"`
			Problem string `json:"problem"`
		} `json:"folders"`
	}
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err != nil {
		b.musicMessage("Saved, but the check failed", friendlyHermes(err))
		return
	}
	var lines []string
	if c.Navidrome != nil {
		lines = append(lines, map[bool]string{true: "Navidrome answers.", false: c.Navidrome.Problem}[c.Navidrome.OK])
	}
	for _, f := range c.Folders {
		if f.OK {
			lines = append(lines, fmt.Sprintf("%s: %d songs.", f.Path, f.Songs))
		} else {
			lines = append(lines, f.Path+": "+f.Problem)
		}
	}
	if c.SoundCloud != nil {
		switch {
		case !c.SoundCloud.OK:
			lines = append(lines, c.SoundCloud.Problem)
		case !c.SoundCloud.FFmpeg:
			lines = append(lines, "SoundCloud works, but its songs need ffmpeg installed on Hermes.")
		default:
			lines = append(lines, "SoundCloud works.")
		}
	}
	if c.Spotify != nil {
		if c.Spotify.OK {
			lines = append(lines, "Spotify works, signed in as "+c.Spotify.Account+".")
		} else {
			lines = append(lines, c.Spotify.Problem)
		}
	}
	if len(lines) == 0 {
		lines = []string{"No music sources yet."}
	}
	b.musicMessage("Music", strings.Join(lines, " "))
}

// musicMessage shows a result; going back returns to the tab, reloaded from Hermes.
func (b *bluetooth) musicMessage(title, note string) {
	b.show(&btScreen{View: "message", Tab: "music", Title: title, Note: note})
}
