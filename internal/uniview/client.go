// Package uniview is a client of the Uniview LightAPI (LAPI, also OEM recorders such as
// BCS Point): device information, channels, disks, recordings and their download over
// RTSP (the playback URL comes from LAPI, the stream is H.264/H.265).
package uniview

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/digest"
)

// ErrDevice is a refusal of the device itself (ResponseCode other than 0).
var ErrDevice = errors.New("device error")

// Client talks to one recorder.
type Client struct {
	base       *url.URL
	http       *http.Client
	plain      *http.Client // main-cgi: no Authorization, the login handle in the body
	auth       *digest.Auth
	user, pass string
}

// NewClient accepts "192.168.1.2", "192.168.1.2:8080" or "https://nvr.lan".
func NewClient(addr, user, pass string, insecure bool) (*Client, error) {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "" || u.Host == "" {
		u, err = url.Parse("http://" + addr)
	}
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid address %q", addr)
	}
	u.Path, u.RawPath, u.RawQuery = "", "", ""
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: insecure},
		ResponseHeaderTimeout: 60 * time.Second,
	}
	auth := digest.New(user, pass)
	return &Client{
		base:  u,
		http:  &http.Client{Transport: &digest.Transport{Auth: auth, Base: tr}},
		plain: &http.Client{Transport: tr},
		auth:  auth,
		user:  user, pass: pass,
	}, nil
}

// Credentials the client logs in with.
func (c *Client) Credentials() (user, pass string) { return c.user, c.pass }

// Host is the host name of the recorder (without port).
func (c *Client) Host() string { return c.base.Hostname() }

type envelope struct {
	Response struct {
		ResponseCode    int             `json:"ResponseCode"`
		SubResponseCode int             `json:"SubResponseCode"`
		ResponseString  string          `json:"ResponseString"`
		Data            json.RawMessage `json:"Data"`
	} `json:"Response"`
}

// Get calls a LAPI path (/LAPI/V1.0/...) and decodes its Data into out (may be nil);
// raw is the whole answer.
func (c *Client) Get(ctx context.Context, path string, q url.Values, out any) (raw []byte, err error) {
	u := *c.base
	u.Path, u.RawQuery = path, q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return raw, fmt.Errorf("%s: HTTP 401: wrong username or password", path)
	}
	if resp.StatusCode != http.StatusOK {
		return raw, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	raw = Lenient(raw)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw, fmt.Errorf("%s: not a LAPI answer: %.80q", path, raw)
	}
	if r := env.Response; r.ResponseCode != 0 {
		return raw, fmt.Errorf("%s: %w: %d/%d %s", path, ErrDevice, r.ResponseCode, r.SubResponseCode, r.ResponseString)
	}
	if out != nil && len(env.Response.Data) > 0 && string(env.Response.Data) != "null" {
		if err := json.Unmarshal(env.Response.Data, out); err != nil {
			return raw, fmt.Errorf("%s: %w", path, err)
		}
	}
	return raw, nil
}

// Device describes the recorder (System/DeviceInfo).
type Device struct {
	DeviceName      string `json:"DeviceName"`
	DeviceModel     string `json:"DeviceModel"`
	SerialNumber    string `json:"SerialNumber"`
	FirmwareVersion string `json:"FirmwareVersion"`
	BuildDate       struct {
		Year, Month, Day int
	} `json:"BuildDate"`
}

// Build is the firmware build date ("" when not told).
func (d Device) Build() string {
	if d.BuildDate.Year == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.BuildDate.Year, d.BuildDate.Month, d.BuildDate.Day)
}

// Uptime of the recorder (System/RunInfo Runtime, seconds).
func (c *Client) Uptime(ctx context.Context) (time.Duration, error) {
	var r struct {
		Runtime int64 `json:"Runtime"`
	}
	_, err := c.Get(ctx, "/LAPI/V1.0/System/RunInfo", nil, &r)
	return time.Duration(r.Runtime) * time.Second, err
}

func (c *Client) DeviceInfo(ctx context.Context) (Device, error) {
	var d Device
	_, err := c.Get(ctx, "/LAPI/V1.0/System/DeviceInfo", nil, &d)
	return d, err
}

// Channel is one video channel (Channels/System/ChannelDetailInfos).
type Channel struct {
	ID          int    `json:"ID"`
	Name        string `json:"Name"`
	Status      int    `json:"Status"` // 0 offline, 1 online, 2 idle (no camera)
	AddressInfo struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"AddressInfo"`
	Manufacturer string `json:"Manufacturer"`
	DeviceModel  string `json:"DeviceModel"`
	OffReason    *int   `json:"OffReason"`
}

var offReasons = map[int]string{
	0: "connecting", 2: "wrong username or password", 3: "network disconnected", 4: "failed to set the live stream",
	5: "failed to start the live stream", 6: "stream stopped", 7: "timeout", 8: "waiting for GB registration",
	9: "GB stream protocol changed", 10: "abnormal PoE power", 11: "insufficient bandwidth", 12: "access denied, weak password",
}

// Reason why an offline camera is offline ("" when unknown).
func (ch Channel) Reason() string {
	if ch.OffReason == nil || ch.Status == 1 {
		return ""
	}
	return offReasons[*ch.OffReason]
}

// State names Status like the Dahua states: Connected, Disconnected, Empty.
func (ch Channel) State() string {
	switch ch.Status {
	case 1:
		return "Connected"
	case 2:
		return "Empty"
	default:
		return "Disconnected"
	}
}

func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var r struct {
		Nums        int       `json:"Nums"`
		DetailInfos []Channel `json:"DetailInfos"`
	}
	if _, err := c.Get(ctx, "/LAPI/V1.0/Channels/System/ChannelDetailInfos", nil, &r); err != nil {
		return nil, err
	}
	if len(r.DetailInfos) == 0 {
		return nil, errors.New("the recorder reports no video channels")
	}
	return r.DetailInfos, nil
}

// Disk is one storage container; capacities in MB.
type Disk struct {
	ID             int    `json:"ID"`
	TotalCapacity  int64  `json:"TotalCapacity"`
	RemainCapacity int64  `json:"RemainCapacity"`
	Status         int    `json:"Status"` // 0 none, 1 unformatted, 2 formatting, 3 normal, 4 dormant, 5 abnormal ...
	Kind           string `json:"-"`      // LocalHDD, SD, Array, NAS, eSATA
}

var diskStates = []string{"Empty", "Unformatted", "Formatting", "Normal", "Dormant", "Abnormal", "Switching", "Unloaded"}

// State names Status.
func (d Disk) State() string {
	if d.Status >= 0 && d.Status < len(diskStates) {
		return diskStates[d.Status]
	}
	return strconv.Itoa(d.Status)
}

// Disks lists the storage containers that hold a disk (Storage/Containers/DetailInfos;
// older recorders answer only Storage/Containers, read leniently).
func (c *Client) Disks(ctx context.Context) ([]Disk, error) {
	var r map[string]json.RawMessage
	if _, err := c.Get(ctx, "/LAPI/V1.0/Storage/Containers/DetailInfos", nil, &r); err != nil {
		var any any
		if _, err2 := c.Get(ctx, "/LAPI/V1.0/Storage/Containers", nil, &any); err2 == nil {
			if ds := findDisks(any, ""); len(ds) > 0 {
				sort.Slice(ds, func(i, j int) bool { return ds[i].Kind+strconv.Itoa(ds[i].ID) < ds[j].Kind+strconv.Itoa(ds[j].ID) })
				return ds, nil
			}
			return nil, fmt.Errorf("%v; Storage/Containers: no disks in the answer", err)
		}
		return nil, err
	}
	var out []Disk
	for _, kind := range []string{"LocalHDD", "SD", "Array", "ExtendCabinet1HDD", "ExtendCabinetHDD", "NAS", "eSATA"} {
		var ds []Disk
		json.Unmarshal(r[kind+"List"], &ds)
		for _, d := range ds {
			if d.Status != 0 { // 0: an empty slot
				d.Kind = kind
				out = append(out, d)
			}
		}
	}
	return out, nil
}

// Time reads the clock of the recorder (UTC seconds).
func (c *Client) Time(ctx context.Context) (time.Time, error) {
	var r struct {
		DeviceTime int64 `json:"DeviceTime"`
	}
	if _, err := c.Get(ctx, "/LAPI/V1.0/System/Time", nil, &r); err != nil {
		return time.Time{}, err
	}
	return time.Unix(r.DeviceTime, 0), nil
}

// Record is one recorded file.
type Record struct {
	FileName   string `json:"FileName"`
	RecordType int64  `json:"RecordType"`
	Begin      int64  `json:"Begin"`
	End        int64  `json:"End"`
}

// Records lists the recordings of a channel's stream (0: main) in [from, to).
func (c *Client) Records(ctx context.Context, channel, stream int, from, to time.Time) ([]Record, error) {
	var r struct {
		Nums        int      `json:"Nums"`
		RecordInfos []Record `json:"RecordInfos"`
	}
	q := url.Values{"Begin": {strconv.FormatInt(from.Unix(), 10)}, "End": {strconv.FormatInt(to.Unix(), 10)}}
	_, err := c.Get(ctx, fmt.Sprintf("/LAPI/V1.0/Channels/%d/Media/Video/Streams/%d/Records", channel, stream), q, &r)
	return r.RecordInfos, err
}

// RecordURL is the RTSP address playing [from, to) of a channel. Its host is replaced
// by the one ffnvr reaches the recorder at (the recorder may report an inner address).
func (c *Client) RecordURL(ctx context.Context, channel int, from, to time.Time) (string, error) {
	var r struct {
		URL string `json:"URL"`
	}
	q := url.Values{"Begin": {strconv.FormatInt(from.Unix(), 10)}, "End": {strconv.FormatInt(to.Unix(), 10)}}
	if _, err := c.Get(ctx, fmt.Sprintf("/LAPI/V1.0/Channels/%d/Media/Video/Streams/RecordURL", channel), q, &r); err != nil {
		return "", err
	}
	return c.ownHost(r.URL)
}

// StreamURL is the live RTSP address of a channel's stream (without credentials); when
// LAPI does not tell it, the usual Uniview address.
func (c *Client) StreamURL(ctx context.Context, channel, stream int) string {
	var r struct {
		URL string `json:"URL"`
	}
	if _, err := c.Get(ctx, fmt.Sprintf("/LAPI/V1.0/Channels/%d/Media/Video/Streams/%d/LiveStreamURL", channel, stream),
		url.Values{"TransType": {"0"}, "TransProtocol": {"0"}}, &r); err == nil && r.URL != "" {
		if u, err := c.ownHost(r.URL); err == nil {
			return u
		}
	}
	return fmt.Sprintf("rtsp://%s:554/unicast/c%d/s%d/live", c.Host(), channel, stream)
}

func (c *Client) ownHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "rtsp" {
		return "", fmt.Errorf("unexpected RTSP address %q", raw)
	}
	port := u.Port()
	if port == "" {
		port = "554"
	}
	u.Host = net.JoinHostPort(c.Host(), port)
	u.User = nil
	return u.String(), nil
}

// ErrEmpty: nothing was recorded in the range.
var ErrEmpty = errors.New("no recordings")

// Open plays [from, to) of a channel and returns its video as an Annex-B stream, its
// codec (H264, H265) and frame rate. speed (0: none) asks the recorder to send faster
// than real time (RTSP Speed header; recorders that do not support it ignore it).
func (c *Client) Open(ctx context.Context, channel int, from, to time.Time, speed float64) (io.ReadCloser, string, float64, error) {
	recs, err := c.Records(ctx, channel, 0, from, to)
	if err != nil && !errors.Is(err, ErrDevice) {
		return nil, "", 0, err
	}
	if len(recs) == 0 {
		return nil, "", 0, ErrEmpty
	}
	rurl, err := c.RecordURL(ctx, channel, from, to)
	if err != nil {
		return nil, "", 0, err
	}
	return c.play(ctx, rurl, playOptions{Speed: speed, Duration: to.Sub(from)})
}

// OpenPlay plays [from, to) of a channel for the player, as the recorder's web client
// does: straight to the playback address (no search first: the player searched the
// day, and a search for every piece would only keep the recorder busy). The stream
// ends with the last picture stamped before to; its FirstFrame tells when its first
// picture was (a range begins at the key frame before its start).
func (c *Client) OpenPlay(ctx context.Context, channel int, from, to time.Time, speed float64) (io.ReadCloser, string, float64, error) {
	rurl, err := c.RecordURL(ctx, channel, from, to)
	if err != nil {
		return nil, "", 0, err
	}
	// Duration: the safety stop, with a key frame's lead before from to spare
	return c.play(ctx, rurl, playOptions{Speed: speed, Duration: to.Sub(from) + time.Minute, End: to, First: &firstFrame{}})
}

// play reads a playback address on one of the recorder's streams (waited for while
// MaxStreams are in use), given back when the stream ends.
func (c *Client) play(ctx context.Context, rurl string, opt playOptions) (io.ReadCloser, string, float64, error) {
	release, err := c.stream(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	ctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	type started struct {
		codec string
		fps   float64
	}
	startc := make(chan started, 1)
	done := make(chan error, 1)
	opt.Start = func(codec string, fps float64) { startc <- started{codec, fps} }
	go func() {
		defer release()
		_, n, err := downloadRTSP(ctx, rurl, c.user, c.pass, pw, opt)
		if err == nil && n == 0 {
			err = ErrEmpty
		}
		pw.CloseWithError(err)
		done <- err
	}()
	select {
	case s := <-startc:
		return readCloser{pr, func() { cancel(); pr.Close() }, opt.First}, s.codec, s.fps, nil
	case err := <-done:
		cancel()
		if err == nil {
			err = ErrEmpty
		}
		return nil, "", 0, err
	}
}

type readCloser struct {
	*io.PipeReader
	stop  func()
	first *firstFrame
}

func (r readCloser) Close() error { r.stop(); return nil }

// FirstFrame: the time of the stream's first picture, from the recorder's stamps (ok
// false: not told). Exact to the frame once the stream has gone on to its next second.
func (r readCloser) FirstFrame() (time.Time, bool) {
	if r.first == nil {
		return time.Time{}, false
	}
	return r.first.get()
}

// Days: the days of a month (1…31) on which a channel recorded, from the recorder's
// calendar (Records/DailyDistribution, a status per day of the month on the recorder's
// clock: 0 nothing recorded), as its web client asks for it. Recorders ask for it in
// two forms, both captured from their web clients:
//
//	…/Channels/4/Media/Video/Streams/0/Records/DailyDistribution  {"Num":30,"DailyStatus":[2, 2, 2, 0, …]}
//	…/Channels/18/Media/Video/Streams/Records/DailyDistribution   {"Nums":30,"DailyStatuses":[1, 1, 1, 1, 1, 0, …]}
//
// (the second of a BCS-P-NVR3204-4K-II); the first is tried first, then the second.
func (c *Client) Days(ctx context.Context, channel int, month time.Time) ([]int, error) {
	q := url.Values{"Year": {strconv.Itoa(month.Year())}, "Month": {strconv.Itoa(int(month.Month()))}}
	var firstErr error
	for _, path := range []string{"/Streams/0/Records/DailyDistribution", "/Streams/Records/DailyDistribution"} {
		var r struct {
			DailyStatus   []int `json:"DailyStatus"`
			DailyStatuses []int `json:"DailyStatuses"`
		}
		_, err := c.Get(ctx, fmt.Sprintf("/LAPI/V1.0/Channels/%d/Media/Video", channel)+path, q, &r)
		st := r.DailyStatus
		if st == nil {
			st = r.DailyStatuses
		}
		if err == nil && st == nil {
			err = fmt.Errorf("%s: no daily statuses in the answer", path)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				break
			}
			continue
		}
		var days []int
		for i, v := range st {
			if v != 0 && i < 31 {
				days = append(days, i+1)
			}
		}
		return days, nil
	}
	return nil, firstErr
}

// Trace plays a playback address briefly on one of the recorder's streams: TraceRTSP.
func (c *Client) Trace(ctx context.Context, rawURL string, speed float64) string {
	release, err := c.stream(ctx)
	if err != nil {
		return "waiting for a stream of the recorder: " + err.Error()
	}
	defer release()
	return TraceRTSP(ctx, rawURL, c.user, c.pass, speed)
}

// findDisks walks any JSON answer for objects that look like disks: a total capacity
// and a free (remaining) or used one, in MB.
func findDisks(v any, kind string) []Disk {
	var out []Disk
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, findDisks(e, kind)...)
		}
	case map[string]any:
		num := func(keys ...string) (int64, bool) {
			for _, k := range keys {
				for kk, vv := range x {
					if strings.EqualFold(kk, k) {
						if f, ok := vv.(float64); ok {
							return int64(f), true
						}
					}
				}
			}
			return 0, false
		}
		total, okT := num("TotalCapacity", "Capacity", "TotalSpace", "Total")
		remain, okR := num("RemainCapacity", "FreeCapacity", "FreeSpace", "Remain", "Free")
		used, okU := num("UsedCapacity", "UsedSpace", "Used")
		if okT && (okR || okU) {
			if !okR {
				remain = total - used
			}
			d := Disk{TotalCapacity: total, RemainCapacity: remain, Status: 3, Kind: or(kind, "HDD")}
			if id, ok := num("ID", "Id"); ok {
				d.ID = int(id)
			}
			if st, ok := num("Status"); ok {
				d.Status = int(st)
			}
			if d.Status != 0 {
				out = append(out, d)
			}
			return out
		}
		for k, vv := range x {
			kind := strings.TrimSuffix(strings.TrimSuffix(k, "List"), "Infos")
			if strings.HasSuffix(kind, "HDDs") {
				kind = strings.TrimSuffix(kind, "s")
			}
			out = append(out, findDisks(vv, kind)...)
		}
	}
	return out
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
