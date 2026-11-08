package uniview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Events of a camera or a recorder (LightAPI 2.1 "Alarm Notification"). LAPI keeps no
// connection open for them: the device pushes them to the subscriber.
//
//	POST /LAPI/V1.0/System/Event/Subscription      {"AddressType":0,"IPAddress":"192.168.1.10",
//	                                                "Port":8091,"Duration":60}
//	  -> {"ID":3,"Reference":"192.168.1.10:8091/Subscription/Subscribers/3",
//	      "CurrentTime":1477104900,"TerminationTime":1477104960}
//	PUT    /LAPI/V1.0/System/Event/Subscription/3  {"Duration":60}   renews it
//	DELETE /LAPI/V1.0/System/Event/Subscription/3                   ends it
//
// and the device sends to http://IPAddress:Port
//
//	POST /LAPI/V1.0/System/Event/Notification/Alarm
//	  {"Reference":"192.168.1.10:8091/Subscription/Subscribers/3",
//	   "AlarmInfo":{"AlarmType":"MotionAlarmOn","TimeStamp":1489040894,"AlarmSeq":327}}
//
// Alarms come to Notification/Alarm (AlarmInfo) and, from recorders, to paths of their
// own with a picture (Notification/FastMovingDetection, HatDetection, ...: AlarmType at
// the top). Other paths carry data, no alarms: structured objects (Structure), people
// (PersonInfo), counts (PeopleCount/LineRuleData, PeopleCount/AreaRuleData), ...
//
// The alarm types are the strings of the attachment "Alarm&Operate Log Type" of the
// document: a state has a beginning and an end (MotionAlarmOn and MotionAlarmOff,
// PeopleGathering and PeopleGatheringCleared), the others are moments
// (LineDetectorCrossed, FieldDetectorObjectsInside).
//
// A subscription lasts Duration seconds (30-3600) unless renewed; a subscriber that is
// gone stops getting pushes by itself. A renewal that fails is the sign that the device
// is gone or has forgotten the subscription (a restart).

// SubscriptionPath is where subscriptions are made.
const SubscriptionPath = "/LAPI/V1.0/System/Event/Subscription"

// NotificationPath is the beginning of the paths the device pushes to.
const NotificationPath = "/LAPI/V1.0/System/Event/Notification/"

// Subscription is one subscription the device gave.
type Subscription struct {
	ID        int64
	Reference string
	// Address is where the device pushes to: IPAddress:Port given to it.
	Address string
	// Until: the end the device told (its clock), zero when not told.
	Until time.Time
}

// number reads an unsigned long as LAPI sends it (a number, at times a string).
type number int64

func (n *number) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return fmt.Errorf("not a number: %.40q", s)
		}
		v = int64(f)
	}
	*n = number(v)
	return nil
}

func unixOrZero(v number) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(v), 0)
}

// call sends JSON (body nil: none) with method to a LAPI path and decodes Data into out
// (may be nil); both forms of the answer's code are checked.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	u := *c.base
	u.Path = path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%s: HTTP 401: wrong username or password", path)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s: HTTP 404: the device has no such interface", path)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	var env struct {
		Response struct {
			ResponseCode    int             `json:"ResponseCode"`
			SubResponseCode int             `json:"SubResponseCode"`
			ResponseString  string          `json:"ResponseString"`
			StatusCode      int             `json:"StatusCode"`
			StatusString    string          `json:"StatusString"`
			Data            json.RawMessage `json:"Data"`
			CreatedID       number          `json:"CreatedID"`
		} `json:"Response"`
	}
	raw = Lenient(raw)
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s: not a LAPI answer: %.80q", path, raw)
	}
	if r := env.Response; r.ResponseCode != 0 || r.StatusCode != 0 {
		msg := r.ResponseString
		if r.StatusString != "" && r.StatusString != msg {
			msg = strings.TrimSpace(msg + " " + r.StatusString)
		}
		return fmt.Errorf("%s: %w: %d/%d %s", path, ErrDevice, r.ResponseCode, r.SubResponseCode, msg)
	}
	if c, ok := out.(*created); ok {
		c.ID = int64(env.Response.CreatedID)
		out = c.Data
	}
	if d := env.Response.Data; out != nil && len(d) > 0 && string(d) != "null" {
		if err := json.Unmarshal(d, out); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// created: Data of an answer that creates something, and its CreatedID (beside Data).
type created struct {
	ID   int64
	Data any
}

// Lenient makes JSON of the devices readable. Seen in the answer of an IPC to a
// subscription: a comma before } ("TerminationTime": 1794064542,\n}) and the last }
// missing. Commas before } or ] are removed, missing } and ] added at the end; strings
// are left as they are; valid JSON is returned unchanged.
func Lenient(b []byte) []byte {
	if json.Valid(b) {
		return b
	}
	out := make([]byte, 0, len(b)+4)
	var open []byte // the closers awaited
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			open = append(open, '}')
		case '[':
			open = append(open, ']')
		case '}', ']':
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\r' || b[j] == '\n') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue // the comma before the end
			}
		}
		out = append(out, c)
	}
	out = bytes.TrimRight(out, " \t\r\n")
	for len(out) > 0 && out[len(out)-1] == ',' && len(open) > 0 {
		out = out[:len(out)-1]
	}
	for i := len(open) - 1; i >= 0; i-- {
		out = append(out, open[i])
	}
	return out
}

// durationSeconds: what LAPI takes, 30 to 3600 s.
func durationSeconds(d time.Duration) int {
	return min(max(int(d/time.Second), 30), 3600)
}

// Subscribe asks the device to push its events to host:port for d (30 s to 1 h). With
// host "" it is the address this host speaks to the device from (LocalAddress). The
// devices take only IPv4 addresses.
func (c *Client) Subscribe(ctx context.Context, host string, port int, d time.Duration) (Subscription, error) {
	if host == "" {
		h, err := c.LocalAddress()
		if err != nil {
			return Subscription{}, err
		}
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return Subscription{}, fmt.Errorf("the device pushes only to an IPv4 address, not to %q", host)
	}
	var r struct {
		ID              number `json:"ID"`
		Reference       string `json:"Reference"`
		CurrentTime     number `json:"CurrentTime"`
		TerminationTime number `json:"TerminationTime"`
	}
	body := map[string]any{"AddressType": 0, "IPAddress": ip.To4().String(), "Port": port, "Duration": durationSeconds(d)}
	cr := &created{Data: &r}
	if err := c.call(ctx, http.MethodPost, SubscriptionPath, body, cr); err != nil {
		return Subscription{}, err
	}
	s := Subscription{ID: int64(r.ID), Reference: r.Reference, Address: net.JoinHostPort(ip.To4().String(), strconv.Itoa(port)),
		Until: unixOrZero(r.TerminationTime)}
	if s.ID == 0 { // an answer without ID in Data: CreatedID, else the end of Reference
		if cr.ID != 0 {
			s.ID = cr.ID
		} else if id, ok := ReferenceID(r.Reference); ok {
			s.ID = id
		}
	}
	return s, nil
}

// LocalAddress: the address of this host on the way to the device, the one the device
// can push to (no NAT between them). It is the routing's answer; nothing is sent.
func (c *Client) LocalAddress() (string, error) {
	port := c.base.Port()
	if port == "" {
		port = "80"
	}
	conn, err := net.Dial("udp", net.JoinHostPort(c.base.Hostname(), port))
	if err != nil {
		return "", fmt.Errorf("no way to the device: %w", err)
	}
	defer conn.Close()
	h, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return "", err
	}
	return h, nil
}

// Renew keeps a subscription for d more; it tells the new end (zero when not told).
func (c *Client) Renew(ctx context.Context, id int64, d time.Duration) (time.Time, error) {
	var r struct {
		TerminationTime number `json:"TerminationTime"`
	}
	err := c.call(ctx, http.MethodPut, SubscriptionPath+"/"+strconv.FormatInt(id, 10), map[string]any{"Duration": durationSeconds(d)}, &r)
	return unixOrZero(r.TerminationTime), err
}

// Unsubscribe ends a subscription.
func (c *Client) Unsubscribe(ctx context.Context, id int64) error {
	return c.call(ctx, http.MethodDelete, SubscriptionPath+"/"+strconv.FormatInt(id, 10), nil, nil)
}

// Events keeps the device subscribed until ctx ends: it subscribes (host, port as
// Subscribe), tells subscribed the subscription and renews it every renew (the
// subscription lasts three times that, at least 30 s). It returns why it ended: a
// renewal failed (the device is gone, restarted, forgot the subscription) - the caller
// subscribes again. At the end the subscription is ended (once, without waiting long).
// The pushes themselves come to the caller's HTTP server (ParseNotification).
func (c *Client) Events(ctx context.Context, host string, port int, renew time.Duration, subscribed func(Subscription)) error {
	if renew <= 0 {
		renew = 20 * time.Second
	}
	d := 3 * renew
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	sub, err := c.Subscribe(sctx, host, port, d)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.Unsubscribe(uctx, sub.ID)
	}()
	if subscribed != nil {
		subscribed(sub)
	}
	t := time.NewTicker(renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, max(renew, 10*time.Second))
		_, err := c.Renew(rctx, sub.ID, d)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("renewing the subscription: %w", err)
		}
	}
}

// ReferenceID: the subscription a Reference names, its last number
// ("192.168.0.13:80/Subscription/Subscribers/1" -> 1).
func ReferenceID(ref string) (int64, bool) {
	ref = strings.TrimRight(strings.TrimSpace(ref), "/")
	i := len(ref)
	for i > 0 && ref[i-1] >= '0' && ref[i-1] <= '9' {
		i--
	}
	if i == len(ref) || i > 0 && ref[i-1] != '/' && ref[i-1] != '=' {
		return 0, false
	}
	id, err := strconv.ParseInt(ref[i:], 10, 64)
	return id, err == nil
}

// Notification is one push of the device.
type Notification struct {
	// Path: what follows NotificationPath - Alarm, Structure, PeopleCount/LineRuleData...
	Path      string
	Reference string
	// Type: the alarm type as sent (MotionAlarmOn); "" for a push of data
	Type    string
	Time    time.Time // TimeStamp, the device's clock (zero when none)
	Seq     int64     // AlarmSeq, Seq
	SrcID   int64     // AlarmSrcID, SourceID, SrcID, ChannelID
	SrcType int       // AlarmSrcType of a recorder (8 video channel, 9 alarm input...), -1: not told
	SrcName string    // AlarmSrcName, SourceName, SrcName
	Picture []byte    // the first JPEG carried (base64 in the push)
	Raw     string    // the JSON on one line, long strings (pictures) cut
}

// Alarm: the push is an alarm (it has an alarm type), not data.
func (n Notification) Alarm() bool { return n.Type != "" }

// clearedStarts: the alarm types whose end is <type>Cleared (the others end in Off).
var clearedStarts = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`AntiDisassemblyAlarm AuxiliaryAlarm BurglaryAlarm Bypass DuressAlarm
		EmergencyAlarm ExpansionDeviceFailureAlarm Failure FireAlarm FireDetectorFailure IPConflict
		NetworkDisconnected PeopleGathering ScheduledRecordingFailure StorageFailure TamperAlarm ZoneAlarm ZoneFailure`) {
		clearedStarts[t] = true
	}
}

// Kind of the event: an alarm type without the On, Off or Cleared that tells its
// beginning or end (MotionAlarmOn, MotionAlarmOff -> MotionAlarm; LineDetectorCrossed
// as it is); a push of data is named by its path with \ (PeopleCount\LineRuleData).
func (n Notification) Kind() string {
	if n.Type == "" {
		return strings.ReplaceAll(strings.Trim(n.Path, "/"), "/", `\`)
	}
	k, _ := splitType(n.Type)
	return k
}

// State: "start", "stop", or "" for a moment.
func (n Notification) State() string {
	if n.Type == "" {
		return ""
	}
	_, st := splitType(n.Type)
	return st
}

func splitType(t string) (kind, state string) {
	switch {
	case len(t) > 3 && strings.HasSuffix(t, "Off"):
		return t[:len(t)-3], "stop"
	case len(t) > 2 && strings.HasSuffix(t, "On"):
		return t[:len(t)-2], "start"
	case len(t) > 7 && strings.HasSuffix(t, "Cleared"):
		return t[:len(t)-7], "stop"
	case clearedStarts[t]:
		return t, "start"
	}
	return t, ""
}

// RecorderChannel: the video channel (from 1) of a recorder's push, 0 for the recorder
// itself. AlarmSrcType tells what AlarmSrcID is: 8 a video channel, 9 an alarm input
// (of a camera: channel*100+input, of the recorder: below 100), 0-7 a disk, 10 the
// system. Without AlarmSrcType the ID is taken for a video channel.
func (n Notification) RecorderChannel() int {
	switch {
	case n.SrcID <= 0:
		return 0
	case n.SrcType == 8 || n.SrcType < 0:
		return int(n.SrcID)
	case n.SrcType == 9:
		return int(n.SrcID / 100)
	}
	return 0
}

// Text: the alarm type as sent and its source, for the list of events.
func (n Notification) Text() string {
	t := n.Type
	if t == "" {
		t = n.Path
	}
	if n.SrcName != "" {
		t += " (" + n.SrcName + ")"
	}
	return t
}

// ParseNotification reads a push: path is the URL path it came to (anything after
// NotificationPath names it; another path is taken as an alarm push), body its JSON.
// Cameras send it as a form field (Content-Type text/plain, body Json={...}), at times
// URL-encoded; both are taken, and bare JSON too.
func ParseNotification(path string, body []byte) (Notification, error) {
	n := Notification{Path: "Alarm", SrcType: -1}
	body = pushJSON(body)
	if i := strings.Index(path, NotificationPath); i >= 0 {
		n.Path = strings.Trim(path[i+len(NotificationPath):], "/")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return n, fmt.Errorf("not JSON: %.80q", body)
	}
	var f struct {
		Reference string `json:"Reference"`
		AlarmInfo *struct {
			AlarmType    string  `json:"AlarmType"`
			TimeStamp    number  `json:"TimeStamp"`
			AlarmSeq     number  `json:"AlarmSeq"`
			AlarmSrcID   number  `json:"AlarmSrcID"`
			AlarmSrcType *number `json:"AlarmSrcType"`
			AlarmSrcName string  `json:"AlarmSrcName"`
		} `json:"AlarmInfo"`
		AlarmType  string `json:"AlarmType"`
		TimeStamp  number `json:"TimeStamp"`
		Seq        number `json:"Seq"`
		SourceID   number `json:"SourceID"`
		SrcID      number `json:"SrcID"`
		ChannelID  number `json:"ChannelID"`
		SourceName string `json:"SourceName"`
		SrcName    string `json:"SrcName"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return n, fmt.Errorf("unreadable push: %v", err)
	}
	n.Reference = f.Reference
	if a := f.AlarmInfo; a != nil && a.AlarmType != "" {
		n.Type, n.Time, n.Seq, n.SrcID, n.SrcName = strings.TrimSpace(a.AlarmType), unixOrZero(a.TimeStamp), int64(a.AlarmSeq), int64(a.AlarmSrcID), a.AlarmSrcName
		if a.AlarmSrcType != nil {
			n.SrcType = int(*a.AlarmSrcType)
		}
	} else {
		n.Type = strings.TrimSpace(f.AlarmType)
		n.Time, n.Seq = unixOrZero(f.TimeStamp), int64(f.Seq)
		n.SrcID = int64(max(f.SourceID, f.SrcID, f.ChannelID))
		n.SrcName = or(f.SourceName, f.SrcName)
	}
	if n.Path == "" {
		n.Path = "Alarm"
	}
	n.Picture = firstJPEG(top)
	n.Raw = compactJSON(top)
	return n, nil
}

// pushJSON: the JSON of a push body - without "Json=" before it, URL-decoded when it
// is, with trailing commas removed (Lenient).
func pushJSON(b []byte) []byte {
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	if i := bytes.IndexByte(b, '='); i > 0 && i < 16 && bytes.EqualFold(bytes.TrimSpace(b[:i]), []byte("json")) {
		b = bytes.TrimSpace(b[i+1:])
	}
	if len(b) > 0 && b[0] == '%' || bytes.HasPrefix(b, []byte("+")) {
		if u, err := url.QueryUnescape(string(b)); err == nil {
			b = []byte(strings.TrimSpace(u))
		}
	}
	return Lenient(b)
}

// firstJPEG: the first image of format 0 (JPEG) with Data anywhere in the push
// (AlarmPicture.ImageList, the images of structured data).
func firstJPEG(v any) []byte {
	switch x := v.(type) {
	case map[string]json.RawMessage:
		var data string
		var format number
		if raw, ok := x["Data"]; ok && json.Unmarshal(raw, &data) == nil && data != "" &&
			(x["Format"] == nil || json.Unmarshal(x["Format"], &format) == nil && format == 0) {
			if p, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data)); err == nil && len(p) > 2 && p[0] == 0xff && p[1] == 0xd8 {
				return p
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // the same push gives the same picture
		for _, k := range keys {
			if p := firstJPEG(x[k]); p != nil {
				return p
			}
		}
	case json.RawMessage:
		t := bytes.TrimSpace(x)
		if len(t) == 0 {
			return nil
		}
		switch t[0] {
		case '{':
			var m map[string]json.RawMessage
			if json.Unmarshal(t, &m) == nil {
				return firstJPEG(m)
			}
		case '[':
			var l []json.RawMessage
			if json.Unmarshal(t, &l) == nil {
				for _, e := range l {
					if p := firstJPEG(e); p != nil {
						return p
					}
				}
			}
		}
	}
	return nil
}

// compactJSON: the push on one line; strings longer than 200 characters (the base64 of
// pictures) are replaced by their length, so that the database keeps no pictures twice.
func compactJSON(top map[string]json.RawMessage) string {
	var v any
	b, _ := json.Marshal(top)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return string(b)
	}
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	if e.Encode(cutStrings(v)) != nil {
		return string(b)
	}
	return strings.TrimSpace(out.String())
}

func cutStrings(v any) any {
	switch x := v.(type) {
	case string:
		if len(x) > 200 {
			return fmt.Sprintf("<%d bytes>", len(x))
		}
	case map[string]any:
		for k, e := range x {
			x[k] = cutStrings(e)
		}
	case []any:
		for i, e := range x {
			x[i] = cutStrings(e)
		}
	}
	return v
}

// NotificationAnswer is what the device is answered for a push (the LAPI envelope).
func NotificationAnswer(path string) []byte {
	b, _ := json.Marshal(map[string]any{"Response": map[string]any{"ResponseURL": path, "ResponseCode": 0, "SubResponseCode": 0,
		"ResponseString": "Succeed", "StatusCode": 0, "StatusString": "Succeed", "Data": "null"}})
	return b
}
