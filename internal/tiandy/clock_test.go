package tiandy

import (
	"context"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/tiandy/tiandytest"
)

const zone = "CST+1:00:00DST01:00:00,M2.0.0/02:00:00,M9.0.0/03:00:00"

func TestClock(t *testing.T) {
	fake := tiandytest.New()
	defer fake.Close()
	fake.Zone, fake.NTPOn = zone, true
	fake.NTPServer, fake.NTPPort, fake.NTPInterval = "192.168.0.254", 123, 60
	c, _ := NewClient(fake.Addr(), tiandytest.User, tiandytest.Pass)
	c.HTTPPort = fake.HTTPPort()
	ctx := context.Background()
	cl, err := c.ClockInfo(ctx)
	if err != nil || time.Since(cl.Time).Abs() > 2*time.Second || cl.Zone != zone || !cl.NTPKnown ||
		cl.NTP != (NTP{Enabled: true, Server: "192.168.0.254", Port: 123, Interval: 60}) {
		t.Fatalf("%+v %v", cl, err)
	}
	// NTP off: the server, then the switch (CommonEnable 45076); the time is not touched
	if err := c.SetNTP(ctx, NTP{Server: "pool.ntp.org"}); err != nil {
		t.Fatal(err)
	}
	if cl, _ = c.ClockInfo(ctx); cl.NTP != (NTP{Server: "pool.ntp.org", Port: 123, Interval: 60}) {
		t.Errorf("off: %+v", cl.NTP)
	}
	if err := c.SetNTP(ctx, NTP{Enabled: true, Server: "pool.ntp.org", Interval: 10}); err != nil {
		t.Fatal(err)
	}
	// setting the clock by hand leaves NTP on
	at := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := c.SetTime(ctx, at); err != nil {
		t.Fatal(err)
	}
	cl, err = c.ClockInfo(ctx)
	if err != nil || cl.Time.Sub(at).Abs() > 2*time.Second || cl.NTP != (NTP{Enabled: true, Server: "pool.ntp.org", Port: 123, Interval: 10}) {
		t.Errorf("after: %+v %v", cl, err)
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	want := []string{"ntp pool.ntp.org:123/60", "switch false", "ntp pool.ntp.org:123/10", "switch true",
		`time manual "` + at.Format("2006-01-02T15:04:05") + `" ` + zone}
	if strings.Join(fake.TimeSets, "\n") != strings.Join(want, "\n") {
		t.Errorf("%q", fake.TimeSets)
	}
	// without the switch's answer the NTP state is unknown
	fake.NoSwitch = true
	fake.Mu.Unlock()
	cl, err = c.ClockInfo(ctx)
	fake.Mu.Lock()
	if err != nil || cl.NTPKnown {
		t.Errorf("no switch: %+v %v", cl, err)
	}
}
