package dahua

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The clock of the recorder, as the web interface sets it (captured on firmware
// 4.005, where the calls go wrapped in system.multiSec; plain JSON-RPC takes them too):
//
//	global.setCurrentTime {"time": "2026-11-05 10:03:17", "tolerance": 5}
//	configManager.getConfig / setConfig {"name": "NTP", "table": {"Enable", "Address",
//	    "Port", "UpdatePeriod" (minutes), "TimeZone", "TimeZoneDesc"}}
//
// Older firmware without JSON-RPC: the CGI of the Dahua HTTP API (global.cgi
// setCurrentTime, configManager.cgi getConfig/setConfig name=NTP).

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
	Zone     string // as the recorder names it
	NTP      NTP
	NTPKnown bool // the NTP settings could be read
}

// rpcOrCGI runs fn over JSON-RPC; without it (no login, an unknown method) cgi.
func (c *Client) rpcOrCGI(ctx context.Context, fn func(*RPC) error, cgi func() error) error {
	rpc, err := c.LoginRPC(ctx)
	if err == nil {
		err = fn(rpc)
		rpc.Close()
		if err == nil || !errors.Is(err, ErrDevice) {
			return err
		}
	}
	if cerr := cgi(); cerr != nil {
		if err != nil {
			return fmt.Errorf("%v; CGI: %w", err, cerr)
		}
		return cerr
	}
	return nil
}

// ClockInfo reads the clock, the time zone and the NTP settings.
func (c *Client) ClockInfo(ctx context.Context) (Clock, error) {
	var out Clock
	err := c.rpcOrCGI(ctx, func(r *RPC) error {
		var t struct {
			Time string `json:"time"`
		}
		if _, err := r.Call(ctx, "global.getCurrentTime", nil, &t); err != nil {
			return err
		}
		tm, err := time.ParseInLocation(timeLayoutIn, t.Time, time.Local)
		if err != nil {
			return fmt.Errorf("global.getCurrentTime: %q: %w", t.Time, err)
		}
		out.Time = tm
		var cfg struct {
			Table map[string]any `json:"table"`
		}
		if _, err := r.Call(ctx, "configManager.getConfig", map[string]any{"name": "NTP"}, &cfg); err == nil && cfg.Table != nil {
			out.NTP, out.Zone, out.NTPKnown = ntpOf(cfg.Table), zoneOf(cfg.Table), true
		}
		return nil
	}, func() error {
		tm, err := c.Time(ctx)
		if err != nil {
			return err
		}
		out.Time = tm
		if s, err := c.cgi(ctx, "configManager.cgi", param{"action", "getConfig"}, param{"name", "NTP"}); err == nil {
			tbl := map[string]any{}
			for k, v := range parseKV(s) {
				tbl[strings.TrimPrefix(k, "table.NTP.")] = v
			}
			out.NTP, out.Zone, out.NTPKnown = ntpOf(tbl), zoneOf(tbl), true
		}
		return nil
	})
	return out, err
}

func ntpOf(t map[string]any) NTP {
	n := NTP{Server: str(t["Address"])}
	switch v := t["Enable"].(type) {
	case bool:
		n.Enabled = v
	case string:
		n.Enabled = v == "true"
	}
	n.Port, _ = num(t["Port"])
	n.Interval, _ = num(t["UpdatePeriod"])
	return n
}

func zoneOf(t map[string]any) string {
	if d := str(t["TimeZoneDesc"]); d != "" {
		return d
	}
	if z := str(t["TimeZone"]); z != "" {
		return "TimeZone " + z
	}
	return ""
}

// SetTime sets the recorder's clock to the wall time of t (in time.Local, as the
// recorders are read).
func (c *Client) SetTime(ctx context.Context, t time.Time) error {
	v := t.In(time.Local).Format(TimeLayout)
	return c.rpcOrCGI(ctx, func(r *RPC) error {
		_, err := r.Call(ctx, "global.setCurrentTime", map[string]any{"time": v, "tolerance": 5}, nil)
		return err
	}, func() error {
		_, err := c.cgi(ctx, "global.cgi", param{"action", "setCurrentTime"}, param{"time", v})
		return err
	})
}

// SetNTP changes the NTP settings (the rest of the NTP table stays).
func (c *Client) SetNTP(ctx context.Context, n NTP) error {
	return c.rpcOrCGI(ctx, func(r *RPC) error {
		var cfg struct {
			Table map[string]any `json:"table"`
		}
		if _, err := r.Call(ctx, "configManager.getConfig", map[string]any{"name": "NTP"}, &cfg); err != nil {
			return err
		}
		if cfg.Table == nil {
			cfg.Table = map[string]any{}
		}
		cfg.Table["Enable"], cfg.Table["Address"] = n.Enabled, n.Server
		if n.Port > 0 {
			cfg.Table["Port"] = n.Port
		}
		if n.Interval > 0 {
			cfg.Table["UpdatePeriod"] = n.Interval
		}
		raw, err := r.Call(ctx, "configManager.setConfig", map[string]any{"name": "NTP", "table": cfg.Table}, nil)
		if err != nil {
			return err
		}
		return validated(raw)
	}, func() error {
		ps := []param{{"action", "setConfig"}, {"NTP.Enable", strconv.FormatBool(n.Enabled)}, {"NTP.Address", n.Server}}
		if n.Port > 0 {
			ps = append(ps, param{"NTP.Port", strconv.Itoa(n.Port)})
		}
		if n.Interval > 0 {
			ps = append(ps, param{"NTP.UpdatePeriod", strconv.Itoa(n.Interval)})
		}
		_, err := c.cgi(ctx, "configManager.cgi", ps...)
		return err
	})
}

// validated: setConfig may answer true and still refuse a value
// ("options": ["ValidateFailed"], as the web interface checks).
func validated(raw []byte) error {
	var r struct {
		Params struct {
			Options any `json:"options"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &r) == nil && strings.Contains(fmt.Sprint(r.Params.Options), "ValidateFailed") {
		return fmt.Errorf("configManager.setConfig NTP: the recorder refused the values (ValidateFailed)")
	}
	return nil
}
