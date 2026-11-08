// Package eventdb keeps the events of the recorders (alarms: motion, video loss, disk
// errors, e-mails of cameras...) in SQLite.
//
// One process writes: the alarm server (ffnvr alarm, ffnvr-alarm.service) through a
// Writer, which also joins the notices of one event (start, stop, repeated pulses) into
// one row. Others read, and mark events as acknowledged (the web panel, the shell); WAL
// lets them do so while the writer works.
//
// The database lies on the system disk (/var/lib/ffnvr/events), never on a storage of
// recordings: SQLite's locks are not to be trusted on mergerfs or NFS.
package eventdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Dir of the database and the pictures (below the root).
const Dir = "/var/lib/ffnvr/events"

// File name of the database in Dir.
const File = "events.db"

// PictureDir: pictures that came with events (e-mails of cameras), by day.
const PictureDir = "pictures"

// Event is one row: an alarm with its start and end.
type Event struct {
	ID      int64
	Nvr     string // "" when the sender is not a known recorder (see Device)
	Channel int    // from 1; 0: the recorder itself (disk, login...)
	Camera  string // the camera's name in the configuration when the event came
	Kind    string // VideoMotion, VideoLoss, StorageFailure, ... as the recorder names it
	Source  string // dvrip, dahua, smtp
	Device  string // who sent it: serial number, address, SMTP user
	Started time.Time
	Ended   time.Time // zero: still going
	Updated time.Time // the last notice
	Count   int       // notices joined into it
	Text    string    // e-mail subject, details of the recorder
	Picture string    // relative to the picture directory, "" none
	Raw     string    // the first notice as it came (diagnostics)
	AckedAt time.Time
	AckedBy string
	User    string // system log: who did it
}

// SourceSystem: entries of the system log (sign ins, configuration, deletions...),
// kept beside the events of the recorders. Nvr is set when the entry is about one.
const SourceSystem = "system"

// System reports whether the event is an entry of the system log.
func (e Event) System() bool { return e.Source == SourceSystem }

// Open reports whether the event is still going.
func (e Event) Open() bool { return e.Ended.IsZero() }

// DB is the event database.
type DB struct {
	sql  *sql.DB
	path string
}

// Path of the database below root.
func Path(root string) string { return filepath.Join(root, Dir, File) }

// PicturesPath: the directory of pictures below root.
func PicturesPath(root string) string { return filepath.Join(root, Dir, PictureDir) }

const schema = `
CREATE TABLE IF NOT EXISTS event (
	id       INTEGER PRIMARY KEY,
	nvr      TEXT    NOT NULL,
	channel  INTEGER NOT NULL DEFAULT 0,
	camera   TEXT    NOT NULL DEFAULT '',
	kind     TEXT    NOT NULL,
	source   TEXT    NOT NULL,
	device   TEXT    NOT NULL DEFAULT '',
	started  INTEGER NOT NULL,
	ended    INTEGER,
	updated  INTEGER NOT NULL,
	count    INTEGER NOT NULL DEFAULT 1,
	text     TEXT    NOT NULL DEFAULT '',
	picture  TEXT    NOT NULL DEFAULT '',
	raw      TEXT    NOT NULL DEFAULT '',
	acked_at INTEGER,
	acked_by TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS event_started ON event(started);
CREATE INDEX IF NOT EXISTS event_nvr ON event(nvr, channel, started);
CREATE INDEX IF NOT EXISTS event_open ON event(nvr, channel, kind) WHERE ended IS NULL;
`

// migrations bring a database of version i+1 to i+2 (version 1 is schema).
var migrations = []string{
	`ALTER TABLE event ADD COLUMN user TEXT NOT NULL DEFAULT '';
	 CREATE INDEX IF NOT EXISTS event_source ON event(source, started);`,
	// the motion of a camera (the bars of the archive)
	`CREATE INDEX IF NOT EXISTS event_camera ON event(nvr, camera, kind, started);`,
}

// schemaVersion is kept in PRAGMA user_version: an older database migrates in Open.
var schemaVersion = 1 + len(migrations)

// Open opens (and creates) the database at path.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// _txlock=immediate: a writing transaction takes the lock at BEGIN, so two writers
	// wait for each other (busy_timeout) instead of failing on the upgrade
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	s, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	db := &DB{sql: s, path: path}
	if err := db.migrate(); err != nil {
		s.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return db, nil
}

// OpenIfExists opens the database when it is there (ok false: no events yet).
func OpenIfExists(path string) (db *DB, ok bool, err error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	db, err = Open(path)
	return db, err == nil, err
}

func (db *DB) migrate() error {
	var v int
	if err := db.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > schemaVersion {
		return fmt.Errorf("the database has schema %d, this ffnvr knows %d: a newer ffnvr made it", v, schemaVersion)
	}
	if v == schemaVersion {
		return nil
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if v == 0 {
		if _, err := tx.Exec(schema); err != nil {
			return err
		}
		v = 1
	}
	for ; v < schemaVersion; v++ {
		if _, err := tx.Exec(migrations[v-1]); err != nil {
			return fmt.Errorf("migration to %d: %w", v+1, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close the database.
func (db *DB) Close() error { return db.sql.Close() }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(v sql.NullInt64) time.Time {
	if !v.Valid || v.Int64 == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64)
}

// Filter of Query; zero values do not filter.
type Filter struct {
	// Nvrs limits the events to these recorders (a user's recorders); nil: all.
	// Unknown adds the events of senders that are no recorder of the configuration,
	// System the entries of the system log about no recorder (sign ins, configuration).
	Nvrs    []string
	Unknown bool
	System  bool
	// Origin: "recorders" (events the recorders sent), "system" (the system log), "": both.
	Origin  string
	User    string
	Text    string // contained in the text (case insensitive for ASCII)
	Nvr     string
	Camera  string
	Kind    string
	From    time.Time // started at or after
	To      time.Time // started before
	Unacked bool
	Open    bool  // only the ones still going
	Checks  bool  // also the detector's marks of segments looked at (MotionChecked)
	Before  int64 // id below this (paging: the last id of the previous page)
	Limit   int   // 0: 100
}

func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	if f.Nvrs != nil {
		ph := make([]string, len(f.Nvrs))
		for i, n := range f.Nvrs {
			ph[i] = "?"
			args = append(args, n)
		}
		c := "nvr IN (" + strings.Join(ph, ",") + ")"
		if len(ph) == 0 {
			c = "0"
		}
		if f.Unknown {
			c += " OR (nvr = '' AND source <> 'system')"
		}
		if f.System {
			c += " OR (nvr = '' AND source = 'system')"
		}
		conds = append(conds, "("+c+")")
	}
	if !f.Checks { // the detector's marks are bookkeeping
		conds = append(conds, "source <> '"+SourceChecked+"'")
	}
	switch f.Origin {
	case "recorders":
		conds = append(conds, "source <> 'system'")
	case "system":
		conds = append(conds, "source = 'system'")
	}
	add := func(c string, v any) { conds = append(conds, c); args = append(args, v) }
	if f.Nvr != "" {
		add("nvr = ?", f.Nvr)
	}
	if f.Camera != "" {
		add("camera = ?", f.Camera)
	}
	if f.Kind != "" {
		if strings.Contains(f.Kind, "*") { // a pattern: * any text, / for \ (kinds of Tiandy)
			esc := strings.NewReplacer(`!`, `!!`, "%", `!%`, "_", `!_`).Replace(strings.ReplaceAll(f.Kind, "/", `\`))
			add("kind LIKE ? ESCAPE '!'", strings.ReplaceAll(esc, "*", "%"))
		} else {
			add("kind = ?", f.Kind)
		}
	}
	if f.User != "" {
		add("user = ?", f.User)
	}
	if f.Text != "" {
		esc := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.Text)
		add(`(text LIKE ? ESCAPE '\' OR device LIKE ? ESCAPE '\')`, "%"+esc+"%")
		args = append(args, "%"+esc+"%")
	}
	if !f.From.IsZero() {
		add("started >= ?", ms(f.From))
	}
	if !f.To.IsZero() {
		add("started < ?", ms(f.To))
	}
	if f.Unacked {
		conds = append(conds, "acked_at IS NULL")
	}
	if f.Open {
		conds = append(conds, "ended IS NULL")
	}
	if f.Before > 0 {
		add("id < ?", f.Before)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const columns = "id, nvr, channel, camera, kind, source, device, started, ended, updated, count, text, picture, raw, acked_at, acked_by, user"

func scan(rows interface{ Scan(...any) error }) (Event, error) {
	var e Event
	var started, ended, updated, acked sql.NullInt64
	err := rows.Scan(&e.ID, &e.Nvr, &e.Channel, &e.Camera, &e.Kind, &e.Source, &e.Device, &started, &ended, &updated,
		&e.Count, &e.Text, &e.Picture, &e.Raw, &acked, &e.AckedBy, &e.User)
	e.Started, e.Ended, e.Updated, e.AckedAt = fromMs(started), fromMs(ended), fromMs(updated), fromMs(acked)
	return e, err
}

// Query returns the events, the newest first. The order is by id - the order in which
// they came - so that Before pages without gaps.
func (db *DB) Query(ctx context.Context, f Filter) ([]Event, error) {
	where, args := f.where()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.sql.QueryContext(ctx, "SELECT "+columns+" FROM event"+where+" ORDER BY id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Count of the events matching f (Before and Limit are ignored).
func (db *DB) Count(ctx context.Context, f Filter) (int, error) {
	f.Before = 0
	where, args := f.where()
	var n int
	err := db.sql.QueryRowContext(ctx, "SELECT count(*) FROM event"+where, args...).Scan(&n)
	return n, err
}

// Get one event.
func (db *DB) Get(ctx context.Context, id int64) (Event, error) {
	return scan(db.sql.QueryRowContext(ctx, "SELECT "+columns+" FROM event WHERE id = ?", id))
}

// Kinds that occur among what f may see (Nvrs, Unknown, System, Origin), for the filters.
func (db *DB) Kinds(ctx context.Context, f Filter) ([]string, error) {
	return db.distinct(ctx, "kind", f)
}

// Users named by the system log among what f may see, for the filters.
func (db *DB) Users(ctx context.Context, f Filter) ([]string, error) {
	f.Origin = "system"
	return db.distinct(ctx, "user", f)
}

func (db *DB) distinct(ctx context.Context, col string, f Filter) ([]string, error) {
	where, args := Filter{Nvrs: f.Nvrs, Unknown: f.Unknown, System: f.System, Origin: f.Origin}.where()
	cond := " WHERE "
	if where != "" {
		cond = " AND "
	}
	rows, err := db.sql.QueryContext(ctx, "SELECT DISTINCT "+col+" FROM event"+where+cond+col+" <> '' ORDER BY "+col, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Entry of the system log.
type Entry struct {
	Time    time.Time // zero: now
	Nvr     string    // the recorder it is about ("" none)
	Channel int
	Camera  string
	Kind    string // Login, LoginFailed, ConfigSaved, RecordingsDeleted, NvrOffline...
	User    string
	Address string // where from (an address, a terminal)
	Text    string
}

// Log writes entries of the system log. Any process may: SQLite serializes the
// writers (busy_timeout), the alarm server's Writer included.
func (db *DB) Log(ctx context.Context, entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range entries {
		t := ms(e.Time)
		if t == 0 {
			t = ms(time.Now())
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO event (nvr, channel, camera, kind, source, device, started, ended, updated, text, user) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			e.Nvr, e.Channel, e.Camera, e.Kind, SourceSystem, e.Address, t, t, t, e.Text, e.User); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Ack marks events as acknowledged by user: the ones of ids, or (ids nil) every
// unacknowledged one matching f. It returns how many changed.
func (db *DB) Ack(ctx context.Context, ids []int64, f Filter, user string, now time.Time) (int64, error) {
	f.Unacked, f.Before, f.Limit = true, 0, 0
	where, args := f.where()
	where += " AND source <> 'system'" // the system log is a record, nothing to acknowledge
	if ids != nil {
		if len(ids) == 0 {
			return 0, nil
		}
		ph := make([]string, len(ids))
		for i, id := range ids {
			ph[i] = "?"
			args = append(args, id)
		}
		where += " AND id IN (" + strings.Join(ph, ",") + ")"
	}
	res, err := db.sql.ExecContext(ctx, "UPDATE event SET acked_at = ?, acked_by = ?"+where, append([]any{ms(now), user}, args...)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Purge deletes the events that started before limit, with their pictures (below
// pictures), in batches so that the writer is never held up for long. It returns how
// many went.
func (db *DB) Purge(ctx context.Context, limit time.Time, pictures string) (int, error) {
	total := 0
	for {
		rows, err := db.sql.QueryContext(ctx, "SELECT id, picture FROM event WHERE started < ? ORDER BY id LIMIT 2000", ms(limit))
		if err != nil {
			return total, err
		}
		var ids []any
		var pics []string
		for rows.Next() {
			var id int64
			var pic string
			if err := rows.Scan(&id, &pic); err != nil {
				rows.Close()
				return total, err
			}
			ids = append(ids, id)
			if pic != "" {
				pics = append(pics, pic)
			}
		}
		rows.Close()
		if len(ids) == 0 {
			return total, nil
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		if _, err := db.sql.ExecContext(ctx, "DELETE FROM event WHERE id IN ("+ph+")", ids...); err != nil {
			return total, err
		}
		for _, p := range pics {
			if full, ok := picturePath(pictures, p); ok {
				os.Remove(full)
			}
		}
		total += len(ids)
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// picturePath: the file of a picture (ok false: not a clean relative path).
func picturePath(dir, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return "", false
	}
	return filepath.Join(dir, filepath.Clean(rel)), true
}

// PictureFile is the file of an event's picture ("" none).
func PictureFile(pictures string, e Event) string {
	p, _ := picturePath(pictures, e.Picture)
	return p
}

// RemoveEmptyDirs drops the day directories of pictures left empty by Purge.
func RemoveEmptyDirs(pictures string) {
	var dirs []string
	filepath.WalkDir(pictures, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != pictures {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- { // children before their parents
		os.Remove(dirs[i]) // fails unless empty
	}
}
