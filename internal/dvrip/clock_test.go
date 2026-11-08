package dvrip

import (
	"context"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dvrip/dvriptest"
)

func TestClock(t *testing.T) {
	fake := dvriptest.New()
	defer fake.Close()
	c, _ := NewClient(fake.Addr(), dvriptest.User, dvriptest.Pass)
	ctx := context.Background()
	cl, err := c.ClockInfo(ctx)
	if err != nil || time.Since(cl.Time).Abs() > 2*time.Second || cl.Zone != "UTC+01:00, DST On" || !cl.NTPKnown ||
		cl.NTP != (NTP{Enabled: true, Server: "192.168.0.254", Port: 123, Interval: 60}) {
		t.Fatalf("%+v %v", cl, err)
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := c.SetTime(ctx, at); err != nil {
		t.Fatal(err)
	}
	if err := c.SetNTP(ctx, NTP{Server: "194.146.251.100", Port: 123, Interval: 30}); err != nil {
		t.Fatal(err)
	}
	cl, err = c.ClockInfo(ctx)
	if err != nil || cl.Time.Sub(at).Abs() > 2*time.Second || cl.NTP != (NTP{Server: "194.146.251.100", Port: 123, Interval: 30}) {
		t.Errorf("after: %+v %v", cl, err)
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if len(fake.TimeSets) != 1 || fake.TimeSets[0] != at.Format(TimeLayout) || fake.NTP["TimeZone"] != float64(13) {
		t.Errorf("%v %v", fake.TimeSets, fake.NTP)
	}
	if !strings.Contains(strings.Join(fake.Requests, ","), "1040 NetWork.NetNTP") {
		t.Errorf("%v", fake.Requests)
	}
}
