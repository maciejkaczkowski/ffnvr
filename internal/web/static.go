package web

import (
	_ "embed"
	"net/http"
)

//go:embed static/live.js
var liveJS []byte

//go:embed static/playback.js
var playbackJS []byte

// static serves the panel's own scripts.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if s.current(r) == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.URL.Path == "/static/playback.js" {
		w.Write(playbackJS)
		return
	}
	w.Write(liveJS)
}
