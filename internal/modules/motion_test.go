package modules

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/eventdb"
)

func TestRangeParts(t *testing.T) {
	// 900 s in 30 parts of 30 s
	got := rangeParts([]MotionRange{{Start: 28, End: 42}, {Start: 899, End: 905}}, 900, 30)
	if got != "110000000000000000000000000001" {
		t.Error(got)
	}
	if got := rangeParts(nil, 900, 30); got != strings.Repeat("0", 30) {
		t.Error(got)
	}
}

func TestMotionDetectorMenu(t *testing.T) {
	s := newStore(t)
	if d := MotionDetectorSettings(s); d.Mode != "auto" || d.Gap != 3 || d.Skip != 4 || d.Window != 60 || d.PixelWidth != 320 {
		t.Errorf("defaults: %+v", d)
	}
	expect(t, s, `/system/motion-detector set mode=heuristic`, "mode=heuristic needs methods=")
	expect(t, s, `/system/motion-detector set mode=manual`, "mode=manual needs exactly one of methods=")
	expect(t, s, `/system/motion-detector set mode=manual methods=packets,pixels`, "mode=manual needs exactly one")
	expect(t, s, `/system/motion-detector set methods=packets`, "methods: only with mode=heuristic or mode=manual")
	expect(t, s, `/system/motion-detector set methods=frames`, `methods: "frames" is not one of`)
	expect(t, s, `/system/motion-detector set mode=heuristic methods=packets,pixels,packets gap=2.5 skip=0 window=30s pixel-fps=2 ratio-min=1.2`, "")
	it := single(s, "/system/motion-detector")
	if it.Val("methods") != "packets,pixels" || it.Val("gap") != "2.5s" || it.Val("skip") != "0s" || it.Val("window") != "30s" {
		t.Errorf("normalised: %v %v %v %v", it.Val("methods"), it.Val("gap"), it.Val("skip"), it.Val("window"))
	}
	d := MotionDetectorSettings(s)
	if d.Mode != "heuristic" || strings.Join(d.Methods, ",") != "packets,pixels" || d.Gap != 2.5 || d.Skip != 0 || d.Window != 30 || d.PixelFPS != 2 || d.RatioMin != 1.2 {
		t.Errorf("%+v", d)
	}
	expect(t, s, `/system/motion-detector set mode=auto`, "methods: only with mode=heuristic or mode=manual")
	expect(t, s, `/system/motion-detector set mode=auto methods=""`, "")
	expect(t, s, `/system/motion-detector set mode=manual methods=pixels`, "")
	for _, c := range []struct{ set, err string }{
		{"window=1.5", "window \"1.5\": a whole number"},
		{"pixel-diff=300", "pixel-diff \"300\": from 1 to 254"},
		{"pixel-width=abc", "pixel-width \"abc\": a number"},
		{"gap=10m", "gap \"10m\": 0s to 5m"},
		{"noise-k=1 noise-k-low=3", "noise-k-low (3) above noise-k (1)"},
		{"ratio-min=0.5", "ratio-min \"0.5\": from 1 to 10"},
		{"pixel-min-percent=0.5 osd-height=0 max-verify=0 global-percent=40 keyframe-max-gop=8 cbr-flat=4 noise-max=0.2 padding=0 min-duration=500ms", ""},
	} {
		expect(t, s, `/system/motion-detector set `+c.set, c.err)
	}
	if d := MotionDetectorSettings(s); d.MinDuration != 0.5 || d.OSDHeight != 0 || d.MaxVerify != 0 || d.KeyframeMaxGOP != 8 {
		t.Errorf("%+v", d)
	}
	// the bars know the settings they were made with
	before := MotionDetectorSettings(s).Signature()
	expect(t, s, `/system/motion-detector set pixel-fps=3`, "")
	if MotionDetectorSettings(s).Signature() == before {
		t.Error("another setting, another signature")
	}

	// the settings of the older method are gone: refused
	expect(t, s, `/system/settings set motion-threshold=0.02`, "motion-threshold")
}

func TestMotionChoose(t *testing.T) {
	vbr := streamStats{gop: 2, noise: 0.1, flat: 2}      // the packets follow motion
	cbrShort := streamStats{gop: 2, noise: 0.5, flat: 2} // noisy, a short GOP
	cbrLong := streamStats{gop: 10, noise: 0.1, flat: 9} // CBR, a long GOP
	for _, c := range []struct {
		mode, methods   string
		vbr, cbrS, cbrL string
	}{
		{"auto", "", "packets", "keyframes", "keyframes"},
		{"heuristic", "packets,keyframes,pixels", "packets", "keyframes", "pixels"},
		{"heuristic", "packets,keyframes", "packets", "keyframes", "keyframes"},
		{"heuristic", "packets,pixels", "packets", "pixels", "pixels"},
		{"heuristic", "packets", "packets", "packets", "packets"},
		{"heuristic", "keyframes,pixels", "keyframes", "keyframes", "pixels"},
		{"heuristic", "pixels", "pixels", "pixels", "pixels"},
		{"manual", "keyframes", "keyframes", "keyframes", "keyframes"},
		{"manual", "packets", "packets", "packets", "packets"},
	} {
		d := DefaultMotionDetector
		d.Mode = c.mode
		if c.methods != "" {
			d.Methods = strings.Split(c.methods, ",")
		}
		for _, x := range []struct {
			a    streamStats
			want string
		}{{vbr, c.vbr}, {cbrShort, c.cbrS}, {cbrLong, c.cbrL}} {
			if got, _ := d.choose(x.a); got != x.want {
				t.Errorf("%s %s, %+v: %s, want %s", c.mode, c.methods, x.a, got, x.want)
			}
		}
	}
}

// The ranges as ruch_auto.py makes them: a pause > gap ends one, shorter than
// min-duration goes, padding on both sides, overlapping ones merge, the peak kept.
func TestRangesFrom(t *testing.T) {
	d := DefaultMotionDetector
	peaks := map[float64]float64{10: 1.5, 11: 2.5, 12: 1.2, 30: 9, 40: 3, 42: 4}
	got := d.rangesFrom([]float64{10, 11, 12, 30, 40, 42, 45}, 3, 1, 1, peaks)
	s := fmt.Sprint(got) // 30 alone lasts 1 s (the step): as long as min-duration, kept
	if s != "[{8 15 2.5 false} {28 33 9 false} {38 48 4 false}]" {
		t.Error(s)
	}
	// min-duration 2 s: the lone second goes; padding 5 s: the rest merges
	d.Padding = 5
	if s := fmt.Sprint(d.rangesFrom([]float64{10, 11, 12, 30, 40, 42, 45}, 3, 2, 1, peaks)); s != "[{5 18 2.5 false} {35 51 4 false}]" {
		t.Error(s)
	}
	// packets: second 0, the last second and the skipped start do not count
	a := streamStats{secs: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, r: map[int]float64{0: 5, 1: 5, 2: 1, 3: 1, 4: 1, 5: 1.3, 6: 2, 7: 1.3, 8: 1, 9: 5}, noise: 0.01}
	d = DefaultMotionDetector
	if s := fmt.Sprint(d.packetRanges(a)); s != "[{3 10 2 false}]" { // skip 4 s: 1 goes; 5-7 (6 is the seed)
		t.Error(s)
	}
	d.Skip = 0
	if s := fmt.Sprint(d.packetRanges(a)); s != "[{0 10 5 false}]" { // 1 and 5-7 merge with the padding
		t.Error(s)
	}
}

// A segment of a still picture with something moving through it from 20 to 40 s; every
// method marks the middle of the bar.
func TestMotion(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffmpeg/ffprobe")
	}
	s := newStore(t)
	disk, root := t.TempDir(), t.TempDir()
	run(t, s, `/storage add name=disk1 path=`+disk, `/nvr add type=local name=nvr01 storage=disk1`, `/camera add name=cam1 nvr=nvr01 stream=rtsp://h/1`)
	seg := "2026-10-29T08-00-00.mkv"
	p := filepath.Join(disk, "nvr01", "cam1", seg)
	os.MkdirAll(filepath.Dir(p), 0o755)
	out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "smptehdbars=size=320x240:rate=10:duration=60",
		"-f", "lavfi", "-i", "color=c=red:size=40x40:rate=10:duration=60",
		"-filter_complex", `[0:v][1:v]overlay=x='mod(t*60\,280)':y=100:enable='between(t\,20\,40)',format=yuv420p`,
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", p).CombinedOutput()
	if err != nil {
		t.Skipf("%v %s", err, out)
	}
	ctx := context.Background()
	if _, ok := ReadMotion(ctx, s, root, "nvr01", "cam1", seg); ok {
		t.Fatal("not made yet")
	}
	RequestMotion(root, "nvr01", "cam1", seg)
	RequestThumb(root, "nvr01", "cam1", seg)
	MakeThumbs(ctx, s, root, "ffmpeg", 10, t.Logf) // the thumbnail first
	if _, ok := ReadMotion(ctx, s, root, "nvr01", "cam1", seg); ok || !PendingThumbs(root) {
		t.Fatal("thumbnails go first")
	}
	MakeThumbs(ctx, s, root, "ffmpeg", 10, t.Logf)
	// what the detector wrote: the ranges as MotionDetect, the method in their text
	method := func() string {
		db, err := eventdb.Open(eventdb.Path(root))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		evs, _ := db.Query(ctx, eventdb.Filter{Nvr: "nvr01", Camera: "cam1", Kind: eventdb.KindMotion})
		if len(evs) == 0 || evs[0].Source != eventdb.SourceDetector || evs[0].AckedAt.IsZero() {
			t.Fatalf("events: %+v", evs)
		}
		_, m, _ := strings.Cut(strings.TrimSuffix(evs[0].Text, ")"), "(")
		return m
	}
	check := func(what string) {
		t.Helper()
		m, ok := ReadMotion(ctx, s, root, "nvr01", "cam1", seg)
		if !ok || m.Duration < 59 || m.Duration > 61 {
			t.Fatalf("%s: %+v %v", what, m, ok)
		}
		// 2 s a part: motion 20-40 s (with 2 s of padding) is parts 9-20
		still1, moving, still2 := m.Parts[:8], m.Parts[11:19], m.Parts[22:]
		if strings.Contains(still1, "1") || strings.Contains(still2, "1") || strings.Count(moving, "1") != 8 {
			t.Errorf("%s (%s): %s", what, method(), m.Parts)
		}
	}
	check("auto")
	if m := method(); m != MethodPackets && m != MethodKeyframes {
		t.Errorf("auto: %s", m)
	}
	for _, mt := range motionMethods {
		expect(t, s, `/system/motion-detector set mode=manual methods=`+mt, "")
		if _, ok := ReadMotion(ctx, s, root, "nvr01", "cam1", seg); ok {
			t.Fatal("other settings: made again")
		}
		if err := MakeMotion(ctx, s, root, "ffmpeg", "nvr01", "cam1", seg); err != nil {
			t.Fatal(err)
		}
		check(mt)
		if m := method(); m != mt {
			t.Errorf("%s: made by %s", mt, m)
		}
	}
	// made again: the ranges of before are replaced, not added to
	db, _ := eventdb.Open(eventdb.Path(root))
	defer db.Close()
	if n, _ := db.Count(ctx, eventdb.Filter{Camera: "cam1", Kind: eventdb.KindMotion}); n != 1 {
		t.Errorf("%d ranges after %d looks", n, len(motionMethods)+1)
	}
	// the marks are not in the lists of events
	if n, _ := db.Count(ctx, eventdb.Filter{Camera: "cam1"}); n != 1 {
		t.Errorf("the lists show %d events", n)
	}
	if n, _ := db.Count(ctx, eventdb.Filter{Camera: "cam1", Checks: true}); n != 2 {
		t.Errorf("with the marks: %d", n)
	}
	// the player: from the database, the detector not asked
	spans, err := MotionSpans(ctx, s, root, "nvr01", "cam1", time.Date(2026, 10, 29, 8, 0, 0, 0, time.Local), time.Date(2026, 10, 29, 8, 1, 0, 0, time.Local))
	if err != nil || len(spans) != 1 || spans[0].End.Sub(spans[0].Start) < 18*time.Second {
		t.Errorf("spans %v %v", spans, err)
	}
}

// The bars of cameras whose motion is their own alarms (dvrip: MotionDetect, tiandy:
// INNER\ALARM\MOTIONDETECT): from the event database, never asked of the detector; the
// others wait for the detector.
func TestMotionBarsOfAlarms(t *testing.T) {
	s := newStore(t)
	root := t.TempDir()
	run(t, s, `/storage add name=disk1 path=`+t.TempDir(), `/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=xm nvr=nvr01 type=dvrip stream=rtsp://10.0.0.1/x`,
		`/camera add name=td nvr=nvr01 type=tiandy stream=rtsp://10.0.0.2/1/1`,
		`/camera add name=other nvr=nvr01 stream=rtsp://10.0.0.3/x`)
	ctx := context.Background()
	seg := "2026-11-06T10-00-00.mkv"
	start, _ := segmentTime(seg)
	files := []RecFile{{Name: seg, Start: start, End: start.Add(900 * time.Second)}}
	// no database yet: the alarms' cameras have still bars, the detector's wait
	for cam, wantPending := range map[string]bool{"xm": false, "td": false, "other": true} {
		bars, pending, err := MotionBars(ctx, s, root, "nvr01", cam, files)
		if err != nil || (len(pending) == 1) != wantPending || (!wantPending && bars[seg].Parts != strings.Repeat("0", MotionParts)) {
			t.Errorf("%s: %v %v %v", cam, bars, pending, err)
		}
	}
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := eventdb.NewWriter(db, t.TempDir(), t.Logf)
	add := func(cam, kind string, a, b int) {
		w.Add(eventdb.Notice{Nvr: "nvr01", Camera: cam, Kind: kind, Source: "x", Time: start.Add(time.Duration(a) * time.Second), State: eventdb.Start})
		w.Add(eventdb.Notice{Nvr: "nvr01", Camera: cam, Kind: kind, Source: "x", Time: start.Add(time.Duration(b) * time.Second), State: eventdb.Stop})
	}
	add("xm", "MotionDetect", 30, 59)           // part 1
	add("xm", "VideoLoss", 300, 400)            // not motion
	add("td", `INNER\ALARM\MOTIONDETECT`, 0, 5) // part 0
	add("td", "MotionDetect", 600, 700)         // not the motion of a Tiandy camera
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	for cam, want := range map[string]string{"xm": "01" + strings.Repeat("0", 28), "td": "1" + strings.Repeat("0", 29)} {
		bars, pending, err := MotionBars(ctx, s, root, "nvr01", cam, files)
		if err != nil || len(pending) != 0 || bars[seg].Parts != want {
			t.Errorf("%s: %+v %v %v", cam, bars, pending, err)
		}
		if err := MakeMotion(ctx, s, root, "ffmpeg", "nvr01", cam, seg); err != nil { // nothing to do: no detector
			t.Errorf("%s: %v", cam, err)
		}
	}
	if n, _ := db.Count(ctx, eventdb.Filter{Kind: eventdb.KindChecked, Checks: true}); n != 0 {
		t.Errorf("the detector looked at a camera of alarms: %d", n)
	}
}
