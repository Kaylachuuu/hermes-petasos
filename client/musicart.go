package main

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// The music page's pictures come through Hermes, which keeps them (the plugin's /music/art), and
// the latest are kept in memory here. Playlists' pictures (?keep=1) are kept on disk too, in
// art/ beside config.json: there are few, they seldom change, and each is written once, so the
// flash hardly notices. A kept one is never rewritten; the oldest go once there are too many.

const (
	artMemoryMax = 12 << 20
	artKeptMax   = 300
	artBiggest   = 4 << 20
)

var artMemory = struct {
	sync.Mutex
	pics  map[string][]byte
	order []string
	size  int
}{pics: map[string][]byte{}}

func rememberArt(ref string, data []byte) {
	artMemory.Lock()
	defer artMemory.Unlock()
	if _, ok := artMemory.pics[ref]; ok {
		return
	}
	artMemory.pics[ref] = data
	artMemory.order = append(artMemory.order, ref)
	artMemory.size += len(data)
	for artMemory.size > artMemoryMax && len(artMemory.order) > 1 {
		old := artMemory.order[0]
		artMemory.order = artMemory.order[1:]
		artMemory.size -= len(artMemory.pics[old])
		delete(artMemory.pics, old)
	}
}

func recalledArt(ref string) []byte {
	artMemory.Lock()
	defer artMemory.Unlock()
	return artMemory.pics[ref]
}

func artFile(dir, ref string) string {
	sum := sha1.Sum([]byte(ref))
	return filepath.Join(dir, "art", hex.EncodeToString(sum[:12]))
}

// keepArt writes a playlist's picture to disk, once.
func keepArt(dir, ref string, data []byte) {
	path := artFile(dir, ref)
	if _, err := os.Stat(path); err == nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil || os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) <= artKeptMax {
		return
	}
	type kept struct {
		path string
		when int64
	}
	all := make([]kept, 0, len(entries))
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			all = append(all, kept{filepath.Join(filepath.Dir(path), e.Name()), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].when < all[j].when })
	for _, k := range all[:len(all)-artKeptMax] {
		os.Remove(k.path)
	}
}

// musicArt serves ?ref= (a track's or playlist's "art"), keeping it on disk with &keep=1.
func musicArt(w http.ResponseWriter, r *http.Request) {
	ref, keep := r.URL.Query().Get("ref"), r.URL.Query().Get("keep") != ""
	h := hereNow()
	if ref == "" || h == nil {
		http.NotFound(w, r)
		return
	}
	data := recalledArt(ref)
	if data == nil {
		if kept, err := os.ReadFile(artFile(h.dir, ref)); err == nil {
			data = kept
			rememberArt(ref, data)
		}
	}
	if data == nil {
		body, err := h.openStream("/api/plugins/petasos/music/art?ref=" + url.QueryEscape(ref))
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(body, artBiggest))
			body.Close()
		}
		if err != nil || len(data) == 0 {
			if strings.HasPrefix(ref, "https://") { // an older plugin: straight from the source
				http.Redirect(w, r, ref, http.StatusFound)
				return
			}
			http.NotFound(w, r)
			return
		}
		rememberArt(ref, data)
	}
	if keep {
		keepArt(h.dir, ref, data)
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data)
}
