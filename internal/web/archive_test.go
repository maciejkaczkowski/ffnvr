package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/jobs"
	"ffnvr/internal/modules"
)

func TestArchive(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	srv, st := setup(t)
	st.exec(t, `/nvr set nvr02 host="`+fake.Addr()+`" password=secret`)
	modules.MaintainLocal(st.e.Store, time.Now(), func(string, ...any) {})
	sp := jobs.Spool{Dir: filepath.Join(st.e.Opts.Root, jobs.Dir)}
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	// a local recorder opens on its player: the cameras of the local recorders on the left
	_, page := get(t, c, srv.URL+"/?p=archive")
	contains(t, "player", page, "<h1>Playback</h1>", `class="srcs lside"`, `class="lcam" draggable="true" data-n="nvr01" data-c="cam1"`,
		"Local recorders", "External recorders", `data-n="nvr02" data-c="ch01"`, `id="pgrid"`, `id="pbcanvas"`,
		`id="pbdata"`, `src="/static/playback.js"`, "File search", "Range download", `id="pbexport"`, `id="arqueuetab"`)
	notContains(t, "player", page, `id="arqueue"`) // the queue has its own tab
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=search")
	contains(t, "archive", page, "<h1>nvr01</h1>", `class="srcs"`, "Local recorders", "External recorders",
		`href="/?p=archive&amp;n=nvr02&amp;tab=search"`, "File search", "Range download", `name="c" value="cam1"`)
	if _, rp := get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=range"); strings.Contains(rp, "<option>rec2</option>") || !strings.Contains(rp, "<option>other</option>") {
		t.Error("only storages of types=archive are archive targets")
	}
	if strings.Contains(page, "nvr09") || strings.Contains(page, "Status dashboard") {
		t.Error("only the user's recorders on the left")
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]
	post := func(path string, v url.Values) string {
		v.Set("csrf", csrf)
		resp, err := c.PostForm(srv.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	// a fixed time of day: the blocks of 1h and 40m must not give the same file names
	y := time.Now().AddDate(0, 0, -1)
	from := time.Date(y.Year(), y.Month(), y.Day(), 7, 14, 0, 0, time.Local)
	to := from.Add(150 * time.Minute)
	in := func(t time.Time) string { return t.Format(inputLayout) }

	// a range: two cameras, one file each
	out := post("/archive/range", url.Values{"n": {"nvr01"}, "c": {"cam1", "cam3"}, "from": {in(from)}, "to": {in(to)},
		"storage": {"disk1"}, "path": {"case-12/gate"}, "percamera": {"1"}})
	contains(t, "range queued", out, "2 jobs queued.")
	js, _ := sp.List()
	if len(js) != 2 || js[0].Camera != "cam1" || js[1].Camera != "cam3" || len(js[0].Parts) != 1 || !strings.Contains(js[0].Target, "/case-12/gate/nvr01/brama/") ||
		!js[0].Start.Equal(from) || !js[0].End.Equal(to) || js[0].Comment != "web: jan" {
		t.Errorf("%+v", js)
	}
	// in blocks of 1h (clock): 3 files
	post("/archive/range", url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {in(from)}, "to": {in(to)}, "storage": {"disk1"}, "block": {"1h"}})
	if j, _ := sp.Get(3); j == nil || j.Block != "1h" || len(j.Parts) < 3 {
		t.Errorf("blocks: %+v", j)
	}
	post("/archive/range", url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {in(from)}, "to": {in(to)}, "storage": {"disk1"}, "block": {"custom"}, "block_custom": {"40m"}})
	if j, _ := sp.Get(4); j == nil || j.Block != "40m" {
		t.Errorf("custom block: %+v", j)
	}
	// every storage may be the target (for now), an unknown one not
	contains(t, "other storage", post("/archive/range", url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {in(from)}, "to": {in(to)}, "storage": {"other"}, "percamera": {"1"}}),
		"1 jobs queued.")
	contains(t, "unknown storage", post("/archive/range", url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {in(from)}, "to": {in(to)}, "storage": {"nope"}, "percamera": {"1"}}),
		"No archive storage is available")
	contains(t, "no camera", post("/archive/range", url.Values{"n": {"nvr01"}, "from": {in(from)}, "to": {in(to)}, "storage": {"disk1"}}), "Choose at least one camera.")
	contains(t, "bad path", post("/archive/range", url.Values{"n": {"nvr01"}, "c": {"cam1"}, "from": {in(from)}, "to": {in(to)}, "storage": {"disk1"}, "path": {"../etc"}, "percamera": {"1"}}),
		"only letters")

	// file search: the local segment of 3 days ago
	q := url.Values{"p": {"archive"}, "n": {"nvr01"}, "tab": {"search"}, "search": {"1"}, "c": {"cam1"},
		"from": {in(time.Now().Add(-80 * time.Hour))}, "to": {in(time.Now())}}
	_, page = get(t, c, srv.URL+"/?"+q.Encode())
	contains(t, "search", page, "1 files found", "Archive selected", `>MKV</a>`, `>MP4</a>`, `data-all="f"`, "15 min")
	contains(t, "reach", page, "Recordings on the server: ", ` min="`, ` max="`)
	contains(t, "reach", page, "Recordings on the server: ", ` min="`, ` max="`)
	val := regexpFind(t, page, `name="f" value="([^"]+)"`)
	out = post("/archive/files", url.Values{"n": {"nvr01"}, "f": {val}, "storage": {"disk1"}, "path": {"secured"}})
	contains(t, "files queued", out, "1 jobs queued.")
	if j, _ := sp.Get(6); j == nil || !strings.Contains(j.Target, "/secured/") || j.End.Sub(j.Start) != 15*time.Minute {
		t.Errorf("%+v", j)
	}
	// the segment itself, as it is and as mp4
	name := regexpFind(t, page, `f=([0-9T-]+\.mkv)&amp;fmt=mkv`)
	resp, _ := c.Get(srv.URL + "/archive/file?n=nvr01&c=cam1&f=" + name + "&fmt=mkv")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(b) != 2000 || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="nvr01_cam1_`) {
		t.Errorf("mkv: %d %d %v", resp.StatusCode, len(b), resp.Header)
	}
	for _, bad := range []string{"../../etc/passwd", "x.mkv", "2026-10-26T10-00-00.mkv/../../a"} {
		resp, _ := c.Get(srv.URL + "/archive/file?n=nvr01&c=cam1&f=" + url.QueryEscape(bad))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%s served", bad)
		}
	}
	resp, _ = c.Get(srv.URL + "/archive/file?n=nvr09&c=x&f=" + name)
	resp.Body.Close()
	if resp.Request.URL.Path != "/" { // redirected away: no access
		t.Errorf("foreign recorder: %v", resp.Request.URL)
	}

	// the remote recorder: its search
	fake.Ranges["1|"+from.Format("2006-01-02 15:04:05")+"|"+to.Format("2006-01-02 15:04:05")] = []byte("x")
	q.Set("n", "nvr02")
	q.Set("c", "ch01")
	q.Set("from", in(from))
	q.Set("to", in(to))
	_, page = get(t, c, srv.URL+"/?"+q.Encode())
	contains(t, "remote search", page, "1 files found", "x.dav")
	if strings.Contains(page, ">MKV</a>") {
		t.Error("remote files are archived, not downloaded one by one")
	}

	// the queue: cancel, retry, download
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=search")
	contains(t, "queue's tab", page, `href="/?p=archive&amp;n=nvr01&amp;tab=queue" title="6 jobs waiting or running">Archive queue <span class="count">6</span></a>`)
	notContains(t, "queue's tab", page, `id="arqueue"`)
	// the queue of the recorder: nvr02 has none of nvr01's jobs
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr02&tab=queue")
	contains(t, "nvr02's queue", page, `id="arqueue"`, "the jobs of this recorder", `tab=queue" aria-selected="true">Archive queue</a>`)
	notContains(t, "nvr02's queue", page, `action="/archive/cancel"`, `Archive queue <span class="count">`)
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=queue")
	contains(t, "queue", page, `action="/archive/cancel"`, `name="back" value="/?p=archive&amp;n=nvr01&amp;tab=queue"`,
		`tab=queue" aria-selected="true" title="6 jobs waiting or running">Archive queue`)
	post("/archive/cancel", url.Values{"id": {"1"}, "back": {"/?p=archive&n=nvr01"}})
	if !sp.CancelRequested(1) {
		t.Error("cancel")
	}
	j2, _ := sp.Get(2)
	j2.State = jobs.Failed
	sp.Save(j2)
	post("/archive/retry", url.Values{"id": {"2"}})
	if js, _ := sp.List(); len(js) != 7 || js[6].Camera != "cam3" || js[6].State != jobs.Queued {
		t.Errorf("retry: %d %+v", len(js), js[len(js)-1])
	}
	j5, _ := sp.Get(6)
	j5.State = jobs.Done
	writeFile(t, j5.Parts[0].Target, "MP4DATA")
	sp.Save(j5)
	resp, _ = c.Get(srv.URL + "/archive/result?id=6")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "MP4DATA" {
		t.Errorf("result: %q", b)
	}
	// someone else's job
	j9, _ := sp.Submit(jobs.Job{Nvr: "nvr09", Target: "/x"})
	resp, _ = c.PostForm(srv.URL+"/archive/cancel", url.Values{"id": {strconv.Itoa(j9.ID)}, "csrf": {csrf}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || sp.CancelRequested(j9.ID) {
		t.Errorf("foreign job: %d", resp.StatusCode)
	}
	// the layout: folders per recorder and camera by default, all in one folder when asked
	_, rp := get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=range")
	contains(t, "layout", rp, `<select class="in" name="layout"><option value="nvr-camera" selected>&lt;recorder&gt;/&lt;camera&gt;/file</option><option value="flat">all files in one folder</option></select>`)
	post("/archive/range", url.Values{"n": {"nvr01"}, "c": {"cam3"}, "from": {in(from)}, "to": {in(to)}, "storage": {"disk1"}, "path": {"flat"}, "percamera": {"1"}, "layout": {"flat"}})
	if js, _ := sp.List(); len(js) == 0 || !strings.Contains(js[len(js)-1].Target, "/flat/nvr01_rampa_") {
		t.Errorf("flat: %+v", js[len(js)-1])
	}
}

func TestArchiveMP4OnTheFly(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	srv, st := setup(t)
	disk := findItemVal(st, "/storage", "disk1", "path")
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	seg := filepath.Join(disk, "nvr01", "cam1", start.Format("2006-01-02T15-04-05")+".mkv")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", "3", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", seg).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	resp, err := c.Get(srv.URL + "/archive/file?n=nvr01&c=cam1&f=" + filepath.Base(seg) + "&fmt=mp4")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(b) < 1000 || string(b[4:8]) != "ftyp" || !strings.HasSuffix(resp.Header.Get("Content-Disposition"), `.mp4"`) {
		t.Errorf("%d bytes %q %v", len(b), b[:8], resp.Header)
	}
}

func TestArchiveTiles(t *testing.T) {
	srv, st := setup(t)
	modules.MaintainLocal(st.e.Store, time.Now(), func(string, ...any) {})
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	q := url.Values{"p": {"archive"}, "n": {"nvr01"}, "tab": {"search"}, "search": {"1"}, "c": {"cam1"}, "view": {"tiles"},
		"from": {time.Now().Add(-80 * time.Hour).Format(inputLayout)}, "to": {time.Now().Format(inputLayout)}}
	_, page := get(t, c, srv.URL+"/?"+q.Encode())
	contains(t, "tiles", page, `class="thumbs"`, `data-thumb="1"`, `<span class="timg loading"><img src="/archive/thumb?`, `.thumb .timg.loading::after`, ">Tiles</a>", `name="f" value="cam1|`)
	seg := regexpFind(t, page, `f=([0-9T-]+\.mkv)" alt=""`)
	resp, _ := c.Get(srv.URL + "/archive/thumb?n=nvr01&c=cam1&f=" + seg)
	resp.Body.Close()
	if resp.StatusCode != 202 || !modules.PendingThumbs(st.e.Opts.Root) {
		t.Errorf("asked of the worker: %d", resp.StatusCode)
	}
	thumb := modules.ThumbPath(st.e.Store, "nvr01", "cam1", seg)
	writeFile(t, thumb, "\xff\xd8JPEG")
	resp, _ = c.Get(srv.URL + "/archive/thumb?n=nvr01&c=cam1&f=" + seg)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "\xff\xd8JPEG" {
		t.Errorf("%d %q", resp.StatusCode, b)
	}
	// the motion bar: being made, then from its file
	contains(t, "pending bar", page, `class="mbar"`, `data-pending="1"`, strings.Repeat(`<i class="wait"></i>`, 30))
	_, js := get(t, c, srv.URL+"/archive/motion?n=nvr01&c=cam1&f="+seg)
	if js != "{\"duration\":0,\"parts\":\"\",\"ready\":false}\n" {
		t.Errorf("%q", js)
	}
	// the detector looked at it: what it found is in the event database
	segStart, _ := time.ParseInLocation("2006-01-02T15-04-05", strings.TrimSuffix(seg, ".mkv"), time.Local)
	db, err := eventdb.Open(eventdb.Path(st.e.Opts.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetMotion(context.Background(), "nvr01", "cam1", seg, segStart, segStart.Add(900*time.Second),
		[]eventdb.DetectedRange{{Start: segStart.Add(305 * time.Second), End: segStart.Add(385 * time.Second)}, {Start: segStart.Add(880 * time.Second), End: segStart.Add(905 * time.Second)}},
		"packets", modules.MotionDetectorSettings(st.e.Store).Signature(), time.Now()); err != nil {
		t.Fatal(err)
	}
	_, js = get(t, c, srv.URL+"/archive/motion?n=nvr01&c=cam1&f="+seg)
	if !strings.Contains(js, `{"duration":900,"parts":"000000000011100000000000000001","ready":true`) {
		t.Errorf("%q", js)
	}
	_, page = get(t, c, srv.URL+"/?"+q.Encode())
	contains(t, "bar", page, strings.Repeat(`<i class="still"></i>`, 10)+strings.Repeat(`<i class="mv"></i>`, 3))
	if code, _ := get(t, c, srv.URL+"/archive/thumb?n=nvr01&c=cam1&f=../../x"); code != 400 {
		t.Errorf("%d", code)
	}
}

func TestSystemMenuOpensItsSection(t *testing.T) {
	srv, _ := setup(t)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	for page, want := range map[string]string{"jobs": "/?p=jobs", "disks": "/?p=disks", "nvr&n=nvr01": "/?p=nvr&amp;n=nvr01", "dash": "/?p=dash"} {
		_, html := get(t, c, srv.URL+"/?p="+page)
		if n := strings.Count(html, "<details open>"); n != 1 {
			t.Errorf("%s: %d sections open", page, n)
			continue
		}
		open := html[strings.Index(html, "<details open>"):]
		open = open[:strings.Index(open, "</details>")]
		if !strings.Contains(open, `href="`+want+`"`) {
			t.Errorf("%s: another section is open:\n%s", page, open)
		}
	}
}

// The side menus of the Archive show the heading of local or external recorders only
// when the user has one of that kind.
func TestArchiveSideHeadings(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/nvr set nvr09 valid-users=@firma2`)
	local, external := `<div class="srcg">Local recorders</div>`, `<div class="srcg">External recorders</div>`
	// ola: nvr02 only, an external recorder
	c := client()
	signIn(t, c, srv.URL, "ola", "ola")
	for _, u := range []string{"/?p=archive&n=nvr02&tab=search", "/?p=archive&tab=play"} {
		_, page := get(t, c, srv.URL+u)
		contains(t, "ola "+u, page, external)
		notContains(t, "ola "+u, page, local)
	}
	// jan without nvr02: local recorders only
	st.exec(t, `/nvr set nvr02 valid-users=ola`)
	c = client()
	signIn(t, c, srv.URL, "jan", "secret")
	for _, u := range []string{"/?p=archive&n=nvr01&tab=search", "/?p=archive&tab=play"} {
		_, page := get(t, c, srv.URL+u)
		contains(t, "jan "+u, page, local)
		notContains(t, "jan "+u, page, external)
	}
}
