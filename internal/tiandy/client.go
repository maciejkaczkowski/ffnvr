package tiandy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/h26x"
)

// Client talks to one recorder. Every call logs in anew (the protocol is stateful and
// the recorder drops idle sessions), except the streams of recordings: they share a
// session per recorder while they run (session.go).
type Client struct {
	addr       string // host:port
	user, pass string
	// FPS is the frame rate given to downloads when not 0; otherwise it is read from
	// the stream (H.264 timing information), else DefaultFPS.
	FPS float64
	// Attempts at starting a download (the recorder sometimes stays silent after the
	// login); 0: 3.
	Attempts int
	// FirstData: how long to wait for the first data of a download; 0: 6 s.
	FirstData time.Duration
	// HTTPPort of the web interface (ISAPI); 0: 80.
	HTTPPort int
	httpc    *http.Client
}

// DefaultFPS when neither the configuration nor the stream tells.
const DefaultFPS = 20

// NewClient accepts "192.168.1.2" or "192.168.1.2:3001".
func NewClient(addr, user, pass string) (*Client, error) {
	addr = strings.TrimPrefix(strings.TrimPrefix(addr, "tcp://"), "//")
	if addr == "" || strings.ContainsAny(addr, "/ ") {
		return nil, fmt.Errorf("invalid address %q", addr)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, DefaultPort)
	}
	return &Client{addr: addr, user: user, pass: pass}, nil
}

// Host is the host name of the recorder (without port).
func (c *Client) Host() string { h, _, _ := net.SplitHostPort(c.addr); return h }

// Channel of the recorder.
type Channel struct {
	Number     int    // from 1
	Name       string // channel title
	IP         string // camera address
	Configured bool   // a camera is assigned
	Online     bool   // valid when Info.OnlineKnown
}

// Info is what the recorder tells after the login.
type Info struct {
	Model, Serial, Firmware, GUI string
	Channels                     []Channel
	OnlineKnown                  bool     // CHANNELCONNECT came
	Records                      []string // everything received, for diagnostics
}

// Info logs in and reads the records the recorder sends then, until it has the
// essentials and the line goes quiet (at most about 15 s).
func (c *Client) Info(ctx context.Context) (*Info, error) {
	cn, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return nil, err
	}
	defer cn.Close()
	stop := context.AfterFunc(ctx, func() { cn.Close() })
	defer stop()

	in := &Info{}
	names := map[int]string{}
	ips := map[int]string{}
	conf := map[int]bool{}
	online := map[int]bool{}
	n := 0
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cn.c.SetReadDeadline(time.Now().Add(2 * time.Second))
		msg, err := cn.recv()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if in.Firmware != "" && len(names)+len(conf) > 0 {
					break // quiet, and the essentials are there
				}
				continue
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			break
		}
		in.Records = append(in.Records, strings.Join(msg, " | "))
		field := func(tok string, off int) (string, bool) {
			if i := idxOf(msg, tok); i >= 0 && i+off < len(msg) {
				return msg[i+off], true
			}
			return "", false
		}
		if v, ok := field("VERSION", 1); ok && in.Firmware == "" {
			in.Firmware = v
			in.GUI, _ = field("VERSION", 2)
		}
		if v, ok := field("DEVMODEL", 1); ok {
			in.Model = v
		}
		if v, ok := field("BARCODE", 1); ok && in.Serial == "" {
			in.Serial = v
		}
		// DIGITALCHANNEL | <idx> | <enabled> | 0 | <IP> | ...
		if i := idxOf(msg, "DIGITALCHANNEL"); i >= 0 && i+4 < len(msg) {
			if idx, e := strconv.Atoi(msg[i+1]); e == nil {
				conf[idx] = msg[i+2] == "1"
				if ip := msg[i+4]; ip != "" && ip != "0.0.0.0" {
					ips[idx] = ip
				}
				n = max(n, idx+1)
			}
		}
		// DEVCOMMONNAME | 0 | <from>:<to> | <names...>  (type 0: channels)
		if i := idxOf(msg, "DEVCOMMONNAME"); i >= 0 && i+2 < len(msg) && msg[i+1] == "0" {
			if a, _, ok := strings.Cut(msg[i+2], ":"); ok {
				if from, e := strconv.Atoi(a); e == nil {
					for j, nm := range msg[i+3:] {
						names[from+j] = nm
						n = max(n, from+j+1)
					}
				}
			}
		}
		// CMD | CHANNELCONNECT | <idx>:<0|1> ...
		if i := idxOf(msg, "CHANNELCONNECT"); i >= 0 {
			in.OnlineKnown = true
			for _, f := range msg[i+1:] {
				if a, b, ok := strings.Cut(f, ":"); ok {
					if idx, e := strconv.Atoi(a); e == nil {
						online[idx] = b == "1"
					}
				}
			}
		}
	}
	if in.Firmware == "" && n == 0 {
		return in, errors.New("the recorder sent no information after the login")
	}
	for i := 0; i < n; i++ {
		in.Channels = append(in.Channels, Channel{Number: i + 1, Name: names[i], IP: ips[i], Configured: conf[i], Online: online[i]})
	}
	return in, nil
}

// ErrEmpty: the recorder sent nothing for the range (no recordings, or the channel
// has no camera).
var ErrEmpty = errors.New("no recordings")

// Open downloads [from, to) of a channel (from 1) and returns its elementary video
// stream, its ffmpeg format (h264 or hevc) and frame rate.
func (c *Client) Open(ctx context.Context, channel int, from, to time.Time) (io.ReadCloser, string, float64, error) {
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for a := 0; a < attempts; a++ {
		if a > 0 {
			select {
			case <-ctx.Done():
				return nil, "", 0, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		r, format, fps, err := c.open(ctx, channel, from, to)
		if err == nil {
			return r, format, fps, nil
		}
		if ctx.Err() != nil {
			return nil, "", 0, ctx.Err()
		}
		if errors.Is(err, ErrLogin) {
			return nil, "", 0, err
		}
		lastErr = err
	}
	if errors.Is(lastErr, ErrEmpty) {
		return nil, "", 0, ErrEmpty
	}
	return nil, "", 0, lastErr
}

func (c *Client) open(ctx context.Context, channel int, from, to time.Time) (io.ReadCloser, string, float64, error) {
	// a stream of the recorder's session, kept until the stream ends
	sess, id, release, err := c.stream(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	ctx, cancel := context.WithCancel(ctx)
	context.AfterFunc(ctx, release)
	fail := func(err error) (io.ReadCloser, string, float64, error) {
		cancel()
		return nil, "", 0, err
	}
	dl, err := dial(ctx, c.addr)
	if err != nil {
		return fail(err)
	}
	context.AfterFunc(ctx, func() { dl.Close() })
	// the range on the recorder's wall clock; 16: as fast as it can (its web client asks
	// for 1, real time, and paces the stream with more CONNECTs)
	wFrom, wTo := wallUnix(from), wallUnix(to)
	vch := strconv.Itoa(id)
	if err := dl.send("IP", "INNER", "CONNECT", vch, "1", sess.cmdID,
		fmt.Sprintf("TM:%d:%d:%d", wFrom, wTo, channel-1),
		"0", "-1", "16", "0", "1", "0:0:0:0", "", "1", "0", "0:", "0", "0", "0", "", "0", "0:0:0"); err != nil {
		return fail(err)
	}
	firstWait := c.FirstData
	if firstWait <= 0 {
		firstWait = 6 * time.Second
	}
	var first []byte
	for first == nil {
		dl.c.SetReadDeadline(time.Now().Add(firstWait))
		isData, body, err := dl.frame()
		if err != nil {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			return fail(ErrEmpty) // silence or a closed line: nothing (this time)
		}
		if isData && len(body) > 0 {
			first = body
		} else if !isData && strings.Contains(string(body), "DISCONNECT") {
			return fail(ErrEmpty)
		}
	}

	pr, pw := io.Pipe()
	type started struct {
		format string
		fps    float64
	}
	startc := make(chan started, 1)
	sw := &sniffWriter{w: pw, fps: c.FPS, start: func(f string, fps float64) { startc <- started{f, fps} }}
	dem := &sdvDemuxer{w: sw, from: wFrom, until: wTo}
	done := make(chan error, 1)
	go func() {
		err := func() error {
			if _, err := dem.Write(first); err != nil {
				return err
			}
			for {
				dl.c.SetReadDeadline(time.Now().Add(10 * time.Second))
				isData, body, err := dl.frame()
				if err != nil {
					var ne net.Error
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if errors.Is(err, io.EOF) || (errors.As(err, &ne) && ne.Timeout()) {
						return nil // the end: the recorder stopped sending
					}
					return err
				}
				if isData {
					if _, err := dem.Write(body); err != nil {
						return err // errRangeEnd: past the range, no need to wait for silence
					}
				} else if strings.Contains(string(body), "DISCONNECT") {
					return nil
				}
			}
		}()
		if errors.Is(err, errRangeEnd) {
			err = nil
		}
		if err == nil {
			dl.send("IP", "INNER", "DISCONNECT", vch, "1", "0", sess.cmdID)
			if err = dem.Close(); err == nil {
				err = sw.finish()
			}
		}
		cancel()
		pw.CloseWithError(err)
		done <- err
	}()
	select {
	case s := <-startc:
		return pipeCloser{pr, cancel}, s.format, s.fps, nil
	case err := <-done:
		if err == nil {
			err = ErrEmpty // data frames without any video
		}
		return nil, "", 0, err
	}
}

type pipeCloser struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (p pipeCloser) Close() error { p.cancel(); return p.PipeReader.Close() }

func waitCmdID(c *conn, d time.Duration) (string, error) {
	c.c.SetReadDeadline(time.Now().Add(d))
	defer c.c.SetReadDeadline(time.Time{})
	for {
		msg, err := c.recv()
		if err != nil {
			return "", fmt.Errorf("waiting for CMDID: %w", err)
		}
		if i := idxOf(msg, "CMDID"); i >= 0 && i+1 < len(msg) && msg[i+1] != "" {
			return msg[i+1], nil
		}
	}
}

// drain reads and drops frames until the line is quiet for idle (at most hardMax).
func drain(c *conn, idle, hardMax time.Duration) {
	deadline := time.Now().Add(hardMax)
	for time.Now().Before(deadline) {
		c.c.SetReadDeadline(time.Now().Add(idle))
		if _, _, err := c.frame(); err != nil {
			return
		}
	}
}

// sniffWriter holds the elementary stream back until its format and frame rate are
// known (from the first parameter sets), then passes everything through.
type sniffWriter struct {
	w       io.Writer
	fps     float64 // configured, 0: from the stream
	start   func(format string, fps float64)
	buf     bytes.Buffer
	started bool
}

const sniffLimit = 4 << 20

func (s *sniffWriter) Write(p []byte) (int, error) {
	if s.started {
		return s.w.Write(p)
	}
	s.buf.Write(p)
	if format, fps, ok := h26x.Detect(s.buf.Bytes()); ok || s.buf.Len() > sniffLimit {
		return len(p), s.begin(format, fps)
	}
	return len(p), nil
}

func (s *sniffWriter) begin(format string, fps float64) error {
	if s.fps > 0 {
		fps = s.fps
	}
	if fps == 0 {
		fps = DefaultFPS
	}
	if format == "" {
		format = "h264"
	}
	s.started = true
	s.start(format, fps)
	_, err := s.w.Write(s.buf.Bytes())
	s.buf = bytes.Buffer{}
	return err
}

// finish starts a stream that ended before its format was recognized.
func (s *sniffWriter) finish() error {
	if s.started || s.buf.Len() == 0 {
		return nil
	}
	f, fps, _ := h26x.Detect(s.buf.Bytes())
	return s.begin(f, fps)
}
