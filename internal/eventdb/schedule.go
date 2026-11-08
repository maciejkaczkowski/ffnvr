package eventdb

import (
	"context"
	"database/sql"
	"time"
)

// What the recording schedule (/record/schedule) reads: the events a camera of a local
// recorder sent itself - not the marks and ranges of the motion detector, not the
// system log.

// CameraEvent is the time and kind of one event of a camera.
type CameraEvent struct {
	Kind       string
	Start, End time.Time // End: now for one still going
}

// CameraEvents returns the events of a camera of a recorder overlapping [from, to), in
// order of their start; one still going lasts until now.
func (db *DB) CameraEvents(ctx context.Context, nvr, camera string, from, to, now time.Time) ([]CameraEvent, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT kind, started, COALESCE(ended, ?) FROM event
		WHERE nvr = ? AND camera = ? AND source NOT IN (?, ?, ?) AND started < ? AND COALESCE(ended, ?) > ?
		ORDER BY started`,
		ms(now), nvr, camera, SourceDetector, SourceChecked, SourceSystem, ms(to), ms(now), ms(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CameraEvent
	for rows.Next() {
		var kind string
		var a, b int64
		if err := rows.Scan(&kind, &a, &b); err != nil {
			return nil, err
		}
		out = append(out, CameraEvent{Kind: kind, Start: time.UnixMilli(a), End: time.UnixMilli(b)})
	}
	return out, rows.Err()
}

// LastCameraEvent: the latest notice (of any kind) a camera sent before a moment, and
// how many events of it began in the day before that moment (zero time: none ever).
func (db *DB) LastCameraEvent(ctx context.Context, nvr, camera string, before time.Time) (time.Time, int, error) {
	var last sql.NullInt64
	if err := db.sql.QueryRowContext(ctx, `SELECT MAX(updated) FROM event
		WHERE nvr = ? AND camera = ? AND source NOT IN (?, ?, ?) AND started <= ?`,
		nvr, camera, SourceDetector, SourceChecked, SourceSystem, ms(before)).Scan(&last); err != nil {
		return time.Time{}, 0, err
	}
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM event
		WHERE nvr = ? AND camera = ? AND source NOT IN (?, ?, ?) AND started > ? AND started <= ?`,
		nvr, camera, SourceDetector, SourceChecked, SourceSystem, ms(before.Add(-24*time.Hour)), ms(before)).Scan(&n); err != nil {
		return time.Time{}, 0, err
	}
	t := fromMs(last)
	if t.After(before) {
		t = before
	}
	return t, n, nil
}
