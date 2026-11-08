package dahua

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Events of the recorder: eventManager.cgi?action=attach keeps an HTTP answer open and
// writes every event into it as one part of a multipart stream:
//
//	--myboundary
//	Content-Type: text/plain
//	Content-Length: 36
//
//	Code=VideoMotion;action=Start;index=0
//
// Firmware differ: some leave out the part headers (Go's multipart reader then fails
// with "malformed MIME header"), some give no boundary in Content-Type at all, and
// events with details carry multi-line JSON after data=. So the stream is read line by
// line: an event begins with "Code=" and ends where its data= JSON closes - at once,
// without waiting for the next part. Boundaries, part headers, blank lines and
// Heartbeat are skipped.

// Event is one event of the recorder.
type Event struct {
	Code   string // VideoMotion, VideoLoss, VideoBlind, AlarmLocal, StorageFailure, ...
	Action string // Start, Stop, Pulse
	Index  int    // channel from 0; -1 when the event has none
	Data   string // the JSON after data= ("" none)
	Raw    string // the event as it came
}

// Channel is the channel from 1 (0: the recorder itself).
func (e Event) Channel() int { return e.Index + 1 }

// ErrStream: the answer is no event stream (wrong address, no permission).
var ErrStream = errors.New("no event stream")

// Events subscribes to the events and calls fn for every one until ctx ends or the
// connection breaks (the error says why; reconnecting is the caller's business). codes
// selects events (nil: All). The recorder sends a heartbeat every heartbeat; silence for
// three of them counts as a broken connection. connected (may be nil) is called once
// the recorder accepted the subscription.
func (c *Client) Events(ctx context.Context, codes []string, heartbeat time.Duration, connected func(), fn func(Event)) error {
	if heartbeat < time.Second {
		heartbeat = 20 * time.Second
	}
	list := "All"
	if len(codes) > 0 {
		list = strings.Join(codes, ",")
	}
	// the brackets stay literal: Dahua does not decode %5B
	u := *c.base
	u.Path = "/cgi-bin/eventManager.cgi"
	u.RawQuery = "action=attach&codes=[" + list + "]&heartbeat=" + strconv.Itoa(int(heartbeat/time.Second))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%w: HTTP %d %s", ErrStream, resp.StatusCode, strings.TrimSpace(firstLine(string(body))))
	}
	if connected != nil {
		connected()
	}
	wd := newWatchdog(3*heartbeat+5*time.Second, cancel)
	defer wd.stop()
	err = ReadEvents(&wdReader{resp.Body, wd}, fn)
	if wd.fired() {
		return fmt.Errorf("no heartbeat for %s", 3*heartbeat+5*time.Second)
	}
	if ctx.Err() != nil && err != nil {
		return ctx.Err()
	}
	if err == nil {
		err = io.ErrUnexpectedEOF // the recorder closed the stream
	}
	return err
}

// ReadEvents parses an event stream (see above) until it ends.
func ReadEvents(r io.Reader, fn func(Event)) error {
	br := bufio.NewReaderSize(r, 16<<10)
	var body strings.Builder
	depth := 0
	inBody := false
	flush := func() {
		if inBody {
			if e, ok := ParseEvent(body.String()); ok {
				fn(e)
			}
		}
		body.Reset()
		inBody, depth = false, 0
	}
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			t := strings.TrimRight(line, "\r\n")
			trim := strings.TrimSpace(t)
			switch {
			case strings.HasPrefix(trim, "--") && !inBodyJSON(inBody, depth):
				flush() // a boundary
			case strings.HasPrefix(trim, "Code="):
				flush() // a new event, also when the previous one was cut
				inBody = true
				body.WriteString(trim)
				depth = jsonDepth(trim, 0)
				if depth <= 0 {
					flush()
				}
			case inBody:
				body.WriteString("\n")
				body.WriteString(t)
				depth = jsonDepth(t, depth)
				if depth <= 0 {
					flush()
				}
				if body.Len() > 1<<20 {
					body.Reset()
					inBody, depth = false, 0 // not an event any more
				}
			}
			// outside an event: part headers, blank lines, Heartbeat
		}
		if err != nil {
			flush()
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func inBodyJSON(inBody bool, depth int) bool { return inBody && depth > 0 }

// jsonDepth follows the nesting of braces and brackets through a line, outside strings.
// A string never spans lines in what the recorders send.
func jsonDepth(line string, depth int) int {
	inStr, esc := false, false
	for _, c := range line {
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			if depth > 0 {
				inStr = true
			}
		case c == '{' || c == '[':
			if depth > 0 || c == '{' {
				depth++
			}
		case c == '}' || c == ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth
}

// ParseEvent reads "Code=VideoMotion;action=Start;index=0[;data={...}]".
func ParseEvent(s string) (Event, bool) {
	e := Event{Index: -1, Raw: s}
	head := s
	if i := strings.Index(strings.ToLower(s), ";data="); i >= 0 {
		head, e.Data = s[:i], strings.TrimSpace(s[i+len(";data="):])
	}
	for _, kv := range strings.Split(head, ";") {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "code":
			e.Code = strings.TrimSpace(v)
		case "action":
			e.Action = strings.TrimSpace(v)
		case "index":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				e.Index = n
			}
		}
	}
	return e, e.Code != ""
}

// watchdog cancels when it is not fed for d.
type watchdog struct {
	mu      sync.Mutex
	t       *time.Timer
	d       time.Duration
	didFire bool
}

func newWatchdog(d time.Duration, cancel func()) *watchdog {
	w := &watchdog{d: d}
	w.t = time.AfterFunc(d, func() {
		w.mu.Lock()
		w.didFire = true
		w.mu.Unlock()
		cancel()
	})
	return w
}

func (w *watchdog) feed() { w.t.Reset(w.d) }
func (w *watchdog) stop() { w.t.Stop() }
func (w *watchdog) fired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.didFire
}

type wdReader struct {
	r  io.Reader
	wd *watchdog
}

func (r *wdReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.wd.feed()
	}
	return n, err
}
