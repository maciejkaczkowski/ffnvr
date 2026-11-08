// Package dahua is a client of the Dahua HTTP API (CGI): device information, channels,
// disks, searching recordings (mediaFileFind.cgi) and downloading them (loadfile.cgi,
// RPC_Loadfile). Based on the Dahua HTTP API documentation and github.com/vov4uk/Dahua.Api.
package dahua

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/digest"
)

const (
	TimeLayout   = "2006-01-02 15:04:05"
	timeLayoutIn = "2006-1-2 15:4:5" // the device may drop leading zeros
	findBatch    = 100               // maximum of findNextFile
)

// ErrDevice is a refusal of the device itself ("Error" in the body), as opposed to a
// network or authentication problem.
var ErrDevice = errors.New("device error")

// Client talks to one recorder.
type Client struct {
	base       *url.URL
	http       *http.Client
	plain      http.RoundTripper // without Digest (JSON-RPC has its own login)
	user, pass string
}

// NewClient accepts "192.168.1.2", "192.168.1.2:8080" or "https://nvr.lan".
func NewClient(addr, user, pass string, insecure bool) (*Client, error) {
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid address %q", addr)
	}
	u.Path, u.RawPath, u.RawQuery = "", "", ""
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure},
		ResponseHeaderTimeout: 90 * time.Second,
		MaxIdleConnsPerHost:   8,
	}
	return &Client{
		base: u,
		// no global timeout: downloading an hour of video takes long; callers use contexts
		http:  &http.Client{Transport: &digest.Transport{Auth: digest.New(user, pass), Base: tr}},
		plain: tr, user: user, pass: pass,
	}, nil
}

// Host is the host name of the recorder (without port).
func (c *Client) Host() string { return c.base.Hostname() }

type param struct{ k, v string }

// Dahua is picky about encoding: brackets in parameter names (condition.Types[0])
// must stay literal and spaces must be %20, not '+'.
func encodeQuery(ps []param) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.k + "=" + escapeValue(p.v)
	}
	return strings.Join(parts, "&")
}

func escapeValue(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || c == ':' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (c *Client) get(ctx context.Context, rawPath, rawQuery string) (*http.Response, error) {
	u := *c.base
	u.Path, u.RawPath, u.RawQuery = rawPath, rawPath, rawQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.URL = &u
	return c.http.Do(req)
}

// cgi calls /cgi-bin/<script> and returns the text of the answer.
func (c *Client) cgi(ctx context.Context, script string, ps ...param) (string, error) {
	resp, err := c.get(ctx, "/cgi-bin/"+script, encodeQuery(ps))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(body))
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("%s: HTTP 401: wrong username or password", script)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d: %s", script, resp.StatusCode, firstLine(s))
	}
	if strings.HasPrefix(strings.ToLower(s), "error") {
		return "", fmt.Errorf("%s: %w: %s", script, ErrDevice, strings.Join(strings.Fields(s), " "))
	}
	return s, nil
}

func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------- search

// Query of FindRecordings.
type Query struct {
	Channel  int // from 1
	From, To time.Time
	Types    []string // e.g. dav, jpg, mp4
	Flags    []string // e.g. Timing, Event, Manual, Marker
	Events   []string // e.g. VideoMotion, AlarmLocal
}

// Recording is one file found on the recorder.
type Recording struct {
	Channel  int
	Start    time.Time
	End      time.Time
	Type     string
	FilePath string
	Length   int64
	Flags    []string
	Events   []string
}

func (r Recording) Duration() time.Duration { return r.End.Sub(r.Start) }

// FindRecordings lists the recorded files of a channel. Most firmware answers an
// empty range with an error of the device (ErrDevice).
func (c *Client) FindRecordings(ctx context.Context, q Query) ([]Recording, error) {
	out, err := c.cgi(ctx, "mediaFileFind.cgi", param{"action", "factory.create"})
	if err != nil {
		return nil, fmt.Errorf("factory.create: %w", err)
	}
	obj := parseKV(out)["result"]
	if obj == "" {
		return nil, fmt.Errorf("factory.create: no object id in %q", out)
	}
	defer func() {
		// clean up even when cancelled: the recorder has few search objects
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.cgi(cctx, "mediaFileFind.cgi", param{"action", "close"}, param{"object", obj})
		c.cgi(cctx, "mediaFileFind.cgi", param{"action", "destroy"}, param{"object", obj})
	}()

	ps := []param{
		{"action", "findFile"},
		{"object", obj},
		{"condition.Channel", strconv.Itoa(q.Channel)},
		{"condition.StartTime", q.From.Format(TimeLayout)},
		{"condition.EndTime", q.To.Format(TimeLayout)},
	}
	for i, v := range q.Types {
		ps = append(ps, param{fmt.Sprintf("condition.Types[%d]", i), v})
	}
	for i, v := range q.Flags {
		ps = append(ps, param{fmt.Sprintf("condition.Flags[%d]", i), v})
	}
	for i, v := range q.Events {
		ps = append(ps, param{fmt.Sprintf("condition.Events[%d]", i), v})
	}
	if _, err := c.cgi(ctx, "mediaFileFind.cgi", ps...); err != nil {
		return nil, fmt.Errorf("findFile (channel %d): %w", q.Channel, err)
	}

	var recs []Recording
	for {
		out, err := c.cgi(ctx, "mediaFileFind.cgi",
			param{"action", "findNextFile"}, param{"object", obj}, param{"count", strconv.Itoa(findBatch)})
		if err != nil {
			return recs, fmt.Errorf("findNextFile: %w", err)
		}
		kv := parseKV(out)
		n, _ := strconv.Atoi(kv["found"])
		for i := 0; i < n; i++ {
			if r, ok := recordingFromKV(kv, i, q.Channel); ok {
				recs = append(recs, r)
			}
		}
		if n < findBatch {
			break
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Start.Before(recs[j].Start) })
	return recs, nil
}

func recordingFromKV(kv map[string]string, i, channel int) (Recording, bool) {
	p := fmt.Sprintf("items[%d].", i)
	r := Recording{
		Channel:  channel,
		Type:     kv[p+"Type"],
		FilePath: kv[p+"FilePath"],
		Flags:    indexed(kv, p+"Flags"),
		Events:   indexed(kv, p+"Events"),
	}
	r.Length, _ = strconv.ParseInt(kv[p+"Length"], 10, 64)
	var err1, err2 error
	r.Start, err1 = time.ParseInLocation(timeLayoutIn, kv[p+"StartTime"], time.Local)
	r.End, err2 = time.ParseInLocation(timeLayoutIn, kv[p+"EndTime"], time.Local)
	return r, err1 == nil && err2 == nil && r.FilePath != ""
}

func indexed(kv map[string]string, prefix string) []string {
	var out []string
	for j := 0; ; j++ {
		v, ok := kv[fmt.Sprintf("%s[%d]", prefix, j)]
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

// ---------------------------------------------------------------- download

// OpenFile downloads a whole recorded file through RPC_Loadfile.
func (c *Client) OpenFile(ctx context.Context, filePath string) (io.ReadCloser, int64, error) {
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	// the path goes literally ([R][0@0] etc.): some firmware rejects %5B
	resp, err := c.get(ctx, "/cgi-bin/RPC_Loadfile"+filePath, "")
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("RPC_Loadfile: HTTP %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}

// OpenRange asks the recorder for an exact time range (loadfile.cgi); the stream is
// in the DAV format (ffmpeg: -f dhav). subtype 0 is the main stream, 1 the extra one.
func (c *Client) OpenRange(ctx context.Context, channel int, from, to time.Time, subtype int) (io.ReadCloser, error) {
	q := encodeQuery([]param{
		{"action", "startLoad"},
		{"channel", strconv.Itoa(channel)},
		{"startTime", from.Format(TimeLayout)},
		{"endTime", to.Format(TimeLayout)},
		{"subtype", strconv.Itoa(subtype)},
	})
	resp, err := c.get(ctx, "/cgi-bin/loadfile.cgi", q)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("loadfile.cgi: HTTP %d", resp.StatusCode)
	}
	return readCloser{skipPreamble(resp.Body), resp.Body}, nil
}

// skipPreamble drops the binary header loadfile.cgi puts before the stream: the video
// starts with the frame marker "DHAV".
func skipPreamble(r io.Reader) io.Reader {
	br := bufio.NewReaderSize(r, 64<<10)
	buf, _ := br.Peek(64 << 10)
	if i := bytes.Index(buf, []byte("DHAV")); i > 0 {
		br.Discard(i)
	}
	return br
}

type readCloser struct {
	io.Reader
	io.Closer
}

// Raw calls a CGI script with "key", "value" pairs and returns the answer as it is
// (for diagnostics).
func (c *Client) Raw(ctx context.Context, script string, kv ...string) (string, error) {
	var ps []param
	for i := 0; i+1 < len(kv); i += 2 {
		ps = append(ps, param{kv[i], kv[i+1]})
	}
	return c.cgi(ctx, script, ps...)
}
