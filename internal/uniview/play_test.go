package uniview

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/uniview/univiewtest"
)

func TestStampOfCapturedPacket(t *testing.T) {
	// the first packet of a playback captured from a recorder's web client (the VPS of
	// H.265 after the extension), the interleaved header ($, channel, length) cut off
	b, _ := hex.DecodeString("906c0001ba880e6acf5b0002abac00036ae94f1c000000000005000040010c01ffff01600000030000030000030000030096ac09")
	p, ok := parseRTP(b)
	if !ok || p.PT != 108 || p.Seq != 1 || p.Stamp != 0x6ae94f1c || p.Payload[0] != 0x40 {
		t.Fatalf("%+v %v", p, ok)
	}
	if got := time.Unix(int64(p.Stamp), 0).UTC().Format(time.RFC3339); got != "2026-11-03T03:03:56Z" {
		t.Errorf("%s", got)
	}
	// another extension: no stamp
	b[12], b[13] = 0xbe, 0xde
	if p, ok := parseRTP(b); !ok || p.Stamp != 0 || p.Payload[0] != 0x40 {
		t.Errorf("%+v %v", p, ok)
	}
}

func TestFirstFrame(t *testing.T) {
	// 20 fps: 9 pictures of second T, then T+1 (as captured): the first was 0.45 s
	// before T+1
	var f firstFrame
	ts := uint32(3129478762)
	for i := 0; i < 30; i++ {
		st := uint32(1793675036)
		if i >= 9 {
			st++
		}
		f.see(ts+uint32(i)*4500, st)
	}
	got, ok := f.get()
	if want := time.Unix(1793675037, 0).Add(-450 * time.Millisecond); !ok || !got.Equal(want) {
		t.Errorf("%v, want %v", got, want)
	}
	var g firstFrame
	g.see(1, 5) // not a time
	if _, ok := g.get(); ok {
		t.Error("1970")
	}
}

func TestDays(t *testing.T) {
	n := univiewtest.New()
	defer n.Close()
	n.Recorded[2] = [2]int64{time.Date(2026, 10, 31, 23, 0, 0, 0, time.Local).Unix(), time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local).Unix()}
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	days, err := c.Days(context.Background(), 2, time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local))
	if err != nil || len(days) != 3 || days[0] != 1 || days[2] != 3 {
		t.Errorf("%v %v", days, err)
	}
	if days, err := c.Days(context.Background(), 1, time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)); err != nil || len(days) != 0 {
		t.Errorf("channel 1: %v %v", days, err)
	}
	if len(n.DaysAsked) != 2 || n.DaysAsked[0] != "2 2026-11" {
		t.Errorf("%v", n.DaysAsked)
	}
	// the other form (a BCS-P-NVR3204-4K-II): asked after the first is refused
	n.OldCalendar = true
	days, err = c.Days(context.Background(), 2, time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local))
	if err != nil || fmt.Sprint(days) != "[1 2 3]" {
		t.Errorf("old form: %v %v", days, err)
	}
	if days, err := c.Days(context.Background(), 2, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)); err != nil || fmt.Sprint(days) != "[31]" {
		t.Errorf("old form, October: %v %v", days, err)
	}
}

func TestOpenPlay(t *testing.T) {
	video := testVideo(t) // 3 s at 10 fps
	n := univiewtest.New()
	defer n.Close()
	n.Video = video
	n.FPS = 10
	n.StampLead = 1 // the key frame 1 s before the range
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	from := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	body, codec, fps, err := c.OpenPlay(context.Background(), 1, from, from.Add(time.Second), 8)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(body)
	body.Close()
	if codec != "H264" || fps != 10 || len(data) == 0 || len(data) >= len(video) {
		t.Errorf("%s %v %d of %d bytes", codec, fps, len(data), len(video))
	}
	// the pictures of the second before and of the range: 20 of 30
	if k := pictures(data); k != 20 {
		t.Errorf("%d pictures, want 20", k)
	}
	if st, ok := body.(interface{ FirstFrame() (time.Time, bool) }).FirstFrame(); !ok || !st.Equal(from.Add(-time.Second)) {
		t.Errorf("first frame %v %v", st, ok)
	}
	n.Mu.Lock()
	defer n.Mu.Unlock()
	if n.SpeedSeen != "8.0" {
		t.Errorf("Speed %q", n.SpeedSeen)
	}
	for _, r := range n.Requests {
		if strings.Contains(r, "/Records?") {
			t.Errorf("searched before playing: %s", r)
		}
	}
}

// pictures counts the coded pictures (slices that begin one) of H.264 Annex B.
func pictures(b []byte) int {
	k := 0
	for i := 0; i+4 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			typ := b[i+3] & 0x1f
			if (typ == 1 || typ == 5) && b[i+4]&0x80 != 0 { // first_mb_in_slice 0
				k++
			}
		}
	}
	return k
}

func TestStreamLimit(t *testing.T) {
	video := testVideo(t)
	n := univiewtest.New()
	defer n.Close()
	n.Video = video
	n.FPS = 10
	n.Hold = 300 * time.Millisecond
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	from := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	var wg sync.WaitGroup
	for i := 0; i < 2*MaxStreams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := from.Add(time.Duration(i) * 30 * time.Second)
			body, _, _, err := c.OpenPlay(context.Background(), 1, b, b.Add(2*time.Second), 0)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, body)
			body.Close()
		}(i)
	}
	wg.Wait()
	n.Mu.Lock()
	defer n.Mu.Unlock()
	if n.MaxActive > MaxStreams || n.MaxActive < 2 || n.Plays != 2*MaxStreams {
		t.Errorf("at once %d, plays %d", n.MaxActive, n.Plays)
	}
	// all given back: waiting is over
	time.Sleep(50 * time.Millisecond)
	if s := c.Streams(); s != 0 {
		t.Errorf("%d streams still taken", s)
	}
	// one waiting for a stream gives up with its context
	var rels []func()
	for i := 0; i < MaxStreams; i++ {
		r, _ := c.stream(context.Background())
		rels = append(rels, r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, _, err := c.OpenPlay(ctx, 1, from, from.Add(time.Second), 0); err == nil {
		t.Error("a fifth stream")
	}
	for _, r := range rels {
		r()
		r() // twice: nothing more
	}
	if s := c.Streams(); s != 0 {
		t.Errorf("%d streams", s)
	}
}
