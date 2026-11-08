package dvrip

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dvrip/dvriptest"
)

func TestSofiaHashAndJSON(t *testing.T) {
	if h := sofiaHash(""); h != "tlJwpbo6" { // the well known hash of an empty password
		t.Errorf("%q", h)
	}
	got := string(spaceJSON([]byte(`{"Name":"OPFileQuery","T":"10:07:36","A":[1,{"b":2}]}`)))
	if got != `{ "Name" : "OPFileQuery", "T" : "10:07:36", "A" : [1, { "b" : 2 }] }` {
		t.Errorf("%s", got)
	}
	tm := time.Date(2026, 10, 26, 10, 7, 36, 0, time.Local)
	if !sofiaTime(packTime(tm)).Equal(tm) || !sofiaTime(0).IsZero() {
		t.Error("time")
	}
}

func TestInfo(t *testing.T) {
	n := dvriptest.New()
	defer n.Close()
	c, _ := NewClient(n.Addr(), dvriptest.User, dvriptest.Pass)
	s, err := c.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.Device()
	if err != nil || d.HardWare != "NBD6808T-PL" || !strings.HasPrefix(d.SoftWareVersion, "V4.02") || d.SerialNo != "a1b2c3d4e5f6a7b8" || d.DigChannel != 4 {
		t.Errorf("%+v %v", d, err)
	}
	chs, serr, err := s.Channels()
	if err != nil || serr != nil || len(chs) != 4 || chs[0] != (Channel{1, "Brama", "Connected", "720P", "1080P"}) || chs[1].Status != "NoConfig" {
		t.Errorf("%+v %v %v", chs, serr, err)
	}
	parts, err := s.Storage()
	if err != nil || len(parts) != 1 || parts[0].TotalMB != 0xE8DE8 || parts[0].RemainMB != 0x81B4C {
		t.Errorf("%+v %v", parts, err)
	}
	if tm, err := s.Time(); err != nil || time.Since(tm) > time.Minute {
		t.Errorf("%v %v", tm, err)
	}
	n.Status = nil // ChnStatus refused: titles only, state unknown
	if chs, serr, err := s.Channels(); err != nil || serr == nil || len(chs) != 4 || chs[3].Title != "Magazyn" {
		t.Errorf("%+v %v %v", chs, serr, err)
	}
	if _, err := (&Client{addr: n.Addr(), user: "admin", pass: "x"}).Login(context.Background()); !errors.Is(err, ErrLogin) || !strings.Contains(err.Error(), "205") {
		t.Errorf("%v", err)
	}
}

func video(t *testing.T, secs string) []byte {
	b, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", secs, "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	return b
}

func countPictures(es []byte) int {
	n := 0
	for _, nal := range bytes.Split(es, []byte{0, 0, 1}) {
		if nal = bytes.TrimRight(nal, "\x00"); len(nal) > 0 && (nal[0]&0x1f == 1 || nal[0]&0x1f == 5) {
			n++
		}
	}
	return n
}

// Three files of 10 s (the second one starts later: a gap); the range 08:00:05-08:00:33
// takes from the I frame at :05 to the one at :33: 5 + 10 + 3 = 18 s, at 10 fps.
func TestOpen(t *testing.T) {
	n := dvriptest.New()
	defer n.Close()
	v := video(t, "10")
	b := time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)
	n.Recordings = []dvriptest.Recording{
		{Channel: 0, Begin: b, End: b.Add(10 * time.Second), Video: v, FPS: 10},
		{Channel: 0, Begin: b.Add(20 * time.Second), End: b.Add(30 * time.Second), Video: v, FPS: 10},
		{Channel: 0, Begin: b.Add(30 * time.Second), End: b.Add(40 * time.Second), Video: v, FPS: 10},
		{Channel: 1, Begin: b, End: b.Add(10 * time.Second), Video: v, FPS: 10},
	}
	c, _ := NewClient(n.Addr(), dvriptest.User, dvriptest.Pass)
	c.Idle = 300 * time.Millisecond
	r, format, fps, err := c.Open(context.Background(), 1, b.Add(5*time.Second), b.Add(33*time.Second))
	if err != nil || format != "h264" || fps != 10 {
		t.Fatalf("%s %v %v", format, fps, err)
	}
	es, err := io.ReadAll(r)
	r.Close()
	if got := countPictures(es); err != nil || got != 180 {
		t.Errorf("%d pictures, want 180 (%v)", got, err)
	}
	// the claim comes before DownloadStart, on another connection
	var seq []string
	for _, q := range n.Requests {
		if strings.HasPrefix(q, "142") {
			seq = append(seq, q)
		}
	}
	if len(seq) < 3 || seq[0] != "1424 OPPlayBack" || seq[1] != "1420 OPPlayBack" {
		t.Errorf("%v", seq)
	}
	if _, _, _, err := c.Open(context.Background(), 1, b.Add(12*time.Second), b.Add(18*time.Second)); !errors.Is(err, ErrEmpty) {
		t.Errorf("the gap: %v", err)
	}
	if _, _, _, err := c.Open(context.Background(), 3, b, b.Add(time.Hour)); !errors.Is(err, ErrEmpty) {
		t.Errorf("channel 3: %v", err)
	}
}

func TestOpenCancel(t *testing.T) {
	n := dvriptest.New()
	defer n.Close()
	b := time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)
	n.Recordings = []dvriptest.Recording{{Channel: 0, Begin: b, End: b.Add(10 * time.Second), Video: video(t, "10"), FPS: 10}}
	c, _ := NewClient(n.Addr(), dvriptest.User, dvriptest.Pass)
	ctx, cancel := context.WithCancel(context.Background())
	r, _, _, err := c.Open(ctx, 1, b, b.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, r); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling does not stop the download")
	}
}

// An 8 channel NBD6808T-PL answers NetWork.ChnStatus with 32 entries, the last 24 empty:
// only the channels of SystemInfo count.
func TestChannelsLimited(t *testing.T) {
	n := dvriptest.New()
	defer n.Close()
	n.ExtraEmpty = 24
	c, _ := NewClient(n.Addr(), dvriptest.User, dvriptest.Pass)
	s, err := c.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	chs, _, err := s.Channels()
	if err != nil || len(chs) != 4 {
		t.Errorf("%d channels: %+v %v", len(chs), chs, err)
	}
}
