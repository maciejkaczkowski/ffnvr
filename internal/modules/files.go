package modules

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"ffnvr/internal/config"
)

// FindFiles lists the recorded files of a camera overlapping [from, to): a local
// recorder's segments (from the worker's index), or what a remote recorder finds.
func FindFiles(ctx context.Context, s *config.Store, nvrName, camera string, from, to time.Time) ([]RecFile, error) {
	nvr := find(s, "/nvr", "name", nvrName)
	if nvr == nil {
		return nil, fmt.Errorf("no such nvr: %s", nvrName)
	}
	var cam *config.Item
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvrName && c.Val("name") == camera {
			cam = c
		}
	}
	if cam == nil {
		return nil, fmt.Errorf("nvr %s has no camera %s", nvrName, camera)
	}
	if !remote(nvr) {
		return localFiles(s, nvr, camera, from, to), nil
	}
	r, err := newRecorder(nvr)
	if err != nil {
		return nil, err
	}
	if cl, ok := r.(io.Closer); ok {
		defer cl.Close()
	}
	ch, _ := strconv.Atoi(cam.Val("channel"))
	files, err := r.Files(ctx, ch, from, to)
	sort.Slice(files, func(i, j int) bool { return files[i].Start.Before(files[j].Start) })
	return files, err
}

// localFiles: the segments of a camera, from its index; a segment lasts until the next
// one begins (the last: 15 minutes, the length of the camera units' segments); a part
// cut out by the recording schedule ends where it was cut.
func localFiles(s *config.Store, nvr *config.Item, camera string, from, to time.Time) []RecFile {
	var idx segIndex
	if !readJSON(filepath.Join(TechDir(localDir(s, nvr), camera), "segments.json"), &idx) {
		return nil
	}
	var out []RecFile
	for i, f := range idx.Files {
		start, ok := segmentTime(f.Name)
		if !ok {
			continue
		}
		end := start.Add(15 * time.Minute)
		if i+1 < len(idx.Files) {
			if next, ok := segmentTime(idx.Files[i+1].Name); ok && next.Before(end) {
				end = next
			}
		}
		if f.End > 0 { // a part cut out by the recording schedule
			if e := time.UnixMilli(f.End); e.After(start) && e.Before(end) {
				end = e
			}
		}
		if end.After(from) && start.Before(to) {
			out = append(out, RecFile{Name: f.Name, Start: start, End: end, Size: f.Size})
		}
	}
	return out
}

// LocalFiles lists the recorded segments of a camera of a local recorder overlapping
// [from, to), from the worker's index (the timeline of the player); an error for a
// remote recorder or an unknown camera, never a question to a recorder.
func LocalFiles(s *config.Store, nvrName, camera string, from, to time.Time) ([]RecFile, error) {
	nvr := find(s, "/nvr", "name", nvrName)
	if nvr == nil || remote(nvr) {
		return nil, fmt.Errorf("%s is not a local recorder", nvrName)
	}
	if err := checkName("camera", camera); err != nil {
		return nil, err
	}
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvrName && c.Val("name") == camera {
			return localFiles(s, nvr, camera, from, to), nil
		}
	}
	return nil, fmt.Errorf("nvr %s has no camera %s", nvrName, camera)
}

// LocalSegment is the path of a recorded segment of a local camera, for downloading it;
// the name must be one of the camera's segments.
func LocalSegment(s *config.Store, nvrName, camera, name string) (string, error) {
	nvr := find(s, "/nvr", "name", nvrName)
	if nvr == nil || remote(nvr) {
		return "", fmt.Errorf("%s is not a local recorder", nvrName)
	}
	if _, ok := segmentTime(name); !ok || filepath.Base(name) != name {
		return "", fmt.Errorf("not a segment: %q", name)
	}
	if err := checkName("camera", camera); err != nil {
		return "", err
	}
	return filepath.Join(localDir(s, nvr), camera, name), nil
}

// ArchiveStorages a user may archive to: the storages of types=archive (for now the
// same for every user).
func ArchiveStorages(s *config.Store, user string) []string { return StoragesOf(s, "archive") }
