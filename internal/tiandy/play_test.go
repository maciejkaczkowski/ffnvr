package tiandy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/tiandy/tiandytest"
)

// pictures counts the pictures (NAL units 1 and 5) of an elementary H.264 stream.
func pictures(es []byte) int {
	k := 0
	for _, nal := range nals(es) {
		if t := nal[0] & 0x1f; t == 1 || t == 5 {
			k++
		}
	}
	return k
}

func TestStreamsShareASession(t *testing.T) {
	defer CloseSessions()
	n := tiandytest.New()
	defer n.Close()
	n.Recorded[1] = testVideo(t, "10")
	n.Hold = 300 * time.Millisecond
	c, _ := NewClient(n.Addr(), tiandytest.User, tiandytest.Pass)
	from := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	var wg sync.WaitGroup
	errs := make(chan error, 7)
	for i := 0; i < 7; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _, _, err := c.Open(context.Background(), 1, from.Add(time.Duration(i)*time.Minute), from.Add(time.Duration(i+1)*time.Minute))
			if err != nil {
				errs <- err
				return
			}
			io.Copy(io.Discard, r)
			r.Close()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for i := 0; i < 50; i++ { // the fake reads the last DISCONNECTs meanwhile
		n.Mu.Lock()
		k := len(n.Disconnects)
		n.Mu.Unlock()
		if k == 7 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	n.Mu.Lock()
	defer n.Mu.Unlock()
	if n.Logins != 1 {
		t.Errorf("logins: %d, want one session", n.Logins)
	}
	if n.MaxActive != MaxStreams || len(n.StreamIDs) != 7 {
		t.Errorf("at once: %d, streams %d", n.MaxActive, len(n.StreamIDs))
	}
	// the first four ran together: four virtual channels; all of the session's CMDID
	seen := map[string]bool{}
	for _, id := range n.StreamIDs[:MaxStreams] {
		seen[id] = true
	}
	if len(seen) != MaxStreams || !seen["254"] {
		t.Errorf("virtual channels: %v", n.StreamIDs)
	}
	for _, id := range n.CmdIDs {
		if id != "7777" {
			t.Errorf("CMDID %q", id)
		}
	}
	if len(n.Disconnects) != 7 {
		t.Errorf("disconnects: %v", n.Disconnects)
	}
}

func TestSessionAfterLoss(t *testing.T) {
	defer CloseSessions()
	n := tiandytest.New()
	n.Recorded[1] = testVideo(t, "10")
	c, _ := NewClient(n.Addr(), tiandytest.User, tiandytest.Pass)
	from := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	open := func() error {
		r, _, _, err := c.Open(context.Background(), 1, from, from.Add(time.Minute))
		if err == nil {
			io.Copy(io.Discard, r)
			r.Close()
		}
		return err
	}
	if err := open(); err != nil {
		t.Fatal(err)
	}
	// the idle session ends; the next stream begins another
	old := sessionIdle
	sessionIdle = 50 * time.Millisecond
	defer func() { sessionIdle = old }()
	if err := open(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := open(); err != nil {
		t.Fatal(err)
	}
	n.Mu.Lock()
	logins := n.Logins
	n.Mu.Unlock()
	if logins != 2 {
		t.Errorf("logins: %d", logins)
	}
	// a recorder gone: an error, not a wait
	n.Close()
	CloseSessions()
	c.Attempts = 1
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := open(); err == nil || ctx.Err() != nil {
		t.Errorf("%v", err)
	}
	// wrong password
	n2 := tiandytest.New()
	defer n2.Close()
	bad, _ := NewClient(n2.Addr(), tiandytest.User, "wrong")
	if _, _, _, err := bad.Open(context.Background(), 1, from, from.Add(time.Minute)); !errors.Is(err, ErrLogin) {
		t.Errorf("%v", err)
	}
}

func TestStampsAndRangeEnd(t *testing.T) {
	defer CloseSessions()
	loc := time.Local
	time.Local = time.FixedZone("CET", 3600)
	defer func() { time.Local = loc }()
	n := tiandytest.New()
	defer n.Close()
	video := testVideo(t, "10") // 3 s, 30 pictures
	n.Recorded[2] = video
	n.Stamp = 10
	c, _ := NewClient(n.Addr(), tiandytest.User, tiandytest.Pass)
	from := time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local)
	t0 := time.Now()
	r, _, _, err := c.Open(context.Background(), 2, from, from.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	// the recorder goes on (and does not hang up): the stream ends at the range's end
	if p := pictures(got); p != 10 || time.Since(t0) > 5*time.Second {
		t.Errorf("pictures %d in %v", p, time.Since(t0))
	}
	// the wall clock as UTC: the midnight of the capture
	if want := fmt.Sprintf("TM:%d:%d:1", int64(1792540800), int64(1792540801)); n.Connects[0] != want {
		t.Errorf("%s, want %s", n.Connects[0], want)
	}
	if st, ok := StreamStart(got); !ok || !st.Equal(from) {
		t.Errorf("start %v %v", st, ok)
	}
	// a stamp of another clock (an hour off): nothing is cut, the silence ends it
	n.Mu.Lock()
	n.Stamp = 0
	n.Mu.Unlock()
	r, _, _, err = c.Open(context.Background(), 2, from, from.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(r)
	r.Close()
	if p := pictures(got); p != 30 {
		t.Errorf("unstamped: %d pictures", p)
	}
}

func TestStreamStart(t *testing.T) {
	// as captured: the SEI before the first picture, 2026-10-20 23:59:59 on the wall
	sei := []byte{0, 0, 0, 1, 0x4e, 0x01, 0xf0, 0x10, 'T', 'D', 'S', 'T', 1, 0, 8, 0, 0xd3, 0xfe, 4, 0, 0x7f, 0x00, 0xd8, 0x6a, 0x80}
	got, ok := StreamStart(append([]byte{0, 0, 0, 1, 0x4e, 0x01}, sei...))
	if !ok || got.Format("2006-01-02 15:04:05") != "2026-10-20 23:59:59" || got.Location() != time.Local {
		t.Errorf("%v %v", got, ok)
	}
	// emulation prevention bytes inside the stamp
	p := append([]byte("TDST\x01\x00\x08\x00\xd3\xfe\x04\x00"), 0x00, 0x00, 0xd8, 0x6a)
	got, ok = StreamStart(tiandytest.Escape(p))
	if !bytes.Contains(tiandytest.Escape(p), []byte{0, 0, 3}) || !ok || got.Format("15:04:05") != "23:57:52" {
		t.Errorf("escaped: %v %v", got, ok)
	}
	if _, ok := StreamStart([]byte("TDST short")); ok {
		t.Error("short")
	}
}

func TestSearch(t *testing.T) {
	n := tiandytest.New()
	defer n.Close()
	n.Searched = map[int][][3]string{2: {
		{"2026-10-20 22:29:20", "2026-10-21 00:22:55"}, // across midnight
		{"2026-10-21 00:22:57", "2026-10-21 00:23:59"},
		{"2026-10-21 05:00:00", "2026-10-21 06:00:00"},
		{"2026-10-28 10:00:00", "2026-10-28 10:05:00"},
	}}
	n.SearchBusy = 1 // the first answer: "Device Error"
	c, _ := NewClient(n.Addr(), tiandytest.User, tiandytest.Pass)
	c.HTTPPort = n.HTTPPort()
	ctx := context.Background()
	days, err := c.Days(ctx, 2, time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local))
	if err != nil || fmt.Sprint(days) != "[20 21 28]" {
		t.Fatalf("%v %v", days, err)
	}
	recs, err := c.Search(ctx, 2, time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local))
	if err != nil || len(recs) != 3 {
		t.Fatalf("%+v %v", recs, err)
	}
	f := "2006-01-02 15:04:05"
	if recs[0].Start.Format(f) != "2026-10-21 00:00:00" || recs[0].End.Format(f) != "2026-10-21 00:22:55" ||
		recs[2].Start.Format(f) != "2026-10-21 05:00:00" || recs[1].Type != "Alarm" {
		t.Errorf("%+v", recs)
	}
	if n.Searches[0] != "track 2 2026-10" || n.Searches[1] != "day 2026-10-21 chanNo 1" {
		t.Errorf("%v", n.Searches)
	}
	// still busy after the tries: an error
	n.Mu.Lock()
	n.SearchBusy = 5
	n.Mu.Unlock()
	if _, err := c.Search(ctx, 2, time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local)); err == nil {
		t.Error("busy")
	}
	// no web search at all
	c.HTTPPort = 1
	if _, err := c.Days(ctx, 2, time.Now()); err == nil {
		t.Error("no web interface")
	}
}
