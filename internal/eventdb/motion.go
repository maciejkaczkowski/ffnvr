package eventdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Motion of local recordings. The motion detector of ffnvr (the worker) writes what it
// found in a segment of a camera of a local recorder into the one table of events: every
// range as an event of KindMotion (source SourceDetector), and a mark that the segment
// was looked at, with the settings it was looked at with (KindChecked, source
// SourceChecked) - also when nothing moved, so it is not looked at again. The marks are
// bookkeeping: the lists of events (Filter) leave them out.

// Kinds and sources of the detector.
const (
	KindMotion     = "MotionDetect"
	KindChecked    = "MotionChecked"
	SourceDetector = "detector"
	SourceChecked  = "check"
	// AckDetector acknowledges the detector's events as they are written: they are the
	// analysis of recordings, no alarm for anybody to see to.
	AckDetector = "detector"
)

// Span of time.
type Span struct{ Start, End time.Time }

// DetectedRange: a range the detector found, its times absolute.
type DetectedRange struct {
	Start, End time.Time
	Peak       float64
}

// SetMotion replaces what the detector wrote for a segment (name, [start, end)) of a
// camera of a local recorder: its ranges (cut to the segment) and the mark with the
// settings; method tells how they were found (in the text).
func (db *DB) SetMotion(ctx context.Context, nvr, camera, segment string, start, end time.Time, ranges []DetectedRange, method, settings string, now time.Time) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM event WHERE nvr = ? AND camera = ? AND source IN (?, ?) AND started >= ? AND started < ?",
		nvr, camera, SourceDetector, SourceChecked, ms(start), ms(end)); err != nil {
		return err
	}
	ins := func(kind, source string, a, b time.Time, text, raw string) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO event (nvr, channel, camera, kind, source, device, started, ended, updated, text, raw, acked_at, acked_by) VALUES (?, 0, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)",
			nvr, camera, kind, source, ms(a), ms(b), ms(b), text, raw, ms(now), AckDetector)
		return err
	}
	for _, r := range ranges {
		a, b := r.Start, r.End
		if a.Before(start) {
			a = start
		}
		if b.After(end) {
			b = end
		}
		if !b.After(a) {
			continue
		}
		raw, _ := json.Marshal(map[string]any{"segment": segment, "start": a.Sub(start).Seconds(), "end": b.Sub(start).Seconds(), "peak": r.Peak, "method": method})
		if err := ins(KindMotion, SourceDetector, a, b, fmt.Sprintf("%s (%s)", segment, method), string(raw)); err != nil {
			return err
		}
	}
	if err := ins(KindChecked, SourceChecked, start, end, segment, settings); err != nil {
		return err
	}
	return tx.Commit()
}

// Checked: a segment looked at by the detector.
type Checked struct {
	Start, End time.Time
	Settings   string
}

// MotionChecked returns the marks of the segments of a camera beginning in [from, to),
// by the segment's name.
func (db *DB) MotionChecked(ctx context.Context, nvr, camera string, from, to time.Time) (map[string]Checked, error) {
	rows, err := db.sql.QueryContext(ctx, "SELECT text, started, ended, raw FROM event WHERE nvr = ? AND camera = ? AND kind = ? AND source = ? AND started >= ? AND started < ?",
		nvr, camera, KindChecked, SourceChecked, ms(from), ms(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Checked{}
	for rows.Next() {
		var name, raw string
		var a int64
		var b sql.NullInt64
		if err := rows.Scan(&name, &a, &b, &raw); err != nil {
			return nil, err
		}
		out[name] = Checked{Start: time.UnixMilli(a), End: time.UnixMilli(b.Int64), Settings: raw}
	}
	return out, rows.Err()
}

// Spans returns the time of the events of the kinds of a camera of a recorder that
// overlap [from, to) - one still going lasts until now - in order, the overlapping
// ones joined (the ends grow with the starts).
func (db *DB) Spans(ctx context.Context, nvr, camera string, kinds []string, from, to, now time.Time) ([]Span, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := []any{nvr, camera}
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, SourceChecked, ms(to), ms(now), ms(from))
	rows, err := db.sql.QueryContext(ctx, "SELECT started, COALESCE(ended, ?) FROM event WHERE nvr = ? AND camera = ? AND kind IN ("+ph+") AND source <> ? AND started < ? AND COALESCE(ended, ?) > ? ORDER BY started",
		append([]any{ms(now)}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Span
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		s := Span{time.UnixMilli(a), time.UnixMilli(b)}
		if n := len(out); n > 0 && !s.Start.After(out[n-1].End) {
			if s.End.After(out[n-1].End) {
				out[n-1].End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
