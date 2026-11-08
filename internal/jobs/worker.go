package jobs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Worker runs the queued jobs: one at a time per recorder, recorders in parallel.
type Worker struct {
	Spool  Spool
	FFmpeg string        // ffmpeg binary (default "ffmpeg")
	Poll   time.Duration // how often the spool is checked (default 1s)
	Log    *log.Logger   // default: stderr
	// Remote opens the recordings of one part of a job on a remote recorder (Job.Remote):
	// a stream and the ffmpeg arguments describing it (e.g. -f dhav), or ErrEmpty when
	// nothing was recorded in that part.
	Remote func(ctx context.Context, j *Job, p Part) (io.ReadCloser, []string, error)
	// Periodic runs right away and then every Every (e.g. reading the state of remote
	// recorders), independently of the jobs.
	Periodic func(ctx context.Context)
	// Background runs over and over, a second after it returns (light work asked for by
	// the panel, e.g. thumbnails); beside the jobs, never blocking them.
	Background func(ctx context.Context)
	Every      time.Duration
}

// ErrEmpty: nothing was recorded in a part of a remote job.
var ErrEmpty = errors.New("no recordings")

func (w *Worker) defaults() {
	if w.FFmpeg == "" {
		w.FFmpeg = "ffmpeg"
	}
	if w.Poll == 0 {
		w.Poll = time.Second
	}
	if w.Log == nil {
		w.Log = log.New(os.Stderr, "", 0)
	}
}

// Run processes jobs until ctx is cancelled. Jobs interrupted by a stop (or a crash
// of an earlier worker) go back to the queue and start again from the beginning.
func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
	if err := os.MkdirAll(w.Spool.Dir, 0o755); err != nil {
		return err
	}
	jobs, err := w.Spool.List()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.State == Running { // finished parts are kept, the rest starts again
			for _, p := range j.Parts {
				os.Remove(p.Target + ".part")
			}
			j.State, j.Progress, j.Speed = Queued, 0, ""
			w.Spool.Save(j)
		}
	}
	var mu sync.Mutex
	busy := map[string]bool{} // recorder -> a job is running
	var wg sync.WaitGroup
	if w.Periodic != nil {
		every := w.Every
		if every <= 0 {
			every = 5 * time.Minute
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				w.Periodic(ctx)
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
	if w.Background != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				w.Background(ctx)
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}()
	}
	tick := time.NewTicker(w.Poll)
	defer tick.Stop()
	for {
		w.Spool.Beat()
		jobs, err := w.Spool.List()
		if err != nil {
			w.Log.Printf("spool: %v", err)
		}
		for _, j := range jobs {
			if j.State != Queued {
				continue
			}
			if w.Spool.CancelRequested(j.ID) {
				j.State, j.Finished = Cancelled, time.Now()
				w.Spool.Save(j)
				w.Log.Printf("job %d: cancelled before it started", j.ID)
				continue
			}
			mu.Lock()
			free := !busy[j.Nvr]
			if free {
				busy[j.Nvr] = true
			}
			mu.Unlock()
			if !free {
				continue
			}
			wg.Add(1)
			go func(j *Job) {
				defer wg.Done()
				w.process(ctx, j)
				mu.Lock()
				delete(busy, j.Nvr)
				mu.Unlock()
			}(j)
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-tick.C:
		}
	}
}

// process runs one job and records how it ended.
func (w *Worker) process(ctx context.Context, j *Job) {
	j.State, j.Started, j.Error = Running, time.Now(), ""
	w.Spool.Save(j)
	w.Log.Printf("job %d: %s/%s %s - %s -> %s", j.ID, j.Nvr, j.Camera,
		j.Start.Format(TimeFormat), j.End.Format(TimeFormat), j.Target)

	jctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var cancelled atomic.Bool
	go func() { // cancellation requested from the shell
		t := time.NewTicker(w.Poll)
		defer t.Stop()
		for {
			select {
			case <-jctx.Done():
				return
			case <-t.C:
				if w.Spool.CancelRequested(j.ID) {
					cancelled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	err := w.archive(jctx, j)
	cancel()
	j.Finished, j.Speed = time.Now(), ""
	switch {
	case ctx.Err() != nil: // worker stopping: run again next time
		j.State, j.Finished = Queued, time.Time{}
		w.Log.Printf("job %d: interrupted, queued again", j.ID)
	case cancelled.Load():
		j.State = Cancelled
		w.Log.Printf("job %d: cancelled", j.ID)
	case err != nil:
		j.State, j.Error = Failed, err.Error()
		w.Log.Printf("job %d: failed: %v", j.ID, err)
	default:
		j.State, j.Progress, j.Size = Done, 1, 0
		for _, p := range j.Parts {
			j.Size += p.Size
		}
		w.Log.Printf("job %d: done in %s", j.ID, j.Finished.Sub(j.Started).Round(time.Second))
	}
	w.Spool.Save(j)
}

// archive joins the segments of every part of the job into its mp4 file (stream copy:
// no re-encoding, cuts fall on key frames). Parts done earlier are kept; a part without
// recordings (a block where the camera was down) produces no file.
func (w *Worker) archive(ctx context.Context, j *Job) error {
	segs := make([][]Segment, len(j.Parts))
	durs := make([]time.Duration, len(j.Parts))
	var total time.Duration
	for i := range j.Parts {
		p := &j.Parts[i]
		if j.Remote != "" { // what is recorded there is only known when asking
			durs[i] = p.End.Sub(p.Start)
			if p.State != "empty" {
				total += durs[i]
			}
			continue
		}
		var err error
		if segs[i], err = Segments(j.Source, p.Start, p.End); err != nil {
			return err
		}
		if len(segs[i]) == 0 && p.State == "" {
			p.State = "empty"
		}
		if p.State != "empty" {
			_, durs[i] = concatList(segs[i], p.Start, p.End)
			total += durs[i]
		}
	}
	noRecordings := func() error {
		where := j.Source
		if j.Remote != "" {
			where = fmt.Sprintf("nvr %s channel %d", j.Nvr, j.Channel)
		}
		return fmt.Errorf("no recordings between %s and %s in %s", j.Start.Format(TimeFormat), j.End.Format(TimeFormat), where)
	}
	if j.Count("empty") == len(j.Parts) {
		return noRecordings()
	}
	var base time.Duration
	for i := range j.Parts {
		p := &j.Parts[i]
		if p.State != "" {
			if p.State == "done" {
				base += durs[i]
			}
			continue
		}
		var err error
		if j.Remote != "" {
			err = w.archiveRemote(ctx, j, p, base, total)
		} else {
			err = w.archivePart(ctx, j, p, segs[i], base, total)
		}
		if errors.Is(err, ErrEmpty) {
			p.State = "empty"
			w.Spool.Save(j)
			continue
		}
		if err != nil {
			if len(j.Parts) > 1 && ctx.Err() == nil {
				return fmt.Errorf("%s: %w", filepath.Base(p.Target), err)
			}
			return err
		}
		p.State = "done"
		if st, err := os.Stat(p.Target); err == nil {
			p.Size = st.Size()
		}
		base += durs[i]
		if total > 0 {
			j.Progress = min(float64(base)/float64(total), 0.99)
		}
		w.Spool.Save(j)
	}
	if j.Count("empty") == len(j.Parts) {
		return noRecordings()
	}
	return nil
}

func (w *Worker) archivePart(ctx context.Context, j *Job, p *Part, segs []Segment, base, total time.Duration) error {
	if _, err := os.Stat(p.Target); err == nil {
		return fmt.Errorf("%s already exists", p.Target)
	}
	if err := os.MkdirAll(filepath.Dir(p.Target), 0o755); err != nil {
		return err
	}
	list, _ := concatList(segs, p.Start, p.End)
	lf, err := os.CreateTemp("", "ffnvr-concat-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(lf.Name())
	if _, err := lf.WriteString(list); err != nil {
		lf.Close()
		return err
	}
	lf.Close()

	return w.ffmpeg(ctx, j, p, nil, base, total,
		"-nostdin", "-f", "concat", "-safe", "0", "-i", lf.Name(), "-map", "0", "-c", "copy")
}

// archiveRemote reads a part from the remote recorder and converts it on the fly: video
// copied, audio (G.711 and the like, which mp4 does not take) encoded to AAC.
func (w *Worker) archiveRemote(ctx context.Context, j *Job, p *Part, base, total time.Duration) error {
	if w.Remote == nil {
		return fmt.Errorf("remote recorders are not available to this worker")
	}
	if _, err := os.Stat(p.Target); err == nil {
		return fmt.Errorf("%s already exists", p.Target)
	}
	j.Speed = "connecting" // shown until ffmpeg reports (asking the recorder can take a while)
	w.Spool.Save(j)
	rc, input, err := w.Remote(ctx, j, *p)
	if err != nil {
		return err
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 256<<10)
	if _, err := br.Peek(1); err == io.EOF {
		return ErrEmpty
	}
	if err := os.MkdirAll(filepath.Dir(p.Target), 0o755); err != nil {
		return err
	}
	// a raw H.264/H.265 stream: its pictures are counted, as ffmpeg (7) may report no
	// time for a stream it copies without timestamps; and the timestamps it gives the mp4
	// are generated from the frame rate (+genpts)
	var stdin io.Reader = br
	var media func() time.Duration
	if format, fps := rawFormat(input); format != "" && fps > 0 {
		pc := &pictureCounter{r: br, hevc: format == "hevc"}
		stdin = pc
		media = func() time.Duration { return time.Duration(float64(pc.Pictures()) / fps * float64(time.Second)) }
		input = append([]string{"-fflags", "+genpts"}, input...)
	}
	args := append(append([]string{}, input...), "-i", "pipe:0", "-map", "0:v", "-map", "0:a?", "-c:v", "copy", "-c:a", "aac")
	return w.ffmpegWith(ctx, j, p, stdin, media, base, total, args...)
}

// rawFormat finds -f h264|hevc and -framerate in ffmpeg input arguments.
func rawFormat(input []string) (format string, fps float64) {
	for i := 0; i+1 < len(input); i++ {
		switch input[i] {
		case "-f":
			if input[i+1] == "h264" || input[i+1] == "hevc" {
				format = input[i+1]
			}
		case "-framerate":
			fps, _ = strconv.ParseFloat(input[i+1], 64)
		}
	}
	return
}

// pictureCounter passes an Annex-B stream through and counts its pictures: slices
// that begin a picture (H.264 types 1/5 with first_mb_in_slice 0, H.265 VCL units with
// first_slice_segment_in_pic_flag).
type pictureCounter struct {
	r        io.Reader
	hevc     bool
	tail     []byte // the last bytes of the previous read: start codes cross reads
	pictures atomic.Int64
}

func (c *pictureCounter) Pictures() int64 { return c.pictures.Load() }

func (c *pictureCounter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		buf := append(c.tail, p[:n]...)
		need := 3 // after 00 00 01: the NAL header and the first slice byte
		if c.hevc {
			need = 4
		}
		last, pending := 0, -1
		for i := 0; i+2 < len(buf); i++ {
			if buf[i] != 0 || buf[i+1] != 0 || buf[i+2] != 1 {
				continue
			}
			if i+2+need >= len(buf) {
				pending = i // the rest comes with the next read
				break
			}
			h := buf[i+3:]
			if c.hevc {
				if t := (h[0] >> 1) & 0x3f; t <= 31 && h[2]&0x80 != 0 {
					c.pictures.Add(1)
				}
			} else if t := h[0] & 0x1f; (t == 1 || t == 5) && h[1]&0x80 != 0 {
				c.pictures.Add(1)
			}
			last = i + 3
		}
		keep := max(last, len(buf)-2) // 00 00 of a start code may end this read
		if pending >= 0 {
			keep = pending
		}
		c.tail = append(c.tail[:0], buf[keep:]...)
	}
	return n, err
}

// ffmpeg writes the part's mp4 (via .part) from the given input arguments.
func (w *Worker) ffmpeg(ctx context.Context, j *Job, p *Part, stdin io.Reader, base, total time.Duration, input ...string) error {
	return w.ffmpegWith(ctx, j, p, stdin, nil, base, total, input...)
}

// ffmpegWith: media (optional) tells how much material went in, for when ffmpeg does
// not report its progress.
func (w *Worker) ffmpegWith(ctx context.Context, j *Job, p *Part, stdin io.Reader, media func() time.Duration, base, total time.Duration, input ...string) error {
	part := p.Target + ".part"
	defer os.Remove(part)
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, input...)
	args = append(args, "-avoid_negative_ts", "make_zero", "-movflags", "+faststart", "-f", "mp4",
		"-progress", "pipe:1", "-nostats", part)
	cmd := exec.CommandContext(ctx, w.FFmpeg, args...)
	cmd.Stdin = stdin
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// a pipe of our own instead of StdoutPipe: when ffmpeg is killed, WaitDelay makes Wait
	// return even if something still holds its output open
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.WaitDelay = 3 * time.Second
	done := make(chan struct{})
	go func() {
		w.readProgress(pr, j, base, total, media)
		close(done)
	}()
	err := cmd.Run()
	pw.Close()
	<-done
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	return os.Rename(part, p.Target)
}

// readProgress follows "ffmpeg -progress" and saves the job at most once a second.
func (w *Worker) readProgress(r io.Reader, j *Job, base, total time.Duration, media func() time.Duration) {
	last := time.Time{}
	started := time.Now()
	var us int64 = -1
	speed := ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_us", "out_time_ms": // both are microseconds; N/A without timestamps
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				us = n
			}
		case "speed":
			speed = strings.TrimSpace(v)
		case "progress":
			done := us
			if done <= 0 && media != nil {
				done = media().Microseconds() // ffmpeg tells nothing: what we counted
				if el := time.Since(started).Seconds(); el > 1 {
					speed = fmt.Sprintf("%.1fx", float64(done)/1e6/el)
				}
			}
			if done > 0 && total > 0 {
				j.Progress = min(float64(base.Microseconds()+done)/float64(total.Microseconds()), 0.99)
			}
			if speed != "" && speed != "N/A" {
				j.Speed = speed
			}
			if time.Since(last) >= time.Second {
				last = time.Now()
				w.Spool.Save(j)
			}
		}
	}
	io.Copy(io.Discard, r)
}

// ErrNoWorker is returned when jobs wait but nothing runs them.
var ErrNoWorker = errors.New("the ffnvr worker is not running (it is started by commit: ffnvr-worker.service)")
