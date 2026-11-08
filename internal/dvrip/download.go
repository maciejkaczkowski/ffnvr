package dvrip

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"ffnvr/internal/h26x"
)

// The download stream wraps every frame in a private Sofia header:
//
//	00 00 01 FC/FE  I frame: 16 bytes - [4] codec, [5] fps, [6] width/8, [7] height/8,
//	                [8:12] time (packed), [12:16] length
//	00 00 01 FD     P frame:  8 bytes - [4:8] length
//	00 00 01 FA/F9  audio:    8 bytes - [6:8] length
//
// The payload of a video frame is an Annex-B stream (00 00 01 67/68/65/41 ...).

type frame struct {
	kind    byte // FC (I), FD (P), FA (audio)
	fps     int  // I frames
	time    time.Time
	payload []byte
}

// sofiaTime decodes the packed time of an I frame: seconds (6 bits), minutes (6),
// hours (5), day (5), month (4), year since 2000 (6). Zero when it makes no sense.
func sofiaTime(v uint32) time.Time {
	sec, min, hour := int(v&63), int(v>>6&63), int(v>>12&31)
	day, month, year := int(v>>17&31), int(v>>22&15), int(v>>26)+2000
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || min > 59 || sec > 59 {
		return time.Time{}
	}
	return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.Local)
}

// packTime is the inverse of sofiaTime (for tests and fakes).
func packTime(t time.Time) uint32 {
	return uint32(t.Second()) | uint32(t.Minute())<<6 | uint32(t.Hour())<<12 | uint32(t.Day())<<17 |
		uint32(t.Month())<<22 | uint32(t.Year()-2000)<<26
}

// deframer cuts the stream (arriving in any pieces) into frames for emit.
type deframer struct {
	buf  []byte
	emit func(frame) error
}

func index3(b []byte, from int) int {
	for i := from; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			return i
		}
	}
	return -1
}

func (d *deframer) Write(p []byte) (int, error) {
	d.buf = append(d.buf, p...)
	return len(p), d.drain(false)
}

// Flush emits a last frame cut short (an interrupted transfer).
func (d *deframer) Flush() error { return d.drain(true) }

func (d *deframer) drain(final bool) error {
	b, n, i := d.buf, len(d.buf), 0
loop:
	for i+4 <= n {
		if !(b[i] == 0 && b[i+1] == 0 && b[i+2] == 1) {
			j := index3(b, i+1)
			if j < 0 {
				if final {
					i = n
				} else if n-i > 2 {
					i = n - 2 // keep the beginning of a start code
				}
				break
			}
			i = j
			continue
		}
		f := frame{kind: b[i+3]}
		var hdr, ln int
		switch f.kind {
		case 0xFC, 0xFE:
			if hdr = 16; n-i < hdr {
				break loop
			}
			f.kind = 0xFC
			f.fps = int(b[i+5])
			f.time = sofiaTime(binary.LittleEndian.Uint32(b[i+8 : i+12]))
			ln = int(binary.LittleEndian.Uint32(b[i+12 : i+16]))
		case 0xFD:
			if hdr = 8; n-i < hdr {
				break loop
			}
			ln = int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		case 0xFA, 0xF9:
			if hdr = 8; n-i < hdr {
				break loop
			}
			f.kind = 0xFA
			ln = int(binary.LittleEndian.Uint16(b[i+6 : i+8]))
		default:
			i++ // not a Sofia header (file header, noise): resynchronize
			continue
		}
		if ln < 0 || ln > 64<<20 {
			i++
			continue
		}
		if n-i < hdr+ln {
			if !final {
				break
			}
			ln = n - i - hdr
		}
		f.payload = b[i+hdr : i+hdr+ln]
		if err := d.emit(f); err != nil {
			return err
		}
		i += hdr + ln
	}
	d.buf = append(d.buf[:0], b[i:]...)
	return nil
}

// errDone: the range is complete, the rest of the file is not needed.
var errDone = errors.New("range complete")

// gate passes the video frames of [from, to): it opens at the first I frame at or
// after from and closes at the first I frame at or after to (whole GOPs: the times
// of the recorder have one second resolution and only I frames carry them).
type gate struct {
	from, to time.Time
	open     bool
	w        io.Writer
	start    func(format string, fps float64)
	started  bool
}

func (g *gate) frame(f frame) error {
	if f.kind == 0xFA {
		return nil // audio is not kept
	}
	if f.kind == 0xFC && !f.time.IsZero() {
		if !f.time.Before(g.to) {
			return errDone
		}
		if !g.open && !f.time.Before(g.from) {
			g.open = true
		}
	}
	if f.kind == 0xFC && f.time.IsZero() && !g.open {
		g.open = true // a recorder without times: everything of the files found
	}
	if !g.open {
		return nil
	}
	if !g.started {
		format, fps, _ := h26x.Detect(f.payload)
		if format == "" {
			format = "h264"
		}
		if f.fps > 0 && f.fps <= 120 {
			fps = float64(f.fps)
		}
		if fps == 0 {
			fps = 25
		}
		g.started = true
		g.start(format, fps)
	}
	_, err := g.w.Write(f.payload)
	return err
}

// ErrEmpty: nothing was recorded in the range.
var ErrEmpty = errors.New("no recordings")

// Open downloads [from, to) of a channel (from 1): the files found for the range, cut
// to it at I frames, as one elementary stream with its ffmpeg format and frame rate.
// At most MaxStreams streams of a recorder are read at once (downloads and the player
// together): one waits for its turn.
func (c *Client) Open(ctx context.Context, channel int, from, to time.Time) (io.ReadCloser, string, float64, error) {
	release, err := c.stream(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	r, format, fps, err := c.open(ctx, channel, from, to, release)
	if err != nil {
		release()
	}
	return r, format, fps, err
}

// open: Open on a stream of the recorder, given back (release) when the stream ends.
func (c *Client) open(ctx context.Context, channel int, from, to time.Time, release func()) (io.ReadCloser, string, float64, error) {
	ctrl, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return nil, "", 0, err
	}
	s := &Session{ctrl}
	var files []File
	for _, typ := range []string{"h264", "h265"} {
		fs, err := s.Files(channel-1, from, to, typ)
		if err != nil {
			ctrl.Close()
			return nil, "", 0, err
		}
		for _, f := range fs {
			b, e1 := time.ParseInLocation(TimeLayout, f.BeginTime, time.Local)
			e, e2 := time.ParseInLocation(TimeLayout, f.EndTime, time.Local)
			if e1 != nil || e2 != nil || (b.Before(to) && e.After(from)) {
				files = append(files, f)
			}
		}
		if len(files) > 0 {
			break
		}
	}
	if len(files) == 0 {
		ctrl.Close()
		return nil, "", 0, ErrEmpty
	}
	ctx, cancel := context.WithCancel(ctx)
	context.AfterFunc(ctx, func() { ctrl.Close() })
	go ctrl.keepAlive(ctx)

	pr, pw := io.Pipe()
	type started struct {
		format string
		fps    float64
	}
	startc := make(chan started, 1)
	g := &gate{from: from, to: to, w: pw, start: func(f string, fps float64) { startc <- started{f, fps} }}
	done := make(chan error, 1)
	go func() {
		var err error
		for _, f := range files {
			if err = c.downloadFile(ctx, ctrl, f, g); err != nil {
				break
			}
		}
		if errors.Is(err, errDone) {
			err = nil
		}
		if err == nil && !g.started {
			err = ErrEmpty // files, but no frame in the range
		}
		cancel()
		release()
		pw.CloseWithError(err)
		done <- err
	}()
	select {
	case s := <-startc:
		return pipeCloser{pr, cancel}, s.format, s.fps, nil
	case err := <-done:
		if err == nil {
			err = ErrEmpty
		}
		return nil, "", 0, err
	}
}

type pipeCloser struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (p pipeCloser) Close() error { p.cancel(); return p.PipeReader.Close() }

func playReq(session, action string, speed int, f File) map[string]any {
	return map[string]any{
		"Name": "OPPlayBack", "SessionID": session,
		"OPPlayBack": map[string]any{
			"Action": action,
			"Parameter": map[string]any{
				"FileName": f.FileName, "PlayMode": "ByName", "StreamType": 0, "Value": 0, "TransMode": "TCP",
				"IntelligentPlayBackEvent": "", "IntelligentPlayBackSpeed": speed,
			},
			"StartTime": f.BeginTime, "EndTime": f.EndTime,
		},
	}
}

// ack reads the answer to a play request; none (some firmware send none) is fine.
func ack(cn *conn, msgid uint16, body any) error {
	_, _, err := cn.call(msgid, body, 3*time.Second)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return nil
	}
	return err
}

// downloadFile: a second connection of the same session (no login) claims the
// stream, then DownloadStart on the control connection starts it - in this order, or
// the recorder acknowledges and sends nothing.
func (c *Client) downloadFile(ctx context.Context, ctrl *conn, f File, g *gate) error {
	data, err := dial(ctx, c.addr)
	if err != nil {
		return fmt.Errorf("data connection: %w", err)
	}
	defer data.Close()
	stop := context.AfterFunc(ctx, func() { data.Close() })
	defer stop()
	data.session = ctrl.session
	if err := ack(data, cmdPlayClaim, playReq(data.sessionHex(), "Claim", 1, f)); err != nil {
		return fmt.Errorf("Claim %s: %w", f.FileName, err)
	}
	if err := ack(ctrl, cmdPlayDownload, playReq(ctrl.sessionHex(), "DownloadStart", 0, f)); err != nil {
		return fmt.Errorf("DownloadStart %s: %w", f.FileName, err)
	}
	defer ack(ctrl, cmdPlayDownload, playReq(ctrl.sessionHex(), "DownloadStop", 0, f))

	d := &deframer{emit: g.frame}
	total := 0
	for {
		data.c.SetReadDeadline(time.Now().Add(c.idle()))
		msgid, payload, err := data.recv()
		if err != nil {
			var ne net.Error
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case total > 0 && (errors.Is(err, io.EOF) || (errors.As(err, &ne) && ne.Timeout())):
				return d.Flush() // silence after the data: the end of the file
			case errors.As(err, &ne) && ne.Timeout():
				return fmt.Errorf("%s: no data within %s", f.FileName, c.idle())
			}
			return err
		}
		if len(payload) == 0 {
			continue
		}
		if msgid != cmdDownloadData && payload[0] == '{' {
			var m map[string]any
			if json.Unmarshal(trimJSON(payload), &m) == nil {
				if r, ok := m["Ret"].(float64); ok && r != 100 && r != 0 {
					return fmt.Errorf("%s: the recorder refused the download, Ret=%d", f.FileName, int(r))
				}
			}
			continue
		}
		total += len(payload)
		if _, err := d.Write(payload); err != nil {
			return err
		}
	}
}
