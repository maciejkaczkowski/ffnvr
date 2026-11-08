package uniview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// The clock, as the web interface sets it (captured on an NVR302 with the older LAPI -
// "StatusCode" in the answers - and on one with the newer - "ResponseCode"):
//
//	GET/PUT /LAPI/V1.0/System/TimeNTP {"TimeZone": <index of zoneHours>, "DeviceTime",
//	    "DateFormat", "HourFormat", "NTPServerInfo": {"Enabled": 0|1, "AddressType": 0,
//	    "Address", "Port", "SynchronizeInterval" (minutes)}}
//	GET/PUT /LAPI/V1.0/System/Time/SyncMode {"Mode": 0 all servers, 3 NTP, 6 cloud}
//	    (the newer LAPI; there the mode, not Enabled, chooses NTP)
//
// Setting the clock by hand (the newer LAPI, confirmed on a recorder): SyncMode
// {"Mode": 0}, then TimeNTP without NTPServerInfo; in mode 3 (NTP) the recorder ignores
// DeviceTime. So setting the clock turns NTP off; on the older LAPI (no SyncMode) the
// same is done with NTPServerInfo.Enabled 0.
//
// DeviceTime reads as UTC seconds, but the web interface writes the wall time it shows
// as if it were UTC (the shown time being UTC plus the zone's hours, without daylight
// saving). How the recorder takes that in summer is not known, so SetTime writes it the
// same way, reads the clock back and corrects a write that missed by whole half hours
// (a zone or daylight saving); any other miss is an error.

// zoneHours: the time zones of the TimeZone index (Config.TimeCfg.TimeZoneArray).
var zoneHours = []float64{-12, -11, -10, -9, -8, -7, -6, -5, -4.5, -4, -3.5, -3, -2, -1, 0, 1, 2, 3, 3.5, 4, 4.5, 5, 5.5, 5.75,
	6, 6.5, 7, 8, 9, 9.5, 10, 11, 12, 13}

// NTP are the time server settings of a recorder.
type NTP struct {
	Enabled  bool
	Server   string
	Port     int
	Interval int // minutes
}

// Clock is what a recorder tells about its time.
type Clock struct {
	Time     time.Time
	Zone     string
	NTP      NTP
	NTPKnown bool
}

type timeNTP struct {
	TimeZone      int            `json:"TimeZone"`
	DeviceTime    int64          `json:"DeviceTime"`
	DateFormat    int            `json:"DateFormat"`
	HourFormat    int            `json:"HourFormat"`
	NTPServerInfo map[string]any `json:"NTPServerInfo,omitempty"`
}

func zoneOffset(idx int) time.Duration {
	if idx < 0 || idx >= len(zoneHours) {
		return 0
	}
	return time.Duration(zoneHours[idx] * float64(time.Hour))
}

func zoneName(idx int) string {
	if idx < 0 || idx >= len(zoneHours) {
		return fmt.Sprintf("TimeZone %d", idx)
	}
	min := int(math.Round(zoneHours[idx] * 60))
	sign := "+"
	if min < 0 {
		sign, min = "-", -min
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, min/60, min%60)
}

func numOf(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// syncMode: the newer LAPI's mode (ok false: the recorder has none).
func (c *Client) syncMode(ctx context.Context) (int, bool) {
	var m struct {
		Mode *int `json:"Mode"`
	}
	if _, err := c.Get(ctx, "/LAPI/V1.0/System/Time/SyncMode", nil, &m); err != nil || m.Mode == nil {
		return 0, false
	}
	return *m.Mode, true
}

// ClockInfo reads the clock, the time zone and the NTP settings.
func (c *Client) ClockInfo(ctx context.Context) (Clock, error) {
	var t timeNTP
	if _, err := c.Get(ctx, "/LAPI/V1.0/System/TimeNTP", nil, &t); err != nil {
		return Clock{}, err
	}
	out := Clock{Time: time.Unix(t.DeviceTime, 0), Zone: zoneName(t.TimeZone)}
	if s := t.NTPServerInfo; s != nil {
		out.NTPKnown = true
		out.NTP = NTP{Enabled: numOf(s["Enabled"]) == 1, Server: fmt.Sprint(s["Address"]), Port: numOf(s["Port"]), Interval: numOf(s["SynchronizeInterval"])}
		if s["Address"] == nil {
			out.NTP.Server = ""
		}
		if mode, ok := c.syncMode(ctx); ok && mode != 3 {
			out.NTP.Enabled = false
		}
	}
	return out, nil
}

// put sends JSON to a LAPI path; both forms of the answer are checked.
func (c *Client) put(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := *c.base
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s: HTTP 401: wrong username or password", path)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	var env struct {
		Response struct {
			ResponseCode   int    `json:"ResponseCode"`
			ResponseString string `json:"ResponseString"`
			StatusCode     int    `json:"StatusCode"`
			StatusString   string `json:"StatusString"`
		} `json:"Response"`
	}
	raw = Lenient(raw)
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: not a LAPI answer: %.80q", path, raw)
	}
	if r := env.Response; r.ResponseCode != 0 || r.StatusCode != 0 {
		return fmt.Errorf("%s: %w: %d/%d %s", path, ErrDevice, r.ResponseCode, r.StatusCode, r.ResponseString+r.StatusString)
	}
	return nil
}

// localZone: the zone of the wall times written (this server's; tests change it).
var localZone = func() *time.Location { return time.Local }

// wallAsUTC: the wall time of t (in this server's zone) written as UTC seconds.
func wallAsUTC(t time.Time) int64 {
	l := t.In(localZone())
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC).Unix()
}

// timeTolerance: how far the clock read back may be from the time written.
const timeTolerance = 10 * time.Second

// writeTime puts TimeNTP with the recorder's clock at target, first written the web
// interface's way (written), then checked; a miss by whole half hours (a zone,
// daylight saving) is corrected once, any other miss is an error.
func (c *Client) writeTime(ctx context.Context, t timeNTP, target time.Time, written int64) error {
	check := func(start time.Time) (time.Duration, bool) {
		got, err := c.Time(ctx)
		if err != nil {
			return 0, false // written; the check is a bonus
		}
		return got.Sub(target.Add(time.Since(start))).Round(time.Second), true
	}
	start := time.Now()
	t.DeviceTime = written
	if err := c.put(ctx, "/LAPI/V1.0/System/TimeNTP", t); err != nil {
		return err
	}
	miss, ok := check(start)
	if !ok || miss.Abs() <= timeTolerance {
		return nil
	}
	zone := miss.Round(30 * time.Minute)
	if zone == 0 || (miss-zone).Abs() > timeTolerance {
		return fmt.Errorf("the recorder did not take the time: its clock is %s off", miss)
	}
	start = time.Now()
	t.DeviceTime = written - int64(zone/time.Second)
	if err := c.put(ctx, "/LAPI/V1.0/System/TimeNTP", t); err != nil {
		return err
	}
	if miss, ok := check(start); ok && miss.Abs() > timeTolerance {
		return fmt.Errorf("the recorder's clock is %s off after setting it", miss)
	}
	return nil
}

// SetTime sets the recorder's clock to t (the wall time in time.Local, as the
// recorders are read) the way the web interface does by hand: NTP goes off (see
// above), the zone stays.
func (c *Client) SetTime(ctx context.Context, t time.Time) error {
	mode, hasMode := c.syncMode(ctx)
	if hasMode && mode == 3 {
		if err := c.put(ctx, "/LAPI/V1.0/System/Time/SyncMode", map[string]any{"Mode": 0}); err != nil {
			return err
		}
	}
	var cur timeNTP
	if _, err := c.Get(ctx, "/LAPI/V1.0/System/TimeNTP", nil, &cur); err != nil {
		return err
	}
	if hasMode {
		cur.NTPServerInfo = nil // the manual form sends none
	} else if info := cur.NTPServerInfo; info != nil && numOf(info["Enabled"]) == 1 {
		info["Enabled"] = 0
	}
	return c.writeTime(ctx, cur, t, wallAsUTC(t))
}

// SetNTP changes the NTP settings; the clock stays (written back as read, plus the
// zone's hours, like the web interface does).
func (c *Client) SetNTP(ctx context.Context, n NTP) error {
	mode, hasMode := c.syncMode(ctx)
	if hasMode {
		switch {
		case n.Enabled && mode != 3:
			if err := c.put(ctx, "/LAPI/V1.0/System/Time/SyncMode", map[string]any{"Mode": 3}); err != nil {
				return err
			}
		case !n.Enabled && mode == 3:
			if err := c.put(ctx, "/LAPI/V1.0/System/Time/SyncMode", map[string]any{"Mode": 0}); err != nil {
				return err
			}
		}
	}
	var cur timeNTP
	if _, err := c.Get(ctx, "/LAPI/V1.0/System/TimeNTP", nil, &cur); err != nil {
		return err
	}
	info := cur.NTPServerInfo
	if info == nil {
		info = map[string]any{"AddressType": 0, "Port": 123, "SynchronizeInterval": 60}
	}
	info["Enabled"], info["Address"] = map[bool]int{true: 1, false: 0}[n.Enabled], n.Server
	if n.Port > 0 {
		info["Port"] = n.Port
	}
	if n.Interval > 0 {
		info["SynchronizeInterval"] = n.Interval
	}
	cur.NTPServerInfo = info
	now := time.Unix(cur.DeviceTime, 0)
	return c.writeTime(ctx, cur, now, cur.DeviceTime+int64(zoneOffset(cur.TimeZone)/time.Second))
}
