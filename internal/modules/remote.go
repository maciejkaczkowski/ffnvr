package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/dahua"
	"ffnvr/internal/dvrip"
	"ffnvr/internal/jobs"
	"ffnvr/internal/script"
	"ffnvr/internal/tiandy"
	"ffnvr/internal/uniview"
)

// A remote recorder keeps its recordings itself; ffnvr reads its cameras, its state and
// its recordings over the maker's API. recorder hides the make (dahua.go, uniview.go).
type recorder interface {
	Device(ctx context.Context) (Device, error)
	// Channels lists the video channels; statesErr: which ones have a camera is unknown
	Channels(ctx context.Context) (chs []Channel, statesErr error, err error)
	Disks(ctx context.Context) ([]Disk, error)
	Time(ctx context.Context) (time.Time, error)
	// StreamURL is the live RTSP address of a channel without credentials; sub 0 the
	// main stream (HQ), 1 the sub stream (LQ); "" when the recorder tells none.
	StreamURL(ctx context.Context, channel, sub int) string
	// Open plays [from, to) of a channel: the stream and the ffmpeg arguments describing
	// it (e.g. -f dhav), or jobs.ErrEmpty when nothing was recorded there
	Open(ctx context.Context, channel int, from, to time.Time) (io.ReadCloser, []string, error)
	// Probe shows the raw answers of the recorder (diagnostics), passwords masked
	Probe(ctx context.Context) string
	// Reboot restarts the recorder.
	Reboot(ctx context.Context) error
	// Backup exports the recorder's configuration: the file and its extension.
	Backup(ctx context.Context) (io.ReadCloser, string, error)
	// Files lists the recorded files of a channel overlapping [from, to)
	// (ErrNoSearch when the recorder cannot be searched).
	Files(ctx context.Context, channel int, from, to time.Time) ([]RecFile, error)
}

// ErrNoSearch: the recorder offers no search of its files.
var ErrNoSearch = errors.New("this recorder cannot be searched for files; use the range download")

// RecFile is one recorded file.
type RecFile struct {
	Name       string
	Start, End time.Time
	Size       int64  // bytes, 0: unknown
	Events     []Span // what was recorded for an alarm (motion), when the recorder tells
}

// Span of time.
type Span struct{ Start, End time.Time }

// newRecorder connects to a remote recorder of the configuration.
func newRecorder(nvr *config.Item) (recorder, error) {
	switch nvr.Val("type") {
	case "dahua":
		c, err := dahua.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"), true)
		return &dahuaRecorder{c: c, cgi: nvr.Val("api") == "cgi"}, err
	case "uniview":
		c, err := uniview.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"), true)
		speed, _ := strconv.ParseFloat(nvr.Val("speed"), 64)
		return univiewRecorder{c, speed}, err
	case "tiandy":
		c, err := tiandy.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"))
		if err != nil {
			return nil, err
		}
		c.FPS, _ = strconv.ParseFloat(nvr.Val("fps"), 64)
		c.HTTPPort, _ = strconv.Atoi(nvr.Val("http-port"))
		return &tiandyRecorder{c: c}, nil
	case "dvrip":
		c, err := dvrip.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"))
		if err != nil {
			return nil, err
		}
		return &dvripRecorder{c: c}, nil
	}
	return nil, fmt.Errorf("nvr %s: type=%s is not a remote recorder", nvr.Val("name"), nvr.Val("type"))
}

// Device describes a recorder; what a make does not tell stays empty.
type Device struct {
	Type     string // model
	Serial   string
	Firmware string
	Name     string
	Build    string        `json:",omitempty"` // firmware build date
	Uptime   time.Duration `json:",omitempty"`
}

// Channel is one video channel of a recorder.
type Channel struct {
	Number  int    // from 1
	Title   string // as named on the recorder
	Address string `json:",omitempty"` // the camera's address, when the recorder tells it
	Detail  string `json:",omitempty"` // e.g. the resolution, the camera model
	Serial  string `json:",omitempty"` // the camera's serial number
	// Connected, Disconnected, Connecting, Empty (no camera), Configured (only the
	// configuration is known), Disabled; "" when the recorder told nothing
	State string
}

// Active reports whether a camera works on the channel; unknown state counts as active.
func (ch Channel) Active() bool {
	return ch.State == "" || strings.EqualFold(ch.State, "Connected") || ch.State == "Configured"
}

// Disk is one storage device of a recorder.
type Disk struct {
	Name       string
	State      string
	TotalBytes int64
	UsedBytes  int64
	Error      bool
	Model      string `json:",omitempty"`
	Serial     string `json:",omitempty"`
}

// StatusDir keeps the last known state of every remote recorder (below the root).
const StatusDir = "/var/lib/ffnvr/status"

// NvrStatus is what ffnvr last learned about a remote recorder.
type NvrStatus struct {
	Checked   time.Time `json:"checked"`
	LastOK    time.Time `json:"last_ok,omitempty"`
	Error     string    `json:"error,omitempty"`
	Device    Device    `json:"device"`
	Channels  int       `json:"channels"`
	Connected int       `json:"connected"`
	// StatesError: the recorder did not tell which channels have a camera (Connected
	// then counts every channel and means nothing)
	StatesError string  `json:"states_error,omitempty"`
	Disks       []Disk  `json:"disks,omitempty"`
	Clock       float64 `json:"clock_offset,omitempty"` // seconds the recorder's clock is ahead
	// ChannelList: every channel as last read, with its state
	ChannelList []Channel `json:"channel_list,omitempty"`
}

// ReadStatus returns the last known state of a remote recorder (nil: never read).
func ReadStatus(root, nvr string) *NvrStatus { return readStatus(root, nvr) }

func statusFile(root, nvr string) string { return filepath.Join(root, StatusDir, nvr+".json") }

func readStatus(root, nvr string) *NvrStatus {
	b, err := os.ReadFile(statusFile(root, nvr))
	if err != nil {
		return nil
	}
	var st NvrStatus
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return &st
}

func writeStatus(root, nvr string, st *NvrStatus) error {
	dir := filepath.Join(root, StatusDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp, err := os.CreateTemp(dir, ".status-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	os.Chmod(tmp.Name(), 0o644)
	return os.Rename(tmp.Name(), statusFile(root, nvr))
}

// fetchStatus reads everything about a recorder; on failure the previous state is kept
// and the error noted.
func fetchStatus(ctx context.Context, root string, nvr *config.Item) (*NvrStatus, []Channel, error) {
	st := readStatus(root, nvr.Val("name"))
	if st == nil {
		st = &NvrStatus{}
	}
	st.Checked = time.Now()
	fail := func(err error) (*NvrStatus, []Channel, error) {
		st.Error = err.Error()
		writeStatus(root, nvr.Val("name"), st)
		return st, nil, err
	}
	c, err := newRecorder(nvr)
	if err != nil {
		return fail(err)
	}
	if cl, ok := c.(io.Closer); ok {
		defer cl.Close() // a session kept for the questions below
	}
	dev, err := c.Device(ctx)
	if err != nil {
		return fail(err)
	}
	chs, statesErr, err := c.Channels(ctx)
	if err != nil {
		return fail(err)
	}
	st.Device, st.Channels, st.Connected, st.Error, st.StatesError = dev, len(chs), 0, "", ""
	st.ChannelList = chs
	if statesErr != nil {
		st.StatesError = statesErr.Error()
	}
	for _, ch := range chs {
		if ch.Active() {
			st.Connected++
		}
	}
	st.Disks, _ = c.Disks(ctx) // not every account may read the disks
	st.Clock = 0
	if t, err := c.Time(ctx); err == nil {
		st.Clock = math.Round(t.Sub(time.Now()).Seconds())
	}
	st.LastOK = st.Checked
	return st, chs, writeStatus(root, nvr.Val("name"), st)
}

// RefreshStatus reads the state of every enabled remote recorder whose last check is
// older than the poll interval (/system/settings poll-interval); the worker calls it
// often, every recorder is read when it is due. Errors are recorded and logged.
func RefreshStatus(ctx context.Context, s *config.Store, root string, logf func(string, ...any)) {
	every := PollInterval(s)
	for _, nvr := range s.Items("/nvr") {
		if !remote(nvr) || nvr.Val("disabled") == "yes" {
			continue
		}
		prev := readStatus(root, nvr.Val("name"))
		if prev != nil && time.Since(prev.Checked) < every-10*time.Second {
			continue // not due yet
		}
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		cur, _, err := fetchStatus(cctx, root, nvr)
		if err != nil && ctx.Err() == nil {
			logf("nvr %s: %v", nvr.Val("name"), err)
		}
		cancel()
		if ctx.Err() == nil {
			SystemLog(root, logf, statusChanges(nvr.Val("name"), prev, cur, channelCameras(s, nvr.Val("name")))...)
		}
	}
}

func human(n int64) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.1fT", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.0fG", float64(n)/1e9)
	default:
		return fmt.Sprintf("%dM", n/1e6)
	}
}

func describeStatus(st *NvrStatus) string {
	if st == nil {
		return "not checked yet (/nvr scan, or the worker within a few minutes)"
	}
	var parts []string
	if st.Device.Type != "" {
		d := st.Device.Type
		if st.Device.Firmware != "" {
			d += " fw " + st.Device.Firmware
		}
		if st.Device.Serial != "" {
			d += " sn " + st.Device.Serial
		}
		parts = append(parts, d)
		if st.StatesError != "" {
			parts = append(parts, fmt.Sprintf("%d channels, camera state unknown", st.Channels))
		} else {
			parts = append(parts, fmt.Sprintf("cameras %d/%d online", st.Connected, st.Channels))
		}
	}
	if len(st.Disks) > 0 {
		var total, used int64
		bad := 0
		for _, d := range st.Disks {
			total += d.TotalBytes
			used += d.UsedBytes
			if d.Error || (d.State != "" && d.State != "Success") {
				bad++
			}
		}
		disk := fmt.Sprintf("disks %d: %s/%s", len(st.Disks), human(used), human(total))
		if total > 0 {
			disk += fmt.Sprintf(" (%.0f%%)", 100*float64(used)/float64(total))
		}
		if bad > 0 {
			disk += fmt.Sprintf(", %d FAILING", bad)
		}
		parts = append(parts, disk)
	}
	if math.Abs(st.Clock) >= 2 {
		c := fmt.Sprintf("clock %+.0fs", st.Clock)
		if math.Abs(st.Clock) >= 60 {
			c += " (downloads are shifted!)"
		}
		parts = append(parts, c)
	}
	line := strings.Join(parts, "; ")
	if st.Error != "" {
		e := "UNREACHABLE: " + st.Error
		if !st.LastOK.IsZero() {
			e += " (last ok " + st.LastOK.Format("2006-01-02 15:04") + ")"
		}
		if line == "" {
			return e
		}
		return e + "\n       " + line
	}
	return line + "; checked " + st.Checked.Format("15:04:05")
}

// ---------------------------------------------------------------- scan

var defaultTitle = regexp.MustCompile(`(?i)^(channel|kanal|kanał|ch|ipc|cam|camera|kamera|d|ip[ _-]?cam(era)?)[ _-]?\d*$`)

var translit = strings.NewReplacer(
	"ą", "a", "ć", "c", "ę", "e", "ł", "l", "ń", "n", "ó", "o", "ś", "s", "ź", "z", "ż", "z",
	"Ą", "A", "Ć", "C", "Ę", "E", "Ł", "L", "Ń", "N", "Ó", "O", "Ś", "S", "Ź", "Z", "Ż", "Z")

// AliasFromTitle turns a channel title of the recorder into an alias: Polish letters
// lose their accents, other characters become "_". Default titles ("Channel2", "IPC")
// give no alias.
func AliasFromTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" || defaultTitle.MatchString(title) {
		return ""
	}
	var b strings.Builder
	for _, r := range translit.Replace(title) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else if !strings.HasSuffix(b.String(), "_") {
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_-.")
}

func camName(ch, width int) string { return fmt.Sprintf("ch%0*d", width, ch) }

// scanRecorder reads a recorder and adds a camera for every active channel that has none:
// name chNN (NN = channel, a stable index), alias from the channel title when it is
// usable. Cameras that exist keep their name and alias; one without an alias gets it.
func scanRecorder(s *config.Store, root string, nvr *config.Item) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := nvr.Val("name")
	st, chs, err := fetchStatus(ctx, root, nvr)
	if err != nil {
		return "", fmt.Errorf("/nvr: %s (%s): %w", name, nvr.Val("host"), err)
	}
	c, err := newRecorder(nvr)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", name, describeStatus(st))
	if st.StatesError != "" {
		fmt.Fprintf(&b, "  warning: the recorder did not tell which channels have a camera, all channels are taken\n"+
			"    (%s)\n    remove the empty ones by hand; /nvr probe %s shows the recorder's answers\n", st.StatesError, name)
	}
	width := max(2, len(strconv.Itoa(len(chs))))
	exec := func(verb string, find, params []script.Param) error {
		_, err := s.Exec(script.Command{Path: "/camera", Verb: verb, Params: params, Find: find, HasFind: find != nil})
		return err
	}
	for _, ch := range chs {
		var cam *config.Item
		for _, x := range s.Items("/camera") {
			if x.Val("nvr") == name && x.Val("channel") == strconv.Itoa(ch.Number) {
				cam = x
			}
		}
		alias := AliasFromTitle(ch.Title)
		title := ""
		if ch.Title != "" {
			title = fmt.Sprintf(" (title %q)", ch.Title)
		}
		switch {
		case cam != nil:
			msg := fmt.Sprintf("  %s: exists as %s", camName(ch.Number, width), cam.Val("name"))
			// live addresses the camera lacks (cameras added before they were read)
			var fill []script.Param
			for sub, key := range []string{"stream", "stream-lq"} {
				if cam.Val(key) == "" {
					if u := c.StreamURL(ctx, ch.Number, sub); u != "" {
						fill = append(fill, script.Param{Key: key, Value: u})
					}
				}
			}
			if fill != nil {
				if err := exec("set", []script.Param{{Key: "name", Value: cam.Val("name")}, {Key: "nvr", Value: name}}, fill); err == nil {
					msg += ", live addresses read"
				}
			}
			if cam.Val("alias") == "" && alias != "" {
				if err := exec("set", []script.Param{{Key: "name", Value: cam.Val("name")}, {Key: "nvr", Value: name}},
					[]script.Param{{Key: "alias", Value: alias}}); err != nil {
					msg += ", alias " + alias + " not set: " + err.Error()
				} else {
					msg += ", alias " + alias + title
				}
			}
			if !ch.Active() {
				msg += ", " + ch.State
			}
			b.WriteString(msg + "\n")
		case !ch.Active():
			fmt.Fprintf(&b, "  %s: skipped, %s%s\n", camName(ch.Number, width), ch.State, title)
		default:
			msg, err := addChannelCamera(ctx, s, c, name, ch, width)
			if err != nil {
				return b.String(), err
			}
			b.WriteString(msg + "\n")
		}
	}
	return b.String(), nil
}

// addChannelCamera adds the camera of a channel: name chNN, the alias from the
// channel's title, the live addresses the recorder gives.
func addChannelCamera(ctx context.Context, s *config.Store, c recorder, name string, ch Channel, width int) (string, error) {
	exec := func(params []script.Param) error {
		_, err := s.Exec(script.Command{Path: "/camera", Verb: "add", Params: params})
		return err
	}
	alias := AliasFromTitle(ch.Title)
	title := ""
	if ch.Title != "" {
		title = fmt.Sprintf(" (title %q)", ch.Title)
	}
	cn := camName(ch.Number, width)
	params := []script.Param{{Key: "name", Value: cn}, {Key: "nvr", Value: name},
		{Key: "channel", Value: strconv.Itoa(ch.Number)}}
	if u := c.StreamURL(ctx, ch.Number, 0); u != "" {
		params = append(params, script.Param{Key: "stream", Value: u})
	}
	if u := c.StreamURL(ctx, ch.Number, 1); u != "" {
		params = append(params, script.Param{Key: "stream-lq", Value: u})
	}
	msg := fmt.Sprintf("  %s: added", cn)
	err := exec(append(params, script.Param{Key: "alias", Value: alias}))
	switch {
	case err == nil && alias != "":
		msg += ", alias " + alias + title
	case err == nil:
		msg += title
	case alias != "":
		// the alias collides (two channels with the same title): add without it
		if err2 := exec(params); err2 != nil {
			return "", err2
		}
		msg += ", alias " + alias + " not set: " + err.Error()
	default:
		return "", err
	}
	return msg, nil
}

// ---------------------------------------------------------------- download

// RemoteSource gives the worker the recordings of jobs on remote recorders. load reads
// the configuration (fresh for every part: credentials may have changed).
func RemoteSource(load func() (*config.Store, error)) func(context.Context, *jobs.Job, jobs.Part) (io.ReadCloser, []string, error) {
	return func(ctx context.Context, j *jobs.Job, p jobs.Part) (io.ReadCloser, []string, error) {
		s, err := load()
		if err != nil {
			return nil, nil, fmt.Errorf("configuration: %w", err)
		}
		nvr := find(s, "/nvr", "name", j.Nvr)
		if nvr == nil || !remote(nvr) {
			return nil, nil, fmt.Errorf("nvr %s is not a remote recorder in the configuration", j.Nvr)
		}
		c, err := newRecorder(nvr)
		if err != nil {
			return nil, nil, err
		}
		return c.Open(ctx, j.Channel, p.Start, p.End)
	}
}

var secretRe = regexp.MustCompile(`(?i)("?[a-z.\[\]0-9]*password"?\s*[:=]\s*)("[^"]*"|[^,\r\n}]*)`)

// mask hides passwords in raw answers.
func mask(s string) string { return secretRe.ReplaceAllString(s, `$1*****`) }

// probeSection formats one raw answer of a probe.
func probeSection(b *strings.Builder, title, out string, err error) {
	fmt.Fprintf(b, "== %s\n", title)
	if err != nil {
		fmt.Fprintf(b, "ERROR: %v\n", err)
	}
	out = mask(strings.ReplaceAll(out, "\r", ""))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 60 {
		lines = append(lines[:60], fmt.Sprintf("... %d more lines", len(lines)-60))
	}
	if strings.TrimSpace(out) != "" {
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}
}

func probeRecorder(nvr *config.Item) string {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := newRecorder(nvr)
	if err != nil {
		return err.Error() + "\n"
	}
	return c.Probe(ctx)
}
