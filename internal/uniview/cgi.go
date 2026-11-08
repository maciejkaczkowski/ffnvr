package uniview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// The recorder's web client asks some things only of main-cgi, the older interface
// beside LAPI: what kind each part of a recording is (normal or recorded for an event,
// the red of its timeline), as captured from a BCS-P-NVR3204-4K-II (B3121P21C00815):
//
//	POST /cgi-bin/main-cgi  json={"cmd":20,"szUserName":"admin","u32UserLoginHandle":2020944489}
//	  {"SimpleChList":[{"stResourceCode":"1100010001000001","szResourceName":"IP Camera 01",…},…],…}
//	POST /cgi-bin/main-cgi  json={"cmd":81,"astResourceCode":["1100120001000012"],"u32RecordType":0,
//	                              "u32StoretypeCount":0,"u32Begin":1793833200,"u32End":1793919599,…}
//	  {"recordList":[{"u32RecordType":0,"u32Begin":1793833200,"u32End":1793838542},
//	                 {"u32RecordType":1,"u32Begin":1793838543,"u32End":1793838661},…],"code":0,"success":true}
//
// A request carries no Authorization: the login handle in it is the whole of it. The
// handle is the nonce the web client answers LAPI's Digest challenges with (both
// captured recorders: the same number in both places, and after LAPI's KeepAlive gave
// a NextNonce, that one in both), so ffnvr uses the nonce of its own LAPI session.
// A camera's code ends with its channel (4 hex digits: …0012 is channel 18, also on
// a recorder of another form of code, 1101000000040004 for channel 4).

// ErrCGI: main-cgi refused a command (or the recorder has none).
var ErrCGI = errors.New("main-cgi refused")

// cgi sends a command of main-cgi with the login handle and decodes the answer into out.
func (c *Client) cgi(ctx context.Context, cmd map[string]any, out any) error {
	try := func() ([]byte, error) {
		h := c.auth.Nonce()
		if h == "" {
			return nil, errors.New("no login handle (no LAPI session)")
		}
		body := map[string]any{"szUserName": c.user}
		if n, err := strconv.ParseUint(h, 10, 32); err == nil {
			body["u32UserLoginHandle"] = n // as the recorder's web client sends it
		} else {
			body["u32UserLoginHandle"] = h
		}
		for k, v := range cmd {
			body[k] = v
		}
		js, _ := json.Marshal(body)
		u := *c.base
		u.Path, u.RawQuery = "/cgi-bin/main-cgi", ""
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(append([]byte("json="), js...)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		resp, err := c.plain.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return raw, fmt.Errorf("main-cgi cmd %v: HTTP %d", cmd["cmd"], resp.StatusCode)
		}
		var st struct {
			Code    *int  `json:"code"`
			Success *bool `json:"success"`
		}
		if json.Unmarshal(raw, &st) != nil {
			return raw, fmt.Errorf("main-cgi cmd %v: %w: not JSON: %.80q", cmd["cmd"], ErrCGI, raw)
		}
		if (st.Success != nil && !*st.Success) || (st.Code != nil && *st.Code != 0) {
			code := -1
			if st.Code != nil {
				code = *st.Code
			}
			return raw, fmt.Errorf("main-cgi cmd %v: %w: code %d", cmd["cmd"], ErrCGI, code)
		}
		return raw, nil
	}
	// a LAPI request first: a session (and its handle), renewed when it went stale
	if c.auth.Nonce() == "" {
		if _, err := c.Time(ctx); err != nil {
			return err
		}
	}
	raw, err := try()
	if errors.Is(err, ErrCGI) {
		before := c.auth.Nonce()
		if _, err2 := c.Time(ctx); err2 == nil && c.auth.Nonce() != before {
			raw, err = try()
		}
	}
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("main-cgi cmd %v: %w", cmd["cmd"], err)
		}
	}
	return nil
}

// CGI sends a command of main-cgi and returns its raw answer: for /nvr probe.
func (c *Client) CGI(ctx context.Context, cmd map[string]any) (string, error) {
	var raw json.RawMessage
	err := c.cgi(ctx, cmd, &raw)
	return string(raw), err
}

// codes of the cameras by channel, of every recorder, kept for a while
var resources = struct {
	sync.Mutex
	m map[string]resourceCodes
}{m: map[string]resourceCodes{}}

type resourceCodes struct {
	at    time.Time
	codes map[int]string
	err   error
}

// channelOfCode: the channel a camera's code ends with (0: none).
func channelOfCode(code string) int {
	if len(code) < 4 {
		return 0
	}
	n, err := strconv.ParseUint(code[len(code)-4:], 16, 16)
	if err != nil {
		return 0
	}
	return int(n)
}

// ResourceCode: the code main-cgi names a channel's camera with (cmd 20).
func (c *Client) ResourceCode(ctx context.Context, channel int) (string, error) {
	key := c.Host()
	resources.Lock()
	rc, ok := resources.m[key]
	resources.Unlock()
	keep := 10 * time.Minute
	if rc.err != nil {
		keep = time.Minute
	}
	if !ok || time.Since(rc.at) > keep {
		var r struct {
			SimpleChList []struct {
				Code string `json:"stResourceCode"`
			} `json:"SimpleChList"`
		}
		rc = resourceCodes{at: time.Now(), codes: map[int]string{}}
		if err := c.cgi(ctx, map[string]any{"cmd": 20}, &r); err != nil {
			rc.err = err
		}
		for i, ch := range r.SimpleChList {
			n := channelOfCode(ch.Code)
			if n == 0 {
				n = i + 1
			}
			rc.codes[n] = ch.Code
		}
		if rc.err == nil && len(rc.codes) == 0 {
			rc.err = fmt.Errorf("main-cgi cmd 20: %w: no cameras", ErrCGI)
		}
		if ctx.Err() == nil {
			resources.Lock()
			resources.m[key] = rc
			resources.Unlock()
		}
	}
	if rc.err != nil {
		return "", rc.err
	}
	code, ok := rc.codes[channel]
	if !ok {
		return "", fmt.Errorf("main-cgi: no camera of channel %d", channel)
	}
	return code, nil
}

// Segment of a recording and its kind: 0 recorded normally, 1 for an event (motion,
// an alarm; the recorder's timeline draws it red).
type Segment struct {
	Type       int
	Begin, End time.Time
}

// Segments: the parts of a channel's recordings in [from, to) with their kinds (main-cgi
// cmd 81, as the recorder's web client draws its timeline).
func (c *Client) Segments(ctx context.Context, channel int, from, to time.Time) ([]Segment, error) {
	code, err := c.ResourceCode(ctx, channel)
	if err != nil {
		return nil, err
	}
	var r struct {
		RecordList []struct {
			Type  int   `json:"u32RecordType"`
			Begin int64 `json:"u32Begin"`
			End   int64 `json:"u32End"`
		} `json:"recordList"`
	}
	if err := c.cgi(ctx, map[string]any{"cmd": 81, "astResourceCode": []string{code}, "u32RecordType": 0,
		"u32StoretypeCount": 0, "u32Begin": from.Unix(), "u32End": to.Unix() - 1}, &r); err != nil {
		return nil, err
	}
	out := make([]Segment, 0, len(r.RecordList))
	for _, s := range r.RecordList {
		out = append(out, Segment{Type: s.Type, Begin: time.Unix(s.Begin, 0), End: time.Unix(s.End, 0)})
	}
	return out, nil
}
