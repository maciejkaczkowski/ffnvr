package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Alignment of archive blocks.
const (
	AlignClock = "clock" // block borders on multiples of the length counted from midnight: 11:50-12:00, 12:00-13:00
	AlignStart = "start" // block borders counted from the start: 11:50-12:50, 12:50-13:13
)

// MaxParts limits the number of files of one job.
const MaxParts = 1000

// Range is a time span [Start, End).
type Range struct{ Start, End time.Time }

// Blocks splits [from, to) into blocks of the given length (0: one block).
// With AlignClock the borders lie on multiples of length counted from local midnight
// (a block never crosses midnight), so the first and the last block may be shorter.
// With AlignStart they are counted from "from" and only the last block may be shorter.
func Blocks(from, to time.Time, length time.Duration, align string) []Range {
	if length <= 0 || !to.After(from) {
		return []Range{{from, to}}
	}
	var out []Range
	for t := from; t.Before(to); {
		next := t.Add(length)
		if align != AlignStart {
			day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			midnight := day.AddDate(0, 0, 1)
			n := t.Sub(day)/length + 1
			next = day.Add(n * length)
			if next.After(midnight) {
				next = midnight
			}
		}
		if next.After(to) {
			next = to
		}
		out = append(out, Range{t, next})
		t = next
	}
	return out
}

// ParseLength reads a block length: "15m", "1h", "1h30m", "1d", "90s".
func ParseLength(v string) (time.Duration, error) {
	s := v
	var d time.Duration
	if i := strings.Index(s, "d"); i > 0 {
		days, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("%q: expected a length like 15m, 1h, 1h30m, 1d", v)
		}
		d, s = time.Duration(days)*24*time.Hour, s[i+1:]
	}
	if s != "" {
		x, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("%q: expected a length like 15m, 1h, 1h30m, 1d", v)
		}
		d += x
	}
	if d < time.Minute {
		return 0, fmt.Errorf("%q: a block must be at least 1m", v)
	}
	return d, nil
}

// FormatLength writes a length the way ParseLength reads it: 1d2h30m.
func FormatLength(d time.Duration) string {
	var b strings.Builder
	for _, u := range []struct {
		d time.Duration
		s string
	}{{24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}} {
		if n := d / u.d; n > 0 {
			fmt.Fprintf(&b, "%d%s", n, u.s)
			d -= n * u.d
		}
	}
	return b.String()
}
