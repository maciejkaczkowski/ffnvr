package uniview

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/uniview/univiewtest"
)

func TestChannelOfCode(t *testing.T) {
	// as captured: two recorders, two forms of code
	for code, want := range map[string]int{"1100010001000001": 1, "11000a000100000a": 10, "1100120001000012": 18,
		"1101000000040004": 4, "200001000e000001": 1, "": 0, "zz": 0} {
		if got := channelOfCode(code); got != want {
			t.Errorf("%q: %d, want %d", code, got, want)
		}
	}
}

func TestSegments(t *testing.T) {
	n := univiewtest.New()
	defer n.Close()
	day := time.Date(2026, 11, 5, 0, 0, 0, 0, time.Local).Unix()
	n.Segments = map[int][][3]int64{2: {{0, day, day + 1000}, {1, day + 1001, day + 1100}, {0, day + 1101, day + 5000}}}
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	ctx := context.Background()
	resources.Lock()
	delete(resources.m, c.Host()) // codes of an earlier test of the same host
	resources.Unlock()
	segs, err := c.Segments(ctx, 2, time.Unix(day, 0), time.Unix(day+86400, 0))
	if err != nil || len(segs) != 3 || segs[1].Type != 1 || segs[1].Begin.Unix() != day+1001 || segs[1].End.Unix() != day+1100 {
		t.Fatalf("%+v %v", segs, err)
	}
	// the cameras' codes asked for once; the day as the web client asks (to 23:59:59)
	c.Segments(ctx, 2, time.Unix(day, 0), time.Unix(day+86400, 0))
	n.Mu.Lock()
	asked := strings.Join(n.CGI, "|")
	n.Mu.Unlock()
	want := "20|81 1100020001000002 " + strconv.FormatInt(day, 10) + " " + strconv.FormatInt(day+86399, 10)
	if !strings.HasPrefix(asked, want+"|81 ") || strings.Count(asked, "20|") != 1 {
		t.Errorf("%s, want %s…", asked, want)
	}
	if code, err := c.ResourceCode(ctx, 4); err != nil || code != "1100040001000004" {
		t.Errorf("%s %v", code, err)
	}
	if _, err := c.ResourceCode(ctx, 9); err == nil {
		t.Error("channel 9 of 4")
	}
	// the handle is ffnvr's LAPI nonce: a stale one is refused, the LAPI session renewed
	// (its new nonce) and the command asked again
	c.auth.Learn([]string{`Digest realm="uniview", nonce="123", qop="auth"`})
	if _, err := c.CGI(ctx, map[string]any{"cmd": 20}); err != nil || c.auth.Nonce() != "2020944489" {
		t.Errorf("a stale handle: %v, nonce %s", err, c.auth.Nonce())
	}
	if _, err := c.CGI(ctx, map[string]any{"cmd": 99}); !errors.Is(err, ErrCGI) {
		t.Errorf("an unknown command: %v", err)
	}
	// a recorder without main-cgi: an error, no events
	n.Mu.Lock()
	n.NoCGI = true
	n.Mu.Unlock()
	resources.Lock()
	delete(resources.m, c.Host())
	resources.Unlock()
	if _, err := c.Segments(ctx, 2, time.Unix(day, 0), time.Unix(day+86400, 0)); err == nil {
		t.Error("no main-cgi")
	}
	resources.Lock()
	delete(resources.m, c.Host())
	resources.Unlock()
}

func TestSegmentsCaptured(t *testing.T) {
	// the answer of a BCS-P-NVR3204-4K-II for IP Camera 18 on 2026-11-05: 32 parts, 16 of
	// them for events (00:29:03-00:31:01 the first, local time)
	b, err := os.ReadFile("testdata/cmd81-bcs.json")
	if err != nil {
		t.Fatal(err)
	}
	n := univiewtest.New()
	defer n.Close()
	n.Answer81 = string(b)
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	resources.Lock()
	delete(resources.m, c.Host())
	resources.Unlock()
	defer func() { resources.Lock(); delete(resources.m, c.Host()); resources.Unlock() }()
	segs, err := c.Segments(context.Background(), 1, time.Unix(1793833200, 0), time.Unix(1793919600, 0))
	if err != nil || len(segs) != 32 {
		t.Fatalf("%d %v", len(segs), err)
	}
	ev := 0
	for _, s := range segs {
		ev += s.Type
	}
	if ev != 16 || segs[1].Type != 1 || segs[1].Begin.Unix() != 1793838543 || segs[1].End.Unix() != 1793838661 {
		t.Errorf("%d events, %+v", ev, segs[1])
	}
}
