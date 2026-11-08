package dahua

import (
	"context"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
)

func TestClock(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	c, _ := NewClient(fake.Addr(), dahuatest.User, dahuatest.Pass, false)
	ctx := context.Background()
	for _, rpc := range []bool{true, false} { // JSON-RPC, then older firmware: the CGI
		fake.Mu.Lock()
		fake.RPCInfo = rpc
		fake.Time = "2026-10-24 10:00:00"
		fake.NTP = map[string]any{"Enable": true, "Address": "192.168.0.254", "Port": 123, "UpdatePeriod": 60, "TimeZoneDesc": "Warsaw"}
		fake.TimeSets = nil
		fake.Mu.Unlock()
		cl, err := c.ClockInfo(ctx)
		want := NTP{Enabled: true, Server: "192.168.0.254", Port: 123, Interval: 60}
		if err != nil || cl.Time.Format(TimeLayout) != "2026-10-24 10:00:00" || !cl.NTPKnown || cl.NTP != want || cl.Zone != "Warsaw" {
			t.Fatalf("rpc=%v: %+v %v", rpc, cl, err)
		}
		at := time.Date(2026, 11, 5, 10, 3, 17, 0, time.Local)
		if err := c.SetTime(ctx, at); err != nil {
			t.Fatalf("rpc=%v: %v", rpc, err)
		}
		if err := c.SetNTP(ctx, NTP{Enabled: false, Server: "pool.ntp.org", Port: 1123, Interval: 10}); err != nil {
			t.Fatalf("rpc=%v: %v", rpc, err)
		}
		cl, err = c.ClockInfo(ctx)
		if err != nil || cl.Time.Format(TimeLayout) != "2026-11-05 10:03:17" || cl.NTP != (NTP{Server: "pool.ntp.org", Port: 1123, Interval: 10}) {
			t.Errorf("rpc=%v: after: %+v %v", rpc, cl, err)
		}
		fake.Mu.Lock()
		via := map[bool]string{true: "rpc 2026-11-05 10:03:17 tolerance=5", false: "cgi 2026-11-05 10:03:17"}[rpc]
		if strings.Join(fake.TimeSets, "|") != via || fake.NTP["TimeZoneDesc"] != "Warsaw" {
			t.Errorf("rpc=%v: %v %v", rpc, fake.TimeSets, fake.NTP)
		}
		fake.Mu.Unlock()
	}
	fake.RPCInfo = true
	if err := c.SetNTP(ctx, NTP{Enabled: true, Server: "invalid"}); err == nil || !strings.Contains(err.Error(), "ValidateFailed") {
		t.Errorf("refused values: %v", err)
	}
}
