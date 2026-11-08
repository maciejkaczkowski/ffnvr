package uniview

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/digest"
)

// A minimal RTSP client: RTP interleaved over TCP (RFC 2326, 10.12), Digest
// authentication, one video track.

type rtspResponse struct {
	Status int
	Reason string
	Header textproto.MIMEHeader
	Body   []byte
}

type rtspConn struct {
	conn    net.Conn
	r       *bufio.Reader
	wmu     sync.Mutex
	cseq    int
	auth    *digest.Auth
	session string
	// variant of the answer to the challenge, tried in turn until one is accepted:
	// 0 as RFC 2617, 1 without qop (RFC 2069), 2 with the path as the digest URI
	variant int
	tried   []string         // what was tried, for the error message
	trace   *strings.Builder // when set: the exchange, for diagnostics (no secrets)
}

func (c *rtspConn) authHeader(method, uri string) string {
	switch c.variant {
	case 1:
		return c.auth.HeaderOpts(method, uri, true)
	case 2:
		if u, err := url.Parse(uri); err == nil {
			return c.auth.Header(method, u.RequestURI())
		}
	}
	return c.auth.Header(method, uri)
}

func dialRTSP(ctx context.Context, u *url.URL, auth *digest.Auth) (*rtspConn, error) {
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "554")
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	return &rtspConn{conn: c, r: bufio.NewReaderSize(c, 1<<20), auth: auth}, nil
}

func (c *rtspConn) Close() error { return c.conn.Close() }

// send writes a request without waiting for the answer (also used for keep-alive).
func (c *rtspConn) send(method, uri string, hdr map[string]string) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.cseq++
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: ffnvr\r\n", method, uri, c.cseq)
	if c.session != "" {
		fmt.Fprintf(&b, "Session: %s\r\n", c.session)
	}
	if h := c.authHeader(method, uri); h != "" {
		fmt.Fprintf(&b, "Authorization: %s\r\n", h)
	}
	for k, v := range hdr {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	if c.trace != nil {
		fmt.Fprintf(c.trace, "> %s %s (CSeq %d", method, uri, c.cseq)
		if h := c.authHeader(method, uri); h != "" {
			fmt.Fprintf(c.trace, ", Digest variant %d", c.variant)
		}
		if s, ok := hdr["Speed"]; ok {
			fmt.Fprintf(c.trace, ", Speed %s", s)
		}
		c.trace.WriteString(")\n")
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := io.WriteString(c.conn, b.String())
	return c.cseq, err
}

// do sends a request and waits for the answer with its CSeq; interleaved frames
// before it are skipped.
func (c *rtspConn) do(method, uri string, hdr map[string]string) (*rtspResponse, error) {
	authed := c.auth.Ready()
	for attempt := 0; attempt < 6; attempt++ {
		cseq, err := c.send(method, uri, hdr)
		if err != nil {
			return nil, err
		}
		c.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		var resp *rtspResponse
		for {
			ch, _, msg, err := c.readPacket()
			if err != nil {
				return nil, err
			}
			if ch >= 0 || msg == nil {
				continue
			}
			if got, _ := strconv.Atoi(msg.Header.Get("CSeq")); got == cseq || msg.Header.Get("CSeq") == "" {
				resp = msg
				break
			}
		}
		if c.trace != nil {
			fmt.Fprintf(c.trace, "< %d %s", resp.Status, resp.Reason)
			for _, h := range resp.Header.Values("Www-Authenticate") {
				fmt.Fprintf(c.trace, "\n  WWW-Authenticate: %s", h)
			}
			if s := resp.Header.Get("Speed"); s != "" {
				fmt.Fprintf(c.trace, "\n  Speed: %s", s)
			}
			c.trace.WriteString("\n")
		}
		if resp.Status == 401 {
			chal := resp.Header.Values("Www-Authenticate")
			if !c.auth.Learn(chal) {
				return resp, fmt.Errorf("RTSP %s: 401 without a usable challenge (%s)", method, strings.Join(chal, " | "))
			}
			stale := false
			for _, h := range chal {
				if strings.EqualFold(digest.ParseParams(strings.TrimPrefix(strings.TrimSpace(h), "Digest "))["stale"], "true") {
					stale = true
				}
			}
			if authed && !stale { // an answer was refused (not just an old nonce): the next form of it
				c.tried = append(c.tried, fmt.Sprintf("%s variant %d", method, c.variant))
				if c.variant++; c.variant > 2 {
					return resp, fmt.Errorf("RTSP: wrong username or password (%s refused; challenge %s)",
						strings.Join(c.tried, ", "), strings.Join(chal, " | "))
				}
			}
			authed = true
			continue
		}
		if resp.Status != 200 {
			return resp, fmt.Errorf("RTSP %s: %d %s", method, resp.Status, resp.Reason)
		}
		return resp, nil
	}
	return nil, errors.New("RTSP: authentication failed")
}

// readPacket returns an interleaved frame (ch >= 0) or an RTSP message (ch = -1).
func (c *rtspConn) readPacket() (int, []byte, *rtspResponse, error) {
	b, err := c.r.Peek(1)
	if err != nil {
		return 0, nil, nil, err
	}
	if b[0] == '$' {
		var h [4]byte
		if _, err := io.ReadFull(c.r, h[:]); err != nil {
			return 0, nil, nil, err
		}
		buf := make([]byte, int(binary.BigEndian.Uint16(h[2:])))
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return 0, nil, nil, err
		}
		return int(h[1]), buf, nil, nil
	}
	msg, err := c.readMessage()
	return -1, nil, msg, err
}

func (c *rtspConn) readMessage() (*rtspResponse, error) {
	tp := textproto.NewReader(c.r)
	line, err := tp.ReadLine()
	if err != nil {
		return nil, err
	}
	hdr, err := tp.ReadMIMEHeader()
	if err != nil && len(hdr) == 0 {
		return nil, err
	}
	resp := &rtspResponse{Header: hdr}
	if strings.HasPrefix(line, "RTSP/") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) >= 2 {
			resp.Status, _ = strconv.Atoi(parts[1])
		}
		if len(parts) == 3 {
			resp.Reason = parts[2]
		}
	} else {
		resp.Status = -1 // a request of the server (e.g. ANNOUNCE): ignored
	}
	if cl, _ := strconv.Atoi(hdr.Get("Content-Length")); cl > 0 {
		resp.Body = make([]byte, cl)
		if _, err := io.ReadFull(c.r, resp.Body); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// ---- SDP ----

type videoTrack struct {
	Codec   string // H264 or H265
	PT      uint8
	Control string
	Params  [][]byte // VPS/SPS/PPS from the SDP
}

func parseSDP(sdp []byte) (*videoTrack, error) {
	var tr *videoTrack
	inVideo := false
	for _, line := range strings.Split(string(sdp), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "m="):
			if tr != nil && tr.Codec != "" {
				return tr, nil
			}
			inVideo = strings.HasPrefix(line, "m=video")
			if inVideo {
				tr = &videoTrack{}
				if f := strings.Fields(line); len(f) >= 4 {
					pt, _ := strconv.Atoi(f[3])
					tr.PT = uint8(pt)
				}
			}
		case !inVideo:
		case strings.HasPrefix(line, "a=rtpmap:"):
			f := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(f) == 2 {
				pt, _ := strconv.Atoi(f[0])
				switch strings.ToUpper(strings.SplitN(f[1], "/", 2)[0]) {
				case "H264":
					tr.Codec, tr.PT = "H264", uint8(pt)
				case "H265", "HEVC":
					tr.Codec, tr.PT = "H265", uint8(pt)
				}
			}
		case strings.HasPrefix(line, "a=control:"):
			tr.Control = strings.TrimPrefix(line, "a=control:")
		case strings.HasPrefix(line, "a=fmtp:"):
			tr.Params = append(tr.Params, parseFmtpParams(line)...)
		}
	}
	if tr == nil || tr.Codec == "" {
		return nil, errors.New("SDP: no H.264/H.265 video track")
	}
	return tr, nil
}

func parseFmtpParams(line string) [][]byte {
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return nil
	}
	var out [][]byte
	vals := map[string]string{}
	for _, kv := range strings.Split(line[sp+1:], ";") {
		kv = strings.TrimSpace(kv)
		if i := strings.IndexByte(kv, '='); i > 0 {
			vals[strings.ToLower(kv[:i])] = kv[i+1:]
		}
	}
	add := func(s string) {
		for _, p := range strings.Split(s, ",") {
			if b, err := decodeB64(p); err == nil && len(b) > 0 {
				out = append(out, b)
			}
		}
	}
	add(vals["sprop-parameter-sets"]) // H.264: SPS,PPS
	add(vals["sprop-vps"])            // H.265
	add(vals["sprop-sps"])
	add(vals["sprop-pps"])
	return out
}

func resolveControl(base, control string) string {
	if control == "" || control == "*" {
		return base
	}
	if strings.HasPrefix(strings.ToLower(control), "rtsp://") {
		return control
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + strings.TrimPrefix(control, "/")
}

// ---- playback ----

type playOptions struct {
	Speed       float64       // RTSP Speed header (0: not sent): faster delivery, if supported
	IdleTimeout time.Duration // the end when no data comes for so long (default 20 s)
	Duration    time.Duration // expected length of the material (a safety stop)
	// Start is called once, before the first byte is written: the codec and the frame
	// rate measured from the first frames' RTP timestamps (the Annex-B stream has none).
	Start func(codec string, fps float64)
	// End: when set, the stream ends at the first picture stamped at or after it (the
	// recorder's stamps, see stampProfile; Duration stays the stop without them)
	End time.Time
	// First, when set, learns the time of the first picture from the stamps.
	First *firstFrame
}

// firstFrame: the time of the first picture of a stream, from the stamps of whole
// seconds and the RTP timestamps (90 kHz): where the stamp first goes on to the next
// second, the RTP time since the first picture tells how far before it that one was.
type firstFrame struct {
	mu      sync.Mutex
	have    bool
	settled bool // the next second was seen: t is exact
	ts      uint32
	stamp   uint32
	t       time.Time
}

func (f *firstFrame) see(ts, stamp uint32) {
	if y := time.Unix(int64(stamp), 0).UTC().Year(); y < 2000 || y > 2100 {
		return // not a time
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case !f.have:
		f.have, f.ts, f.stamp, f.t = true, ts, stamp, time.Unix(int64(stamp), 0)
	case !f.settled && stamp != f.stamp:
		f.settled = true
		back := time.Duration(int32(ts-f.ts)) * time.Second / 90000
		if stamp == f.stamp+1 && back > 0 && back <= time.Second {
			f.t = time.Unix(int64(stamp), 0).Add(-back)
		}
	}
}

// get: the time of the first picture; ok false: no stamps (yet).
func (f *firstFrame) get() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t, f.have
}

// startWriter keeps what is written until start, then passes everything through.
type startWriter struct {
	w       io.Writer
	buf     bytes.Buffer
	started bool
}

func (s *startWriter) Write(b []byte) (int, error) {
	if !s.started {
		return s.buf.Write(b)
	}
	return s.w.Write(b)
}

func (s *startWriter) start() error {
	if s.started {
		return nil
	}
	s.started = true
	_, err := s.w.Write(s.buf.Bytes())
	s.buf = bytes.Buffer{}
	return err
}

// fpsFrames: how many frames the frame rate is measured over
const fpsFrames = 25

func measureFPS(first, last uint32, frames int) float64 {
	if frames < 2 || last == first {
		return 25
	}
	fps := float64(frames-1) * 90000 / float64(last-first)
	if r := math.Round(fps); math.Abs(fps-r) < 0.1 {
		return r
	}
	return math.Round(fps*100) / 100
}

// downloadRTSP plays a recording and writes its video track as an Annex-B stream.
// Cancelling ctx stops it.
func downloadRTSP(ctx context.Context, rawURL, user, pass string, w io.Writer, opt playOptions) (codec string, written int64, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", 0, err
	}
	u.User = nil // credentials never go into request URIs
	uri := u.String()

	c, err := dialRTSP(ctx, u, digest.New(user, pass))
	if err != nil {
		return "", 0, err
	}
	defer c.Close()
	stopCtx := context.AfterFunc(ctx, func() { c.conn.Close() })
	defer stopCtx()

	c.do("OPTIONS", uri, nil) // some servers want OPTIONS before DESCRIBE; errors do not matter
	resp, err := c.do("DESCRIBE", uri, map[string]string{"Accept": "application/sdp"})
	if err != nil {
		return "", 0, ctxErr(ctx, err)
	}
	base := uri
	if cb := resp.Header.Get("Content-Base"); cb != "" {
		base = cb
	}
	track, err := parseSDP(resp.Body)
	if err != nil {
		return "", 0, err
	}
	resp, err = c.do("SETUP", resolveControl(base, track.Control),
		map[string]string{"Transport": "RTP/AVP/TCP;unicast;interleaved=0-1"})
	if err != nil {
		return track.Codec, 0, ctxErr(ctx, err)
	}
	sess := resp.Header.Get("Session")
	timeout := 60 * time.Second
	if i := strings.IndexByte(sess, ';'); i >= 0 {
		if t := strings.TrimSpace(sess[i+1:]); strings.HasPrefix(t, "timeout=") {
			if s, e := strconv.Atoi(strings.TrimPrefix(t, "timeout=")); e == nil && s > 0 {
				timeout = time.Duration(s) * time.Second
			}
		}
		sess = sess[:i]
	}
	c.session = strings.TrimSpace(sess)
	rtpCh, rtcpCh := 0, 1
	if tr := resp.Header.Get("Transport"); tr != "" {
		if i := strings.Index(tr, "interleaved="); i >= 0 {
			fmt.Sscanf(tr[i+len("interleaved="):], "%d-%d", &rtpCh, &rtcpCh)
		}
	}
	playHdr := map[string]string{"Range": "npt=0.000-"}
	if opt.Speed > 0 && opt.Speed != 1 {
		playHdr["Speed"] = strconv.FormatFloat(opt.Speed, 'f', 1, 64)
	}
	if _, err = c.do("PLAY", base, playHdr); err != nil {
		return track.Codec, 0, ctxErr(ctx, err)
	}

	// keep-alive (GET_PARAMETER); the answers are skipped by the reading loop
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(timeout / 2)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				c.send("GET_PARAMETER", base, nil)
			}
		}
	}()

	sw := &startWriter{w: w}
	dep := newDepacketizer(track.Codec, sw)
	for _, p := range track.Params {
		dep.writeNAL(p)
	}
	var (
		firstTS, lastTS uint32
		frames          int
		mediaTicks      int64
		maxTicks        int64 = -1
	)
	start := func() error {
		if sw.started {
			return nil
		}
		if opt.Start != nil {
			opt.Start(track.Codec, measureFPS(firstTS, lastTS, frames))
		}
		return sw.start()
	}
	if opt.Duration > 0 {
		maxTicks = int64(opt.Duration.Seconds()*90000) + 90000 // 1 s to spare
	}
	idle := opt.IdleTimeout
	if idle <= 0 {
		idle = 20 * time.Second
	}
	for {
		c.conn.SetReadDeadline(time.Now().Add(idle))
		ch, payload, msg, err := c.readPacket()
		if err != nil {
			var ne net.Error
			if ctx.Err() != nil {
				return track.Codec, dep.written, ctx.Err()
			}
			if errors.Is(err, io.EOF) || (errors.As(err, &ne) && ne.Timeout()) {
				break // the end: the server closed the connection or stopped sending
			}
			c.send("TEARDOWN", base, nil)
			return track.Codec, dep.written, err
		}
		if msg != nil {
			continue
		}
		if ch == rtcpCh && rtcpHasBye(payload) {
			break
		}
		if ch != rtpCh {
			continue
		}
		pkt, ok := parseRTP(payload)
		if !ok || pkt.PT != track.PT {
			continue
		}
		if pkt.Stamp != 0 {
			if !opt.End.IsZero() && int64(pkt.Stamp) >= opt.End.Unix() && (frames == 0 || pkt.TS != lastTS) {
				break // the first picture past the range
			}
			if opt.First != nil {
				opt.First.see(pkt.TS, pkt.Stamp)
			}
		}
		if frames == 0 {
			firstTS, lastTS, frames = pkt.TS, pkt.TS, 1
		} else if pkt.TS != lastTS {
			if d := int32(pkt.TS - lastTS); d > 0 {
				mediaTicks += int64(d)
			}
			lastTS = pkt.TS
			frames++
		}
		if frames > fpsFrames {
			if err := start(); err != nil {
				return track.Codec, dep.written, err
			}
		}
		if err := dep.push(pkt); err != nil {
			c.send("TEARDOWN", base, nil)
			return track.Codec, dep.written, err
		}
		if maxTicks > 0 && mediaTicks >= maxTicks {
			break
		}
	}
	c.send("TEARDOWN", base, nil)
	if frames == 0 {
		return track.Codec, 0, nil // nothing was played
	}
	if err := start(); err != nil {
		return track.Codec, dep.written, err
	}
	return track.Codec, dep.written, nil
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func rtcpHasBye(b []byte) bool {
	for len(b) >= 4 {
		if b[1] == 203 {
			return true
		}
		l := (int(binary.BigEndian.Uint16(b[2:])) + 1) * 4
		if l > len(b) {
			break
		}
		b = b[l:]
	}
	return false
}

// TraceRTSP plays a recording's address briefly (OPTIONS, DESCRIBE, SETUP, PLAY) and
// returns the exchange without secrets: for /nvr probe.
func TraceRTSP(ctx context.Context, rawURL, user, pass string, speed float64) string {
	var tr strings.Builder
	u, err := url.Parse(rawURL)
	if err != nil {
		return err.Error()
	}
	u.User = nil
	uri := u.String()
	c, err := dialRTSP(ctx, u, digest.New(user, pass))
	if err != nil {
		return "connect: " + err.Error()
	}
	defer c.Close()
	c.trace = &tr
	c.do("OPTIONS", uri, nil)
	resp, err := c.do("DESCRIBE", uri, map[string]string{"Accept": "application/sdp"})
	if err == nil {
		base := uri
		if cb := resp.Header.Get("Content-Base"); cb != "" {
			base = cb
		}
		if track, err2 := parseSDP(resp.Body); err2 == nil {
			fmt.Fprintf(&tr, "  SDP: %s, payload type %d, control %q\n", track.Codec, track.PT, track.Control)
			if r, err3 := c.do("SETUP", resolveControl(base, track.Control), map[string]string{"Transport": "RTP/AVP/TCP;unicast;interleaved=0-1"}); err3 == nil {
				c.session = strings.TrimSpace(strings.SplitN(r.Header.Get("Session"), ";", 2)[0])
				hdr := map[string]string{"Range": "npt=0.000-"}
				if speed > 0 {
					hdr["Speed"] = strconv.FormatFloat(speed, 'f', 1, 64)
				}
				_, err = c.do("PLAY", base, hdr)
				c.send("TEARDOWN", base, nil)
			} else {
				err = err3
			}
		} else {
			err = err2
		}
	}
	if err != nil {
		fmt.Fprintf(&tr, "ERROR: %v\n", err)
	} else {
		tr.WriteString("OK: the recording plays\n")
	}
	return tr.String()
}
