package modules

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/eventdb"
)

func TestRecordScheduleMenus(t *testing.T) {
	s := newStore(t)
	run(t, s, `/storage add name=disk1 path=`+t.TempDir(), `/nvr add type=local name=nvr01 storage=disk1`,
		`/nvr add type=dahua name=nvr02 host=h username=a password=b`,
		`/camera add name=cam1 nvr=nvr01 stream=rtsp://10.0.0.1/x`)
	expect(t, s, `/record/day add name=bad spans="16:00-08:00=regular"`, "end must come after")
	expect(t, s, `/record/day add name=bad spans="08:00-12:00=regular,11:00-13:00=alarm"`, "overlap")
	expect(t, s, `/record/day add name=bad spans="08:00-12:00=motion"`, "not one of")
	expect(t, s, `/record/day add name=bad spans="8:00-12:5=regular"`, "HH:MM")
	expect(t, s, `/record/day add name=office default=none spans="8:00-12:00=regular,12:00-16:00=regular,16:00-24:00=alarm"`, "")
	if got := s.Export("/record/day"); !strings.Contains(got, `spans=08:00-16:00=regular,16:00-24:00=alarm`) {
		t.Errorf("normalised: %s", got)
	}
	// the export reads back
	s2 := newStore(t)
	for _, l := range strings.Split(strings.TrimSpace(s.Export("/record/day")), "\n")[1:] {
		if errs := run(t, s2, "/record/day "+l); errs[0] != "" {
			t.Fatalf("%s: %s", l, errs[0])
		}
	}
	if s2.Export("/record/day") != s.Export("/record/day") {
		t.Errorf("round trip:\n%s\n%s", s2.Export("/record/day"), s.Export("/record/day"))
	}
	expect(t, s, `/record/day add name=night default=alarm spans="06:00-22:00=regular"`, "")
	expect(t, s, `/record/schedule add name=w week="mon-fri:nope"`, "no /record/day")
	expect(t, s, `/record/schedule add name=w week="mon-fri:office,fri-sun:night"`, "fri is named twice")
	expect(t, s, `/record/schedule add name=w week="mon-fri:office,sat-sun:night" pre-alarm=5 post-alarm=2m alarm-silence=6h`, "")
	exp := s.Export("/record/schedule")
	for _, want := range []string{"mon=office", "fri=office", "sat=night", "sun=night", "pre-alarm=5s", "post-alarm=2m", "alarm-silence=6h"} {
		if !strings.Contains(exp, want) {
			t.Errorf("%s missing in %s", want, exp)
		}
	}
	if strings.Contains(exp, "week=") {
		t.Errorf("week= is stored as the days: %s", exp)
	}
	expect(t, s, `/record/schedule set w post-alarm=11m`, "0s to 10m")
	expect(t, s, `/record/schedule set w alarm-silence=1m`, "at least 10m")
	expect(t, s, `/event/filter add name=dropvl action=drop kinds=VideoLoss`, "")
	expect(t, s, `/event/filter add name=alarms action=accept kinds="*/ALARM/*,AlarmLocal"`, "")
	expect(t, s, `/record/schedule set w alarm-filters=dropvl`, "action=drop")
	expect(t, s, `/record/schedule set w alarm-filters=alarms`, "")
	// assignment
	expect(t, s, `/nvr set nvr02 record-schedule=w`, "not used by type=dahua")
	expect(t, s, `/nvr set nvr01 record-schedule=nope`, "nope")
	expect(t, s, `/nvr set nvr01 record-schedule=w`, "")
	expect(t, s, `/record/day remove office`, "used")
	expect(t, s, `/record/schedule remove w`, "used")
	cam := find(s, "/camera", "name", "cam1")
	if n, from := CameraSchedule(s, cam); n != "w" || from != "nvr" {
		t.Errorf("%s %s", n, from)
	}
	expect(t, s, `/record/schedule add name=always`, "")
	expect(t, s, `/camera set cam1 record-schedule=always`, "")
	if n, from := CameraSchedule(s, cam); n != "always" || from != "camera" {
		t.Errorf("the camera's own wins: %s %s", n, from)
	}
	out, err := s.Exec(cmd(t, `/record/schedule status w`))
	if err != nil || !strings.Contains(out, "used by no camera") || !strings.Contains(out, "mon  office") {
		t.Errorf("%v\n%s", err, out)
	}
	if got := FormatWeek(find(s, "/record/schedule", "name", "w")); got != "mon-fri:office,sat-sun:night" {
		t.Errorf("%s", got)
	}
}

func TestScheduleModes(t *testing.T) {
	sc := &Schedule{}
	sc.Days[time.Monday] = &DayTemplate{Name: "office", Default: RecNone, Spans: []DaySpan{{8 * 60, 16 * 60, RecRegular}, {16 * 60, 18 * 60, RecAlarm}}}
	mon := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local) // a Monday
	got := sc.Modes(mon.Add(7*time.Hour+50*time.Minute), mon.Add(8*time.Hour+5*time.Minute))
	if len(got) != 2 || got[0].Mode != RecNone || got[1].Mode != RecRegular || !got[1].Start.Equal(mon.Add(8*time.Hour)) {
		t.Errorf("%+v", got)
	}
	// Monday night into Tuesday (no template: regular)
	got = sc.Modes(mon.Add(23*time.Hour), mon.Add(25*time.Hour))
	if len(got) != 2 || got[0].Mode != RecNone || got[1].Mode != RecRegular || !got[1].Start.Equal(mon.AddDate(0, 0, 1)) {
		t.Errorf("%+v", got)
	}
	if !sc.HasAlarm() {
		t.Error("alarm")
	}
	full := sc.Days[time.Monday].Full()
	if FormatDaySpans(full) != "00:00-08:00=none,08:00-16:00=regular,16:00-18:00=alarm,18:00-24:00=none" {
		t.Errorf("%s", FormatDaySpans(full))
	}
}

func TestKeepSpans(t *testing.T) {
	a := time.Date(2026, 11, 2, 16, 0, 0, 0, time.Local)
	at := func(sec int) time.Time { return a.Add(time.Duration(sec) * time.Second) }
	modes := []ModeSpan{{at(0), at(300), RecAlarm}, {at(300), at(600), RecNone}, {at(600), at(900), RecRegular}}
	alarms := []Span{{at(100), at(110)}, {at(115), at(120)}, {at(290), at(400)}}
	got := keepSpans(modes, true, true, alarms, 10*time.Second, 20*time.Second)
	// 90-130 and 105-140 join; 280-300 clipped at the end of the alarm time; regular 600-900
	want := []Span{{at(90), at(140)}, {at(280), at(300)}, {at(600), at(900)}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
			t.Errorf("%d: %v want %v", i, got[i], want[i])
		}
	}
	// not trusted, or a camera without alarms: the alarm time whole
	for _, c := range [][2]bool{{true, false}, {false, true}} {
		got = keepSpans(modes, c[0], c[1], nil, 0, 0)
		if len(got) != 2 || !got[0].Start.Equal(at(0)) || !got[0].End.Equal(at(300)) {
			t.Errorf("%v: %v", c, got)
		}
	}
	if got := keepSpans([]ModeSpan{{at(0), at(900), RecNone}}, true, true, alarms, 0, 0); len(got) != 0 {
		t.Errorf("none: %v", got)
	}
}

// scheduleEnv: a local recorder with a dvrip camera, its segments and alarm server.
type scheduleEnv struct {
	t          *testing.T
	root, disk string
	db         *eventdb.DB
	w          *eventdb.Writer
}

func newScheduleEnv(t *testing.T) *scheduleEnv {
	e := &scheduleEnv{t: t, root: t.TempDir(), disk: t.TempDir()}
	db, err := eventdb.Open(eventdb.Path(e.root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e.db, e.w = db, eventdb.NewWriter(db, t.TempDir(), t.Logf)
	return e
}

func (e *scheduleEnv) seg(cam string, start time.Time, size int) string {
	p := filepath.Join(e.disk, "nvr01", cam, start.Format("2006-01-02T15-04-05")+".mkv")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o644)
	os.Chtimes(p, start.Add(15*time.Minute), start.Add(15*time.Minute))
	return p
}

func (e *scheduleEnv) alarmServer(started, updated time.Time, subs map[string]SubscribeState) {
	if err := WriteAlarmState(e.root, &AlarmState{Started: started, Updated: updated,
		Listeners: map[string]string{"alarm-dvrip": ":15002"}, Subs: subs}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *scheduleEnv) event(cam, kind string, a, b time.Time) {
	e.w.Add(eventdb.Notice{Nvr: "nvr01", Camera: cam, Kind: kind, Source: "dvrip", Time: a, State: eventdb.Start})
	e.w.Add(eventdb.Notice{Nvr: "nvr01", Camera: cam, Kind: kind, Source: "dvrip", Time: b, State: eventdb.Stop})
	if err := e.w.Flush(); err != nil {
		e.t.Fatal(err)
	}
}

func TestApplySchedules(t *testing.T) {
	e := newScheduleEnv(t)
	s := newStore(t)
	run(t, s, `/storage add name=disk1 path=`+e.disk, `/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=xm nvr=nvr01 type=dvrip stream=rtsp://10.0.0.1/x`,
		`/camera add name=plain nvr=nvr01 stream=rtsp://10.0.0.2/x`,
		`/camera add name=td nvr=nvr01 type=tiandy stream=rtsp://10.0.0.3/1/1`,
		`/record/day add name=d default=none spans="08:00-09:00=regular,09:00-10:00=alarm"`,
		`/record/schedule add name=w week="mon-sun:d" pre-alarm=10s post-alarm=20s`,
		`/nvr set nvr01 record-schedule=w`)
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	at := func(h, m, sec int) time.Time {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second)
	}

	var cuts [][]Span
	cutPieces = func(_ context.Context, _, in string, start time.Time, parts []Span, dir string) ([]string, error) {
		cuts = append(cuts, parts)
		var out []string
		for range parts {
			f, _ := os.CreateTemp(dir, ".schedule-*.tmp")
			f.WriteString("pp")
			f.Close()
			out = append(out, f.Name())
		}
		return out, nil
	}
	defer func() { cutPieces = defaultCutPieces }()
	logf := t.Logf

	// the first pass assigns: nothing before it is touched
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(7, 0, 0), logf)
	segs := map[string]string{}
	for _, cam := range []string{"xm", "plain", "td"} {
		for _, st := range []time.Time{at(6, 45, 0), at(7, 45, 0), at(8, 0, 0), at(9, 0, 0), at(9, 15, 0), at(10, 0, 0)} {
			segs[cam+st.Format("1504")] = e.seg(cam, st, 100)
		}
	}
	e.alarmServer(at(6, 0, 0), at(11, 0, 0), map[string]SubscribeState{"nvr01/td": {Connected: false, Error: "refused"}})
	e.event("xm", "MotionDetect", at(9, 5, 0), at(9, 5, 30))
	e.event("xm", "VideoLoss", at(9, 6, 0), at(9, 7, 0)) // no alarm (no alarm-filters)
	e.event("xm", "MotionDetect", at(9, 16, 0), at(9, 16, 5))
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(10, 3, 0), logf) // 9:45 segment is not due yet? it is: 10:00+20s+2m < 10:03
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	for k, want := range map[string]bool{
		"xm0645": true, "xm0745": false, "xm0800": true, "xm0900": false, "xm0915": false, "xm1000": true,
		"plain0745": false, "plain0800": true, "plain0900": true, "plain0915": true, // no alarms: regular
		"td0900": true, "td0915": true, // the subscription is down: kept whole
	} {
		if exists(segs[k]) != want {
			t.Errorf("%s: exists %v, want %v", k, !want, want)
		}
	}
	// the parts of xm: 9:04:50-9:05:50 and 9:15:50-9:16:25 (the 09:00 segment ends 09:15)
	if len(cuts) != 2 || !cuts[0][0].Start.Equal(at(9, 4, 50)) || !cuts[0][0].End.Equal(at(9, 5, 50)) || !cuts[1][0].Start.Equal(at(9, 15, 50)) {
		t.Errorf("%v", cuts)
	}
	piece := filepath.Join(e.disk, "nvr01", "xm", "2026-11-02T09-04-50.mkv")
	if !exists(piece) {
		t.Error("the part is named by its start")
	}
	st := ReadScheduleState(s, "nvr01", "xm")
	if st == nil || st.Cut != 2 || st.Deleted != 1 || st.Kept != 1 || st.Fallback || !st.Done.Equal(at(9, 30, 0)) {
		t.Errorf("%+v", st)
	}
	if td := ReadScheduleState(s, "nvr01", "td"); td == nil || !td.Fallback || !strings.Contains(td.Reason, "refused") {
		t.Errorf("tiandy: %+v", td)
	}
	// the index knows where the part ends
	MaintainLocal(s, at(10, 3, 0), logf)
	files, _ := LocalFiles(s, "nvr01", "xm", at(9, 0, 0), at(10, 0, 0))
	if len(files) < 1 || files[0].Name != "2026-11-02T09-04-50.mkv" || !files[0].End.Equal(at(9, 5, 50)) {
		t.Errorf("%+v", files)
	}
	// nothing is done twice
	cuts = nil
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(10, 4, 0), logf)
	if len(cuts) != 0 || !exists(piece) {
		t.Errorf("again: %v", cuts)
	}
	// the alarm server goes down: alarm time kept whole
	e.alarmServer(at(6, 0, 0), at(9, 40, 0), nil)
	later := e.seg("xm", at(9, 45, 0), 100)
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(10, 5, 0), logf)
	if !exists(later) {
		t.Error("kept whole while the alarm server is down")
	}
	if st := ReadScheduleState(s, "nvr01", "xm"); st == nil || !st.Fallback || !strings.Contains(st.Reason, "not running") {
		t.Errorf("%+v", st)
	}
	n, _ := e.db.Count(context.Background(), eventdb.Filter{Kind: SysRecordSchedule, Origin: "system"})
	if n == 0 {
		t.Error("no entry of the system log")
	}
	// unassigned: the state goes, a new assignment starts anew
	run(t, s, `/nvr unset nvr01 record-schedule`)
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(10, 6, 0), logf)
	if ReadScheduleState(s, "nvr01", "xm") != nil {
		t.Error("state kept")
	}
}

func TestAlarmSilence(t *testing.T) {
	e := newScheduleEnv(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)
	e.alarmServer(now.Add(-48*time.Hour), now, nil)
	src := openAlarmSource(e.root)
	defer src.close()
	ctx := context.Background()
	if ok, why := src.trusted(ctx, "nvr01", "xm", "dvrip", time.Hour, now.Add(-15*time.Minute), now); ok || !strings.Contains(why, "never sent") {
		t.Errorf("%v %s", ok, why)
	}
	e.event("xm", "Heartbeat", now.Add(-3*time.Hour), now.Add(-3*time.Hour+time.Second))
	if ok, why := src.trusted(ctx, "nvr01", "xm", "dvrip", time.Hour, now.Add(-15*time.Minute), now); ok || !strings.Contains(why, "alarm-silence") {
		t.Errorf("%v %s", ok, why)
	}
	if ok, why := src.trusted(ctx, "nvr01", "xm", "dvrip", 0, now.Add(-15*time.Minute), now); !ok {
		t.Errorf("no check: %s", why)
	}
	if ok, _ := src.trusted(ctx, "nvr01", "xm", "dvrip", 4*time.Hour, now.Add(-15*time.Minute), now); !ok {
		t.Error("an event within alarm-silence")
	}
}

func TestIsAlarmFilters(t *testing.T) {
	s := newStore(t)
	run(t, s, `/event/filter add name=a action=accept kinds="*/ALARM/*"`,
		`/record/schedule add name=w alarm-filters=a`)
	sc := ReadSchedule(s, "w")
	for _, c := range []struct {
		typ, kind string
		want      bool
	}{{"dvrip", "MotionDetect", true}, {"tiandy", "MotionDetect", false}, {"tiandy", `INNER\ALARM\MOTIONDETECT`, true},
		{"tiandy", `INNER\ALARM\IOALARM`, true}, {"dvrip", "VideoLoss", false},
		{"dahua", "VideoMotion", true}, {"dahua", "MotionDetect", false}, {"", "VideoMotion", false}} {
		if got := sc.IsAlarm(c.typ, c.kind); got != c.want {
			t.Errorf("%s %s: %v", c.typ, c.kind, got)
		}
	}
}

// TestCutPiecesFFmpeg cuts a real file (skipped without ffmpeg).
func TestCutPiecesFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.mkv")
	if b, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=20:size=160x120:rate=10",
		"-c:v", "libx264", "-g", "10", "-f", "matroska", in).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot make a test file: %v %s", err, b)
	}
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	parts := []Span{{start.Add(2500 * time.Millisecond), start.Add(5 * time.Second)}, {start.Add(12 * time.Second), start.Add(15 * time.Second)}}
	out, err := defaultCutPieces(context.Background(), "ffmpeg", in, start, parts, dir)
	if err != nil || len(out) != 2 {
		t.Fatalf("%v %v", out, err)
	}
	for i, f := range out {
		b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", f).Output()
		if err != nil {
			t.Fatal(err)
		}
		d, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if d < 2 || d > 4.5 { // 2.5 s and 3 s, from the key frame before
			t.Errorf("part %d: %.2f s", i, d)
		}
	}
}

// A Dahua camera follows its alarms (VideoMotion) only when subscribed to: without
// events=subscribe it is like any other camera, alarm recorded as regular, as before.
func TestApplySchedulesDahua(t *testing.T) {
	e := newScheduleEnv(t)
	s := newStore(t)
	run(t, s, `/storage add name=disk1 path=`+e.disk, `/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=dh nvr=nvr01 type=dahua stream=rtsp://10.0.0.4/cam events=subscribe`,
		`/camera add name=dhn nvr=nvr01 type=dahua stream=rtsp://10.0.0.5/cam`,
		`/record/day add name=d default=none spans="08:00-09:00=regular,09:00-10:00=alarm"`,
		`/record/schedule add name=w week="mon-sun:d" pre-alarm=10s post-alarm=20s`,
		`/nvr set nvr01 record-schedule=w`)
	for cam, want := range map[string]bool{"dh": true, "dhn": false} {
		if ok, why := AlarmCapable(s, find(s, "/camera", "name", cam)); ok != want || (!ok && !strings.Contains(why, "events=subscribe")) {
			t.Errorf("%s capable %v (%s), want %v", cam, ok, why, want)
		}
	}
	for cam, want := range map[string]bool{"dh": false, "dhn": true} {
		kinds, detector := MotionKinds(s, find(s, "/camera", "name", cam))
		if detector != want || (!detector && kinds[0] != "VideoMotion") {
			t.Errorf("%s motion: %v %v", cam, kinds, detector)
		}
	}
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	at := func(h, m, sec int) time.Time {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec)*time.Second)
	}
	var cuts [][]Span
	cutPieces = func(_ context.Context, _, in string, start time.Time, parts []Span, dir string) ([]string, error) {
		cuts = append(cuts, parts)
		var out []string
		for range parts {
			f, _ := os.CreateTemp(dir, ".schedule-*.tmp")
			f.WriteString("pp")
			f.Close()
			out = append(out, f.Name())
		}
		return out, nil
	}
	defer func() { cutPieces = defaultCutPieces }()
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(7, 0, 0), t.Logf)
	segs := map[string]string{}
	for _, cam := range []string{"dh", "dhn"} {
		for _, st := range []time.Time{at(9, 0, 0), at(9, 15, 0)} {
			segs[cam+st.Format("1504")] = e.seg(cam, st, 100)
		}
	}
	e.alarmServer(at(6, 0, 0), at(11, 0, 0), map[string]SubscribeState{"nvr01/dh": {Connected: true, Since: at(6, 0, 0)}})
	e.event("dh", "VideoMotion", at(9, 5, 0), at(9, 5, 30))
	e.event("dh", "VideoLoss", at(9, 20, 0), at(9, 21, 0)) // no alarm (no alarm-filters)
	e.event("dhn", "VideoMotion", at(9, 5, 0), at(9, 5, 30))
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(10, 3, 0), t.Logf)
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	for k, want := range map[string]bool{"dh0900": false, "dh0915": false, "dhn0900": true, "dhn0915": true} {
		if exists(segs[k]) != want {
			t.Errorf("%s: exists %v, want %v", k, !want, want)
		}
	}
	if len(cuts) != 1 || !cuts[0][0].Start.Equal(at(9, 4, 50)) || !cuts[0][0].End.Equal(at(9, 5, 50)) {
		t.Errorf("cuts %v", cuts)
	}
	if st := ReadScheduleState(s, "nvr01", "dh"); st == nil || st.Fallback || st.Cut != 1 || st.Deleted != 1 {
		t.Errorf("dh: %+v", st)
	}
	// the subscription breaks: the alarm time is kept whole
	e.alarmServer(at(6, 0, 0), at(11, 0, 0), map[string]SubscribeState{"nvr01/dh": {Connected: false, Error: "HTTP 401"}})
	later := e.seg("dh", at(9, 45, 0), 100)
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(10, 5, 0), t.Logf)
	if st := ReadScheduleState(s, "nvr01", "dh"); !exists(later) || st == nil || !st.Fallback || !strings.Contains(st.Reason, "401") {
		t.Errorf("subscription down: %v %+v", exists(later), st)
	}
}

// The alarm server (or a subscription) that starts within the alarm time is no fault:
// that segment is kept whole, but the state says "waiting" (no alert) and the next whole
// segment follows the alarms again.
func TestScheduleWaiting(t *testing.T) {
	e := newScheduleEnv(t)
	s := newStore(t)
	run(t, s, `/storage add name=disk1 path=`+e.disk, `/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=xm nvr=nvr01 type=dvrip stream=rtsp://10.0.0.1/x`,
		`/record/day add name=d default=alarm`,
		`/record/schedule add name=w week="mon-sun:d" pre-alarm=10s post-alarm=20s`,
		`/nvr set nvr01 record-schedule=w`)
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	cutPieces = func(_ context.Context, _, in string, start time.Time, parts []Span, dir string) ([]string, error) {
		var out []string
		for range parts {
			f, _ := os.CreateTemp(dir, ".schedule-*.tmp")
			f.Close()
			out = append(out, f.Name())
		}
		return out, nil
	}
	defer func() { cutPieces = defaultCutPieces }()
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(8, 0), t.Logf)
	first := e.seg("xm", at(9, 0), 100)
	e.seg("xm", at(9, 15), 100)
	e.alarmServer(at(9, 5), at(9, 18), nil) // restarted within the 09:00 segment
	e.event("xm", "MotionDetect", at(9, 20), at(9, 21))
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(9, 18), t.Logf)
	if _, err := os.Stat(first); err != nil {
		t.Error("the segment of the start is kept whole")
	}
	st := ReadScheduleState(s, "nvr01", "xm")
	if st == nil || !st.Fallback || !st.Waiting || !strings.Contains(st.Reason, "started") {
		t.Fatalf("waiting: %+v", st)
	}
	infos := ScheduleInfos(context.Background(), s, e.root, nil, "", at(9, 18))
	if len(infos) != 1 || infos[0].Trusted || !infos[0].Waiting {
		t.Errorf("info: %+v", infos)
	}
	// the next whole segment: trusted, the waiting is over
	e.alarmServer(at(9, 5), at(9, 40), nil)
	ApplySchedules(context.Background(), s, e.root, "ffmpeg", at(9, 40), t.Logf)
	if st := ReadScheduleState(s, "nvr01", "xm"); st == nil || st.Fallback || st.Waiting {
		t.Errorf("after the next segment: %+v", st)
	}

	// subscriptions: begun within the time - waiting; down - a fault
	src := &alarmSource{st: &AlarmState{Started: at(6, 0), Updated: at(11, 0), Subs: map[string]SubscribeState{
		"nvr01/a": {Connected: true, Since: at(9, 5)},
		"nvr01/b": {Connected: false, Since: at(9, 5), Error: "HTTP 401"},
	}}, db: e.db}
	if ok, _, waiting := src.check(context.Background(), "nvr01", "a", "dahua", 0, at(9, 0), at(9, 15)); ok || !waiting {
		t.Errorf("a: %v %v", ok, waiting)
	}
	if ok, why, waiting := src.check(context.Background(), "nvr01", "b", "dahua", 0, at(9, 0), at(9, 15)); ok || waiting || !strings.Contains(why, "401") {
		t.Errorf("b: %v %v %s", ok, waiting, why)
	}
	// the alarm server not running is a fault, also when it started within the time
	src.st.Started, src.st.Updated = at(9, 5), at(9, 10)
	if _, _, waiting := src.check(context.Background(), "nvr01", "a", "dahua", 0, at(9, 0), at(9, 15)); waiting {
		t.Error("an alarm server that stopped is no waiting")
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{240266000: "229M", 512 << 20: "512M", 3 << 29: "1.5G", 12595: "12.3K", 1 << 30: "1G", 500: "500", 99*(1<<20) + 1<<19: "99.5M"} {
		if got := HumanSize(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
	if FormatSize(3<<29) != "1.5G" {
		t.Error("FormatSize stays exact")
	}
}
