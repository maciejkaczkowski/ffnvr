package tiandy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/tiandy/tiandytest"
)

func testVideo(t *testing.T, fps string) []byte {
	t.Helper()
	b, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate="+fps,
		"-t", "3", "-c:v", "libx264", "-preset", "ultrafast", "-g", fps, "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	return b
}

func TestInfo(t *testing.T) {
	n := tiandytest.New()
	defer n.Close()
	c, _ := NewClient(n.Addr(), tiandytest.User, tiandytest.Pass)
	in, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if in.Model != "TC-R3120 SPEC:I/B/V3.0" || in.Serial != "482608C3387A00255" || in.Firmware != "DVRS_V13.13.20.20250102_SP01" || !in.OnlineKnown {
		t.Errorf("%+v", in)
	}
	want := []Channel{
		{1, "Site16", "192.168.0.80", true, true},
		{2, "Plac żółty", "192.168.0.78", true, true},
		{3, "Channel 3", "192.168.0.79", true, false},
		{4, "Channel 4", "", false, false},
	}
	if len(in.Channels) != 4 {
		t.Fatalf("%+v", in.Channels)
	}
	for i, ch := range want {
		if in.Channels[i] != ch {
			t.Errorf("channel %d: %+v, want %+v", i+1, in.Channels[i], ch)
		}
	}
	bad, _ := NewClient(n.Addr(), tiandytest.User, "wrong")
	if _, err := bad.Info(context.Background()); !errors.Is(err, ErrLogin) {
		t.Errorf("%v", err)
	}
	if c, _ := NewClient("10.0.0.1", "a", "b"); c.addr != "10.0.0.1:3001" {
		t.Error(c.addr)
	}
}

func TestDemux(t *testing.T) {
	video := []byte{0, 0, 0, 1, 0x67, 0x88, 0, 0, 0, 1, 0x68, 0x99, 0, 0, 0, 1, 0x65, 0x77, 0, 0, 0, 1, 0x41, 0x55}
	sdv := tiandytest.SDV(video)
	if got := DemuxSDV(sdv); !bytes.Equal(got, video) {
		t.Fatalf("% x", got)
	}
	// the same, arriving in pieces of any size: magics split between them
	for _, size := range []int{1, 3, 5, 31, 1000} {
		var out bytes.Buffer
		d := &sdvDemuxer{w: &out}
		for i := 0; i < len(sdv); i += size {
			d.Write(sdv[i:min(i+size, len(sdv))])
		}
		d.Close()
		if !bytes.Equal(out.Bytes(), video) {
			t.Errorf("pieces of %d: % x", size, out.Bytes())
		}
	}
}

func nals(es []byte) [][]byte {
	var out [][]byte
	for _, nal := range bytes.Split(es, []byte{0, 0, 1}) {
		if nal = bytes.TrimRight(nal, "\x00"); len(nal) > 0 {
			out = append(out, nal)
		}
	}
	return out
}

func TestOpen(t *testing.T) {
	n := tiandytest.New()
	defer n.Close()
	video := testVideo(t, "10")
	n.Recorded[2] = video
	n.SilentConnects = 1 // the first try stays silent: the client tries again
	c, _ := NewClient(n.Addr(), tiandytest.User, tiandytest.Pass)
	c.FirstData = 500 * time.Millisecond
	from := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	r, format, fps, err := c.Open(context.Background(), 2, from, from.Add(5*time.Minute))
	if err != nil || format != "h264" || fps != 10 {
		t.Fatalf("%s %v %v", format, fps, err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if g, w := nals(got), nals(video); len(g) != len(w) || !bytes.Equal(bytes.Join(g, nil), bytes.Join(w, nil)) {
		t.Errorf("the NAL units differ: %d, want %d", len(g), len(w))
	}
	if len(n.Connects) != 2 || n.Connects[1] != "TM:"+itoa(wallUnix(from))+":"+itoa(wallUnix(from.Add(5*time.Minute)))+":1" {
		t.Errorf("%v", n.Connects)
	}
	// a configured rate wins; a channel without recordings is empty
	c.FPS = 12.5
	r, _, fps, err = c.Open(context.Background(), 2, from, from.Add(time.Minute))
	if err != nil || fps != 12.5 {
		t.Errorf("%v %v", fps, err)
	} else {
		r.Close()
	}
	c.Attempts = 2
	if _, _, _, err := c.Open(context.Background(), 1, from, from.Add(time.Minute)); !errors.Is(err, ErrEmpty) {
		t.Errorf("%v", err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestISAPI(t *testing.T) {
	n := tiandytest.New()
	defer n.Close()
	c, _ := NewClient(n.Addr(), tiandytest.User, tiandytest.Pass)
	c.HTTPPort = n.HTTPPort()
	ctx := context.Background()
	hdds, err := c.HDDs(ctx)
	if err != nil || len(hdds) != 1 || hdds[0].Capacity != 7577600 || hdds[0].FreeSpace != 46080 || hdds[0].Failed() || !hdds[0].Working() || hdds[0].Status != "formating" {
		t.Errorf("%+v %v", hdds, err)
	}
	chs, err := c.ChannelStatus(ctx)
	if err != nil || len(chs) != 3 || chs[0].Source.IPAddress != "192.168.0.80" || !chs[0].Online || chs[2].Online || chs[0].Source.DevModel != "TC-C34WS SPEC:I5/E/Y/2.8mm" || chs[0].Source.Barcode != "480677C0466A00153" {
		t.Errorf("%+v %v", chs, err)
	}
	if tm, err := c.Time(ctx); err != nil || time.Since(tm) > time.Minute {
		t.Errorf("%v %v", tm, err)
	}
	bad, _ := NewClient(n.Addr(), tiandytest.User, "wrong")
	bad.HTTPPort = n.HTTPPort()
	if _, err := bad.HDDs(ctx); err == nil || !strings.Contains(err.Error(), "wrong username or password") {
		t.Errorf("%v", err)
	}
}
