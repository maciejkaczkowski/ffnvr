package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SegmentLayout is the file name of a recorded segment (ffmpeg -strftime, local time),
// see the camera units: <storage>/<nvr>/<camera>/%Y-%m-%dT%H-%M-%S.mkv
const SegmentLayout = "2006-01-02T15-04-05"

// Segment is one recorded file and the time it covers.
type Segment struct {
	Path       string
	Start, End time.Time
}

// Segments returns the recorded segments of a directory overlapping [from, to), oldest
// first. The end of a segment is the start of the next one, or its modification time
// when that is earlier (a gap: the camera was down) or when it is the last one.
func Segments(dir string, from, to time.Time) ([]Segment, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var all []Segment
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".mkv") {
			continue
		}
		t, err := time.ParseInLocation(SegmentLayout, strings.TrimSuffix(name, ".mkv"), time.Local)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		all = append(all, Segment{Path: filepath.Join(dir, name), Start: t, End: info.ModTime()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	var out []Segment
	for i, s := range all {
		if i+1 < len(all) && (s.End.After(all[i+1].Start) || !s.End.After(s.Start)) {
			s.End = all[i+1].Start
		}
		if !s.End.After(s.Start) { // last file, just created
			s.End = s.Start.Add(time.Second)
		}
		if s.End.After(from) && s.Start.Before(to) {
			out = append(out, s)
		}
	}
	return out, nil
}

// concatList writes an ffconcat script joining the segments and cutting them to
// [from, to); it returns the script and the expected duration of the result.
func concatList(segs []Segment, from, to time.Time) (string, time.Duration) {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	var total time.Duration
	for _, s := range segs {
		fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(s.Path, "'", `'\''`))
		in, out := time.Duration(0), s.End.Sub(s.Start)
		if from.After(s.Start) {
			in = from.Sub(s.Start)
			fmt.Fprintf(&b, "inpoint %.3f\n", in.Seconds())
		}
		if to.Before(s.End) {
			out = to.Sub(s.Start)
			fmt.Fprintf(&b, "outpoint %.3f\n", out.Seconds())
		}
		total += out - in
	}
	return b.String(), total
}
