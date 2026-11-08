package dvrip

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/dvrip/dvriptest"
	"ffnvr/internal/h26x"
)

// the requests are written as the XMeye client wrote them in the captured session
func TestCapturedPlayRequests(t *testing.T) {
	f := File{BeginTime: "2026-11-03 05:00:00", EndTime: "2026-11-03 06:00:00",
		FileName: "/idea0/2026-11-03/002/05.00.00-06.00.00[R][@16da0][0].h264"}
	captured := map[string]string{
		"Claim":  `{ "Name" : "OPPlayBack", "OPPlayBack" : { "Action" : "Claim", "EndTime" : "2026-11-03 06:00:00", "Parameter" : { "FileName" : "/idea0/2026-11-03/002/05.00.00-06.00.00[R][@16da0][0].h264", "IntelligentPlayBackEvent" : "", "IntelligentPlayBackSpeed" : 1, "PlayMode" : "ByName", "StreamType" : 0, "TransMode" : "TCP", "Value" : 0 }, "StartTime" : "2026-11-03 05:00:00" }, "SessionID" : "0xe8" }`,
		"Start":  `{ "Name" : "OPPlayBack", "OPPlayBack" : { "Action" : "Start", "EndTime" : "2026-11-03 06:00:00", "Parameter" : { "FileName" : "/idea0/2026-11-03/002/05.00.00-06.00.00[R][@16da0][0].h264", "IntelligentPlayBackEvent" : "", "IntelligentPlayBackSpeed" : 0, "PlayMode" : "ByName", "StreamType" : 0, "TransMode" : "TCP", "Value" : 0 }, "StartTime" : "2026-11-03 05:00:00" }, "SessionID" : "0xe8" }`,
		"Locate": `{ "Name" : "OPPlayBack", "OPPlayBack" : { "Action" : "Locate", "EndTime" : "2026-11-03 06:00:00", "Parameter" : { "FileName" : "/idea0/2026-11-03/002/05.00.00-06.00.00[R][@16da0][0].h264", "IntelligentPlayBackEvent" : "", "IntelligentPlayBackSpeed" : 0, "PlayMode" : "ByName", "StreamType" : 0, "TransMode" : "TCP", "Value" : 0 }, "StartTime" : "2026-11-03 05:28:08" }, "SessionID" : "0xe8" }`,
	}
	for action, want := range captured {
		speed, start := 0, ""
		if action == "Claim" {
			speed = 1
		}
		if action == "Locate" {
			start = "2026-11-03 05:28:08"
		}
		b, _ := json.Marshal(playAction("0xe8", action, speed, f, start))
		if got := string(spaceJSON(b)); got != want {
			t.Errorf("%s:\n got %s\nwant %s", action, got, want)
		}
	}
	// the second capture: the client speeding up to the most
	f = File{BeginTime: "2026-11-05 10:00:00", EndTime: "2026-11-05 10:36:37",
		FileName: "/idea0/2026-11-05/001/10.00.00-10.36.37[R][@1fbb2][1].h264"}
	want := `{ "Name" : "OPPlayBack", "OPPlayBack" : { "Action" : "Fast", "EndTime" : "2026-11-05 10:36:37", "Parameter" : { "FileName" : "/idea0/2026-11-05/001/10.00.00-10.36.37[R][@1fbb2][1].h264", "IntelligentPlayBackEvent" : "", "IntelligentPlayBackSpeed" : 0, "PlayMode" : "ByName", "StreamType" : 0, "TransMode" : "TCP", "Value" : 4 }, "StartTime" : "2026-11-05 10:00:00" }, "SessionID" : "0x2bd" }`
	b, _ := json.Marshal(fastAction("0x2bd", 4, f))
	if got := string(spaceJSON(b)); got != want {
		t.Errorf("Fast:\n got %s\nwant %s", got, want)
	}
}

// the first frame of the captured playing: a key frame of H.265, 12 fps, stamped by a
// camera 1:55:23 behind the recorder (the file of 05:00:00)
func TestCapturedPlayFrame(t *testing.T) {
	b, _ := hex.DecodeString("000001fc030cf0872531c66a67ff00000000000140010c01")
	var got []frame
	d := &deframer{emit: func(f frame) error { got = append(got, f); return nil }}
	d.Write(b)
	d.Flush()
	if len(got) != 1 || got[0].kind != 0xFC || got[0].fps != 12 || b[6] != 1920/8 || b[7] != 1080/8 ||
		!got[0].time.Equal(time.Date(2026, 11, 3, 3, 4, 37, 0, time.Local)) {
		t.Fatalf("%+v", got)
	}
	if n := binary.LittleEndian.Uint32(b[12:16]); n != 65383 {
		t.Errorf("length %d", n)
	}
	if format, _, _ := h26x.Detect(got[0].payload); format != "hevc" {
		t.Errorf("format %q", format)
	}
}

func playFake(t *testing.T, secs string) (*dvriptest.NVR, *Client, time.Time) {
	t.Helper()
	n := dvriptest.New()
	t.Cleanup(n.Close)
	begin := time.Date(2026, 11, 3, 5, 0, 0, 0, time.Local)
	v := video(t, secs)
	n.Recordings = []dvriptest.Recording{
		{Channel: 1, Begin: begin.Add(-time.Hour), End: begin, Video: v, FPS: 10},
		{Channel: 1, Begin: begin, End: begin.Add(time.Hour), Video: v, FPS: 10},
	}
	c, _ := NewClient(n.Addr(), dvriptest.User, dvriptest.Pass)
	return n, c, begin
}

func readPlay(t *testing.T, c *Client, ch int, from, to time.Time) (string, int, time.Time) {
	t.Helper()
	p, format, fps, err := c.OpenPlay(context.Background(), ch, from, to)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	es, err := io.ReadAll(p)
	if err != nil {
		t.Fatal(err)
	}
	if fps != 10 {
		t.Errorf("fps %v", fps)
	}
	first, ok := p.FirstFrame()
	if !ok {
		t.Error("no first frame")
	}
	return format, countPictures(es), first
}

func TestPlayLocate(t *testing.T) {
	for _, skew := range []time.Duration{0, -(time.Hour + 55*time.Minute + 22*time.Second)} {
		t.Run(fmt.Sprint(skew), func(t *testing.T) {
			n, c, begin := playFake(t, "60")
			n.CameraSkew = skew
			// 05:00:30-05:00:40 of channel 2: located, as the client jumps
			format, pics, first := readPlay(t, c, 2, begin.Add(30*time.Second), begin.Add(40*time.Second))
			if format != "h264" || pics != 100 || !first.Equal(begin.Add(30*time.Second)) {
				t.Errorf("%s, %d pictures, first %v", format, pics, first)
			}
			n.Mu.Lock()
			if got := strings.Join(n.Actions, ","); got != "Start,Pause,Locate 2026-11-03 05:00:30,Continue,Fast 4,Stop" {
				t.Errorf("%s", got)
			}
			n.Actions = nil
			n.Mu.Unlock()
			// near the beginning of the file: played from it, no Locate
			_, pics, first = readPlay(t, c, 2, begin.Add(3*time.Second), begin.Add(5*time.Second))
			if pics != 20 || !first.Equal(begin.Add(3*time.Second)) {
				t.Errorf("%d pictures, first %v", pics, first)
			}
			n.Mu.Lock()
			if got := strings.Join(n.Actions, ","); got != "Start,Fast 4,Stop" {
				t.Errorf("%s", got)
			}
			n.Mu.Unlock()
		})
	}
}

func TestPlayFast(t *testing.T) {
	// a recorder sending at real time: 20 s of a piece take some 2 s at Fast 4 (not 20)
	n, c, begin := playFake(t, "60")
	n.PlaySpeed = 1
	start := time.Now()
	_, pics, _ := readPlay(t, c, 2, begin.Add(20*time.Second), begin.Add(40*time.Second))
	if took := time.Since(start); pics != 200 || took > 5*time.Second {
		t.Errorf("%d pictures in %v", pics, took)
	}
}

func TestPlayEndOfFile(t *testing.T) {
	// a file of 20 s recorded, the piece reaches past its end: what there is
	n, c, begin := playFake(t, "20")
	n.Recordings[1].End = begin.Add(20 * time.Second)
	PlayIdle = time.Second
	defer func() { PlayIdle = 6 * time.Second }()
	_, pics, first := readPlay(t, c, 2, begin.Add(10*time.Second), begin.Add(30*time.Second))
	if pics != 100 || !first.Equal(begin.Add(10*time.Second)) {
		t.Errorf("%d pictures, first %v", pics, first)
	}
	// nothing recorded there
	if _, _, _, err := c.OpenPlay(context.Background(), 2, begin.Add(2*time.Hour), begin.Add(2*time.Hour+10*time.Second)); !errors.Is(err, ErrEmpty) {
		t.Errorf("%v", err)
	}
	if _, _, _, err := c.OpenPlay(context.Background(), 1, begin, begin.Add(10*time.Second)); !errors.Is(err, ErrEmpty) {
		t.Errorf("channel 1: %v", err)
	}
}

func TestPlayStreams(t *testing.T) {
	n, c, begin := playFake(t, "60")
	n.PlaySpeed = 5
	var wg sync.WaitGroup
	for i := 0; i < 7; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			from := begin.Add(time.Duration(10+5*i) * time.Second)
			p, _, _, err := c.OpenPlay(context.Background(), 2, from, from.Add(4*time.Second))
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, p)
			p.Close()
		}(i)
	}
	wg.Wait()
	n.Mu.Lock()
	defer n.Mu.Unlock()
	if n.MaxPlaying > MaxStreams || n.MaxPlaying < 2 {
		t.Errorf("at once %d", n.MaxPlaying)
	}
	if c.Streams() != 0 {
		t.Errorf("streams left: %d", c.Streams())
	}
}
