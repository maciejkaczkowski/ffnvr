package modules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/dahua"
	"ffnvr/internal/dvrip"
	"ffnvr/internal/tiandy"
	"ffnvr/internal/uniview"
)

// The clock of remote recorders: reading it with the time zone and the NTP settings,
// setting it (to this server's time or a given one) and changing the NTP settings. The
// recorders' times are wall times of this server's zone, as everywhere in ffnvr.

// NTPSettings are the time server settings of a recorder.
type NTPSettings struct {
	Enabled  bool
	Server   string
	Port     int // 0: unchanged
	Interval int // minutes; 0: unchanged
}

// ClockInfo is what a recorder tells about its time.
type ClockInfo struct {
	Time     time.Time // the recorder's clock when read
	Read     time.Time // this server's clock then
	Zone     string    // as the recorder names it
	NTP      NTPSettings
	NTPKnown bool // the NTP settings could be read
}

// Offset: how far the recorder's clock is ahead of this server's (the recorders tell
// whole seconds: half a second is added).
func (c ClockInfo) Offset() time.Duration {
	return c.Time.Add(500 * time.Millisecond).Sub(c.Read).Round(time.Second)
}

// clocker: a recorder whose clock ffnvr can set.
type clocker interface {
	ClockInfo(ctx context.Context) (ClockInfo, error)
	SetTime(ctx context.Context, t time.Time) error
	SetNTP(ctx context.Context, n NTPSettings) error
}

// clockTypes: the types of recorders whose clock can be set.
var clockTypes = map[string]bool{"dahua": true, "uniview": true, "tiandy": true, "dvrip": true}

// ClockType tells whether ffnvr can set the clock of recorders of a type.
func ClockType(typ string) bool { return clockTypes[typ] }

// CanSetClock tells whether ffnvr can set the clock of a recorder.
func CanSetClock(nvr *config.Item) bool { return remote(nvr) && clockTypes[nvr.Val("type")] }

func clockRecorder(s *config.Store, name string) (clocker, func(), error) {
	r, err := toolRecorder(s, name)
	if err != nil {
		return nil, nil, err
	}
	done := func() {
		if cl, ok := r.(io.Closer); ok {
			cl.Close()
		}
	}
	c, ok := r.(clocker)
	if !ok {
		done()
		return nil, nil, fmt.Errorf("%s: setting the clock is not supported for this type of recorder", name)
	}
	return c, done, nil
}

// NvrClock reads the clock of a remote recorder.
func NvrClock(ctx context.Context, s *config.Store, name string) (ClockInfo, error) {
	c, done, err := clockRecorder(s, name)
	if err != nil {
		return ClockInfo{}, err
	}
	defer done()
	return c.ClockInfo(ctx)
}

// SetNvrTime sets the clock of a remote recorder to t (a wall time of this server's
// zone) and reads it back.
func SetNvrTime(ctx context.Context, s *config.Store, name string, t time.Time) (ClockInfo, error) {
	c, done, err := clockRecorder(s, name)
	if err != nil {
		return ClockInfo{}, err
	}
	defer done()
	if err := c.SetTime(ctx, t); err != nil {
		return ClockInfo{}, err
	}
	return c.ClockInfo(ctx)
}

// SyncNvrTime sets the clock of a remote recorder to this server's time.
func SyncNvrTime(ctx context.Context, s *config.Store, name string) (ClockInfo, error) {
	c, done, err := clockRecorder(s, name)
	if err != nil {
		return ClockInfo{}, err
	}
	defer done()
	if err := c.SetTime(ctx, time.Now().Truncate(time.Second)); err != nil {
		return ClockInfo{}, err
	}
	return c.ClockInfo(ctx)
}

// SetNvrNTP changes the NTP settings of a remote recorder.
func SetNvrNTP(ctx context.Context, s *config.Store, name string, n NTPSettings) (ClockInfo, error) {
	if err := CheckNTP(n); err != nil {
		return ClockInfo{}, err
	}
	c, done, err := clockRecorder(s, name)
	if err != nil {
		return ClockInfo{}, err
	}
	defer done()
	if err := c.SetNTP(ctx, n); err != nil {
		return ClockInfo{}, err
	}
	return c.ClockInfo(ctx)
}

// CheckNTP validates NTP settings: an address when it is on, sane numbers.
func CheckNTP(n NTPSettings) error {
	n.Server = strings.TrimSpace(n.Server)
	switch {
	case n.Enabled && n.Server == "":
		return errors.New("NTP needs a server address")
	case strings.ContainsAny(n.Server, " \t\"'<>&/\\"):
		return fmt.Errorf("invalid NTP server address %q", n.Server)
	case len(n.Server) > 127:
		return errors.New("the NTP server address is too long")
	case n.Port < 0 || n.Port > 65535:
		return fmt.Errorf("invalid NTP port %d", n.Port)
	case n.Interval < 0 || n.Interval > 7*24*60:
		return fmt.Errorf("invalid NTP interval %d (minutes, at most a week)", n.Interval)
	}
	return nil
}

// ParseClock reads a time as given in the shell or the panel: now, 2026-11-05 10:03:17,
// 2026-11-05T10:03 (a wall time of this server's zone).
func ParseClock(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "now" {
		return time.Now().Truncate(time.Second), nil
	}
	for _, l := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(l, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q (now or YYYY-MM-DD HH:MM[:SS])", v)
}

// NoteClock writes a freshly read clock into the recorder's status (the offset the
// panel shows), so that it need not wait for the next poll.
func NoteClock(root, name string, c ClockInfo) {
	st := readStatus(root, name)
	if st == nil || c.Time.IsZero() {
		return
	}
	st.Clock = math.Round(c.Offset().Seconds())
	writeStatus(root, name, st)
}

// FormatClock describes a clock for the shell.
func FormatClock(name string, c ClockInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: clock %s (offset %+ds)", name, c.Time.In(time.Local).Format("2006-01-02 15:04:05"), int(c.Offset()/time.Second))
	if c.Zone != "" {
		fmt.Fprintf(&b, ", zone %s", c.Zone)
	}
	if c.NTPKnown {
		state := "off"
		if c.NTP.Enabled {
			state = "on"
		}
		fmt.Fprintf(&b, ", ntp %s", state)
		if c.NTP.Server != "" {
			fmt.Fprintf(&b, " server %s", c.NTP.Server)
			if c.NTP.Port > 0 && c.NTP.Port != 123 {
				b.WriteString(":" + strconv.Itoa(c.NTP.Port))
			}
		}
		if c.NTP.Interval > 0 {
			fmt.Fprintf(&b, " every %d min", c.NTP.Interval)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// ---------------------------------------------------------------- the makes

// stamp: the server's time of a reading begun at start.
func stamp(start time.Time, c ClockInfo, err error) (ClockInfo, error) {
	c.Read = start
	return c, err
}

func (r *dahuaRecorder) ClockInfo(ctx context.Context) (ClockInfo, error) {
	start := time.Now()
	c, err := r.c.ClockInfo(ctx)
	return stamp(start, ClockInfo{Time: c.Time, Zone: c.Zone, NTPKnown: c.NTPKnown, NTP: NTPSettings(c.NTP)}, err)
}
func (r *dahuaRecorder) SetTime(ctx context.Context, t time.Time) error { return r.c.SetTime(ctx, t) }
func (r *dahuaRecorder) SetNTP(ctx context.Context, n NTPSettings) error {
	return r.c.SetNTP(ctx, dahua.NTP(n))
}

func (r univiewRecorder) ClockInfo(ctx context.Context) (ClockInfo, error) {
	start := time.Now()
	c, err := r.c.ClockInfo(ctx)
	return stamp(start, ClockInfo{Time: c.Time, Zone: c.Zone, NTPKnown: c.NTPKnown, NTP: NTPSettings(c.NTP)}, err)
}
func (r univiewRecorder) SetTime(ctx context.Context, t time.Time) error { return r.c.SetTime(ctx, t) }
func (r univiewRecorder) SetNTP(ctx context.Context, n NTPSettings) error {
	return r.c.SetNTP(ctx, uniview.NTP(n))
}

func (r *tiandyRecorder) ClockInfo(ctx context.Context) (ClockInfo, error) {
	start := time.Now()
	c, err := r.c.ClockInfo(ctx)
	return stamp(start, ClockInfo{Time: c.Time, Zone: c.Zone, NTPKnown: c.NTPKnown, NTP: NTPSettings(c.NTP)}, err)
}
func (r *tiandyRecorder) SetTime(ctx context.Context, t time.Time) error { return r.c.SetTime(ctx, t) }
func (r *tiandyRecorder) SetNTP(ctx context.Context, n NTPSettings) error {
	return r.c.SetNTP(ctx, tiandy.NTP(n))
}

func (r *dvripRecorder) ClockInfo(ctx context.Context) (ClockInfo, error) {
	start := time.Now()
	c, err := r.c.ClockInfo(ctx)
	return stamp(start, ClockInfo{Time: c.Time, Zone: c.Zone, NTPKnown: c.NTPKnown, NTP: NTPSettings(c.NTP)}, err)
}
func (r *dvripRecorder) SetTime(ctx context.Context, t time.Time) error { return r.c.SetTime(ctx, t) }
func (r *dvripRecorder) SetNTP(ctx context.Context, n NTPSettings) error {
	return r.c.SetNTP(ctx, dvrip.NTP(n))
}
