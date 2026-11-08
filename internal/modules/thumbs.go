package modules

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ffnvr/internal/config"
)

// Thumbnails of local recordings: the first full picture (key frame) of a segment, made
// in the background by the worker when the panel asks for one, kept in the camera's
// technical directory (<storage>/<nvr>/.<camera>/thumbs/<segment>.jpg) and deleted after
// ThumbKeep, or with their segment.

// ThumbDir is where the panel leaves its requests (relative to the root).
const ThumbDir = "var/lib/ffnvr/thumbs"

// ThumbKeep is how long a thumbnail is kept.
const ThumbKeep = 7 * 24 * time.Hour

// ThumbPath of a segment of a local camera ("" when it is not one).
func ThumbPath(s *config.Store, nvr, camera, segment string) string {
	p, err := LocalSegment(s, nvr, camera, segment)
	if err != nil {
		return ""
	}
	return filepath.Join(TechDir(filepath.Dir(filepath.Dir(p)), camera), "thumbs", strings.TrimSuffix(segment, ".mkv")+".jpg")
}

// RequestThumb asks the worker for a thumbnail.
func RequestThumb(root, nvr, camera, segment string) error {
	return request(root, "thumb", nvr, camera, segment)
}

// RequestMotion asks the worker for the motion bar of a segment.
func RequestMotion(root, nvr, camera, segment string) error {
	return request(root, "motion", nvr, camera, segment)
}

func request(root, kind, nvr, camera, segment string) error {
	dir := filepath.Join(root, ThumbDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, kind+"@"+nvr+"@"+camera+"@"+segment), nil, 0o644)
}

// PendingThumbs reports whether the panel waits for thumbnails.
func PendingThumbs(root string) bool {
	ents, _ := os.ReadDir(filepath.Join(root, ThumbDir))
	return len(ents) > 0
}

// MakeThumbs does what the panel asked for: thumbnails first (quick), then motion bars
// (a pass over the segment's key frames, into the event database), at most max things
// in this call.
func MakeThumbs(ctx context.Context, s *config.Store, root, ffmpeg string, max int, logf func(string, ...any)) {
	dir := filepath.Join(root, ThumbDir)
	ents, _ := os.ReadDir(dir)
	var thumbs, motions [][]string
	for _, e := range ents {
		parts := strings.Split(e.Name(), "@")
		switch {
		case len(parts) == 4 && parts[0] == "thumb":
			thumbs = append(thumbs, append([]string{e.Name()}, parts[1:]...))
		case len(parts) == 4 && parts[0] == "motion":
			motions = append(motions, append([]string{e.Name()}, parts[1:]...))
		default:
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	done := 0
	for _, m := range motions {
		if done >= max || ctx.Err() != nil {
			return
		}
		if len(thumbs) > 0 { // thumbnails go first: the panel shows them
			break
		}
		os.Remove(filepath.Join(dir, m[0]))
		if err := MakeMotion(ctx, s, root, ffmpeg, m[1], m[2], m[3]); err != nil {
			logf("motion %s/%s/%s: %v", m[1], m[2], m[3], err)
		}
		done++
	}
	for _, t := range thumbs {
		if done >= max || ctx.Err() != nil {
			return
		}
		done++
		os.Remove(filepath.Join(dir, t[0]))
		parts := t[1:]
		seg, err := LocalSegment(s, parts[0], parts[1], parts[2])
		out := ThumbPath(s, parts[0], parts[1], parts[2])
		if err != nil || out == "" {
			continue
		}
		if _, err := os.Stat(out); err == nil {
			continue
		}
		if err := makeThumb(ctx, ffmpeg, seg, out); err != nil {
			logf("thumbnail %s: %v", seg, err)
		}
	}
}

func makeThumb(ctx context.Context, ffmpeg, seg, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	tmp := out + ".tmp.jpg"
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-skip_frame", "nokey", "-i", seg, "-frames:v", "1", "-vf", "scale=320:-2", "-q:v", "6", tmp).CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(b)))
	}
	os.Chmod(tmp, 0o644)
	return os.Rename(tmp, out)
}

// cleanThumbs deletes the thumbnails of a camera that are old or whose segment is gone.
func cleanThumbs(recDir, camera string, now time.Time) {
	dir := filepath.Join(TechDir(recDir, camera), "thumbs")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		info, err := e.Info()
		seg := filepath.Join(recDir, camera, strings.TrimSuffix(e.Name(), ".jpg")+".mkv")
		_, segErr := os.Stat(seg)
		if err != nil || now.Sub(info.ModTime()) > ThumbKeep || segErr != nil {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
