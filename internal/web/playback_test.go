package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/jobs"
	"ffnvr/internal/modules"
)

func TestPlayback(t *testing.T) {
	srv, st := setup(t)
	sp := jobs.Spool{Dir: filepath.Join(st.e.Opts.Root, jobs.Dir)}
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// the page: the cameras asked for go on the tiles, from the time asked for
	_, page := get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=play&c=cam1&c=nvr01|cam3&c=nope&t=1700000000")
	contains(t, "player", page, `"init":[{"n":"nvr01","c":"cam1"},{"n":"nvr01","c":"cam3"}]`, `"t0":1700000000`,
		`"export":true`, `"label":"brama"`, `aria-selected="true">Playback</a>`, `data-layout="16"`, `id="pbspeed"`, `id="calgrid"`,
		`name="storage"`, `id="percamera"`)
	contains(t, "player: Dahua", page, `data-n="nvr02" data-c="ch01"`, `"n":"nvr02","c":"ch01"`)
	notContains(t, "player", page, `data-n="nvr09"`) // a foreign recorder
	if strings.Contains(page, `<div class="upd">`) {
		t.Error("no refresh button over the player")
	}
	csrf := regexpFind(t, page, `"csrf":"([^"]+)"`)
	if resp, err := c.Get(srv.URL + "/static/playback.js"); err != nil || resp.StatusCode != 200 {
		t.Errorf("the script: %v %v", err, resp)
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), "/archive/segments") {
			t.Error("the script of the player")
		}
	}

	// the segments of a day: the recording of 3 days ago, the days that have recordings
	segs := func(q url.Values) (int, map[string]any) {
		t.Helper()
		resp, err := c.Get(srv.URL + "/archive/segments?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	ago := time.Now().Add(-72 * time.Hour)
	unix := func(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }
	code, m := segs(url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {unix(ago.Add(-time.Hour))}, "to": {unix(ago.Add(time.Hour))}})
	if code != 200 || len(m["segs"].([]any)) != 1 || len(m["days"].([]any)) == 0 || m["days"].([]any)[0] != ago.Format("2006-01-02") {
		t.Errorf("%d %v", code, m)
	} else if s := m["segs"].([]any)[0].(map[string]any); s["e"].(float64)-s["b"].(float64) != 900 || !strings.HasSuffix(s["f"].(string), ".mkv") {
		t.Errorf("a segment: %v", s)
	}
	// the motion of the segment, from the event database only: a Tiandy camera's alarms
	// (the detector's ranges are not the motion of a Tiandy camera)
	if s, ok := m["segs"].([]any)[0].(map[string]any); !ok || s["m"] != nil {
		t.Errorf("no motion yet: %v", m)
	}
	st.exec(t, `/camera set cam1 type=tiandy`)
	b0 := time.Unix(int64(m["segs"].([]any)[0].(map[string]any)["b"].(float64)), 0)
	db, err := eventdb.Open(eventdb.Path(st.e.Opts.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := eventdb.NewWriter(db, t.TempDir(), t.Logf)
	for _, n := range []eventdb.Notice{
		{Nvr: "nvr01", Camera: "cam1", Kind: `INNER\ALARM\MOTIONDETECT`, Source: "tiandy", Time: b0.Add(60 * time.Second), State: eventdb.Start},
		{Nvr: "nvr01", Camera: "cam1", Kind: `INNER\ALARM\MOTIONDETECT`, Source: "tiandy", Time: b0.Add(120 * time.Second), State: eventdb.Stop},
		{Nvr: "nvr01", Camera: "cam1", Kind: "MotionDetect", Source: "detector", Time: b0.Add(600 * time.Second), State: eventdb.Start},
		{Nvr: "nvr01", Camera: "cam1", Kind: "MotionDetect", Source: "detector", Time: b0.Add(610 * time.Second), State: eventdb.Stop},
		{Nvr: "nvr01", Camera: "cam3", Kind: `INNER\ALARM\MOTIONDETECT`, Source: "tiandy", Time: b0.Add(300 * time.Second), State: eventdb.Pulse},
	} {
		w.Add(n)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_, m = segs(url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {unix(ago.Add(-time.Hour))}, "to": {unix(ago.Add(time.Hour))}})
	if mv, _ := m["segs"].([]any)[0].(map[string]any)["m"].(string); len(mv) != 900 || strings.Count(mv, "1") != 60 || strings.Index(mv, "1") != 60 {
		t.Errorf("tiandy motion: %d parts, %d moving, from %d", len(mv), strings.Count(mv, "1"), strings.Index(mv, "1"))
	}
	st.exec(t, `/camera unset cam1 type`) // the detector's camera: MotionDetect
	_, m = segs(url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {unix(ago.Add(-time.Hour))}, "to": {unix(ago.Add(time.Hour))}})
	if mv, _ := m["segs"].([]any)[0].(map[string]any)["m"].(string); strings.Index(mv, "1") != 600 {
		t.Errorf("detector motion: from %d", strings.Index(mv, "1"))
	}

	if code, m = segs(url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {unix(time.Now().Add(-time.Hour))}, "to": {unix(time.Now())}}); code != 200 || len(m["segs"].([]any)) != 0 || len(m["days"].([]any)) == 0 {
		t.Errorf("nothing in this hour, the days all the same: %d %v", code, m)
	}
	for _, bad := range []url.Values{
		{"n": {"nvr01"}, "c": {"cam1"}, "from": {"x"}, "to": {"1"}},
		{"n": {"nvr01"}, "c": {"nope"}, "from": {"1"}, "to": {"2"}},
		{"n": {"nvr01"}, "c": {"../cam1"}, "from": {"1"}, "to": {"2"}},
	} {
		if code, _ := segs(bad); code != 400 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if resp, _ := c.Get(srv.URL + "/archive/segments?n=nvr09&c=cam1&from=1&to=2"); resp.Request.URL.Path != "/" {
		t.Errorf("a foreign recorder: %v", resp.Request.URL)
	}

	// archiving the marked range: the cameras of the tiles, to the second
	export := func(v url.Values) (int, map[string]any) {
		t.Helper()
		resp, err := c.PostForm(srv.URL+"/archive/export", v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	from := time.Date(ago.Year(), ago.Month(), ago.Day(), 7, 14, 21, 0, time.Local)
	to := from.Add(9*time.Minute + 13*time.Second)
	v := url.Values{"csrf": {csrf}, "from": {unix(from)}, "to": {unix(to)}, "storage": {"disk1"}, "path": {"case-7"},
		"percamera": {"1"}, "cam": {"nvr01|cam1", "nvr01|cam3", "nvr01|cam1"}}
	if code, m := export(v); code != 200 || m["ok"] != true || m["queued"].(float64) != 2 {
		t.Fatalf("%d %v", code, m)
	}
	js, _ := sp.List()
	if len(js) != 2 || js[0].Camera != "cam1" || js[1].Camera != "cam3" || !js[0].Start.Equal(from) || !js[0].End.Equal(to) ||
		!strings.Contains(js[0].Target, "/case-7/nvr01/brama/") || js[0].Comment != "web: jan" || len(js[0].Parts) != 1 {
		t.Errorf("%+v", js)
	}
	v.Del("percamera")
	v.Set("block", "custom")
	v.Set("block_custom", "5m")
	v["cam"] = []string{"nvr01|cam1"}
	if code, m := export(v); code != 200 || m["queued"].(float64) != 1 {
		t.Errorf("blocks: %d %v", code, m)
	} else if j, _ := sp.Get(3); j == nil || j.Block != "5m" || len(j.Parts) < 2 {
		t.Errorf("blocks: %+v", j)
	}
	for _, x := range []struct {
		change func(url.Values)
		code   int
		text   string
	}{
		{func(v url.Values) { v.Set("csrf", "x") }, 403, "forbidden"},
		{func(v url.Values) { v.Set("to", v.Get("from")) }, 400, "Give the start and the end."},
		{func(v url.Values) { v.Set("storage", "nope") }, 400, "No archive storage"},
		{func(v url.Values) { v.Del("cam") }, 400, "Choose at least one camera."},
		{func(v url.Values) { v["cam"] = []string{"nvr09|cam1"} }, 400, "nvr09: no access"},
	} {
		w := url.Values{}
		for k, vs := range v {
			w[k] = append([]string{}, vs...)
		}
		x.change(w)
		if code, m := export(w); code != x.code || !strings.Contains(m["error"].(string), x.text) {
			t.Errorf("%s: %d %v", x.text, code, m)
		}
	}
}

func TestPlaybackRights(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`,
		`/user/policy add name=viewer allow="live,archive"`, // no archive.play, no archive.export
		`/user set jan policy=viewer`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=play")
	contains(t, "no player", page, "<h1>nvr01</h1>", `name="c" value="cam1"`)
	notContains(t, "no player", page, `id="pbdata"`, ">Playback</a>")
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "live: local cameras", page, `"n":"nvr01","c":"cam1","label":"brama"`, `"play":true`)
	resp, _ := c.Get(srv.URL + "/archive/segments?n=nvr01&c=cam1&from=1&to=2")
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Error("segments need archive.play")
	}
	csrf := regexpFind(t, page, `"csrf":"([^"]+)"`)
	resp, _ = c.PostForm(srv.URL+"/archive/export", url.Values{"csrf": {csrf}, "from": {"1"}, "to": {"2"}, "storage": {"disk1"}, "cam": {"nvr01|cam1"}})
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("export needs archive.export: %d", resp.StatusCode)
	}
}

func TestCapRange(t *testing.T) {
	for in, want := range map[string]string{
		"bytes=0-":          "bytes=0-4194303",
		"bytes=100-":        "bytes=100-4194403",
		"bytes=0-1":         "bytes=0-1",        // a small range stays
		"bytes=10-99999999": "bytes=10-4194313", // a long one is cut
		"bytes=-500":        "bytes=-500",       // the end of the file
		"bytes=0-10,20-30":  "bytes=0-10,20-30", // several ranges: ServeContent's
		"items=0-":          "items=0-",
		"bytes=x-":          "bytes=x-",
	} {
		if got := capRange(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestPlaybackTabOfDVRIP(t *testing.T) {
	// a DVRIP (XMeye) recorder was the one the player did not play (its file search
	// only); now every remote type plays
	srv, st := setup(t)
	st.exec(t, `/nvr add type=dvrip name=nvr03 host=10.0.4.30 username=admin password=x valid-users=@firma2`,
		`/camera add name=ch01 nvr=nvr03 channel=1`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=archive&n=nvr03")
	contains(t, "dvrip", page, `<a role="tab" href="/?p=archive&amp;n=nvr03&amp;tab=play" aria-selected="true">Playback</a>`,
		`id="pbdata"`, "4 at once")
	// in the player's tree beside the others
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=play")
	contains(t, "tree", page, `data-n="nvr03" data-c="ch01"`, `data-n="nvr02" data-c="ch01"`)
	notContains(t, "tree", page, `href="/?p=archive&amp;n=nvr03&amp;tab=search"`)
	// a Dahua recorder opens on its player
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr02")
	contains(t, "dahua", page, `<a role="tab" href="/?p=archive&amp;n=nvr02&amp;tab=play" aria-selected="true">Playback</a>`, `id="pbdata"`, "4 at once")
}

func TestPlaybackDahua(t *testing.T) {
	n := dahuatest.New()
	defer n.Close()
	n.Recorded = map[int][][2]string{1: {
		{"2026-11-01 00:00:00", "2026-11-01 01:00:00"},
		{"2026-11-01 01:00:00", "2026-11-01 01:00:45"},
		{"2026-11-03 10:00:00", "2026-11-03 11:00:00"},
	}}
	n.Load = func(ch int, from, to string) []byte { return []byte("DHAV, but not a stream") }
	n.LoadHold = 400 * time.Millisecond
	srv, st := setup(t)
	st.exec(t, `/nvr set nvr02 host=`+n.Addr()+` username=`+dahuatest.User+` password=`+dahuatest.Pass)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// the files of the recorder in pieces of 30 s, the days of its month
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)
	segs := func(from, to time.Time) map[string]any {
		t.Helper()
		resp, err := c.Get(fmt.Sprintf("%s/archive/segments?n=nvr02&c=ch01&from=%d&to=%d", srv.URL, from.Unix(), to.Unix()))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		if resp.StatusCode != 200 {
			t.Fatalf("%d %v", resp.StatusCode, m)
		}
		return m
	}
	countSearches := func() int {
		n.Mu.Lock()
		defer n.Mu.Unlock()
		k := 0
		for _, r := range n.Requests {
			if strings.Contains(r, "findFile") {
				k++
			}
		}
		return k
	}
	m := segs(day, day.Add(24*time.Hour))
	before := countSearches() // October (a file begun before midnight) and November
	ss := m["segs"].([]any)
	first, last := ss[0].(map[string]any), ss[len(ss)-1].(map[string]any)
	if len(ss) != 122 || m["remote"] != true || first["f"] != fmt.Sprintf("%d-%d", day.Unix(), day.Unix()+30) ||
		last["b"].(float64) != float64(day.Unix()+3630) || last["e"].(float64) != float64(day.Unix()+3645) {
		t.Errorf("%d %v %v %v", len(ss), m["remote"], first, last)
	}
	if d := fmt.Sprint(m["days"]); d != "[2026-11-01 2026-11-03]" {
		t.Errorf("days: %s", d)
	}
	m = segs(day.Add(10*time.Minute+10*time.Second), day.Add(11*time.Minute))
	if ss := m["segs"].([]any); len(ss) != 2 || ss[0].(map[string]any)["b"].(float64) != float64(day.Unix()+600) {
		t.Errorf("a piece in the middle: %v", ss)
	}
	segs(day.Add(3*time.Hour), day.Add(27*time.Hour))
	if before != 2 || countSearches() != before {
		t.Errorf("a month is searched once: %d %d", before, countSearches())
	}

	// at most 4 streams from the recorder at once: 8 tiles take turns
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := day.Unix() + int64(i)*30
			resp, err := c.Get(fmt.Sprintf("%s/archive/play?n=nvr02&c=ch01&f=%d-%d", srv.URL, b, b+30))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		}(i)
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 500 { // not a DHAV stream: ffmpeg refuses it (once more, then gives up)
			t.Errorf("play: %d", code)
		}
	}
	n.Mu.Lock()
	loads := 0
	for _, r := range n.Requests {
		if strings.Contains(r, "loadfile.cgi") {
			loads++
		}
	}
	if n.MaxLoads != modules.PlayStreams || loads != 16 {
		t.Errorf("at once: %d, loads %d (8 pieces, tried twice)", n.MaxLoads, loads)
	}
	for _, r := range n.Requests {
		if strings.Contains(r, "loadfile.cgi") && strings.Contains(r, "findFile") {
			t.Error("a piece is read without a search")
		}
	}
	searches := 0
	for _, r := range n.Requests {
		if strings.Contains(r, "findFile") {
			searches++
		}
	}
	if searches != 2 { // the months of the timeline only
		t.Errorf("searches: %d", searches)
	}
	n.MaxLoads = 0
	n.Mu.Unlock()

	// read-ahead leaves a stream free for the pieces being played
	play := func(i int, ahead bool, wg *sync.WaitGroup) {
		defer wg.Done()
		b := day.Unix() + 600 + int64(i)*30
		u := fmt.Sprintf("%s/archive/play?n=nvr02&c=ch01&f=%d-%d", srv.URL, b, b+30)
		if ahead {
			u += "&ahead=1"
		}
		if resp, err := c.Get(u); err == nil {
			resp.Body.Close()
		}
	}
	var wa sync.WaitGroup
	for i := 0; i < 5; i++ {
		wa.Add(1)
		go play(i, true, &wa)
	}
	wa.Wait()
	n.Mu.Lock()
	if n.MaxLoads != modules.PlayStreams-1 {
		t.Errorf("read-ahead at once: %d", n.MaxLoads)
	}
	n.MaxLoads = 0
	n.Mu.Unlock()
	var wb sync.WaitGroup
	for i := 10; i < 15; i++ {
		wb.Add(1)
		go play(i, true, &wb)
	}
	time.Sleep(150 * time.Millisecond)
	wb.Add(1)
	go play(20, false, &wb) // played now: takes the free one
	wb.Wait()
	n.Mu.Lock()
	if n.MaxLoads != modules.PlayStreams {
		t.Errorf("with a piece played: %d", n.MaxLoads)
	}
	n.Mu.Unlock()
	// nothing recorded there: no stream asked for
	resp, _ := c.Get(fmt.Sprintf("%s/archive/play?n=nvr02&c=ch01&f=%d-%d", srv.URL, day.Unix()+7200, day.Unix()+7230))
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("no recording: %d", resp.StatusCode)
	}
	for _, f := range []string{"1-9999", "x-1", "../x"} {
		resp, _ := c.Get(srv.URL + "/archive/play?n=nvr02&c=ch01&f=" + url.QueryEscape(f))
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("%s: %d", f, resp.StatusCode)
		}
	}
}
