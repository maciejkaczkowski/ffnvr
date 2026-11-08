package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ffnvr/internal/config"
)

// Thumbnails of all cameras for the Live view (its Thumbnails tab): one picture per
// camera, taken by the worker on a schedule through go2rtc (so every type of recorder
// works, DVRIP too), scaled down by ffmpeg and kept with the outcome of the last try.
//
//	/system/snapshot
//	set enabled=yes interval=daily time=03:00
//	set interval=weekly day=sunday time=02:30
//
// A camera that cannot be read keeps its last picture; the panel shows when it was
// taken and that the last try failed. A changed schedule (and the first enabling)
// takes the pictures right away.

// SnapDir holds the pictures (<nvr>/<camera>.jpg) and state.json, below the root.
const SnapDir = "var/lib/ffnvr/snapshots"

// SnapWidth is the width of the pictures (a tile of the 3×3 grid).
const SnapWidth = 640

var weekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

func snapshotMenu() *config.Menu {
	return &config.Menu{
		Path: "/system/snapshot",
		Kind: config.Single,
		Help: "thumbnails of all cameras for the Live view, taken by the worker through go2rtc (needs /system/live enabled=yes)",
		Fields: []config.Field{
			{Name: "enabled", Type: config.TBool, Help: "take the thumbnails (default no)"},
			{Name: "interval", Type: config.TEnum, Enum: []string{"daily", "weekly"}, Help: "default daily"},
			{Name: "time", Help: "HH:MM, local time (default 03:00)"},
			{Name: "day", Type: config.TEnum, Enum: append(weekdays[1:], weekdays[0]), Help: "interval=weekly: the day (default monday)"},
			{Name: "quality", Type: config.TEnum, Enum: []string{"lq", "hq"}, Help: "the stream to take them from (default lq, the other one when it is missing)"},
			{Name: "attempts", Help: "how many times a camera is tried, every time go2rtc's picture and then ffmpeg on its stream (1 to 10, default 3)"},
		},
		Validate: func(_ *config.Store, it *config.Item) error {
			if v := it.Val("time"); v != "" {
				if _, _, err := parseClock(v); err != nil {
					return err
				}
			}
			if v := it.Val("attempts"); v != "" {
				if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 10 {
					return fmt.Errorf("attempts %q: a whole number from 1 to 10", v)
				}
			}
			return nil
		},
	}
}

func parseClock(v string) (h, m int, err error) {
	hs, ms, ok := strings.Cut(v, ":")
	h, e1 := strconv.Atoi(hs)
	m, e2 := strconv.Atoi(ms)
	if !ok || e1 != nil || e2 != nil || len(ms) != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("time %q: HH:MM expected, e.g. 03:00", v)
	}
	return h, m, nil
}

// Snapshot is the schedule of /system/snapshot.
type Snapshot struct {
	Enabled      bool
	Weekly       bool
	Day          time.Weekday
	Hour, Minute int
	Quality      string
	Attempts     int // tries of a camera
}

// SnapshotSettings reads /system/snapshot.
func SnapshotSettings(s *config.Store) Snapshot {
	it := single(s, "/system/snapshot")
	c := Snapshot{Enabled: it.Val("enabled") == "yes", Weekly: it.Val("interval") == "weekly", Day: time.Monday, Hour: 3, Quality: or(it.Val("quality"), "lq"), Attempts: 3}
	if n, err := strconv.Atoi(it.Val("attempts")); err == nil && n >= 1 {
		c.Attempts = n
	}
	if h, m, err := parseClock(it.Val("time")); err == nil {
		c.Hour, c.Minute = h, m
	}
	for i, d := range weekdays {
		if d == it.Val("day") {
			c.Day = time.Weekday(i)
		}
	}
	return c
}

// String identifies the schedule (a changed one runs at once).
func (c Snapshot) String() string {
	at := fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
	if c.Weekly {
		return "weekly " + weekdays[c.Day] + " " + at + " " + c.Quality
	}
	return "daily " + at + " " + c.Quality
}

// Last is the latest time of the schedule not after now.
func (c Snapshot) Last(now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), c.Hour, c.Minute, 0, 0, now.Location())
	for t.After(now) || (c.Weekly && t.Weekday() != c.Day) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

// Next is the first time of the schedule after now.
func (c Snapshot) Next(now time.Time) time.Time {
	if c.Weekly {
		return c.Last(now).AddDate(0, 0, 7)
	}
	return c.Last(now).AddDate(0, 0, 1)
}

// SnapCam is the outcome for one camera.
type SnapCam struct {
	Taken time.Time `json:"taken,omitempty"` // when the kept picture was taken (zero: none)
	Tried time.Time `json:"tried"`           // the last try
	OK    bool      `json:"ok"`              // the last try got a picture
	Error string    `json:"error,omitempty"` // why it did not
	Via   string    `json:"via,omitempty"`   // the kept picture: go2rtc (frame.jpeg) or ffmpeg (on go2rtc's stream)
	Tries int       `json:"tries,omitempty"` // the tries the last time took
}

// SnapState is what the last runs left (state.json).
type SnapState struct {
	Schedule string             `json:"schedule"`
	LastRun  time.Time          `json:"last_run"`
	Error    string             `json:"error,omitempty"` // the whole run failed (live view off)
	Cams     map[string]SnapCam `json:"cams"`            // by nvr/camera
}

// ReadSnapState reads the state (empty when there is none).
func ReadSnapState(root string) SnapState {
	st := SnapState{Cams: map[string]SnapCam{}}
	if b, err := os.ReadFile(filepath.Join(root, SnapDir, "state.json")); err == nil {
		json.Unmarshal(b, &st)
	}
	if st.Cams == nil {
		st.Cams = map[string]SnapCam{}
	}
	return st
}

func writeSnapState(root string, st SnapState) error {
	b, _ := json.MarshalIndent(st, "", " ")
	p := filepath.Join(root, SnapDir, "state.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// SnapshotPath is the picture of a camera.
func SnapshotPath(root, nvr, camera string) string {
	return filepath.Join(root, SnapDir, nvr, camera+".jpg")
}

// Due tells whether the schedule wants a run now.
func (c Snapshot) Due(st SnapState, now time.Time) bool {
	return c.Enabled && (st.Schedule != c.String() || st.LastRun.Before(c.Last(now)))
}

var snapRunning atomic.Bool

// ScheduleSnapshots starts a run in the background when one is due and none runs
// (called by the worker every minute or so).
func ScheduleSnapshots(ctx context.Context, s *config.Store, root, ffmpeg string, now time.Time, logf func(string, ...any)) {
	c := SnapshotSettings(s)
	if !c.Due(ReadSnapState(root), now) || !snapRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer snapRunning.Store(false)
		TakeSnapshots(ctx, s, root, ffmpeg, logf)
	}()
}

type snapTarget struct{ nvr, camera, stream string }

// TakeSnapshots takes the pictures of all cameras now, four at a time.
func TakeSnapshots(ctx context.Context, s *config.Store, root, ffmpeg string, logf func(string, ...any)) {
	c, l := SnapshotSettings(s), LiveSettings(s)
	st := ReadSnapState(root)
	st.Schedule, st.LastRun, st.Error = c.String(), time.Now(), ""
	if !l.Enabled {
		st.Error = "the live view (/system/live) is off: go2rtc takes the pictures"
		logf("thumbnails: %s", st.Error)
		writeSnapState(root, st)
		return
	}
	streams := LiveStreams(s)
	other := map[string]string{"lq": "hq", "hq": "lq"}[c.Quality]
	var targets []snapTarget
	keep := map[string]bool{}
	for _, cam := range s.Items("/camera") {
		n, name := cam.Val("nvr"), cam.Val("name")
		key := n + "/" + name
		keep[key] = true
		stream := ""
		for _, q := range []string{c.Quality, other} {
			if _, ok := streams[LiveName(n, name, q)]; ok && stream == "" {
				stream = LiveName(n, name, q)
			}
		}
		if stream == "" {
			sc := st.Cams[key]
			sc.Tried, sc.OK, sc.Error = time.Now(), false, "no live stream (disabled, or no address)"
			st.Cams[key] = sc
			continue
		}
		targets = append(targets, snapTarget{n, name, stream})
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	work := make(chan snapTarget)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				var via string
				var err error
				tries := 0
				for tries < c.Attempts {
					tries++
					if via, err = takeSnapshot(ctx, l.API(), ffmpeg, t.stream, SnapshotPath(root, t.nvr, t.camera)); err == nil || tries == c.Attempts {
						break
					}
					select { // a camera starting again needs a moment
					case <-time.After(snapRetryPause):
					case <-ctx.Done():
					}
					if ctx.Err() != nil {
						break
					}
				}
				mu.Lock()
				sc := st.Cams[t.nvr+"/"+t.camera]
				sc.Tried, sc.OK, sc.Error, sc.Tries = time.Now(), err == nil, "", tries
				if err != nil {
					sc.Error = err.Error()
					if tries > 1 {
						sc.Error = fmt.Sprintf("%d tries; the last: %v", tries, err)
					}
				} else {
					sc.Taken, sc.Via = sc.Tried, via
				}
				st.Cams[t.nvr+"/"+t.camera] = sc
				mu.Unlock()
			}
		}()
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		work <- t
	}
	close(work)
	wg.Wait()
	// cameras that are gone: their state and pictures go too
	for k := range st.Cams {
		if !keep[k] {
			delete(st.Cams, k)
			n, cam, _ := strings.Cut(k, "/")
			os.Remove(SnapshotPath(root, n, cam))
		}
	}
	bad, viaFFmpeg := 0, 0
	for _, sc := range st.Cams {
		if !sc.OK {
			bad++
		} else if sc.Via == "ffmpeg" {
			viaFFmpeg++
		}
	}
	if err := writeSnapState(root, st); err != nil {
		logf("thumbnails: %v", err)
	}
	logf("thumbnails: %d cameras, %d not reachable, %d taken by ffmpeg (go2rtc's picture failed)", len(st.Cams), bad, viaFFmpeg)
}

// Timeouts of the two ways to a picture.
var (
	snapRetryPause    = 5 * time.Second  // between two tries of a camera
	snapFrameTimeout  = 30 * time.Second // go2rtc's frame.jpeg
	snapStreamTimeout = 25 * time.Second // ffmpeg on go2rtc's stream: a key frame has to come
)

// takeSnapshot keeps a picture of a stream, scaled down, and says how it got it:
// go2rtc's frame.jpeg first; when that fails, ffmpeg takes the first frame of the
// stream go2rtc gives (stream.mp4: the camera's own video, nothing made into JPEG by
// go2rtc).
func takeSnapshot(ctx context.Context, api, ffmpeg, stream, out string) (via string, err error) {
	pic, err1 := frameJPEG(ctx, api, stream)
	via = "go2rtc"
	if err1 == nil {
		if small, err := scaleJPEG(ctx, ffmpeg, pic); err == nil {
			pic = small
		} // else: kept as it came
	} else {
		if ctx.Err() != nil {
			return "", err1
		}
		var err2 error
		if pic, err2 = streamFrame(ctx, api, ffmpeg, stream); err2 != nil {
			return "", fmt.Errorf("%v; ffmpeg on the stream: %v", err1, err2)
		}
		via = "ffmpeg"
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, pic, 0o644); err != nil {
		return "", err
	}
	return via, os.Rename(tmp, out)
}

// frameJPEG asks go2rtc for a picture.
func frameJPEG(ctx context.Context, api, stream string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, snapFrameTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+api+"/api/frame.jpeg?src="+url.QueryEscape(stream), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("go2rtc: no picture within %v", snapFrameTimeout)
		}
		return nil, fmt.Errorf("go2rtc: %v", err)
	}
	defer resp.Body.Close()
	pic, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, fmt.Errorf("go2rtc: %v", err)
	}
	if resp.StatusCode != http.StatusOK || len(pic) < 4 || pic[0] != 0xff || pic[1] != 0xd8 {
		msg := strings.TrimSpace(string(pic))
		if len(msg) > 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/") {
			msg = ""
		}
		return nil, errors.New(strings.TrimSpace(fmt.Sprintf("go2rtc: no picture (HTTP %d) %s", resp.StatusCode, msg)))
	}
	return pic, nil
}

// streamFrame: ffmpeg reads go2rtc's stream of the camera and keeps its first frame,
// scaled down.
func streamFrame(ctx context.Context, api, ffmpeg, stream string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, snapStreamTimeout)
	defer cancel()
	src := "http://" + api + "/api/stream.mp4?src=" + url.QueryEscape(stream)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-rw_timeout", strconv.Itoa(int(snapStreamTimeout/time.Microsecond)), "-i", src,
		"-an", "-frames:v", "1", "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", SnapWidth),
		"-q:v", "5", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	cmd.Stderr = &stderr
	pic, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("no picture within %v", snapStreamTimeout)
	}
	if err != nil || len(pic) < 4 || pic[0] != 0xff || pic[1] != 0xd8 {
		msg := lastLine(stderr.String())
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "no picture"
		}
		return nil, errors.New(msg)
	}
	return pic, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 200 {
		l = l[:200]
	}
	return l
}

func scaleJPEG(ctx context.Context, ffmpeg string, pic []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-f", "jpeg_pipe", "-i", "pipe:0",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", SnapWidth), "-frames:v", "1", "-q:v", "5", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	cmd.Stdin = bytes.NewReader(pic)
	out, err := cmd.Output()
	if err != nil || len(out) < 4 {
		return nil, fmt.Errorf("ffmpeg: %v", err)
	}
	return out, nil
}

// Thumb is a camera's thumbnail as the panel shows it.
type Thumb struct {
	Nvr, Camera string
	Symbol      string // the alias, else the recorder in capitals and the camera: cam03 ch01
	SnapCam
	Has bool // a picture is there
}

// CameraSymbol: the alias of a camera, else the recorder's name in capitals with the
// camera's name (cam03 ch01).
func CameraSymbol(nvr, camera, alias string) string {
	if alias != "" {
		return alias
	}
	return strings.ToUpper(nvr) + " " + camera
}

// Thumbs lists the cameras of the given recorders with their thumbnails, by symbol.
func Thumbs(s *config.Store, root string, nvrs map[string]bool) []Thumb {
	st := ReadSnapState(root)
	var out []Thumb
	for _, cam := range s.Items("/camera") {
		n, name := cam.Val("nvr"), cam.Val("name")
		if !nvrs[n] {
			continue
		}
		t := Thumb{Nvr: n, Camera: name, Symbol: CameraSymbol(n, name, cam.Val("alias")), SnapCam: st.Cams[n+"/"+name]}
		if _, err := os.Stat(SnapshotPath(root, n, name)); err == nil {
			t.Has = true
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return natLess(out[i].Symbol, out[j].Symbol)
	})
	return out
}

// natLess compares with the numbers in the strings as numbers (cam08 before
// cam04), letters without regard to case.
func natLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digits(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}
