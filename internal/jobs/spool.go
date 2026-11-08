// Package jobs keeps archive jobs in a spool directory and runs them.
//
// The shell only creates job files, asks for cancellation (a <id>.cancel marker) and
// deletes finished jobs; everything else - the state of a job and its progress - is
// written by the worker ("ffnvr worker"). That way both sides never write the same file.
// Jobs of one recorder run one after another, jobs of different recorders in parallel.
package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Dir is the spool directory (below the root).
const Dir = "/var/lib/ffnvr/jobs"

// TimeFormat of start/end in files and print.
const TimeFormat = "2006-01-02 15:04:05"

type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Done      State = "done"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Finished reports whether a job will not change any more.
func (s State) Finished() bool { return s == Done || s == Failed || s == Cancelled }

// Part is one output file of a job.
type Part struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Target string    `json:"target"`
	// State: "" waiting, "done", "empty" (no recordings in this block, no file)
	State string `json:"state,omitempty"`
	Size  int64  `json:"size,omitempty"`
}

// Job is one archive request: the recordings of a camera between Start and End,
// joined into one mp4 file per part (one part, or one per block).
type Job struct {
	ID     int       `json:"id"`
	Nvr    string    `json:"nvr"`
	Camera string    `json:"camera"`
	Alias  string    `json:"alias,omitempty"` // the camera's alias when the job was made
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Source string    `json:"source"` // directory with the camera's segments
	// Remote recorders keep the recordings themselves: Remote is their type (e.g. dahua)
	// and Channel the camera's channel there; Source is then empty.
	Remote   string    `json:"remote,omitempty"`
	Channel  int       `json:"channel,omitempty"`
	Target   string    `json:"target"`          // the mp4 file, or the directory of the parts
	Block    string    `json:"block,omitempty"` // block length ("" one file)
	Align    string    `json:"align,omitempty"` // clock or start
	Parts    []Part    `json:"parts"`
	State    State     `json:"state"`
	Progress float64   `json:"progress"` // 0..1
	Speed    string    `json:"speed,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Error    string    `json:"error,omitempty"`
	Comment  string    `json:"comment,omitempty"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
}

// Spool is the directory with the jobs.
type Spool struct{ Dir string }

func (s Spool) file(id int) string   { return filepath.Join(s.Dir, strconv.Itoa(id)+".json") }
func (s Spool) marker(id int) string { return filepath.Join(s.Dir, strconv.Itoa(id)+".cancel") }

// Submit stores a new queued job and returns it with its id.
func (s Spool) Submit(j Job) (*Job, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	jobs, err := s.List()
	if err != nil {
		return nil, err
	}
	next := 1
	for _, o := range jobs {
		next = max(next, o.ID+1)
	}
	j.State, j.Progress, j.Created = Queued, 0, time.Now()
	if len(j.Parts) == 0 {
		j.Parts = []Part{{Start: j.Start, End: j.End, Target: j.Target}}
	}
	for ; ; next++ {
		j.ID = next
		b, _ := json.MarshalIndent(j, "", "  ")
		// O_EXCL: a parallel submit never takes the same id
		f, err := os.OpenFile(s.file(next), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_, err = f.Write(append(b, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(s.file(next))
			return nil, err
		}
		return &j, nil
	}
}

// Save rewrites a job atomically (worker only).
func (s Spool) Save(j *Job) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".job-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), s.file(j.ID))
}

// Get reads one job.
func (s Spool) Get(id int) (*Job, error) {
	b, err := os.ReadFile(s.file(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no such job: %d", id)
	}
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("job %d: %w", id, err)
	}
	if len(j.Parts) == 0 { // a job written before blocks existed
		j.Parts = []Part{{Start: j.Start, End: j.End, Target: j.Target}}
	}
	return &j, nil
}

// List returns all jobs sorted by id; a missing spool directory means no jobs.
func (s Spool) List() ([]*Job, error) {
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Job
	for _, e := range ents {
		id, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		j, err := s.Get(id)
		if err != nil {
			continue // removed meanwhile
		}
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

// Cancel asks the worker to stop a queued or running job.
func (s Spool) Cancel(id int) error {
	j, err := s.Get(id)
	if err != nil {
		return err
	}
	if j.State.Finished() {
		return fmt.Errorf("job %d is already %s", id, j.State)
	}
	return os.WriteFile(s.marker(id), nil, 0o644)
}

// CancelRequested reports whether Cancel was called for a job.
func (s Spool) CancelRequested(id int) bool {
	_, err := os.Stat(s.marker(id))
	return err == nil
}

// Remove deletes a finished job (the archive file stays).
func (s Spool) Remove(id int) error {
	j, err := s.Get(id)
	if err != nil {
		return err
	}
	if !j.State.Finished() {
		return fmt.Errorf("job %d is %s, cancel it first", id, j.State)
	}
	os.Remove(s.marker(id))
	return os.Remove(s.file(id))
}

const heartbeat = "worker.alive"

// Beat records that the worker is alive.
func (s Spool) Beat() {
	os.MkdirAll(s.Dir, 0o755)
	p := filepath.Join(s.Dir, heartbeat)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		os.WriteFile(p, nil, 0o644)
	}
}

// WorkerAlive reports whether the worker gave a sign of life recently.
func (s Spool) WorkerAlive() bool {
	st, err := os.Stat(filepath.Join(s.Dir, heartbeat))
	return err == nil && time.Since(st.ModTime()) < 15*time.Second
}

// Count returns how many parts are in the given state.
func (j *Job) Count(state string) int {
	n := 0
	for _, p := range j.Parts {
		if p.State == state {
			n++
		}
	}
	return n
}
