package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/dhowden/tag"
)

// The iPod: an iPod in disk mode (or a USB drive with music on it) plugged into the Car Thing's
// port, with the port on the phone's side, through a powered hub when the phone is plugged in
// too. Its music plays here, through the library player, with no internet: ffmpeg decodes it
// (AAC as well as MP3), straight from the iPod, which is only ever read (ipod_linux.go mounts
// it read-only).
//
// The songs, albums and playlists come from the iPod's own database, iTunesDB (compressed, as
// iTunesCDB, on newer nanos); a drive without one has its files' tags read instead.

const ipodSource = "ipod"

type ipodTrack struct {
	Path    string // under the mount: iPod_Control/Music/F12/ABCD.m4a
	Title   string
	Artist  string
	Album   string
	Number  int // on its album
	Disc    int
	Seconds int
	id      uint32 // the database's, for its playlists
}

type ipodPlaylist struct {
	Name   string
	Tracks []int // into the library's tracks
}

type ipodLibrary struct {
	Name      string // the iPod's: "Kayla's iPod"
	Tracks    []ipodTrack
	Playlists []ipodPlaylist
	Protected int    // iTunes Store songs from before 2009, which can't be played here
	From      string // where it was read from: "iTunesCDB", "iTunesDB" or "tags"
}

// ipodState is what the screen shows: the library once read, or why there's none.
var ipodState struct {
	mu      sync.Mutex
	root    string // where the iPod is mounted, or ""
	lib     *ipodLibrary
	reading bool
	problem string // why it couldn't be read
}

func currentIPod() (string, *ipodLibrary, bool, string) {
	ipodState.mu.Lock()
	defer ipodState.mu.Unlock()
	return ipodState.root, ipodState.lib, ipodState.reading, ipodState.problem
}

// playable is whether a file is music ffmpeg can play here: not a video, and not an iTunes
// Store song protected against copying (.m4p).
func playable(name string) (ok, protected bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp3", ".m4a", ".m4b", ".aac", ".wav", ".aif", ".aiff", ".flac", ".ogg", ".opus":
		return true, false
	case ".m4p":
		return false, true
	}
	return false, false
}

// readIPod reads the music on a drive mounted at root.
func readIPod(root string) (*ipodLibrary, error) {
	var best *ipodLibrary
	for _, name := range []string{"iTunesCDB", "iTunesDB"} {
		data, err := os.ReadFile(filepath.Join(root, "iPod_Control", "iTunes", name))
		if err != nil {
			continue
		}
		lib, err := parseITunesDB(data)
		if err != nil {
			log.Printf("ipod: reading its %s: %v", name, err)
			continue
		}
		lib.From = name
		if best == nil || len(lib.Tracks) > len(best.Tracks) {
			best = lib
		}
	}
	if best != nil && len(best.Tracks) > 0 {
		return best, nil
	}
	lib, err := scanTags(root)
	if err != nil {
		return nil, err
	}
	if best != nil && lib.Name == "" {
		lib.Name = best.Name
	}
	return lib, nil
}

// --- iTunesDB ------------------------------------------------------------------------------------
//
// A tree of chunks, each a four-letter name, the header's length and (but for the lists) the
// chunk's length with what it holds, little-endian: mhbd, the database, holds mhsd sections; the
// tracks' (type 1) an mhlt of mhit tracks, the playlists' (type 2) an mhlp of mhyp playlists of
// mhip items. Tracks and playlists carry mhod strings: a title, the file's location, the album.
// On newer nanos it is iTunesCDB: what follows mhbd's header is zlib-compressed.

var errNotITunesDB = errors.New("not an iTunes database")

func parseITunesDB(data []byte) (*ipodLibrary, error) {
	if len(data) < 24 || string(data[:4]) != "mhbd" {
		return nil, errNotITunesDB
	}
	hl := int(le32(data, 4))
	if hl < 24 || hl > len(data) {
		return nil, errNotITunesDB
	}
	if len(data) > hl+2 && data[hl] == 0x78 { // zlib: iTunesCDB
		z, err := zlib.NewReader(bytes.NewReader(data[hl:]))
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(z, 256<<20))
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
		data = append(append([]byte(nil), data[:hl]...), body...)
	}
	lib := &ipodLibrary{}
	byID := map[uint32]int{}
	var lists, podcastLists []ipodPlaylist
	var name, podcastName string
	for off := hl; off+16 <= len(data); {
		if string(data[off:off+4]) != "mhsd" {
			break
		}
		shl, stl := int(le32(data, off+4)), int(le32(data, off+8))
		if stl < shl || shl < 16 || off+stl > len(data) {
			break
		}
		section := data[off : off+stl]
		switch le32(section, 12) {
		case 1:
			readTracks(section[shl:], lib, byID)
		case 2:
			lists, name = readPlaylists(section[shl:], byID)
		case 3:
			podcastLists, podcastName = readPlaylists(section[shl:], byID)
		}
		off += stl
	}
	if lists == nil {
		lists, name = podcastLists, podcastName
	}
	lib.Playlists, lib.Name = lists, name
	return lib, nil
}

func le32(b []byte, at int) uint32 {
	if at+4 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint32(b[at:])
}

// readTracks reads an mhlt.
func readTracks(b []byte, lib *ipodLibrary, byID map[uint32]int) {
	if len(b) < 12 || string(b[:4]) != "mhlt" {
		return
	}
	n, off := int(le32(b, 8)), int(le32(b, 4))
	for i := 0; i < n && off+16 <= len(b) && string(b[off:off+4]) == "mhit"; i++ {
		hl, tl := int(le32(b, off+4)), int(le32(b, off+8))
		if tl < hl || off+tl > len(b) {
			return
		}
		it := b[off : off+tl]
		t := ipodTrack{id: le32(it, 16), Seconds: int(le32(it, 40)) / 1000, Number: int(le32(it, 44))}
		if hl >= 96 {
			t.Disc = int(le32(it, 92))
		}
		eachMhod(it[hl:], func(kind uint32, s string) {
			switch kind {
			case 1:
				t.Title = s
			case 2:
				t.Path = strings.TrimLeft(strings.ReplaceAll(s, ":", "/"), "/")
			case 3:
				t.Album = s
			case 4:
				t.Artist = s
			case 22:
				if t.Artist == "" {
					t.Artist = s
				}
			}
		})
		off += tl
		ok, protected := playable(t.Path)
		if protected {
			lib.Protected++
		}
		if !ok {
			continue
		}
		if t.Title == "" {
			t.Title = strings.TrimSuffix(path.Base(t.Path), path.Ext(t.Path))
		}
		byID[t.id] = len(lib.Tracks)
		lib.Tracks = append(lib.Tracks, t)
	}
}

// readPlaylists reads an mhlp: the playlists, and the master playlist's name, the iPod's own.
func readPlaylists(b []byte, byID map[uint32]int) ([]ipodPlaylist, string) {
	if len(b) < 12 || string(b[:4]) != "mhlp" {
		return nil, ""
	}
	var lists []ipodPlaylist
	name := ""
	n, off := int(le32(b, 8)), int(le32(b, 4))
	for i := 0; i < n && off+24 <= len(b) && string(b[off:off+4]) == "mhyp"; i++ {
		hl, tl := int(le32(b, off+4)), int(le32(b, off+8))
		if tl < hl || tl < 24 || off+tl > len(b) {
			break
		}
		yp := b[off : off+tl]
		master := yp[20] != 0
		var p ipodPlaylist
		for c := hl; c+12 <= len(yp); {
			chl, ctl := int(le32(yp, c+4)), int(le32(yp, c+8))
			if ctl < chl || ctl < 12 || c+ctl > len(yp) {
				break
			}
			switch string(yp[c : c+4]) {
			case "mhod":
				eachMhod(yp[c:c+ctl], func(kind uint32, s string) {
					if kind == 1 && p.Name == "" {
						p.Name = s
					}
				})
			case "mhip":
				if i, ok := byID[le32(yp, c+24)]; ok {
					p.Tracks = append(p.Tracks, i)
				}
			}
			c += ctl
		}
		off += tl
		if master {
			name = p.Name
		} else if len(p.Tracks) > 0 && p.Name != "" {
			lists = append(lists, p)
		}
	}
	return lists, name
}

// eachMhod calls f with each string mhod in b: its type and text.
func eachMhod(b []byte, f func(kind uint32, s string)) {
	for off := 0; off+24 <= len(b) && string(b[off:off+4]) == "mhod"; {
		tl := int(le32(b, off+8))
		if tl < 24 || off+tl > len(b) {
			return
		}
		m := b[off : off+tl]
		if kind := le32(m, 12); kind < 50 && len(m) >= 40 { // 50 and up aren't strings
			n := int(le32(m, 28))
			if 40+n <= len(m) {
				raw := m[40 : 40+n]
				if le32(m, 24) == 2 {
					f(kind, string(raw))
				} else {
					u := make([]uint16, len(raw)/2)
					for i := range u {
						u[i] = binary.LittleEndian.Uint16(raw[2*i:])
					}
					f(kind, string(utf16.Decode(u)))
				}
			}
		}
		off += tl
	}
}

// --- a drive's tags ------------------------------------------------------------------------------

const ipodScanMax = 20000 // files looked at, so a huge drive doesn't take all day

func scanTags(root string) (*ipodLibrary, error) {
	lib := &ipodLibrary{From: "tags"}
	seen := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > ipodScanMax {
			return filepath.SkipAll
		}
		ok, protected := playable(d.Name())
		if protected {
			lib.Protected++
		}
		if !ok || strings.HasPrefix(d.Name(), "._") { // macOS's resource forks
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		t := ipodTrack{Path: filepath.ToSlash(rel), Title: strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))}
		if f, err := os.Open(p); err == nil {
			if m, err := tag.ReadFrom(f); err == nil {
				t.Title = firstNonEmpty(m.Title(), t.Title)
				t.Artist = firstNonEmpty(m.Artist(), m.AlbumArtist())
				t.Album = m.Album()
				t.Number, _ = m.Track()
				t.Disc, _ = m.Disc()
			}
			f.Close()
		}
		lib.Tracks = append(lib.Tracks, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return lib, nil
}

// --- the iPod's page -----------------------------------------------------------------------------

const ipodListMax = 2000 // rows in a list, so the screen keeps up

func (t ipodTrack) lib() libTrack {
	return libTrack{Source: ipodSource, ID: t.Path, Title: t.Title, Artist: t.Artist, Album: t.Album, Seconds: t.Seconds}
}

// inOrder is the tracks by artist, album and their order on it.
func inOrder(tracks []ipodTrack, idx []int) []int {
	key := func(t ipodTrack) string { return strings.ToLower(t.Artist + "\x00" + t.Album) }
	sort.SliceStable(idx, func(a, b int) bool {
		ta, tb := tracks[idx[a]], tracks[idx[b]]
		if ka, kb := key(ta), key(tb); ka != kb {
			return ka < kb
		}
		if ta.Disc != tb.Disc {
			return ta.Disc < tb.Disc
		}
		if ta.Number != tb.Number {
			return ta.Number < tb.Number
		}
		return strings.ToLower(ta.Title) < strings.ToLower(tb.Title)
	})
	return idx
}

func trackList(lib *ipodLibrary, idx []int) []libTrack {
	out := make([]libTrack, 0, min(len(idx), ipodListMax))
	for _, i := range idx {
		if len(out) == ipodListMax {
			break
		}
		out = append(out, lib.Tracks[i].lib())
	}
	return out
}

// ipodAlbums are the albums, by name and artist, each with its tracks in order.
func ipodAlbums(lib *ipodLibrary) []ipodPlaylist {
	byKey := map[string]int{}
	var albums []ipodPlaylist
	var artists []string
	all := make([]int, len(lib.Tracks))
	for i := range all {
		all[i] = i
	}
	for _, i := range inOrder(lib.Tracks, all) {
		t := lib.Tracks[i]
		name := firstNonEmpty(t.Album, "Unknown album")
		key := strings.ToLower(t.Artist + "\x00" + name)
		a, ok := byKey[key]
		if !ok {
			a = len(albums)
			byKey[key] = a
			albums = append(albums, ipodPlaylist{Name: name})
			artists = append(artists, t.Artist)
		}
		albums[a].Tracks = append(albums[a].Tracks, i)
	}
	for i := range albums {
		if artists[i] != "" {
			albums[i].Name += "\x00" + artists[i] // split again for the page
		}
	}
	return albums
}

// ipodBrowse answers the iPod page's lists, as the plugin's /music/browse does the others':
// what=albums or playlists (each a list to open), playlist (with id), or search (with q).
func ipodBrowse(what, id, q string) map[string]any {
	root, lib, reading, problem := currentIPod()
	empty := func(note string) map[string]any {
		return map[string]any{"tracks": []any{}, "playlists": []any{}, "note": note}
	}
	switch {
	case reading:
		return empty("Reading the iPod…")
	case lib == nil && problem != "":
		return empty(problem)
	case lib == nil || root == "":
		return empty(ipodPlugInNote())
	case len(lib.Tracks) == 0:
		note := "There's no music on the iPod."
		if lib.Protected > 0 {
			note = fmt.Sprintf("Its %d songs are copy-protected iTunes Store purchases, which only Apple's players can play.", lib.Protected)
		}
		return empty(note)
	}
	folder := func(id, title, artist string, n int) map[string]any {
		return map[string]any{"source": ipodSource, "id": id, "title": title, "artist": artist, "count": n}
	}
	switch what {
	case "albums":
		var out []map[string]any
		for i, a := range ipodAlbums(lib) {
			title, artist, _ := strings.Cut(a.Name, "\x00")
			out = append(out, folder(fmt.Sprintf("album:%d", i), title, artist, len(a.Tracks)))
		}
		return map[string]any{"playlists": out}
	case "playlists":
		out := []map[string]any{folder("all", "All songs", "", len(lib.Tracks))}
		for i, p := range lib.Playlists {
			out = append(out, folder(fmt.Sprintf("list:%d", i), p.Name, "", len(p.Tracks)))
		}
		return map[string]any{"playlists": out}
	case "playlist":
		var idx []int
		switch {
		case id == "all":
			for i := range lib.Tracks {
				idx = append(idx, i)
			}
			idx = inOrder(lib.Tracks, idx)
		case strings.HasPrefix(id, "album:"):
			albums := ipodAlbums(lib)
			if n, err := strconv.Atoi(strings.TrimPrefix(id, "album:")); err == nil && n >= 0 && n < len(albums) {
				idx = albums[n].Tracks
			}
		case strings.HasPrefix(id, "list:"):
			if n, err := strconv.Atoi(strings.TrimPrefix(id, "list:")); err == nil && n >= 0 && n < len(lib.Playlists) {
				idx = lib.Playlists[n].Tracks
			}
		}
		return map[string]any{"tracks": trackList(lib, idx)}
	case "search":
		words := strings.Fields(strings.ToLower(q))
		var idx []int
		for i, t := range lib.Tracks {
			hay := strings.ToLower(t.Title + " " + t.Artist + " " + t.Album)
			all := len(words) > 0
			for _, w := range words {
				all = all && strings.Contains(hay, w)
			}
			if all {
				idx = append(idx, i)
			}
		}
		got := map[string]any{"tracks": trackList(lib, inOrder(lib.Tracks, idx))}
		if len(idx) == 0 {
			got["note"] = "Nothing on the iPod matches."
		}
		return got
	}
	return empty("")
}

// ipodPlugInNote says what the page needs to find an iPod.
func ipodPlugInNote() string {
	if virtual {
		return "Plug your iPod into the Car Thing to play its music."
	}
	if usbRole() == "device" {
		return "Set the USB port to Phone on the Network tab, then plug in your iPod."
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "The iPod needs Petasos system image 0.4.0 or later on the Car Thing."
	}
	return "Plug in your iPod. With your phone plugged in too, use a powered hub."
}

// ipodFile is where a track's file is, on the iPod plugged in.
func ipodFile(id string) (string, error) {
	root, _, _, _ := currentIPod()
	if root == "" {
		return "", fmt.Errorf("the iPod isn't plugged in")
	}
	clean := path.Clean("/" + id)
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}

// ipodPCM decodes a track from the iPod with ffmpeg, from a frame on, as the library player
// plays it: 16-bit stereo at libraryRate. stop ends ffmpeg.
func ipodPCM(t libTrack, from int) (io.Reader, func(), error) {
	file, err := ipodFile(t.ID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(file); err != nil {
		return nil, nil, fmt.Errorf("it isn't on the iPod: %v", err)
	}
	args := []string{"-nostdin", "-hide_banner", "-v", "error"}
	if from > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", float64(from)/libraryRate))
	}
	args = append(args, "-i", file, "-vn", "-f", "s16le", "-acodec", "pcm_s16le", "-ac", "2",
		"-ar", fmt.Sprint(libraryRate), "-")
	cmd := exec.Command("ffmpeg", args...)
	errs := &capped{max: 4 << 10}
	cmd.Stderr = errs
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("can't decode it: %v", err)
	}
	var once sync.Once
	wait := func() error {
		var err error
		once.Do(func() { err = cmd.Wait() })
		return err
	}
	stop := func() {
		_ = cmd.Process.Kill()
		_ = wait()
	}
	return &decoded{r: out, wait: wait, errs: errs}, stop, nil
}

// decoded is ffmpeg's output, ending in its error if it failed.
type decoded struct {
	r    io.Reader
	wait func() error
	errs *capped
}

func (d *decoded) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if err == io.EOF {
		if werr := d.wait(); werr != nil {
			return n, fmt.Errorf("ffmpeg: %v: %s", werr, strings.TrimSpace(d.errs.String()))
		}
	}
	return n, err
}

// capped keeps the first max bytes written to it.
type capped struct {
	bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
