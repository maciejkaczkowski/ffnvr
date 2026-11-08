package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTileProxy(t *testing.T) {
	var hits atomic.Int32
	var ua atomic.Value
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		ua.Store(r.Header.Get("User-Agent"))
		if r.URL.Path != "/16/36000/21000.png" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNGtile"))
	}))
	defer up.Close()
	old := tileClient
	tileClient = up.Client()
	defer func() { tileClient = old }()
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes map-tiles="`+up.URL+`/{z}/{x}/{y}.png"`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	for i := 0; i < 2; i++ {
		resp, _ := c.Get(srv.URL + "/live/tiles/16/36000/21000.png")
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != "\x89PNGtile" {
			t.Fatalf("%d %q", resp.StatusCode, b)
		}
	}
	if hits.Load() != 1 || !strings.HasPrefix(ua.Load().(string), "ffnvr/1.0") {
		t.Errorf("fetched %d times (cached after the first), user agent %v", hits.Load(), ua.Load())
	}
	if code, _ := get(t, c, srv.URL+"/live/tiles/3/9/1.png"); code != 404 {
		t.Errorf("x beyond the zoom: %d", code)
	}
	if code, _ := get(t, client(), srv.URL+"/live/tiles/16/36000/21000.png"); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	_, page := get(t, c, srv.URL+"/?p=live")
	contains(t, "live", page, `"tiles":"/live/tiles/{z}/{x}/{y}.png"`)
}
