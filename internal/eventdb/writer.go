package eventdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// State of a notice.
type State int

const (
	Start State = iota // the event begins (motion starts)
	Stop               // it ends
	Pulse              // a moment: an e-mail, a line crossed, a failed login
	lost               // the connection to the recorder is gone: its open events end
)

func (s State) String() string {
	switch s {
	case Start:
		return "start"
	case Stop:
		return "stop"
	case Pulse:
		return "pulse"
	}
	return "lost"
}

// Notice is one message of a recorder, as a protocol understood it.
type Notice struct {
	Nvr     string // "" when the sender is unknown
	Channel int    // from 1, 0: the recorder itself
	Camera  string
	Kind    string
	Source  string
	Device  string
	Time    time.Time
	State   State
	Text    string
	Raw     string
	Picture []byte // a JPEG that came with it (only the first one of an event is kept)
}

// Writer is the one writer of the database. Notices are queued (Add) and written in
// batches, one transaction every quarter of a second at most: a few hundred notices a
// second cost a few fsyncs.
//
// Notices of one event are joined: a key is recorder, channel, camera, kind (and the
// sender when the recorder is unknown); the camera tells apart the cameras of a local
// recorder, which have no channels.
//
//	Start  opens the event; a Start while it is open counts only; a Start within Gap
//	       after its end opens the same event again (motion that pauses for a moment)
//	Stop   ends the open event (a Stop without one is dropped)
//	Pulse  a moment: within Gap of the last one, or while the event is open, it joins it
type Writer struct {
	db       *DB
	pictures string
	gap      atomic.Int64 // ns
	in       chan Notice
	logf     func(string, ...any)
	dropped  atomic.Int64
	cache    map[string]*cached
	now      func() time.Time
	// Written counts the notices written (tests, diagnostics).
	Written atomic.Int64
}

type cached struct {
	id      int64
	nvr     string
	camera  string
	started int64
	ended   int64 // 0: open
	updated int64
	picture bool
}

// DefaultGap: a pause within which notices are one event.
const DefaultGap = 5 * time.Second

// NewWriter writes into db; pictures go below the directory pictures.
func NewWriter(db *DB, pictures string, logf func(string, ...any)) *Writer {
	w := &Writer{db: db, pictures: pictures, in: make(chan Notice, 10000), logf: logf, cache: map[string]*cached{}, now: time.Now}
	w.gap.Store(int64(DefaultGap))
	return w
}

// SetGap changes the gap (it is read from the configuration while running).
func (w *Writer) SetGap(d time.Duration) { w.gap.Store(int64(d)) }

// Add queues a notice; it never blocks: when the queue is full (the disk stalls) the
// notice is dropped and counted.
func (w *Writer) Add(n Notice) {
	if n.Time.IsZero() {
		n.Time = w.now()
	}
	select {
	case w.in <- n:
	default:
		if w.dropped.Add(1)%1000 == 1 {
			w.logf("events: the queue is full, notices are dropped (%d so far)", w.dropped.Load())
		}
	}
}

// Lost ends the open events of a recorder whose connection broke: nobody will send
// their Stop. They end at their last notice.
func (w *Writer) Lost(nvr string) { w.Add(Notice{Nvr: nvr, State: lost}) }

// LostCamera is Lost for one camera of a recorder (a camera subscribed to by itself).
func (w *Writer) LostCamera(nvr, camera string) { w.Add(Notice{Nvr: nvr, Camera: camera, State: lost}) }

// Run writes until ctx ends; what is queued then is still written.
func (w *Writer) Run(ctx context.Context) error {
	// events left open by the previous run end at their last notice
	if _, err := w.db.sql.Exec("UPDATE event SET ended = updated WHERE ended IS NULL"); err != nil {
		return err
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var batch []Notice
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.write(batch); err != nil {
			w.logf("events: %v (%d notices lost)", err, len(batch))
		}
		batch = batch[:0]
	}
	for {
		select {
		case n := <-w.in:
			batch = append(batch, n)
			if len(batch) >= 500 {
				flush()
			}
		case <-tick.C:
			flush()
			w.prune()
		case <-ctx.Done():
			for {
				select {
				case n := <-w.in:
					batch = append(batch, n)
				default:
					flush()
					return nil
				}
			}
		}
	}
}

// Flush writes what is queued now (tests).
func (w *Writer) Flush() error {
	var batch []Notice
	for {
		select {
		case n := <-w.in:
			batch = append(batch, n)
		default:
			return w.write(batch)
		}
	}
}

func key(n Notice) string {
	k := n.Nvr + "\x00" + strconv.Itoa(n.Channel) + "\x00" + n.Camera + "\x00" + n.Kind
	if n.Nvr == "" {
		k += "\x00" + n.Device
	}
	return k
}

// prune forgets the events that ended more than a gap ago: nothing joins them any more.
func (w *Writer) prune() {
	limit := ms(w.now()) - w.gap.Load()/int64(time.Millisecond) - 1000
	for k, c := range w.cache {
		if c.ended != 0 && c.updated < limit {
			delete(w.cache, k)
		}
	}
}

func (w *Writer) write(batch []Notice) error {
	if len(batch) == 0 {
		return nil
	}
	tx, err := w.db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type pic struct {
		c    *cached
		data []byte
		t    time.Time
	}
	var pics []pic
	changed := map[string]*cached{} // the cache as it was, to undo on failure
	remember := func(k string) {
		if _, ok := changed[k]; !ok {
			if c := w.cache[k]; c != nil {
				cp := *c
				changed[k] = &cp
			} else {
				changed[k] = nil
			}
		}
	}
	gap := w.gap.Load() / int64(time.Millisecond)
	for _, n := range batch {
		if n.State == lost {
			for k, c := range w.cache {
				if c.nvr == n.Nvr && (n.Camera == "" || c.camera == n.Camera) && c.ended == 0 {
					remember(k)
					c.ended = c.updated
					if _, err := tx.Exec("UPDATE event SET ended = ? WHERE id = ?", c.ended, c.id); err != nil {
						return err
					}
				}
			}
			continue
		}
		k := key(n)
		t := ms(n.Time)
		c := w.cache[k]
		remember(k)
		switch {
		case n.State == Stop:
			if c == nil || c.ended != 0 {
				continue // nothing open: the start came before we listened
			}
			c.ended, c.updated = max(t, c.started), max(t, c.updated)
			if _, err := tx.Exec("UPDATE event SET ended = ?, updated = ? WHERE id = ?", c.ended, c.updated, c.id); err != nil {
				return err
			}
		case c != nil && (c.ended == 0 || t-c.ended <= gap):
			// joins the event: a repeated start, a start soon after the end, a pulse
			if n.State == Start {
				c.ended = 0
			} else if c.ended != 0 {
				c.ended = max(c.ended, t)
			}
			c.updated = max(c.updated, t)
			var ended any
			if c.ended != 0 {
				ended = c.ended
			}
			if _, err := tx.Exec("UPDATE event SET ended = ?, updated = ?, count = count + 1 WHERE id = ?", ended, c.updated, c.id); err != nil {
				return err
			}
		default:
			var ended any
			if n.State == Pulse {
				ended = t
			}
			res, err := tx.Exec("INSERT INTO event (nvr, channel, camera, kind, source, device, started, ended, updated, text, raw) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				n.Nvr, n.Channel, n.Camera, n.Kind, n.Source, n.Device, t, ended, t, n.Text, n.Raw)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			c = &cached{id: id, nvr: n.Nvr, camera: n.Camera, started: t, updated: t}
			if n.State == Pulse {
				c.ended = t
			}
			w.cache[k] = c
		}
		if len(n.Picture) > 0 && !c.picture {
			c.picture = true
			pics = append(pics, pic{c, n.Picture, n.Time})
		}
	}
	for _, p := range pics {
		rel, err := w.savePicture(p.c.id, p.t, p.data)
		if err != nil {
			w.logf("events: picture of event %d: %v", p.c.id, err)
			continue
		}
		if _, err := tx.Exec("UPDATE event SET picture = ? WHERE id = ?", rel, p.c.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		for k, c := range changed { // the rows are not there: forget what we thought
			if c == nil {
				delete(w.cache, k)
			} else {
				w.cache[k] = c
			}
		}
		return err
	}
	w.Written.Add(int64(len(batch)))
	return nil
}

func (w *Writer) savePicture(id int64, t time.Time, data []byte) (string, error) {
	rel := filepath.Join(t.Format("2006/01/02"), fmt.Sprintf("%d.jpg", id))
	full := filepath.Join(w.pictures, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}
