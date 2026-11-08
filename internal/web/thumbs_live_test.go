package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/modules"
)

func TestLiveThumbnails(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`, `/system/snapshot set enabled=yes interval=weekly day=sunday time=02:30`)
	root := st.e.Opts.Root
	taken := time.Date(2026, 10, 25, 2, 30, 5, 0, time.Local)
	state := modules.SnapState{Schedule: modules.SnapshotSettings(st.e.Store).String(), LastRun: taken.Add(time.Minute), Cams: map[string]modules.SnapCam{
		"nvr01/cam1": {Taken: taken, Tried: taken, OK: true},
		"nvr01/cam3": {Taken: taken.AddDate(0, 0, -7), Tried: taken, Error: "go2rtc: rtsp://admin:tajne@10.0.0.9/s: i/o timeout"},
		"nvr09/x":    {Taken: taken, Tried: taken, OK: true},
	}}
	b, _ := json.Marshal(state)
	os.MkdirAll(filepath.Join(root, modules.SnapDir, "nvr01"), 0o755)
	os.WriteFile(filepath.Join(root, modules.SnapDir, "state.json"), b, 0o644)
	os.WriteFile(modules.SnapshotPath(root, "nvr01", "cam1"), []byte("\xff\xd8jpeg-cam1"), 0o644)
	os.WriteFile(modules.SnapshotPath(root, "nvr01", "cam3"), []byte("\xff\xd8jpeg-cam3"), 0o644)

	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=live")
	contains(t, "live page", page, `id="tabthumbs"`, `id="lthumbs"`, `class="lthumbside"`, `"thumbs":true`,
		`{"n":"nvr01","c":"cam1","sym":"brama","has":true,"v":`+strconv.FormatInt(taken.Unix(), 10)+`,"taken":"2026-10-25 02:30","tried":"2026-10-25 02:30","ok":true,"err":""}`,
		`{"n":"nvr01","c":"cam2","sym":"NVR01 cam2","has":false,"v":`, // never taken
		`"err":"go2rtc: rtsp://admin:*****@10.0.0.9/s: i/o timeout"`,
		`"sched":"every Sunday at 02:30"`,
		// many cameras: rows keep their size and the list scrolls (overflow:hidden tiles would shrink to nothing)
		`grid-auto-rows:max-content`, `overflow-y:auto`, `"last":"2026-10-25 02:31"`, `"tb_tab":"Thumbnails"`)
	notContains(t, "live page", page, `"c":"x"`, "tajne") // nvr09: no access; the password is masked
	// by symbol across recorders: brama (cam1, an alias) before NVR02 ch01
	if i, j := strings.Index(page, `"c":"cam1","sym"`), strings.Index(page, `"c":"ch01","sym"`); i < 0 || j < 0 || i > j {
		t.Errorf("by name: cam1 %d, ch01 %d", i, j)
	}

	code, body := get(t, c, srv.URL+"/live/thumb?n=nvr01&c=cam1&v=1")
	if code != 200 || body != "\xff\xd8jpeg-cam1" {
		t.Errorf("thumb: %d %q", code, body)
	}
	resp, _ := c.Get(srv.URL + "/live/thumb?n=nvr01&c=cam1&v=1")
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(resp.Header.Get("Cache-Control"), "max-age") {
		t.Errorf("headers: %v", resp.Header)
	}
	for _, x := range []struct {
		q    string
		code int
	}{{"n=nvr01&c=cam2", 404}, {"n=nvr09&c=x", 403}, {"n=nvr01&c=..%2F..%2Fstate", 403}, {"n=nvr01&c=nope", 403}} {
		if code, _ := get(t, c, srv.URL+"/live/thumb?"+x.q); code != x.code {
			t.Errorf("%s: %d, want %d", x.q, code, x.code)
		}
	}

	// without the right: no tab data, no pictures
	st.exec(t, `/user/policy add name=bez allow="live,live.map,live.views"`, `/user set jan policy=bez`)
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "without live.thumbs", page, `"thumbs":false`, `"snaps":[]`)
	if code, body := get(t, c, srv.URL+"/live/thumb?n=nvr01&c=cam1"); code != 403 || !strings.Contains(body, "live.thumbs") {
		t.Errorf("%d %s", code, body)
	}

	// polish texts, the schedule off
	st.exec(t, `/user set jan policy=admin`, `/system/snapshot set enabled=no`)
	u, _ := url.Parse(srv.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: "lang", Value: "pl"}})
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "pl", page, `"tb_tab":"Miniatury"`, `"sched":"wyłączony (/system/snapshot set enabled=yes)"`, `"next":""`)
}
