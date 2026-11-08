package dvrip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"ffnvr/internal/h26x"
)

// Playing, as the XMeye client does it (captured from the client playing a channel of
// an XMeye recorder and jumping by minutes):
//
//	data:    1424 OPPlayBack Claim     (the file, IntelligentPlayBackSpeed 1) → 1425 Ret 100
//	control: 1420 OPPlayBack Start     (StartTime: the file's beginning)      → 1421 Ret 100
//	data:    ← 1422 the file from its beginning, Sofia frames as in a download
//	control: 1420 Pause, Locate (StartTime: where to go), Continue            → 1421 each
//	data:    ← 1422 from the key frame there
//	control: 1420 Stop
//
// Unlike a download, the recorder sends at the speed of playing (after a second or two
// sent at once). Faster is asked for with Fast and a Value - a second capture, the
// client speeding up step by step, measured on two files (12 and 24 fps): 0 real time,
// 1 about 2x, 2 about 3x, 3 about 5x, 4 about 10.5x, every picture sent. The player
// asks for PlayFast. On Pause the data stops, after Continue it goes on from the key
// frame Locate found.
//
// The times in the frames are those of the camera, which need not be the recorder's: in
// the first capture the frames of the file of 05:00:00 were stamped 03:04:37, and Locate
// to 05:28:08 brought a key frame stamped 03:32:46 - the same 1:55:22 apart (in the
// second, the clock set, a file of 08:41:56 began with 08:41:56). So the first
// key frame of the file tells how far the camera's clock is off (skew), and the rest
// is reckoned with that: where the piece's first picture is, where it ends.

const (
	cmdPlayData = 1422 // the file being played, from the recorder

	// locateMin: from this far into the file the piece is located (Locate); nearer
	// its beginning the file is played from it
	locateMin = 8 * time.Second
	// locateSlack: a key frame at least this near to where Locate went is past it; the
	// frames sent before Pause are at the file's beginning
	locateSlack = 3 * time.Second
	// PlayFast: the Value of Fast the player asks for (about 10 times real time: a piece
	// of 30 s in some 3 s)
	PlayFast = 4

	// skewSame: a camera clock this near to the recorder's is the recorder's (the first
	// key frame of a file comes a little after its beginning)
	skewSame = 3 * time.Second
)

// PlayIdle: silence on the data connection that ends a piece being played.
var PlayIdle = 6 * time.Second

func playAction(session, action string, speed int, f File, start string) map[string]any {
	req := playReq(session, action, speed, f)
	if start != "" {
		req["OPPlayBack"].(map[string]any)["StartTime"] = start
	}
	return req
}

// fastAction: Fast with a Value, as the client asks (the speed field 0).
func fastAction(session string, value int, f File) map[string]any {
	req := playReq(session, "Fast", 0, f)
	req["OPPlayBack"].(map[string]any)["Parameter"].(map[string]any)["Value"] = value
	return req
}

// OpenPlay plays [from, to) of a channel (from 1) for the panel's player: the file of
// the recorder with from, located there, until to, at PlayFast. The stream is the elementary video
// (format "h264" or "hevc", fps from the recorder); it begins at a key frame near from
// and ends at the key frame at to (or the file's end). FirstFrame tells when its first
// picture was. At most MaxStreams streams of a recorder are read at once.
func (c *Client) OpenPlay(ctx context.Context, channel int, from, to time.Time) (*Play, string, float64, error) {
	release, err := c.stream(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	p, format, fps, err := c.openPlay(ctx, channel, from, to, release)
	if err != nil {
		release()
	}
	return p, format, fps, err
}

// playFile: the file of a channel (from 0) to play [from, to) from - the one with from,
// else the first one after it. Searched from 2 hours before: some recorders tell only
// the files beginning in the range searched (a file lasts an hour, at most 2).
func playFile(s *Session, channel int, from, to time.Time) (File, time.Time, error) {
	for _, typ := range []string{"h264", "h265"} {
		fs, err := s.Files(channel, from.Add(-2*time.Hour), to, typ)
		if err != nil {
			return File{}, time.Time{}, err
		}
		var best File
		var bestBegin time.Time
		for _, f := range fs {
			b, e1 := time.ParseInLocation(TimeLayout, f.BeginTime, time.Local)
			e, e2 := time.ParseInLocation(TimeLayout, f.EndTime, time.Local)
			if e1 != nil || e2 != nil || !b.Before(to) || !e.After(from) {
				continue
			}
			if !b.After(from) {
				return f, b, nil
			}
			if bestBegin.IsZero() || b.Before(bestBegin) {
				best, bestBegin = f, b
			}
		}
		if !bestBegin.IsZero() {
			return best, bestBegin, nil
		}
	}
	return File{}, time.Time{}, ErrEmpty
}

func (c *Client) openPlay(ctx context.Context, channel int, from, to time.Time, release func()) (*Play, string, float64, error) {
	ctrl, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return nil, "", 0, err
	}
	f, begin, err := playFile(&Session{ctrl}, channel-1, from, to)
	if err != nil {
		ctrl.Close()
		return nil, "", 0, err
	}
	ctx, cancel := context.WithCancel(ctx)
	context.AfterFunc(ctx, func() { ctrl.Close() })
	fail := func(err error) (*Play, string, float64, error) {
		cancel()
		return nil, "", 0, err
	}
	data, err := dial(ctx, c.addr)
	if err != nil {
		return fail(fmt.Errorf("data connection: %w", err))
	}
	context.AfterFunc(ctx, func() { data.Close() })
	data.session = ctrl.session
	// Claim on the data connection first, then Start: as the client does
	if err := ack(data, cmdPlayClaim, playAction(data.sessionHex(), "Claim", 1, f, "")); err != nil {
		return fail(fmt.Errorf("Claim %s: %w", f.FileName, err))
	}
	if err := ack(ctrl, cmdPlayDownload, playAction(ctrl.sessionHex(), "Start", 0, f, "")); err != nil {
		return fail(fmt.Errorf("Start %s: %w", f.FileName, err))
	}
	go ctrl.keepAlive(ctx)
	fast := func() error {
		if err := ack(ctrl, cmdPlayDownload, fastAction(ctrl.sessionHex(), PlayFast, f)); err != nil {
			return fmt.Errorf("Fast %s: %w", f.FileName, err)
		}
		return nil
	}

	p := &Player{from: from, to: to, begin: begin, locate: from.Sub(begin) >= locateMin}
	pr, pw := io.Pipe()
	type started struct {
		format string
		fps    float64
	}
	startc := make(chan started, 1)
	p.w = pw
	p.start = func(format string, fps float64) { startc <- started{format, fps} }
	p.seek = func() error {
		// as the client: Pause, Locate, Continue (its Pause and Continue carry the
		// file's beginning); then faster (only now: the frames sent before Pause are
		// told from those after Locate by their times, few of them keep that sure)
		for _, a := range []struct{ action, start string }{{"Pause", ""}, {"Locate", from.Format(TimeLayout)}, {"Continue", ""}} {
			if err := ack(ctrl, cmdPlayDownload, playAction(ctrl.sessionHex(), a.action, 0, f, a.start)); err != nil {
				return fmt.Errorf("%s %s: %w", a.action, f.FileName, err)
			}
		}
		return fast()
	}
	if !p.locate {
		if err := fast(); err != nil {
			return fail(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		err := p.read(ctx, data, f.FileName)
		if errors.Is(err, errDone) {
			err = nil
		}
		if err == nil && !p.open {
			err = ErrEmpty // the file, but no picture of the piece
		}
		if ctx.Err() == nil {
			ack(ctrl, cmdPlayDownload, playAction(ctrl.sessionHex(), "Stop", 0, f, ""))
		}
		cancel()
		release()
		pw.CloseWithError(err)
		done <- err
	}()
	select {
	case s := <-startc:
		return &Play{PipeReader: pr, cancel: cancel, p: p}, s.format, s.fps, nil
	case err := <-done:
		if err == nil {
			err = ErrEmpty
		}
		return nil, "", 0, err
	}
}

// Play is a piece being played: the elementary video stream.
type Play struct {
	*io.PipeReader
	cancel context.CancelFunc
	p      *Player
}

func (p *Play) Close() error { p.cancel(); return p.PipeReader.Close() }

// FirstFrame: when the stream's first picture was, on the recorder's clock (ok false: the
// recorder does not stamp its frames).
func (p *Play) FirstFrame() (time.Time, bool) { return p.p.First() }

// Player cuts a piece [from, to) out of a file being played from begin: it waits for the
// first key frame (the file's beginning: how far the camera's clock is off), asks seek
// to locate from (when locate), passes the pictures from the key frame where the
// recorder went and ends at the key frame at to.
type Player struct {
	from, to, begin time.Time
	locate          bool
	w               io.Writer
	start           func(format string, fps float64)
	seek            func() error

	sought   bool          // Locate was asked for
	skew     time.Duration // the camera's clock minus the recorder's
	t0       time.Time     // the first key frame of the file (camera's clock)
	open     bool          // the pictures of the piece are being passed
	fps      int
	lastKey  time.Time // the last key frame passed (camera's clock)
	sinceKey int       // pictures since it

	mu    sync.Mutex
	first time.Time // the first picture passed (recorder's clock)
}

// First: when the first picture passed was.
func (p *Player) First() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.first, !p.first.IsZero()
}

func (p *Player) frame(f frame) error {
	if f.kind == 0xFA {
		return nil // audio is not kept
	}
	key := f.kind == 0xFC
	if key && p.t0.IsZero() && !p.sought {
		// the file's first key frame: the camera's clock against the recorder's
		p.t0 = f.time
		if !f.time.IsZero() {
			if p.skew = f.time.Sub(p.begin); p.skew.Abs() <= skewSame {
				p.skew = 0
			}
		}
		if p.locate {
			p.sought = true
			if err := p.seek(); err != nil {
				return err
			}
			return nil // this frame is of the file's beginning
		}
	}
	if !p.open {
		if !key {
			return nil
		}
		switch {
		case p.sought && !p.t0.IsZero() && !f.time.IsZero():
			// the first key frame where Locate went (the ones before it are of the
			// file's beginning, sent before Pause)
			if f.time.Before(p.t0.Add(p.from.Sub(p.begin) - locateSlack)) {
				return nil
			}
		case p.sought:
			// a recorder without times: the first key frame after Continue
		case !f.time.IsZero() && f.time.Before(p.from.Add(p.skew)):
			return nil // played from the file's beginning: up to from
		}
		p.open = true
		format, fps, _ := h26x.Detect(f.payload)
		if format == "" {
			format = "h264"
		}
		if f.fps > 0 && f.fps <= 120 {
			fps = float64(f.fps)
			p.fps = f.fps
		}
		if fps == 0 {
			fps = 25
		}
		if !f.time.IsZero() {
			p.mu.Lock()
			p.first = f.time.Add(-p.skew)
			p.mu.Unlock()
		}
		p.lastKey, p.sinceKey = f.time, 0
		p.start(format, fps)
		_, err := p.w.Write(f.payload)
		return err
	}
	end := p.to.Add(p.skew)
	if key && !f.time.IsZero() {
		if !f.time.Before(end) {
			return errDone // the key frame at to begins the next piece
		}
		p.lastKey, p.sinceKey = f.time, 0
	} else if !key {
		p.sinceKey++
		// a long group of pictures, or the last piece of a file: past to by the count
		// of pictures (the stamps are whole seconds: a second to spare)
		if p.fps > 0 && !p.lastKey.IsZero() &&
			!p.lastKey.Add(time.Duration(p.sinceKey)*time.Second/time.Duration(p.fps)).Before(end.Add(time.Second)) {
			return errDone
		}
	}
	_, err := p.w.Write(f.payload)
	return err
}

// read passes the frames of the data connection until the piece ends.
func (p *Player) read(ctx context.Context, data *conn, name string) error {
	d := &deframer{emit: p.frame}
	total := 0
	for {
		data.c.SetReadDeadline(time.Now().Add(PlayIdle))
		msgid, payload, err := data.recv()
		if err != nil {
			var ne net.Error
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case total > 0 && (errors.Is(err, io.EOF) || (errors.As(err, &ne) && ne.Timeout())):
				return d.Flush() // silence after the data: the end of the file
			case errors.As(err, &ne) && ne.Timeout():
				return fmt.Errorf("%s: no data within %s", name, PlayIdle)
			}
			return err
		}
		if len(payload) == 0 {
			continue
		}
		if msgid != cmdPlayData && payload[0] == '{' {
			var m map[string]any
			if json.Unmarshal(trimJSON(payload), &m) == nil {
				if r, ok := m["Ret"].(float64); ok && r != 100 && r != 0 {
					return fmt.Errorf("%s: the recorder refused to play it, Ret=%d", name, int(r))
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
