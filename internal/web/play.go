package web

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ffnvr/internal/jobs"
	"ffnvr/internal/modules"
)

// The player of the archive: a local segment (mkv) is repacked into an mp4 with its
// index in front (faststart, the video copied, nothing re-encoded), so that browsers
// play it and can jump anywhere in it (Range requests). The mp4 files are kept for a
// while in a directory of their own below the temporary directory (PrivateTmp of
// ffnvr-www.service: gone when it restarts).

const (
	playKeep  = 2 * time.Hour   // an mp4 not asked for this long goes
	playFiles = 24              // at most this many are kept…
	playBusy  = 5 * time.Minute // …but one asked for this recently is being played: kept
)

var playTimeout = 3 * time.Minute

// playChunk: the most one answer of the player carries. A <video> asks for its file from
// where it stands to the end ("bytes=N-") and keeps the connection while it reads; a
// browser opens only 6 connections to a host (HTTP/1.1), so the 7th tile of the
// synchronous player (and every other request of the panel) would wait for one of them
// for good. An answer of a part (206, Content-Range of fewer bytes, as HTTP allows) ends
// soon; the browser asks for the next part, and the connections go round all the tiles.
const playChunk = 4 << 20

// capRange limits a single range "bytes=a-" or "bytes=a-b" to playChunk bytes; other
// forms are left as they are (http.ServeContent answers them).
func capRange(h string) string {
	spec, ok := strings.CutPrefix(h, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return h
	}
	from, to, ok := strings.Cut(spec, "-")
	a, err := strconv.ParseInt(strings.TrimSpace(from), 10, 64)
	if !ok || err != nil || a < 0 {
		return h // a suffix (bytes=-N) or nonsense
	}
	limit := a + playChunk - 1
	if b, err := strconv.ParseInt(strings.TrimSpace(to), 10, 64); err == nil && b < limit {
		return h
	}
	return fmt.Sprintf("bytes=%d-%d", a, limit)
}

type playCache struct {
	mu      sync.Mutex
	dir     string
	ffmpeg  string
	pending map[string]*making      // being made: the others wait for it
	slots   map[string]*streamSlots // the streams read from a remote recorder at once
}

// making: an mp4 being made; ahead is true while only read-ahead asked for it.
type making struct {
	done  chan struct{}
	ahead atomic.Bool
}

// streamSlots: the streams of a recorder in use, at most modules.PlayStreams; read-ahead
// leaves one free for the pieces being played.
type streamSlots struct {
	used int
	wake chan struct{} // closed when a slot is freed or a wait gets urgent
}

func newPlayCache(ffmpeg string) *playCache {
	return &playCache{dir: filepath.Join(os.TempDir(), "ffnvr-play"), ffmpeg: ffmpeg,
		pending: map[string]*making{}, slots: map[string]*streamSlots{}}
}

// playSrc is what an mp4 is made of: a local segment (file) or a range read from a
// remote recorder (open, at most modules.PlayStreams at once from one recorder).
type playSrc struct {
	key    string // the identity of the content
	file   string
	open   func(ctx context.Context) (io.ReadCloser, []string, error) // the stream and its ffmpeg input arguments
	nvr    string                                                     // the recorder of open
	from   time.Time                                                  // the start of the range of open
	hevc   bool                                                       // the browser plays H.265
	ahead  bool                                                       // asked for ahead of time (read-ahead), not to play now
	logged string                                                     // what the log names
}

// fileSrc: a local segment; one still being recorded has another size or time: made again.
func fileSrc(seg string, hevc bool) (playSrc, error) {
	st, err := os.Stat(seg)
	if err != nil {
		return playSrc{}, err
	}
	return playSrc{key: fmt.Sprintf("%s|%d|%d", seg, st.Size(), st.ModTime().UnixNano()), file: seg, hevc: hevc, logged: seg}, nil
}

// slot takes one of the stream slots of a recorder: waits for it, or for ctx. A
// read-ahead (m.ahead) takes one only when another stays free.
func (c *playCache) slot(ctx context.Context, nvr string, m *making) (func(), error) {
	for {
		c.mu.Lock()
		sl := c.slots[nvr]
		if sl == nil {
			sl = &streamSlots{wake: make(chan struct{})}
			c.slots[nvr] = sl
		}
		free := modules.PlayStreams - sl.used
		if free > 1 || free == 1 && !m.ahead.Load() {
			sl.used++
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				sl.used--
				close(sl.wake)
				sl.wake = make(chan struct{})
				c.mu.Unlock()
			}, nil
		}
		wake := sl.wake
		c.mu.Unlock()
		select {
		case <-wake:
		case <-time.After(time.Second): // an ahead turned urgent: look again
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// get returns the mp4 of a source, made when it is not there.
func (c *playCache) get(ctx context.Context, src playSrc) (string, error) {
	variant := "h264"
	if src.hevc {
		variant = "hevc"
	}
	sum := sha1.Sum([]byte(src.key + "|" + variant + "|2")) // 2: kept whole, X-Start
	key := hex.EncodeToString(sum[:10])
	out := filepath.Join(c.dir, key+".mp4")
	for {
		c.mu.Lock()
		if _, err := os.Stat(out); err == nil {
			c.mu.Unlock()
			now := time.Now()
			os.Chtimes(out, now, now) // asked for: kept longer
			return out, nil
		}
		m, busy := c.pending[key]
		if !busy {
			m = &making{done: make(chan struct{})}
			m.ahead.Store(src.ahead)
			c.pending[key] = m
			c.mu.Unlock()
			err := c.make(ctx, src, out, m)
			c.mu.Lock()
			delete(c.pending, key)
			close(m.done)
			c.mu.Unlock()
			if err != nil {
				return "", err
			}
			c.prune(out)
			return out, nil
		}
		if !src.ahead {
			m.ahead.Store(false) // wanted now: no longer only read-ahead
		}
		c.mu.Unlock()
		select {
		case <-m.done: // made by another request: look again
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// probeBytes: the start of a stream that tells its codec (an I frame of 3K H.265 included)
const probeBytes = 1 << 20

var videoCodecRe = regexp.MustCompile(`Stream #\d+:\d+.*?: Video: (\w+)`)

// missingSetRe: the decoder found no parameter set the stream names - in the header of
// the file (hvcC, CodecPrivate), where it reads them first
var missingSetRe = regexp.MustCompile(`\b[VSP]PS \d+ does not exist`)

// codec names the video codec of an input (ffmpeg -i, without an output): "" unknown.
func (c *playCache) codec(ctx context.Context, in []string, stdin io.Reader) string {
	codec, _ := c.probe(ctx, in, stdin)
	return codec
}

// probe: the video codec of an input, and whether the parameter sets of its header
// are short of one (H.265 recorded from a camera whose RTSP description had
// sprop-sps and sprop-pps but no sprop-vps: hvcC without the VPS; the frames carry
// them, so ffmpeg decodes after its "VPS 0 does not exist", a browser does not).
func (c *playCache) probe(ctx context.Context, in []string, stdin io.Reader) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := append([]string{"-hide_banner"}, in...)
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	cmd.Stdin = stdin            // nil: none
	b, _ := cmd.CombinedOutput() // "At least one output file must be specified": fine
	if m := videoCodecRe.FindSubmatch(b); m != nil {
		return string(m[1]), missingSetRe.Match(b)
	}
	return "", false
}

func (c *playCache) make(ctx context.Context, src playSrc, out string, m *making) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	if src.open == nil {
		return c.convert(ctx, src, out)
	}
	// a stream slot of the recorder, waited for while someone asks
	release, err := c.slot(ctx, src.nvr, m)
	if err != nil {
		return err
	}
	defer release()
	// a recorder busy with other streams fails now and then: once more, after a while
	for try := 0; ; try++ {
		err = c.convert(ctx, src, out)
		if err == nil || errors.Is(err, jobs.ErrEmpty) || ctx.Err() != nil || try == 1 {
			return err
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// convert makes the mp4 of a source.
func (c *playCache) convert(ctx context.Context, src playSrc, out string) error {
	// not tied to the request: a browser asking again (Range) needs it all the same
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playTimeout)
	defer cancel()
	var in []string
	var stdin io.Reader
	var codec string
	start := "" // where the first frame is against the start of the range (s)
	if src.open != nil {
		body, args, err := src.open(ctx)
		if err != nil {
			return err
		}
		defer body.Close()
		head := make([]byte, probeBytes)
		n, err := io.ReadFull(body, head)
		if n == 0 {
			if err == nil || err == io.EOF || err == io.ErrUnexpectedEOF {
				return jobs.ErrEmpty
			}
			return err
		}
		head = head[:n]
		if args[len(args)-1] == "dhav" && !bytes.Contains(head, []byte("DHAV")) {
			return jobs.ErrEmpty // what a recorder answers for a range without recordings
		}
		// the time of the first frame against the start of the range: a range read
		// begins at a key frame before it, or after it when nothing was recorded at its
		// start. The mp4 keeps all of it, from that key frame (an offset of its time
		// would make ffmpeg drop the frames before zero, the key frame among them); the
		// player is told where it begins (X-Start) and shows it from the range's start.
		if t, ok := modules.PlayStart(body, head); ok && src.from.Unix() > 0 {
			if d := t.Sub(src.from); d != 0 && d.Abs() < time.Minute {
				start = strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
			}
		}
		in = append(append([]string{}, args...), "-i", "pipe:0")
		codec = c.codec(ctx, in, bytes.NewReader(head))
		stdin = io.MultiReader(bytes.NewReader(head), body)
	} else {
		in = []string{"-i", src.file}
		var short bool
		codec, short = c.probe(ctx, in, nil)
		if codec == "hevc" && src.hevc && short {
			// copied as it is the mp4 would get the same hvcC, without the VPS: one
			// the browser cannot decode by
			return c.rebuildHeader(ctx, src.file, out)
		}
	}
	// H.265 plays where the browser has its decoder (Safari, Chrome and Edge with one);
	// elsewhere it is made H.264, smaller (it costs the server: one core about real time)
	video := []string{"-c:v", "copy"}
	switch {
	case codec == "hevc" && src.hevc:
		video = append(video, "-tag:v", "hvc1")
	case codec == "hevc":
		video = []string{"-vf", "scale=-2:'min(720,ih)'", "-c:v", "libx264", "-preset", "veryfast", "-crf", "26", "-pix_fmt", "yuv420p", "-threads", "0"}
	}
	tmp := out + ".tmp"
	defer os.Remove(tmp)
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, in...)
	args = append(args, "-map", "0:v:0", "-map", "0:a:0?")
	args = append(args, video...)
	args = append(args, "-c:a", "aac")
	args = append(args, "-movflags", "+faststart", "-f", "mp4", tmp)
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	cmd.Stdin = stdin
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v %s", err, strings.TrimSpace(string(b)))
	}
	if st, err := os.Stat(tmp); err != nil || st.Size() < 100 {
		return jobs.ErrEmpty
	}
	os.Remove(out + startExt)
	if start != "" {
		if err := os.WriteFile(out+startExt, []byte(start), 0o600); err != nil {
			return err
		}
	}
	return os.Rename(tmp, out)
}

// rebuildHeader makes the mp4 of an H.265 file whose header lacks a parameter set from
// the parameter sets its frames carry: the frames go out as Annex B in MPEG-TS (no
// header there; hevc_mp4toannexb puts the header's sets before the key frames, beside
// those of the frame), and a second ffmpeg reading them builds the hvcC anew from the
// first key frame - VPS, SPS, PPS - with the times kept. Nothing is decoded.
func (c *playCache) rebuildHeader(ctx context.Context, file, out string) error {
	tmp := out + ".tmp"
	defer os.Remove(tmp)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	demux := exec.CommandContext(ctx, c.ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", file,
		"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "copy", "-bsf:v", "hevc_mp4toannexb", "-c:a", "aac", "-f", "mpegts", "pipe:1")
	mux := exec.CommandContext(ctx, c.ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-probesize", "32M", "-analyzeduration", "30M", "-f", "mpegts", "-i", "pipe:0",
		"-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-tag:v", "hvc1", "-movflags", "+faststart", "-f", "mp4", tmp)
	pipe, err := demux.StdoutPipe()
	if err != nil {
		return err
	}
	mux.Stdin = pipe
	var demuxErr, muxErr bytes.Buffer
	demux.Stderr, mux.Stderr = &demuxErr, &muxErr
	if err := demux.Start(); err != nil {
		return err
	}
	merr := mux.Run()
	if merr != nil {
		cancel() // the reader is gone: the writer would wait
	}
	derr := demux.Wait()
	switch {
	case merr != nil:
		return fmt.Errorf("ffmpeg (the header of H.265 rebuilt): %v %s", merr, strings.TrimSpace(muxErr.String()))
	case derr != nil:
		return fmt.Errorf("ffmpeg (the header of H.265 rebuilt): %v %s", derr, strings.TrimSpace(demuxErr.String()))
	}
	if st, err := os.Stat(tmp); err != nil || st.Size() < 100 {
		return jobs.ErrEmpty
	}
	os.Remove(out + startExt)
	return os.Rename(tmp, out)
}

// startExt: beside an mp4 of a remote piece, where its first frame is against the
// piece's start in seconds (X-Start; none: at it)
const startExt = ".start"

// startOf: the X-Start of an mp4.
func startOf(mp4 string) string {
	b, err := os.ReadFile(mp4 + startExt)
	if err != nil {
		return ""
	}
	if _, err := strconv.ParseFloat(string(b), 64); err != nil {
		return ""
	}
	return string(b)
}

// prune removes old mp4 files and keeps at most playFiles (not keep: just made).
func (c *playCache) prune(keep string) {
	ents, _ := os.ReadDir(c.dir)
	type f struct {
		path string
		t    time.Time
	}
	var fs []f
	for _, e := range ents {
		p := filepath.Join(c.dir, e.Name())
		info, err := e.Info()
		if err != nil || p == keep || !strings.HasSuffix(p, ".mp4") {
			continue
		}
		if time.Since(info.ModTime()) > playKeep {
			os.Remove(p)
			os.Remove(p + startExt)
			continue
		}
		fs = append(fs, f{p, info.ModTime()})
	}
	// the synchronous player plays up to 16 tiles and asks for their next segments ahead
	sort.Slice(fs, func(i, j int) bool { return fs[i].t.After(fs[j].t) })
	for i := playFiles - 1; i < len(fs); i++ {
		if time.Since(fs[i].t) > playBusy {
			os.Remove(fs[i].path)
			os.Remove(fs[i].path + startExt)
		}
	}
}

// GET /archive/play?n=&c=&f=: a local segment as mp4 for the player.
func (s *Server) archivePlay(w http.ResponseWriter, r *http.Request) {
	store, se, name, ok := s.archiveRequest(w, r, modules.PermArchivePlay)
	if !ok {
		return
	}
	cam, f, hevc := r.FormValue("c"), r.FormValue("f"), r.FormValue("hevc") == "1"
	var src playSrc
	if from, to, ok := remoteChunk(f); ok && modules.Playable(store, name) && !isLocal(store, name) {
		// a range of a remote recorder: one still being recorded is read again later
		key := fmt.Sprintf("remote|%s|%s|%d|%d", name, cam, from.Unix(), to.Unix())
		if time.Since(to) < 2*time.Minute {
			key += fmt.Sprintf("|%d", time.Now().Unix()/30)
		}
		src = playSrc{key: key, nvr: name, from: from, hevc: hevc, ahead: r.FormValue("ahead") == "1", logged: fmt.Sprintf("%s %s %s", name, cam, f),
			open: func(ctx context.Context) (io.ReadCloser, []string, error) {
				return modules.OpenPlay(ctx, store, name, cam, from, to)
			}}
	} else {
		seg, err := modules.LocalSegment(store, name, cam, f)
		if err == nil {
			src, err = fileSrc(seg, hevc)
		}
		if err != nil {
			code := http.StatusBadRequest
			if os.IsNotExist(err) {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
	}
	w = longAnswer(w) // the mp4 is made first; then a slow connection
	if r.FormValue("ahead") != "1" && (r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-")) {
		s.opt.Log.Printf("web: %s plays %s", se.user, src.logged)
	}
	mp4, err := s.play.get(r.Context(), src)
	if err != nil {
		switch {
		case errors.Is(err, jobs.ErrEmpty), os.IsNotExist(err):
			http.Error(w, "no recording", http.StatusNotFound)
		case r.Context().Err() != nil: // the browser went away (another tile, another time)
		default:
			s.opt.Log.Printf("web: play %s: %v", src.logged, err)
			http.Error(w, "the file cannot be played", http.StatusInternalServerError)
		}
		return
	}
	f2, err := os.Open(mp4)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f2.Close()
	st, _ := f2.Stat()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, no-store")
	if v := startOf(mp4); v != "" {
		w.Header().Set("X-Start", v) // the first frame, seconds after the piece's start
	}
	if rg := r.Header.Get("Range"); rg != "" {
		r.Header.Set("Range", capRange(rg))
	}
	http.ServeContent(w, r, "", st.ModTime(), f2)
}
