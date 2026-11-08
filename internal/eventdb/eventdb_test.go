package eventdb

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTest(t *testing.T) (*DB, *Writer, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, File))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w := NewWriter(db, filepath.Join(dir, PictureDir), t.Logf)
	return db, w, dir
}

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

func all(t *testing.T, db *DB) []Event {
	t.Helper()
	evs, err := db.Query(context.Background(), Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestJoinStartStop(t *testing.T) {
	db, w, _ := newTest(t)
	m := Notice{Nvr: "nvr01", Channel: 3, Camera: "door", Kind: "VideoMotion", Source: "dahua"}
	add := func(s float64, st State) { n := m; n.Time, n.State = at(s), st; w.Add(n) }
	add(0, Start)
	add(1, Start) // repeated: counts only
	add(10, Stop)
	add(12, Start) // within the gap of 5 s: the same event again
	add(20, Stop)
	add(40, Start) // a new one
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	evs := all(t, db)
	if len(evs) != 2 {
		t.Fatalf("events: %d, want 2: %+v", len(evs), evs)
	}
	first, second := evs[1], evs[0]
	if !first.Started.Equal(at(0)) || !first.Ended.Equal(at(20)) || first.Count != 3 {
		t.Errorf("first: %v - %v count %d", first.Started, first.Ended, first.Count)
	}
	if !second.Open() || !second.Started.Equal(at(40)) {
		t.Errorf("second: %+v", second)
	}
	// the connection breaks: the open one ends at its last notice
	w.Lost("nvr01")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if e := all(t, db)[0]; e.Open() || !e.Ended.Equal(at(40)) {
		t.Errorf("after lost: %+v", e)
	}
}

func TestPulsesAndChannels(t *testing.T) {
	db, w, _ := newTest(t)
	for _, s := range []float64{0, 2, 4, 6, 30} { // a burst of e-mails, then one more later
		w.Add(Notice{Nvr: "nvr02", Channel: 1, Kind: "Mail", Source: "smtp", Time: at(s), State: Pulse})
	}
	w.Add(Notice{Nvr: "nvr02", Channel: 2, Kind: "Mail", Source: "smtp", Time: at(1), State: Pulse})
	w.Add(Notice{Nvr: "nvr02", Channel: 1, Kind: "VideoLoss", Source: "dahua", Time: at(3), State: Stop}) // nothing open: dropped
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	evs := all(t, db)
	if len(evs) != 3 {
		t.Fatalf("events: %d, want 3: %+v", len(evs), evs)
	}
	burst := evs[2]
	if burst.Count != 4 || !burst.Ended.Equal(at(6)) {
		t.Errorf("burst: count %d end %v", burst.Count, burst.Ended)
	}
}

func TestUnknownSendersApart(t *testing.T) {
	db, w, _ := newTest(t)
	for _, dev := range []string{"serial A", "serial B"} {
		w.Add(Notice{Device: dev, Kind: "MotionDetect", Source: "dvrip", Time: at(0), State: Start})
	}
	w.Flush()
	if n := len(all(t, db)); n != 2 {
		t.Fatalf("two unknown senders are two events, got %d", n)
	}
	evs, _ := db.Query(context.Background(), Filter{Nvrs: []string{"nvr01"}})
	if len(evs) != 0 {
		t.Errorf("unknown ones are not a user's: %+v", evs)
	}
	evs, _ = db.Query(context.Background(), Filter{Nvrs: []string{"nvr01"}, Unknown: true})
	if len(evs) != 2 {
		t.Errorf("with Unknown: %d", len(evs))
	}
}

func TestRestartClosesOpenEvents(t *testing.T) {
	db, w, _ := newTest(t)
	w.Add(Notice{Nvr: "n", Channel: 1, Kind: "VideoMotion", Time: at(0), State: Start})
	w.Flush()
	w2 := NewWriter(db, "", t.Logf)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w2.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if e := all(t, db)[0]; e.Open() {
		t.Errorf("still open after a restart: %+v", e)
	}
}

func TestAckPurgePictures(t *testing.T) {
	db, w, dir := newTest(t)
	ctx := context.Background()
	w.Add(Notice{Nvr: "a", Channel: 1, Kind: "Mail", Time: at(0), State: Pulse, Picture: []byte("JPEG1")})
	w.Add(Notice{Nvr: "a", Channel: 1, Kind: "Mail", Time: at(1), State: Pulse, Picture: []byte("JPEG2")}) // joins: picture kept from the first
	w.Add(Notice{Nvr: "b", Channel: 1, Kind: "Mail", Time: at(100), State: Pulse})
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	evs := all(t, db)
	old := evs[1]
	pic := PictureFile(filepath.Join(dir, PictureDir), old)
	if b, err := os.ReadFile(pic); err != nil || string(b) != "JPEG1" {
		t.Fatalf("picture %q: %q %v", old.Picture, b, err)
	}
	n, err := db.Ack(ctx, nil, Filter{Nvrs: []string{"a"}}, "jan", at(200))
	if err != nil || n != 1 {
		t.Fatalf("ack: %d %v", n, err)
	}
	if c, _ := db.Count(ctx, Filter{Unacked: true}); c != 1 {
		t.Errorf("unacked: %d", c)
	}
	if e, _ := db.Get(ctx, old.ID); e.AckedBy != "jan" || !e.AckedAt.Equal(at(200)) {
		t.Errorf("acked: %+v", e)
	}
	gone, err := db.Purge(ctx, at(50), filepath.Join(dir, PictureDir))
	if err != nil || gone != 1 {
		t.Fatalf("purge: %d %v", gone, err)
	}
	if _, err := os.Stat(pic); !os.IsNotExist(err) {
		t.Errorf("picture left: %v", err)
	}
	RemoveEmptyDirs(filepath.Join(dir, PictureDir))
	if ents, _ := os.ReadDir(filepath.Join(dir, PictureDir)); len(ents) != 0 {
		t.Errorf("empty day directories left: %v", ents)
	}
}

func TestManyNotices(t *testing.T) {
	db, w, _ := newTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()
	// 50 recorders x 16 channels, motion starting and stopping
	for i := 0; i < 4000; i++ {
		st := Start
		if i%2 == 1 {
			st = Stop
		}
		w.Add(Notice{Nvr: "nvr" + string(rune('A'+i%50)), Channel: i % 16, Kind: "VideoMotion", Time: at(float64(i) / 100), State: st})
	}
	deadline := time.Now().Add(20 * time.Second)
	for w.Written.Load() < 4000 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if w.Written.Load() != 4000 {
		t.Fatalf("written %d of 4000", w.Written.Load())
	}
	if n, _ := db.Count(context.Background(), Filter{}); n == 0 {
		t.Fatal("no events")
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	db, _, dir := newTest(t)
	db.sql.Exec("PRAGMA user_version=99")
	db.Close()
	if _, err := Open(filepath.Join(dir, File)); err == nil {
		t.Fatal("a database of a newer ffnvr must not be opened")
	}
}

func TestMigrationFromV1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// make it a database of the first release: schema 1 with a row
	db.sql.Exec("DROP TABLE event")
	db.sql.Exec(schema)
	db.sql.Exec("PRAGMA user_version=1")
	db.sql.Exec("INSERT INTO event (nvr, kind, source, started, updated) VALUES ('n', 'VideoMotion', 'dahua', 1, 1)")
	db.Close()
	if db, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	evs, err := db.Query(context.Background(), Filter{})
	if err != nil || len(evs) != 1 || evs[0].User != "" {
		t.Fatalf("after migration: %+v %v", evs, err)
	}
	var v int
	db.sql.QueryRow("PRAGMA user_version").Scan(&v)
	if v != schemaVersion {
		t.Errorf("version %d", v)
	}
}

func TestSystemLogVisibility(t *testing.T) {
	db, w, _ := newTest(t)
	ctx := context.Background()
	w.Add(Notice{Nvr: "nvr01", Channel: 1, Kind: "VideoMotion", Source: "dahua", Time: at(0), State: Pulse})
	w.Add(Notice{Device: "10.9.9.9", Kind: "Mail", Source: "smtp", Time: at(0), State: Pulse})
	w.Flush()
	err := db.Log(ctx,
		Entry{Kind: "Login", User: "jan", Address: "10.0.0.7", Text: "signed in", Time: at(1)},
		Entry{Kind: "LoginFailed", User: "x", Address: "10.0.0.66", Text: "100% wrong", Time: at(2)},
		Entry{Kind: "RecordingsDeleted", Nvr: "nvr01", Text: "12 segments", Time: at(3)},
		Entry{Kind: "RecordingsDeleted", Nvr: "nvr02", Text: "3 segments", Time: at(4)},
	)
	if err != nil {
		t.Fatal(err)
	}
	count := func(f Filter) int {
		t.Helper()
		n, err := db.Count(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	mine := []string{"nvr01"}
	for name, c := range map[string]struct {
		f    Filter
		want int
	}{
		"everything":               {Filter{}, 6},
		"a user of nvr01":          {Filter{Nvrs: mine}, 2},
		"with the system log":      {Filter{Nvrs: mine, System: true}, 4},
		"with unknown senders":     {Filter{Nvrs: mine, Unknown: true}, 3},
		"recorders only":           {Filter{Origin: "recorders"}, 2},
		"system only":              {Filter{Origin: "system"}, 4},
		"system of nvr01":          {Filter{Origin: "system", Nvr: "nvr01"}, 1},
		"user jan":                 {Filter{User: "jan"}, 1},
		"text with %, escaped":     {Filter{Text: "100%"}, 1},
		"text matches the address": {Filter{Text: "10.0.0.66"}, 1},
		"a literal _ matches none": {Filter{Text: "_"}, 0},
	} {
		if n := count(c.f); n != c.want {
			t.Errorf("%s: %d, want %d", name, n, c.want)
		}
	}
	users, _ := db.Users(ctx, Filter{Nvrs: mine, System: true})
	if strings.Join(users, ",") != "jan,x" {
		t.Errorf("users: %v", users)
	}
	kinds, _ := db.Kinds(ctx, Filter{Nvrs: mine})
	if strings.Join(kinds, ",") != "RecordingsDeleted,VideoMotion" {
		t.Errorf("kinds a user of nvr01 sees: %v", kinds)
	}
	if e, _ := db.Query(ctx, Filter{User: "jan"}); len(e) != 1 || !e[0].System() || e[0].Device != "10.0.0.7" || e[0].Open() {
		t.Errorf("%+v", e)
	}
}
