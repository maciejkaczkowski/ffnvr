package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func at(hm string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-20 "+hm, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func ranges(rs []Range) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Start.Format("15:04")+"-"+r.End.Format("15:04"))
	}
	return strings.Join(out, " ")
}

func TestBlocks(t *testing.T) {
	for _, c := range []struct {
		from, to string
		length   time.Duration
		align    string
		want     string
	}{
		{"11:50", "13:13", time.Hour, AlignClock, "11:50-12:00 12:00-13:00 13:00-13:13"},
		{"11:50", "13:13", time.Hour, AlignStart, "11:50-12:50 12:50-13:13"},
		{"12:00", "14:00", time.Hour, AlignClock, "12:00-13:00 13:00-14:00"},
		{"11:50", "12:40", 15 * time.Minute, AlignClock, "11:50-12:00 12:00-12:15 12:15-12:30 12:30-12:40"},
		{"11:50", "12:40", 0, AlignClock, "11:50-12:40"},
		{"11:50", "11:55", time.Hour, AlignClock, "11:50-11:55"},
		{"09:10", "13:10", 2 * time.Hour, AlignClock, "09:10-10:00 10:00-12:00 12:00-13:10"}, // counted from midnight
		{"22:30", "23:59", 5 * time.Hour, AlignClock, "22:30-23:59"},
	} {
		if got := ranges(Blocks(at(c.from), at(c.to), c.length, c.align)); got != c.want {
			t.Errorf("%s-%s %v %s: %s, want %s", c.from, c.to, c.length, c.align, got, c.want)
		}
	}
	// a clock block never crosses midnight
	got := Blocks(at("22:00"), at("23:00").Add(3*time.Hour), 7*time.Hour, AlignClock)
	if len(got) != 2 || got[0].End.Hour() != 0 {
		t.Errorf("midnight: %v", got)
	}
}

func TestParseLength(t *testing.T) {
	for in, want := range map[string]string{"15m": "15m", "1h": "1h", "90m": "1h30m", "1d": "1d", "1d12h": "1d12h", "3600s": "1h"} {
		d, err := ParseLength(in)
		if err != nil || FormatLength(d) != want {
			t.Errorf("%s: %v %v", in, FormatLength(d), err)
		}
	}
	for _, bad := range []string{"", "x", "30s", "1", "-1h", "1dx"} {
		if _, err := ParseLength(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

// Real ffmpeg: 30 s segments 08:00:00-08:03:00 with 08:01:00-08:01:30 missing, archived
// 08:00:40-08:03:30 in 1 minute blocks aligned to the clock.
func TestWorkerBlocksRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	src, out := t.TempDir(), t.TempDir()
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
	for i := 0; i < 6; i++ {
		if i == 2 {
			continue
		}
		s := base.Add(time.Duration(i) * 30 * time.Second)
		p := filepath.Join(src, s.Format(SegmentLayout)+".mkv")
		cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
			"-t", "30", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", p)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg: %v %s", err, b)
		}
		os.Chtimes(p, s.Add(30*time.Second), s.Add(30*time.Second))
	}
	from, to := base.Add(40*time.Second), base.Add(210*time.Second)
	var parts []Part
	for _, r := range Blocks(from, to, time.Minute, AlignClock) {
		parts = append(parts, Part{Start: r.Start, End: r.End, Target: filepath.Join(out, r.Start.Format("15-04-05")+".mp4")})
	}
	sp := Spool{Dir: t.TempDir()}
	j, _ := sp.Submit(Job{Nvr: "A", Camera: "c", Start: from, End: to, Source: src, Target: out + "/", Parts: parts})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- (&Worker{Log: quiet, Spool: sp, Poll: 20 * time.Millisecond}).Run(ctx) }()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if j, _ = sp.Get(j.ID); j.State.Finished() || time.Now().After(deadline) {
			break
		}
	}
	stop()
	<-done
	if j.State != Done {
		t.Fatalf("%+v", j)
	}
	want := map[string]float64{"08-00-40.mp4": 20, "08-01-00.mp4": 30, "08-02-00.mp4": 60}
	for _, p := range j.Parts {
		name := filepath.Base(p.Target)
		if name == "08-03-00.mp4" {
			if p.State != "empty" {
				t.Errorf("the block after the last recording is empty: %+v", p)
			}
			continue
		}
		b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", p.Target).Output()
		d, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil || p.State != "done" || d < want[name]-1.5 || d > want[name]+1.5 {
			t.Errorf("%s: %v %s %.1f s, want %.0f s", name, err, p.State, d, want[name])
		}
	}
	if j.Size == 0 || j.Count("done") != 3 {
		t.Errorf("%+v", j)
	}
}
