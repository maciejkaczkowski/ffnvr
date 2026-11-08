package uniview

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/uniview/univiewtest"
)

func TestClock(t *testing.T) {
	// the server in the recorder's zone (UTC+1), as the web interface assumes
	old := localZone
	localZone = func() *time.Location { return time.FixedZone("CET", 3600) }
	defer func() { localZone = old }()
	for _, newer := range []bool{false, true} {
		for _, dst := range []bool{false, true} {
			fake := univiewtest.New()
			fake.DST = dst
			if newer {
				mode := 3
				fake.SyncMode = &mode
			}
			c, _ := NewClient(fake.Addr(), univiewtest.User, univiewtest.Pass, false)
			ctx := context.Background()
			cl, err := c.ClockInfo(ctx)
			if err != nil || cl.Zone != "UTC+01:00" || !cl.NTPKnown || cl.NTP != (NTP{Enabled: true, Server: "192.168.0.254", Port: 123, Interval: 60}) {
				t.Fatalf("newer=%v: %+v %v", newer, cl, err)
			}
			at := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
			if err := c.SetTime(ctx, at); err != nil {
				t.Fatalf("newer=%v dst=%v: %v", newer, dst, err)
			}
			got, _ := c.Time(ctx)
			if got.Sub(at).Abs() > 5*time.Second {
				t.Errorf("newer=%v dst=%v: the clock is %v, want %v (written %v)", newer, dst, got, at, fake.TimeSets)
			}
			// the web interface's way first: the wall time as UTC
			if fake.TimeSets[0] != wallAsUTC(at) || (len(fake.TimeSets) == 2) != dst {
				t.Errorf("newer=%v dst=%v: written %v", newer, dst, fake.TimeSets)
			}
			before := fake.Time
			if err := c.SetNTP(ctx, NTP{Enabled: false, Server: "pool.ntp.org", Interval: 10}); err != nil {
				t.Fatal(err)
			}
			cl, _ = c.ClockInfo(ctx)
			if cl.NTP != (NTP{Server: "pool.ntp.org", Port: 123, Interval: 10}) || fake.Time.Sub(before).Abs() > 5*time.Second {
				t.Errorf("newer=%v dst=%v: %+v; the clock moved %v", newer, dst, cl.NTP, fake.Time.Sub(before))
			}
			if newer && *fake.SyncMode != 0 {
				t.Errorf("mode %d", *fake.SyncMode)
			}
			c.SetNTP(ctx, NTP{Enabled: true, Server: "pool.ntp.org"})
			if cl, _ = c.ClockInfo(ctx); !cl.NTP.Enabled || (newer && *fake.SyncMode != 3) {
				t.Errorf("newer=%v: enabled again: %+v", newer, cl.NTP)
			}
			fake.Close()
		}
	}
}

func TestClockOtherZone(t *testing.T) {
	// a server in another zone than the recorder: the write is corrected
	fake := univiewtest.New()
	defer fake.Close()
	c, _ := NewClient(fake.Addr(), univiewtest.User, univiewtest.Pass, false)
	old := localZone
	localZone = func() *time.Location { return time.FixedZone("X", 5*3600) }
	defer func() { localZone = old }()
	at := time.Now().Truncate(time.Second)
	if err := c.SetTime(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Time(context.Background()); got.Sub(at).Abs() > 5*time.Second || len(fake.TimeSets) != 2 {
		t.Errorf("%v want %v (%v)", got, at, fake.TimeSets)
	}
}

// TestClockByHand: moving the clock by two minutes with NTP on, as captured on a
// recorder with the newer LAPI: SyncMode 0, then TimeNTP without NTPServerInfo.
func TestClockByHand(t *testing.T) {
	old := localZone
	localZone = func() *time.Location { return time.FixedZone("CET", 3600) }
	defer func() { localZone = old }()
	for _, newer := range []bool{true, false} {
		fake := univiewtest.New()
		if newer {
			mode := 3
			fake.SyncMode = &mode
		}
		c, _ := NewClient(fake.Addr(), univiewtest.User, univiewtest.Pass, false)
		ctx := context.Background()
		at := fake.Time.Add(2 * time.Minute).Truncate(time.Second)
		if err := c.SetTime(ctx, at); err != nil {
			t.Fatalf("newer=%v: %v", newer, err)
		}
		if got, _ := c.Time(ctx); got.Sub(at).Abs() > 5*time.Second {
			t.Errorf("newer=%v: the clock is %v, want %v", newer, got, at)
		}
		body := fake.TimeBodies[0]
		want := fmt.Sprintf(`{"TimeZone":15,"DeviceTime":%d,"DateFormat":0,"HourFormat":1}`, wallAsUTC(at))
		if newer && (body != want || *fake.SyncMode != 0 || len(fake.TimeBodies) != 1) {
			t.Errorf("newer: mode %d, written %q, want %q", *fake.SyncMode, fake.TimeBodies, want)
		}
		if !newer && !strings.Contains(body, `"Enabled":0`) {
			t.Errorf("older: %s", body)
		}
		if cl, _ := c.ClockInfo(ctx); cl.NTP.Enabled {
			t.Errorf("newer=%v: NTP should be off", newer)
		}
		fake.Close()
	}
	// a recorder that does not take the time: an error, not a silent second write
	fake := univiewtest.New()
	defer fake.Close()
	fake.IgnoreTime = true
	c, _ := NewClient(fake.Addr(), univiewtest.User, univiewtest.Pass, false)
	err := c.SetTime(context.Background(), fake.Time.Add(2*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "did not take the time") || len(fake.TimeBodies) != 1 {
		t.Errorf("%v %q", err, fake.TimeBodies)
	}
}
