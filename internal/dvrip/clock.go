package dvrip

import (
	"context"
	"fmt"
	"time"
)

// The clock, as the XMeye/NETsurveillance client sets it (captured on an NBD6808T-PL):
//
//	1450 {"Name": "OPTimeSetting", "OPTimeSetting": "2026-11-06 10:47:08"}
//	1042/1040 NetWork.NetNTP {"Enable", "Server": {"Name": <address>, "Port"}, "UpdatePeriod" (minutes), "TimeZone"}
//	1042 System.TimeZone {"timeMin": -60} (minutes, the sign of JavaScript's getTimezoneOffset)
//	1042 General.Location {"DSTRule": "On"|"Off", ...}

const cmdTimeSetting = 1450 // OPTimeSetting (the id of OPMachine)

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

func (c *Client) session(ctx context.Context, fn func(*conn) error) error {
	cn, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return err
	}
	defer cn.Close()
	return fn(cn)
}

// ClockInfo reads the clock, the time zone and the NTP settings.
func (c *Client) ClockInfo(ctx context.Context) (Clock, error) {
	var out Clock
	err := c.session(ctx, func(cn *conn) error {
		t, err := (&Session{c: cn}).Time()
		if err != nil {
			return fmt.Errorf("OPTimeQuery: %w", err)
		}
		out.Time = t
		if m, _, err := cn.get(cmdConfigGet, "NetWork.NetNTP"); err == nil {
			if v, ok := m["NetWork.NetNTP"].(map[string]any); ok {
				out.NTP, out.NTPKnown = ntpOf(v), true
			}
		}
		if m, _, err := cn.get(cmdConfigGet, "System.TimeZone"); err == nil {
			if v, ok := m["System.TimeZone"].(map[string]any); ok {
				if min, ok := v["timeMin"].(float64); ok {
					out.Zone = utcOffset(-int(min))
				}
			}
		}
		if m, _, err := cn.get(cmdConfigGet, "General.Location"); err == nil && out.Zone != "" {
			if v, ok := m["General.Location"].(map[string]any); ok {
				if r, _ := v["DSTRule"].(string); r != "" {
					out.Zone += ", DST " + r
				}
			}
		}
		return nil
	})
	return out, err
}

// utcOffset writes minutes east of UTC: UTC+01:00.
func utcOffset(min int) string {
	sign := "+"
	if min < 0 {
		sign, min = "-", -min
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, min/60, min%60)
}

func ntpOf(v map[string]any) NTP {
	n := NTP{}
	n.Enabled, _ = v["Enable"].(bool)
	if s, ok := v["Server"].(map[string]any); ok {
		n.Server, _ = s["Name"].(string)
		if p, ok := s["Port"].(float64); ok {
			n.Port = int(p)
		}
	}
	if p, ok := v["UpdatePeriod"].(float64); ok {
		n.Interval = int(p)
	}
	return n
}

// SetTime sets the recorder's clock to the wall time of t (in time.Local).
func (c *Client) SetTime(ctx context.Context, t time.Time) error {
	return c.session(ctx, func(cn *conn) error {
		_, _, err := cn.call(cmdTimeSetting, map[string]any{"Name": "OPTimeSetting",
			"OPTimeSetting": t.In(time.Local).Format(TimeLayout), "SessionID": cn.sessionHex()}, 15*time.Second)
		if err != nil {
			return fmt.Errorf("OPTimeSetting: %w", err)
		}
		return nil
	})
}

// SetNTP changes the NTP settings (the rest of NetWork.NetNTP stays).
func (c *Client) SetNTP(ctx context.Context, n NTP) error {
	return c.session(ctx, func(cn *conn) error {
		m, _, err := cn.get(cmdConfigGet, "NetWork.NetNTP")
		if err != nil {
			return fmt.Errorf("NetWork.NetNTP: %w", err)
		}
		v, ok := m["NetWork.NetNTP"].(map[string]any)
		if !ok {
			return fmt.Errorf("NetWork.NetNTP: no settings in the answer")
		}
		srv, _ := v["Server"].(map[string]any)
		if srv == nil {
			srv = map[string]any{"Address": "0x00000000", "Anonymity": false, "Password": "", "UserName": "", "Port": 123}
		}
		srv["Name"] = n.Server
		if n.Port > 0 {
			srv["Port"] = n.Port
		}
		v["Server"], v["Enable"] = srv, n.Enabled
		if n.Interval > 0 {
			v["UpdatePeriod"] = n.Interval
		}
		_, _, err = cn.call(cmdConfigSet, map[string]any{"Name": "NetWork.NetNTP", "NetWork.NetNTP": v, "SessionID": cn.sessionHex()}, 15*time.Second)
		if err != nil {
			return fmt.Errorf("NetWork.NetNTP: %w", err)
		}
		return nil
	})
}
