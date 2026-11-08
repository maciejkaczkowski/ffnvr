package modules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/fsusage"
	"ffnvr/internal/jobs"
)

// The recordings of a local recorder: <storage>/<nvr>/<camera>/<start>.mkv. Reading
// tens of thousands of names on every page is slow, so the worker keeps an index per
// camera in a hidden technical directory beside it: <storage>/<nvr>/.<camera>/ (the
// place for everything technical about the camera, e.g. cached thumbnails later):
//
//	segments.json  every segment with its size, and the directory's modification time
//	summary.json   oldest, newest, count, bytes - what the panel reads
//
// A directory whose modification time did not change is not read again; only new
// segments (and the newest one, still growing) are looked at.

// TechDir is the technical directory of a camera of a local recorder.
func TechDir(recDir, camera string) string { return filepath.Join(recDir, "."+camera) }

type segIndex struct {
	DirMtime int64     `json:"dir_mtime"`
	Files    []segFile `json:"files"` // sorted by name, i.e. by time
}

type segFile struct {
	Name string `json:"n"`
	Size int64  `json:"s"`
	// End (unix ms) of a part the recording schedule cut out of a segment (pieces.json);
	// 0: the segment lasts until the next one begins
	End int64 `json:"e,omitempty"`
}

// LocalRecordings sums up what a local recorder (or a camera) has on its storage.
type LocalRecordings struct {
	Updated        time.Time `json:"updated"`
	Oldest, Newest time.Time
	Files          int
	Bytes          int64
}

// localDir is where a local recorder records: <storage path>/<nvr>.
func localDir(s *config.Store, nvr *config.Item) string {
	st := find(s, "/storage", "name", nvr.Val("storage"))
	if st == nil {
		return ""
	}
	return filepath.Join(st.Val("path"), nvr.Val("name"))
}

func segmentTime(name string) (time.Time, bool) {
	if !strings.HasSuffix(name, ".mkv") {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(jobs.SegmentLayout, strings.TrimSuffix(name, ".mkv"), time.Local)
	return t, err == nil
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), path)
}

// indexCamera brings the index of one camera directory up to date and deletes the
// segments that started before limit (zero: none); it returns the summary.
func indexCamera(recDir, camera string, limit time.Time, logf func(string, ...any)) (LocalRecordings, int) {
	dir := filepath.Join(recDir, camera)
	tech := TechDir(recDir, camera)
	var idx segIndex
	readJSON(filepath.Join(tech, "segments.json"), &idx)
	st, err := os.Stat(dir)
	if err != nil {
		return LocalRecordings{}, 0
	}
	changed := false
	// The names are read on every pass: the modification time of the directory cannot
	// be trusted to change - a mergerfs pool (OpenMediaVault, /srv/<uuid>) reports the
	// directory of one branch while new files land on another. Only new files are
	// looked at (stat); so is the one that was the newest, as it grew until now.
	{
		known := map[string]int64{}
		for _, f := range idx.Files {
			known[f.Name] = f.Size
		}
		prevLast := ""
		if n := len(idx.Files); n > 0 {
			prevLast = idx.Files[n-1].Name
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return LocalRecordings{}, 0
		}
		pieces := readPieces(tech)
		var files []segFile
		for _, e := range ents {
			if _, ok := segmentTime(e.Name()); !ok || !e.Type().IsRegular() {
				continue
			}
			size, ok := known[e.Name()]
			if !ok || e.Name() == prevLast {
				if info, err := e.Info(); err == nil {
					size = info.Size()
				}
			}
			files = append(files, segFile{e.Name(), size, pieces[e.Name()]})
		}
		prunePieces(tech, pieces, files)
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		if len(files) != len(idx.Files) {
			changed = true
		} else {
			for i := range files {
				if files[i] != idx.Files[i] {
					changed = true
					break
				}
			}
		}
		idx.Files = files
		idx.DirMtime = st.ModTime().UnixNano() // kept for information only
	}
	// the newest segment may still be written: its size is read again
	if n := len(idx.Files); n > 0 {
		if info, err := os.Stat(filepath.Join(dir, idx.Files[n-1].Name)); err == nil && info.Size() != idx.Files[n-1].Size {
			idx.Files[n-1].Size, changed = info.Size(), true
		}
	}
	deleted := 0
	if !limit.IsZero() {
		keep := idx.Files[:0]
		for _, f := range idx.Files {
			if t, _ := segmentTime(f.Name); t.Before(limit) {
				if err := os.Remove(filepath.Join(dir, f.Name)); err == nil || os.IsNotExist(err) {
					deleted++
					continue
				} else {
					logf("retention: %v", err)
				}
			}
			keep = append(keep, f)
		}
		if deleted > 0 {
			idx.Files, changed = keep, true
			if st, err := os.Stat(dir); err == nil {
				idx.DirMtime = st.ModTime().UnixNano()
			}
		}
	}
	sum := LocalRecordings{Updated: time.Now(), Files: len(idx.Files)}
	for _, f := range idx.Files {
		sum.Bytes += f.Size
	}
	if len(idx.Files) > 0 {
		sum.Oldest, _ = segmentTime(idx.Files[0].Name)
		sum.Newest, _ = segmentTime(idx.Files[len(idx.Files)-1].Name)
	}
	if err := os.MkdirAll(tech, 0o755); err == nil {
		if changed || !fileExists(filepath.Join(tech, "segments.json")) {
			if err := writeJSON(filepath.Join(tech, "segments.json"), idx); err != nil {
				logf("index %s: %v", dir, err)
			}
		}
		writeJSON(filepath.Join(tech, "summary.json"), sum)
	}
	return sum, deleted
}

// prunePieces forgets the parts whose files are gone (retention, the disk reserve).
func prunePieces(tech string, pieces map[string]int64, files []segFile) {
	if len(pieces) == 0 {
		return
	}
	have := make(map[string]bool, len(files))
	for _, f := range files {
		have[f.Name] = true
	}
	n := len(pieces)
	for k := range pieces {
		if !have[k] {
			delete(pieces, k)
		}
	}
	if len(pieces) != n {
		writeJSON(filepath.Join(tech, piecesFile), pieces)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// MaintainLocal keeps the indexes of all local recorders and enforces their
// retention= (run periodically by the worker): segments whose file name says they
// ended (started + SegmentLength) before now - retention are deleted - one segment
// more is kept, so the oldest recording is at least the full retention old.
func MaintainLocal(s *config.Store, now time.Time, logf func(string, ...any)) {
	MaintainLocalLog(s, "", now, logf)
}

// MaintainLocalLog is MaintainLocal noting the deletions in the system log below root.
func MaintainLocalLog(s *config.Store, root string, now time.Time, logf func(string, ...any)) {
	var entries []eventdb.Entry
	defer func() { SystemLog(root, logf, entries...) }()
	for _, nvr := range s.Items("/nvr") {
		if remote(nvr) {
			continue
		}
		dir := localDir(s, nvr)
		if dir == "" {
			continue
		}
		var limit time.Time
		if r := nvr.Val("retention"); r != "" {
			if keep, err := jobs.ParseLength(r); err == nil {
				// one segment more: a segment is deleted only once it ENDED before
				// now - retention, so the recordings reach back the whole retention
				// (not 19 days 23:45 of retention=20d)
				limit = now.Add(-keep - SegmentLength)
			}
		}
		ents, _ := os.ReadDir(dir)
		total := 0
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				_, n := indexCamera(dir, e.Name(), limit, logf)
				total += n
				cleanThumbs(dir, e.Name(), now)
				cleanMotion(dir, e.Name())
			}
		}
		if total > 0 {
			logf("retention %s: %d segments older than %s deleted", nvr.Val("name"), total, nvr.Val("retention"))
			entries = append(entries, eventdb.Entry{Nvr: nvr.Val("name"), Kind: SysRecordingsDeleted,
				Text: fmt.Sprintf("retention %s: %s deleted", nvr.Val("retention"), plural(total, "segment", "segments"))})
		}
	}
	if reserve := DiskReserve(s); reserve > 0 {
		entries = append(entries, keepReserve(s, reserve, logf)...)
	}
}

// freeSpace of the filesystem of a path (replaced in tests).
var freeSpace = func(path string) (int64, error) {
	u, err := fsusage.Of(path)
	return int64(u.Avail), err
}

// keepReserve: on every storage of local recorders whose free space is below the
// reserve, the oldest segments go (of all cameras on it, the oldest first; never the
// newest of a camera, which may be recording) until the reserve is free again.
func keepReserve(s *config.Store, reserve int64, logf func(string, ...any)) (entries []eventdb.Entry) {
	type seg struct {
		dir, cam, name string
		start          time.Time
	}
	for _, st := range s.Items("/storage") {
		path := st.Val("path")
		free, err := freeSpace(path)
		if err != nil || free >= reserve {
			continue
		}
		var segs []seg
		for _, nvr := range s.Items("/nvr") {
			if remote(nvr) || nvr.Val("storage") != st.Val("name") {
				continue
			}
			dir := localDir(s, nvr)
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				var idx segIndex
				readJSON(filepath.Join(TechDir(dir, e.Name()), "segments.json"), &idx)
				for i, f := range idx.Files {
					if i == len(idx.Files)-1 {
						break // the newest: perhaps being written
					}
					if t, ok := segmentTime(f.Name); ok {
						segs = append(segs, seg{dir, e.Name(), f.Name, t})
					}
				}
			}
		}
		sort.Slice(segs, func(i, j int) bool { return segs[i].start.Before(segs[j].start) })
		deleted := 0
		touched := map[[2]string]bool{}
		for _, sg := range segs {
			if free >= reserve {
				break
			}
			if os.Remove(filepath.Join(sg.dir, sg.cam, sg.name)) == nil {
				deleted++
				touched[[2]string{sg.dir, sg.cam}] = true
			}
			if free, err = freeSpace(path); err != nil {
				break
			}
		}
		for k := range touched {
			indexCamera(k[0], k[1], time.Time{}, logf)
		}
		if deleted > 0 {
			logf("disk reserve %s: %d oldest segments deleted to keep %s free", st.Val("name"), deleted, FormatSize(reserve))
			nvrs := map[string]bool{}
			for k := range touched {
				nvrs[filepath.Base(k[0])] = true
			}
			entries = append(entries, eventdb.Entry{Kind: SysDiskReserve,
				Text: fmt.Sprintf("storage %s: %s of %s deleted to keep %s free", st.Val("name"), plural(deleted, "oldest segment", "oldest segments"), strings.Join(keysOf(nvrs), ", "), FormatSize(reserve))})
		}
		if free < reserve {
			logf("disk reserve %s: still only %s free", st.Val("name"), HumanSize(free))
		}
	}
	return entries
}

// Recordings of a local recorder, from the indexes the worker keeps (zero, with
// Updated zero, when there is none yet).
func Recordings(s *config.Store, name string) LocalRecordings {
	var r LocalRecordings
	nvr := find(s, "/nvr", "name", name)
	if nvr == nil || remote(nvr) {
		return r
	}
	dir := localDir(s, nvr)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".") {
			continue
		}
		var c LocalRecordings
		if !readJSON(filepath.Join(dir, e.Name(), "summary.json"), &c) {
			continue
		}
		if r.Updated.IsZero() || c.Updated.Before(r.Updated) {
			r.Updated = c.Updated // the oldest update counts
		}
		if c.Files > 0 && (r.Files == 0 || c.Oldest.Before(r.Oldest)) {
			r.Oldest = c.Oldest
		}
		if c.Newest.After(r.Newest) {
			r.Newest = c.Newest
		}
		r.Files += c.Files
		r.Bytes += c.Bytes
	}
	return r
}
