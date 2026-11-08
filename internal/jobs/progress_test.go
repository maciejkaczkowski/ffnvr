package jobs

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPictureCounter(t *testing.T) {
	for _, codec := range []struct{ enc, f string }{{"libx264", "h264"}, {"libx265", "hevc"}} {
		v, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
			"-t", "5", "-c:v", codec.enc, "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", codec.f, "-").Output()
		if err != nil {
			t.Skipf("%s: %v", codec.enc, err)
		}
		for _, chunk := range []int{1, 2, 3, 7, 1000, 1 << 20} {
			pc := &pictureCounter{r: &chunked{bytes.NewReader(v), chunk}, hevc: codec.f == "hevc"}
			out, _ := io.ReadAll(pc)
			if !bytes.Equal(out, v) || pc.Pictures() != 50 {
				t.Errorf("%s in pieces of %d: %d pictures", codec.f, chunk, pc.Pictures())
			}
		}
	}
	if f, fps := rawFormat([]string{"-f", "hevc", "-framerate", "12.5"}); f != "hevc" || fps != 12.5 {
		t.Error(f, fps)
	}
	if f, _ := rawFormat([]string{"-f", "dhav"}); f != "" {
		t.Error(f)
	}
}

type chunked struct {
	r *bytes.Reader
	n int
}

func (c *chunked) Read(p []byte) (int, error) { return c.r.Read(p[:min(len(p), c.n)]) }

// ffmpeg 7 copying a raw stream reports out_time N/A: the counted material counts.
func TestProgressWithoutFFmpegTime(t *testing.T) {
	sp := Spool{Dir: t.TempDir()}
	j, _ := sp.Submit(Job{Nvr: "n"})
	w := &Worker{Spool: sp}
	lines := "out_time_us=N/A\nspeed=N/A\nprogress=continue\n"
	w.readProgress(strings.NewReader(lines), j, 0, time.Hour, func() time.Duration { return 15 * time.Minute })
	if j.Progress != 0.25 {
		t.Errorf("%v", j.Progress)
	}
	// ffmpeg's own time wins when it has one
	w.readProgress(strings.NewReader("out_time_us=1800000000\nspeed=12x\nprogress=continue\n"), j, 0, time.Hour, func() time.Duration { return time.Minute })
	if j.Progress != 0.5 || j.Speed != "12x" {
		t.Errorf("%v %s", j.Progress, j.Speed)
	}
}
