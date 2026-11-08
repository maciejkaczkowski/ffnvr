package modules

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() { snapRetryPause = 10 * time.Millisecond } // the tests do not wait

func TestSnapshotSchedule(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := func(s string) time.Time { v, _ := time.ParseInLocation("2006-01-02 15:04", s, loc); return v }
	daily := Snapshot{Enabled: true, Hour: 3, Quality: "lq"}
	// Friday 2026-10-30
	if got := daily.Last(at("2026-10-30 10:00")); !got.Equal(at("2026-10-30 03:00")) {
		t.Errorf("daily, after the hour: %v", got)
	}
	if got := daily.Last(at("2026-10-30 02:59")); !got.Equal(at("2026-10-29 03:00")) {
		t.Errorf("daily, before the hour: %v", got)
	}
	if got := daily.Next(at("2026-10-30 10:00")); !got.Equal(at("2026-10-31 03:00")) {
		t.Errorf("daily next: %v", got)
	}
	weekly := Snapshot{Enabled: true, Weekly: true, Day: time.Sunday, Hour: 2, Minute: 30, Quality: "lq"}
	if got := weekly.Last(at("2026-10-30 10:00")); !got.Equal(at("2026-10-25 02:30")) {
		t.Errorf("weekly: %v", got)
	}
	if got := weekly.Last(at("2026-11-01 02:30")); !got.Equal(at("2026-11-01 02:30")) {
		t.Errorf("weekly, that very minute: %v", got)
	}
	if got := weekly.Next(at("2026-10-30 10:00")); !got.Equal(at("2026-11-01 02:30")) {
		t.Errorf("weekly next: %v", got)
	}

	st := SnapState{Schedule: daily.String(), LastRun: at("2026-10-30 03:00")}
	for _, c := range []struct {
		now  string
		st   SnapState
		snap Snapshot
		due  bool
	}{
		{"2026-10-30 10:00", st, daily, false},
		{"2026-10-31 02:59", st, daily, false},
		{"2026-10-31 03:00", st, daily, true},
		{"2026-10-30 10:00", SnapState{}, daily, true},                                  // the first run: at once
		{"2026-10-30 10:00", st, Snapshot{Enabled: true, Hour: 4, Quality: "lq"}, true}, // a changed schedule
		{"2026-10-31 03:00", st, Snapshot{Hour: 3, Quality: "lq"}, false},               // disabled
	} {
		if got := c.snap.Due(c.st, at(c.now)); got != c.due {
			t.Errorf("%s %s (last %v): due=%v", c.now, c.snap, c.st.LastRun, got)
		}
	}

	s := newStore(t)
	expect(t, s, `/system/snapshot set enabled=yes interval=weekly day=sunday time=02:30 quality=hq`, "")
	if got := SnapshotSettings(s); got != (Snapshot{Enabled: true, Weekly: true, Day: time.Sunday, Hour: 2, Minute: 30, Quality: "hq", Attempts: 3}) {
		t.Errorf("%+v", got)
	}
	expect(t, s, `/system/snapshot set time=3:00`, "") // one digit hour is fine
	expect(t, s, `/system/snapshot set time=24:00`, `time "24:00": HH:MM expected`)
	expect(t, s, `/system/snapshot set time=03:5`, `time "03:5": HH:MM expected`)
	expect(t, s, `/system/snapshot set day=niedziela`, `day: "niedziela" is not one of`)
}

func testJPEG(t *testing.T, w, h int) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// fakeGo2rtc answers /api/frame.jpeg: a picture for the streams in pics, an error else.
func fakeGo2rtc(t *testing.T, pics map[string][]byte) (*httptest.Server, *[]string) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := r.URL.Query().Get("src")
		asked = append(asked, src)
		if r.URL.Path != "/api/frame.jpeg" || pics[src] == nil {
			w.Header().Set("Content-Type", "text/plain")
			http.Error(w, "streams: dial tcp 10.0.0.9:554: i/o timeout", 500)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(pics[src])
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func TestTakeSnapshots(t *testing.T) {
	big := testJPEG(t, 1280, 720)
	srv, asked := fakeGo2rtc(t, map[string][]byte{"nvr01/cam1/lq": big, "nvr01/cam3/hq": big, "nvr01/cam5/lq": big})
	u, _ := url.Parse(srv.URL)
	s, root := newStore(t), t.TempDir()
	for i, err := range run(t, s,
		`/storage add name=disk1 path=/srv/disk1`,
		`/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=cam1 nvr=nvr01 stream=rtsp://a/1 stream-lq=rtsp://a/1s`,
		`/camera add name=cam2 nvr=nvr01 stream=rtsp://a/2 stream-lq=rtsp://a/2s`,
		`/camera add name=cam3 nvr=nvr01 stream=rtsp://a/3`, // no lq: hq is taken
		`/camera add name=cam4 nvr=nvr01 stream=rtsp://a/4 enabled=no`,
		`/camera add name=cam5 nvr=nvr01 stream=rtsp://a/5 stream-lq=rtsp://a/5s`,
		`/system/snapshot set enabled=yes`,
	) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, f) }

	// live view off: no run, one line, and it is not due again before the next time
	TakeSnapshots(context.Background(), s, root, "ffmpeg", logf)
	st := ReadSnapState(root)
	if !strings.Contains(st.Error, "/system/live) is off") || len(st.Cams) != 0 || SnapshotSettings(s).Due(st, time.Now()) {
		t.Errorf("%+v", st)
	}

	expect(t, s, `/system/live set enabled=yes port=`+u.Port(), "")
	before := time.Now()
	TakeSnapshots(context.Background(), s, root, "ffmpeg", logf)
	st = ReadSnapState(root)
	if st.Error != "" || st.Schedule != "daily 03:00 lq" || st.LastRun.Before(before) || len(st.Cams) != 5 {
		t.Fatalf("%+v", st)
	}
	for _, c := range []struct {
		cam string
		ok  bool
		err string
	}{{"cam1", true, ""}, {"cam2", false, "3 tries; the last: go2rtc: no picture (HTTP 500) streams: dial tcp 10.0.0.9:554: i/o timeout; ffmpeg on the stream: "}, {"cam3", true, ""},
		{"cam4", false, "no live stream (disabled, or no address)"}, {"cam5", true, ""}} {
		sc := st.Cams["nvr01/"+c.cam]
		if sc.OK != c.ok || !strings.HasPrefix(sc.Error, c.err) || (c.err == "") != (sc.Error == "") || sc.Tried.Before(before) || (c.ok && !sc.Taken.Equal(sc.Tried)) || (!c.ok && !sc.Taken.IsZero()) {
			t.Errorf("%s: %+v", c.cam, sc)
		}
	}
	if strings.Join(*asked, ",") == "" || strings.Contains(strings.Join(*asked, ","), "cam4") {
		t.Errorf("asked: %v", *asked)
	}
	// scaled down by ffmpeg to 640 wide
	f, _ := os.Open(SnapshotPath(root, "nvr01", "cam1"))
	cfg, err := jpeg.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != SnapWidth || cfg.Height != 360 {
		t.Errorf("scaled: %+v %v", cfg, err)
	}
	if _, err := os.Stat(SnapshotPath(root, "nvr01", "cam2")); err == nil {
		t.Error("no picture of an unreachable camera")
	}

	// cam1 unreachable now: its picture stays, the try is recorded; no ffmpeg: kept as is;
	// a removed camera goes with its picture
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("src") == "nvr01/cam5/lq" {
			w.Write(big)
			return
		}
		w.Write([]byte("<html>not a picture"))
	})
	taken := st.Cams["nvr01/cam1"].Taken
	expect(t, s, `/camera remove [find name=cam3]`, "")
	TakeSnapshots(context.Background(), s, root, "/nonexistent/ffmpeg", logf)
	st = ReadSnapState(root)
	if c1 := st.Cams["nvr01/cam1"]; c1.OK || !c1.Taken.Equal(taken) || !strings.HasPrefix(c1.Error, "3 tries; the last: go2rtc: no picture (HTTP 200)") {
		t.Errorf("cam1: %+v", c1)
	}
	if _, err := os.Stat(SnapshotPath(root, "nvr01", "cam1")); err != nil {
		t.Error("the last picture is kept")
	}
	if b, _ := os.ReadFile(SnapshotPath(root, "nvr01", "cam5")); !bytes.Equal(b, big) {
		t.Error("without ffmpeg the picture is kept as it came")
	}
	if _, ok := st.Cams["nvr01/cam3"]; ok {
		t.Error("a removed camera leaves the state")
	}
	if _, err := os.Stat(SnapshotPath(root, "nvr01", "cam3")); err == nil {
		t.Error("a removed camera's picture goes")
	}

	th := Thumbs(s, root, map[string]bool{"nvr01": true})
	if len(th) != 4 || th[0].Camera != "cam1" || !th[0].Has || th[1].Has || th[0].OK {
		t.Errorf("%+v", th)
	}
	if len(Thumbs(s, root, map[string]bool{})) != 0 {
		t.Error("only the recorders given")
	}

	// ScheduleSnapshots: due now (a new schedule), runs in the background
	expect(t, s, `/system/snapshot set time=04:00`, "")
	ScheduleSnapshots(context.Background(), s, root, "ffmpeg", time.Now(), logf)
	deadline := time.Now().Add(10 * time.Second)
	for ReadSnapState(root).Schedule != "daily 04:00 lq" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	for snapRunning.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ReadSnapState(root).Schedule != "daily 04:00 lq" {
		t.Error("the scheduled run did not happen")
	}
}

func TestCameraSymbolOrder(t *testing.T) {
	if got := CameraSymbol("cam21", "ch01", ""); got != "CAM21 ch01" {
		t.Error(got)
	}
	if got := CameraSymbol("cam24", "ch01", "cam17"); got != "cam17" {
		t.Error(got)
	}
	want := []string{"cam04", "cam05", "cam17", "cam18", "cam19", "CAM21 ch01", "CAM21 ch02", "CAM21 ch10", "cam23"}
	got := append([]string(nil), want...)
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 {
		got[i], got[j] = got[j], got[i]
	}
	sort.SliceStable(got, func(i, j int) bool { return natLess(got[i], got[j]) })
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("\n got %v\nwant %v", got, want)
	}
}

func TestThumbsBySymbol(t *testing.T) {
	s, root := newStore(t), t.TempDir()
	for i, err := range run(t, s,
		`/storage add name=disk1 path=/srv/disk1`,
		`/nvr add type=local name=cam24 storage=disk1`,
		`/nvr add type=local name=cam21 storage=disk1`,
		`/camera add name=ch01 nvr=cam24 alias=cam17 stream=rtsp://a/1`,
		`/camera add name=ch02 nvr=cam21 stream=rtsp://a/2`,
		`/camera add name=ch01 nvr=cam21 stream=rtsp://a/3`,
		`/camera add name=ch02 nvr=cam24 alias=cam19 stream=rtsp://a/4`,
	) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	var got []string
	for _, th := range Thumbs(s, root, map[string]bool{"cam24": true, "cam21": true}) {
		got = append(got, th.Symbol+"="+th.Nvr+"/"+th.Camera)
	}
	if want := "cam17=cam24/ch01,cam19=cam24/ch02,CAM21 ch01=cam21/ch01,CAM21 ch02=cam21/ch02"; strings.Join(got, ",") != want {
		t.Errorf("\n got %s\nwant %s", strings.Join(got, ","), want)
	}
}

// go2rtc's picture fails (an H.265 camera: its ffmpeg cannot make a JPEG): ffmpeg takes
// the first frame of go2rtc's stream.mp4 instead.
func TestSnapshotFallback(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	mp4 := filepath.Join(t.TempDir(), "s.mp4")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=10:duration=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-movflags", "frag_keyframe+empty_moov", mp4).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	video, _ := os.ReadFile(mp4)
	big := testJPEG(t, 1280, 720)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := r.URL.Query().Get("src")
		asked = append(asked, r.URL.Path+"?"+src)
		switch {
		case r.URL.Path == "/api/frame.jpeg" && src == "nvr01/cam1/lq":
			w.Write(big)
		case r.URL.Path == "/api/frame.jpeg":
			w.Header().Set("Content-Type", "text/plain")
			http.Error(w, "exit status 234", 500)
		case r.URL.Path == "/api/stream.mp4" && src == "nvr01/cam2/lq":
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(video)
		default:
			http.Error(w, "streams: dial tcp 10.0.0.9:554: i/o timeout", 500)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	s, root := newStore(t), t.TempDir()
	for i, err := range run(t, s,
		`/storage add name=disk1 path=/srv/disk1`,
		`/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=cam1 nvr=nvr01 stream=rtsp://a/1 stream-lq=rtsp://a/1s`, // go2rtc's picture
		`/camera add name=cam2 nvr=nvr01 stream=rtsp://a/2 stream-lq=rtsp://a/2s`, // H.265: the fallback
		`/camera add name=cam3 nvr=nvr01 stream=rtsp://a/3 stream-lq=rtsp://a/3s`, // neither
		`/system/live set enabled=yes port=`+u.Port(), `/system/snapshot set enabled=yes`,
	) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	defer func(a, b time.Duration) { snapFrameTimeout, snapStreamTimeout = a, b }(snapFrameTimeout, snapStreamTimeout)
	snapFrameTimeout, snapStreamTimeout = 5*time.Second, 5*time.Second
	var logs []string
	TakeSnapshots(context.Background(), s, root, "ffmpeg", func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	st := ReadSnapState(root)
	if c := st.Cams["nvr01/cam1"]; !c.OK || c.Via != "go2rtc" {
		t.Errorf("cam1: %+v", c)
	}
	c2 := st.Cams["nvr01/cam2"]
	if !c2.OK || c2.Via != "ffmpeg" || c2.Error != "" {
		t.Errorf("cam2: %+v", c2)
	}
	f, _ := os.Open(SnapshotPath(root, "nvr01", "cam2"))
	cfg, err := jpeg.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != SnapWidth || cfg.Height != 360 {
		t.Errorf("the fallback's picture, scaled: %+v %v", cfg, err)
	}
	c3 := st.Cams["nvr01/cam3"]
	if c3.OK || !strings.HasPrefix(c3.Error, "3 tries; the last: go2rtc: no picture (HTTP 500) exit status 234; ffmpeg on the stream: ") || len(c3.Error) > 400 {
		t.Errorf("cam3: both reasons: %+v", c3)
	}
	if !strings.Contains(strings.Join(asked, " "), "/api/stream.mp4?nvr01/cam2/lq") || strings.Contains(strings.Join(asked, " "), "/api/stream.mp4?nvr01/cam1/lq") {
		t.Errorf("the stream only when the picture failed: %v", asked)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "3 cameras, 1 not reachable, 1 taken by ffmpeg") {
		t.Errorf("%v", logs)
	}
}

// A camera is tried attempts= times; one that answers the third time has its picture.
func TestSnapshotAttempts(t *testing.T) {
	big := testJPEG(t, 640, 360)
	calls := map[string]int{}
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src := r.URL.Query().Get("src")
		mu.Lock()
		calls[r.URL.Path+" "+src]++
		n := calls["/api/frame.jpeg "+src]
		mu.Unlock()
		if r.URL.Path == "/api/frame.jpeg" && src == "nvr01/late/lq" && n >= 3 {
			w.Write(big)
			return
		}
		http.Error(w, "no", 500)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	s, root := newStore(t), t.TempDir()
	for i, err := range run(t, s,
		`/storage add name=disk1 path=/srv/disk1`, `/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=late nvr=nvr01 stream=rtsp://a/1 stream-lq=rtsp://a/1s`,
		`/camera add name=dead nvr=nvr01 stream=rtsp://a/2 stream-lq=rtsp://a/2s`,
		`/system/live set enabled=yes port=`+u.Port(), `/system/snapshot set enabled=yes`) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	expect(t, s, `/system/snapshot set attempts=0`, `attempts "0": a whole number from 1 to 10`)
	expect(t, s, `/system/snapshot set attempts=11`, `attempts "11": a whole number from 1 to 10`)
	expect(t, s, `/system/snapshot set attempts=x`, `attempts "x": a whole number from 1 to 10`)
	if SnapshotSettings(s).Attempts != 3 {
		t.Error("default 3")
	}
	TakeSnapshots(context.Background(), s, root, "ffmpeg", t.Logf)
	st := ReadSnapState(root)
	if c := st.Cams["nvr01/late"]; !c.OK || c.Tries != 3 || c.Via != "go2rtc" {
		t.Errorf("late: %+v", c)
	}
	if c := st.Cams["nvr01/dead"]; c.OK || c.Tries != 3 || !strings.HasPrefix(c.Error, "3 tries; the last: go2rtc: no picture (HTTP 500)") {
		t.Errorf("dead: %+v", c)
	}
	// every try: go2rtc's picture, then ffmpeg on the stream
	if calls["/api/frame.jpeg nvr01/dead/lq"] != 3 || calls["/api/stream.mp4 nvr01/dead/lq"] != 3 || calls["/api/stream.mp4 nvr01/late/lq"] != 2 {
		t.Errorf("%v", calls)
	}

	// one try: the late camera is not waited for
	expect(t, s, `/system/snapshot set attempts=1`, "")
	calls = map[string]int{}
	TakeSnapshots(context.Background(), s, root, "ffmpeg", t.Logf)
	st = ReadSnapState(root)
	if c := st.Cams["nvr01/late"]; c.OK || c.Tries != 1 || strings.Contains(c.Error, "tries") || c.Via != "go2rtc" {
		t.Errorf("late, one try: %+v", c) // the picture of before stays, with its way
	}
	if calls["/api/frame.jpeg nvr01/dead/lq"] != 1 {
		t.Errorf("%v", calls)
	}
}
