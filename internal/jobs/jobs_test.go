package jobs

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var quiet = log.New(io.Discard, "", 0)

var t0 = time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)

// segment creates an empty segment file named after its start, modified at its end.
func segment(t *testing.T, dir string, start time.Time, length time.Duration) string {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, start.Format(SegmentLayout)+".mkv")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, start.Add(length), start.Add(length))
	return p
}

func TestSegmentsAndConcat(t *testing.T) {
	dir := t.TempDir()
	segment(t, dir, t0, 15*time.Minute)
	segment(t, dir, t0.Add(15*time.Minute), 10*time.Minute) // camera down for 5 minutes after it
	segment(t, dir, t0.Add(30*time.Minute), 15*time.Minute)
	os.WriteFile(filepath.Join(dir, "junk.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "not-a-date.mkv"), nil, 0o644)

	segs, err := Segments(dir, t0.Add(10*time.Minute), t0.Add(35*time.Minute))
	if err != nil || len(segs) != 3 {
		t.Fatalf("%v %v", segs, err)
	}
	if !segs[1].End.Equal(t0.Add(25 * time.Minute)) {
		t.Errorf("a gap ends the segment at its mtime: %v", segs[1].End)
	}
	list, total := concatList(segs, t0.Add(10*time.Minute), t0.Add(35*time.Minute))
	want := "ffconcat version 1.0\nfile '" + segs[0].Path + "'\ninpoint 600.000\nfile '" + segs[1].Path + "'\nfile '" + segs[2].Path + "'\noutpoint 300.000\n"
	if list != want {
		t.Errorf("list:\n%s\nwant\n%s", list, want)
	}
	if total != 20*time.Minute { // 5 + 10 + 5, the gap is not in the file
		t.Errorf("total %v", total)
	}
	if segs, _ := Segments(dir, t0.Add(25*time.Minute), t0.Add(30*time.Minute)); len(segs) != 0 {
		t.Errorf("inside the gap: %v", segs)
	}
}

func TestSpool(t *testing.T) {
	sp := Spool{Dir: t.TempDir()}
	if js, err := sp.List(); err != nil || len(js) != 0 {
		t.Fatal(js, err)
	}
	a, _ := sp.Submit(Job{Nvr: "n", Camera: "a"})
	b, _ := sp.Submit(Job{Nvr: "n", Camera: "b"})
	if a.ID != 1 || b.ID != 2 || a.State != Queued {
		t.Errorf("%+v %+v", a, b)
	}
	if err := sp.Remove(1); err == nil || !strings.Contains(err.Error(), "cancel it first") {
		t.Errorf("remove of a queued job: %v", err)
	}
	if err := sp.Cancel(1); err != nil || !sp.CancelRequested(1) {
		t.Error(err)
	}
	a.State = Done
	sp.Save(a)
	if err := sp.Cancel(1); err == nil {
		t.Error("a finished job cannot be cancelled")
	}
	if err := sp.Remove(1); err != nil || sp.CancelRequested(1) {
		t.Error(err)
	}
	if _, err := sp.Get(1); err == nil {
		t.Error("removed")
	}
	if c, _ := sp.Submit(Job{}); c.ID != 3 {
		t.Errorf("ids are not reused while higher ones exist: %d", c.ID)
	}
}

// fakeFFmpeg logs when it starts and ends (with the output file) and reports progress.
func fakeFFmpeg(t *testing.T, sleep string) (bin, logf string) {
	dir := t.TempDir()
	bin, logf = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "log")
	script := `#!/bin/sh
for out; do :; done
echo "start $(date +%s.%N) $out" >> ` + logf + `
echo out_time_us=1000000; echo speed=10x; echo progress=continue
sleep ` + sleep + `
echo "end $(date +%s.%N) $out" >> ` + logf + `
echo data > "$out"
echo progress=end
`
	os.WriteFile(bin, []byte(script), 0o755)
	return
}

func runWorker(t *testing.T, w *Worker, until func() bool) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for !until() {
		if time.Now().After(deadline) {
			stop()
			<-done
			t.Fatal("timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func allFinished(sp Spool) func() bool {
	return func() bool {
		js, _ := sp.List()
		for _, j := range js {
			if !j.State.Finished() {
				return false
			}
		}
		return true
	}
}

func TestWorkerQueuesPerNvr(t *testing.T) {
	sp := Spool{Dir: t.TempDir()}
	src, out := t.TempDir(), t.TempDir()
	segment(t, src, t0, 15*time.Minute)
	bin, logf := fakeFFmpeg(t, "0.4")
	for _, n := range []string{"A", "A", "B"} {
		sp.Submit(Job{Nvr: n, Camera: "c", Start: t0, End: t0.Add(time.Minute), Source: src,
			Target: filepath.Join(out, fmt.Sprintf("%s-%d.mp4", n, time.Now().UnixNano()))})
	}
	missing, _ := sp.Submit(Job{Nvr: "C", Camera: "c", Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour), Source: src, Target: filepath.Join(out, "x.mp4")})
	runWorker(t, &Worker{Log: quiet, Spool: sp, FFmpeg: bin, Poll: 50 * time.Millisecond}, allFinished(sp))

	js, _ := sp.List()
	for _, j := range js[:3] {
		if j.State != Done || j.Progress != 1 || j.Size == 0 {
			t.Errorf("job %d: %+v", j.ID, j)
		}
	}
	if j, _ := sp.Get(missing.ID); j.State != Failed || !strings.Contains(j.Error, "no recordings between") {
		t.Errorf("no recordings: %+v", j)
	}
	// intervals [start,end] per target
	b, _ := os.ReadFile(logf)
	iv := map[string][2]float64{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		x, _ := strconv.ParseFloat(f[1], 64)
		v := iv[filepath.Base(f[2])]
		if f[0] == "start" {
			v[0] = x
		} else {
			v[1] = x
		}
		iv[filepath.Base(f[2])] = v
	}
	var a, bb [][2]float64
	for k, v := range iv {
		if strings.HasPrefix(k, "A-") {
			a = append(a, v)
		} else {
			bb = append(bb, v)
		}
	}
	overlap := func(x, y [2]float64) bool { return x[0] < y[1] && y[0] < x[1] }
	if len(a) != 2 || len(bb) != 1 {
		t.Fatalf("log: %s", b)
	}
	if overlap(a[0], a[1]) {
		t.Errorf("jobs of one nvr must not overlap: %v", a)
	}
	if !overlap(a[0], bb[0]) && !overlap(a[1], bb[0]) {
		t.Errorf("different nvrs run in parallel: %v %v", a, bb)
	}
}

func TestWorkerCancel(t *testing.T) {
	sp := Spool{Dir: t.TempDir()}
	src, out := t.TempDir(), t.TempDir()
	segment(t, src, t0, 15*time.Minute)
	bin, _ := fakeFFmpeg(t, "30")
	j1, _ := sp.Submit(Job{Nvr: "A", Start: t0, End: t0.Add(time.Minute), Source: src, Target: filepath.Join(out, "1.mp4")})
	j2, _ := sp.Submit(Job{Nvr: "A", Start: t0, End: t0.Add(time.Minute), Source: src, Target: filepath.Join(out, "2.mp4")})
	sp.Cancel(j2.ID) // still queued
	runWorker(t, &Worker{Log: quiet, Spool: sp, FFmpeg: bin, Poll: 50 * time.Millisecond}, func() bool {
		j, _ := sp.Get(j1.ID)
		if j.State == Running && !sp.CancelRequested(j1.ID) {
			sp.Cancel(j1.ID)
		}
		return allFinished(sp)()
	})
	for _, id := range []int{j1.ID, j2.ID} {
		if j, _ := sp.Get(id); j.State != Cancelled {
			t.Errorf("job %d: %s", id, j.State)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "1.mp4.part")); err == nil {
		t.Error("the partial file must be removed")
	}
}

func TestWorkerRequeuesInterrupted(t *testing.T) {
	sp := Spool{Dir: t.TempDir()}
	src, out := t.TempDir(), t.TempDir()
	segment(t, src, t0, 15*time.Minute)
	bin, _ := fakeFFmpeg(t, "0")
	j, _ := sp.Submit(Job{Nvr: "A", Start: t0, End: t0.Add(time.Minute), Source: src, Target: filepath.Join(out, "1.mp4")})
	j.State, j.Progress = Running, 0.5 // a worker died while running it
	sp.Save(j)
	os.WriteFile(j.Target+".part", []byte("half"), 0o644)
	runWorker(t, &Worker{Log: quiet, Spool: sp, FFmpeg: bin, Poll: 50 * time.Millisecond}, allFinished(sp))
	if j, _ := sp.Get(j.ID); j.State != Done {
		t.Errorf("%+v", j)
	}
}

// With the real ffmpeg: segments recorded like the camera units, cut and joined into mp4.
func TestWorkerRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffprobe")
	}
	src, out := t.TempDir(), t.TempDir()
	start := t0
	for i := 0; i < 3; i++ { // three 10 s segments, 1 key frame per second
		p := segment(t, src, start, 10*time.Second)
		cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
			"-t", "10", "-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-pix_fmt", "yuv420p", p)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg cannot encode test segments: %v %s", err, b)
		}
		os.Chtimes(p, start.Add(10*time.Second), start.Add(10*time.Second))
		start = start.Add(10 * time.Second)
	}
	sp := Spool{Dir: t.TempDir()}
	j, _ := sp.Submit(Job{Nvr: "A", Camera: "c", Start: t0.Add(3 * time.Second), End: t0.Add(25 * time.Second), Source: src,
		Target: filepath.Join(out, "case", "a.mp4")})
	runWorker(t, &Worker{Log: quiet, Spool: sp, Poll: 50 * time.Millisecond}, allFinished(sp))
	if j, _ = sp.Get(j.ID); j.State != Done {
		t.Fatalf("%+v", j)
	}
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration,format_name", "-of", "default=nw=1", j.Target).Output()
	if err != nil {
		t.Fatal(err)
	}
	var dur float64
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "duration="); ok {
			dur, _ = strconv.ParseFloat(v, 64)
		}
	}
	if !strings.Contains(string(b), "mp4") || dur < 21 || dur > 23.5 {
		t.Errorf("expected an mp4 of ~22 s:\n%s", b)
	}
}

func TestWorkerResumesParts(t *testing.T) {
	sp := Spool{Dir: t.TempDir()}
	src, out := t.TempDir(), t.TempDir()
	segment(t, src, t0, 15*time.Minute)
	bin, logf := fakeFFmpeg(t, "0")
	parts := []Part{
		{Start: t0, End: t0.Add(5 * time.Minute), Target: filepath.Join(out, "1.mp4"), State: "done", Size: 5},
		{Start: t0.Add(5 * time.Minute), End: t0.Add(10 * time.Minute), Target: filepath.Join(out, "2.mp4")},
	}
	os.WriteFile(parts[0].Target, []byte("done!"), 0o644)
	j, _ := sp.Submit(Job{Nvr: "A", Start: t0, End: t0.Add(10 * time.Minute), Source: src, Target: out + "/", Parts: parts})
	j.State = Running // interrupted after the first part
	sp.Save(j)
	runWorker(t, &Worker{Log: quiet, Spool: sp, FFmpeg: bin, Poll: 50 * time.Millisecond}, allFinished(sp))
	j, _ = sp.Get(j.ID)
	b, _ := os.ReadFile(logf)
	if j.State != Done || j.Count("done") != 2 || strings.Count(string(b), "start ") != 1 || !strings.Contains(string(b), "2.mp4") {
		t.Errorf("only the missing part runs again: %+v\n%s", j, b)
	}
	if j.Size != 5+5 { // "data\n" from the fake ffmpeg
		t.Errorf("size %d", j.Size)
	}
}
