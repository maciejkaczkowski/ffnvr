package jobs

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitDone(t *testing.T, sp Spool, w *Worker) {
	t.Helper()
	runWorker(t, w, allFinished(sp))
}

// A remote job: the recorder's stream (here mpegts with mp2 audio instead of dhav, which
// ffmpeg cannot write) is converted to mp4 with AAC audio; an empty block makes no file.
func TestWorkerRemoteRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffmpeg")
	}
	clip, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000", "-t", "6", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10",
		"-pix_fmt", "yuv420p", "-c:a", "mp2", "-f", "mpegts", "-").Output()
	if err != nil {
		t.Skipf("ffmpeg: %v", err)
	}
	out := t.TempDir()
	sp := Spool{Dir: t.TempDir()}
	parts := []Part{
		{Start: t0, End: t0.Add(time.Hour), Target: filepath.Join(out, "a.mp4")},
		{Start: t0.Add(time.Hour), End: t0.Add(2 * time.Hour), Target: filepath.Join(out, "b.mp4")},
	}
	j, _ := sp.Submit(Job{Nvr: "nvr02", Camera: "ch01", Remote: "dahua", Channel: 1, Start: t0, End: t0.Add(2 * time.Hour), Target: out + "/", Parts: parts})
	var asked []string
	remote := func(ctx context.Context, j *Job, p Part) (io.ReadCloser, []string, error) {
		asked = append(asked, p.Start.Format("15:04")+" ch"+strconv.Itoa(j.Channel))
		if p.Start.Equal(t0) {
			return io.NopCloser(bytes.NewReader(clip)), []string{"-f", "mpegts"}, nil
		}
		return nil, nil, ErrEmpty
	}
	waitDone(t, sp, &Worker{Log: quiet, Spool: sp, Poll: 20 * time.Millisecond, Remote: remote})
	j, _ = sp.Get(j.ID)
	if j.State != Done || j.Parts[0].State != "done" || j.Parts[1].State != "empty" || strings.Join(asked, ",") != "08:00 ch1,09:00 ch1" {
		t.Fatalf("%+v %v", j, asked)
	}
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name:format=duration,format_name",
		"-of", "default=nw=1", parts[0].Target).Output()
	if err != nil || !strings.Contains(string(b), "codec_name=h264") || !strings.Contains(string(b), "codec_name=aac") || !strings.Contains(string(b), "mp4") {
		t.Errorf("%v\n%s", err, b)
	}
	if _, err := os.Stat(parts[1].Target); err == nil {
		t.Error("an empty block makes no file")
	}
}

func TestWorkerRemoteErrors(t *testing.T) {
	sp := Spool{Dir: t.TempDir()}
	out := t.TempDir()
	j1, _ := sp.Submit(Job{Nvr: "n", Remote: "dahua", Channel: 1, Start: t0, End: t0.Add(time.Hour), Target: filepath.Join(out, "1.mp4")})
	j2, _ := sp.Submit(Job{Nvr: "m", Remote: "dahua", Channel: 2, Start: t0, End: t0.Add(time.Hour), Target: filepath.Join(out, "2.mp4")})
	var periodic atomic.Int32
	w := &Worker{Log: quiet, Spool: sp, Poll: 20 * time.Millisecond, Every: 10 * time.Millisecond,
		Periodic: func(context.Context) { periodic.Add(1) },
		Remote: func(_ context.Context, j *Job, _ Part) (io.ReadCloser, []string, error) {
			if j.Channel == 1 {
				return nil, nil, ErrEmpty
			}
			return io.NopCloser(strings.NewReader("")), []string{"-f", "dhav"}, nil // the recorder sent nothing
		}}
	waitDone(t, sp, w)
	for _, id := range []int{j1.ID, j2.ID} {
		if j, _ := sp.Get(id); j.State != Failed || !strings.Contains(j.Error, "no recordings between") || !strings.Contains(j.Error, "channel") {
			t.Errorf("%+v", j)
		}
	}
	if periodic.Load() < 1 {
		t.Error("the periodic task runs")
	}
	// a worker without remote access
	j3, _ := sp.Submit(Job{Nvr: "n", Remote: "dahua", Channel: 1, Start: t0, End: t0.Add(time.Hour), Target: filepath.Join(out, "3.mp4")})
	waitDone(t, sp, &Worker{Log: quiet, Spool: sp, Poll: 20 * time.Millisecond})
	if j, _ := sp.Get(j3.ID); j.State != Failed || !strings.Contains(j.Error, "not available") {
		t.Errorf("%+v", j)
	}
}
