package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/modules"
)

// The map's tiles come through the panel: the server fetches them (identifying itself,
// as the OpenStreetMap tile policy asks - browsers of a panel that sends no referrer
// get "Access blocked") and keeps them for a week in /var/lib/ffnvr/tiles. The browser
// asks only the panel.

const tileDir = "var/lib/ffnvr/tiles"
const tileKeep = 7 * 24 * time.Hour

var tileClient = &http.Client{Timeout: 15 * time.Second}

// GET /live/tiles/{z}/{x}/{y}
func (s *Server) tile(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	z, e1 := strconv.Atoi(r.PathValue("z"))
	x, e2 := strconv.Atoi(r.PathValue("x"))
	y, e3 := strconv.Atoi(strings.TrimSuffix(r.PathValue("y"), ".png"))
	if e1 != nil || e2 != nil || e3 != nil || z < 0 || z > 19 || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		http.Error(w, "no such tile", http.StatusNotFound)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, modules.PermLiveMap) {
		s.denied(w, se, modules.PermLiveMap)
		return
	}
	src := modules.LiveSettings(store).MapTiles
	cache := filepath.Join(s.opt.Root, tileDir, fmt.Sprintf("%x", hashString(src)), strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+".png")
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < tileKeep {
		serveTile(w, cache)
		return
	}
	u := strings.NewReplacer("{z}", strconv.Itoa(z), "{x}", strconv.Itoa(x), "{y}", strconv.Itoa(y), "{s}", string("abc"[(x+y)%3])).Replace(src)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "ffnvr/"+s.opt.Version+" (NVR web panel; tiles cached for 7 days)")
	resp, err := tileClient.Do(req)
	if err != nil {
		if _, e := os.Stat(cache); e == nil {
			serveTile(w, cache) // an old one is better than none
			return
		}
		s.opt.Log.Printf("web: map tile: %v", err)
		http.Error(w, "tile not available", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		s.opt.Log.Printf("web: map tile %s: HTTP %d", u, resp.StatusCode)
		http.Error(w, "tile not available", http.StatusBadGateway)
		return
	}
	if os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		tmp := cache + ".tmp"
		if os.WriteFile(tmp, body, 0o644) == nil {
			os.Rename(tmp, cache)
		}
	}
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(body)
}

func serveTile(w http.ResponseWriter, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "tile not available", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(b)
}

func hashString(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	return h
}
