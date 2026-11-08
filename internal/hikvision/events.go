package hikvision

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Events of the camera: GET /ISAPI/Event/notification/alertStream keeps an HTTP answer
// open (multipart/mixed) and writes every alert into it as one part:
//
//	--boundary
//	Content-Type: application/xml; charset="UTF-8"
//	Content-Length: 478
//
//	<EventNotificationAlert version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
//	<ipAddress>192.168.1.64</ipAddress>
//	<channelID>1</channelID>
//	<dateTime>2026-11-07T12:00:00+01:00</dateTime>
//	<activePostCount>1</activePostCount>
//	<eventType>VMD</eventType>
//	<eventState>active</eventState>
//	<eventDescription>Motion alarm</eventDescription>
//	</EventNotificationAlert>
//
// Firmware differ in the part headers, in the line ends, in Content-Length that does not
// fit and in parts with pictures (image/jpeg) after the alerts of the smart events. So
// the stream is not read as MIME: everything from <EventNotificationAlert to its end
// tag is an alert, everything else is skipped.
//
// What the alerts mean:
//
//	active    sent again and again (about once a second) while the event lasts;
//	          most firmware send no inactive at the end - the alerts stop coming
//	inactive  the end, where the firmware sends it
//	videoloss inactive with nothing lost: the heartbeat of an idle stream (every few
//	          seconds to a minute, by the firmware)
//
// Tracker folds that into the beginnings and ends of events.

// Alert is one EventNotificationAlert as the camera sent it.
type Alert struct {
	Type        string    // eventType: VMD, videoloss, tamperdetection, linedetection, fielddetection, IO, ...
	State       string    // eventState: active, inactive
	Channel     int       // channelID (else dynChannelID), from 1; 0 when none
	Time        time.Time // dateTime, the camera's clock (zero when unreadable)
	Description string    // eventDescription, e.g. "Motion alarm"
	Count       int       // activePostCount: the posts of the event so far
	Regions     []string  // the regionIDs of DetectionRegionList (smart events)
	Raw         string    // the XML on one line
}

// Active: the event goes on (an alert without eventState counts as active).
func (a Alert) Active() bool { return !strings.EqualFold(strings.TrimSpace(a.State), "inactive") }

// Heartbeat: the alert of an idle stream (videoloss inactive).
func (a Alert) Heartbeat() bool { return strings.EqualFold(a.Type, "videoloss") && !a.Active() }

// Text: the description and the regions, for the list of events.
func (a Alert) Text() string {
	t := a.Description
	if len(a.Regions) > 0 {
		r := "region " + strings.Join(a.Regions, ",")
		if t == "" {
			return r
		}
		t += " (" + r + ")"
	}
	return t
}

type xmlAlert struct {
	Channel    string   `xml:"channelID"`
	DynChannel string   `xml:"dynChannelID"`
	DateTime   string   `xml:"dateTime"`
	Count      string   `xml:"activePostCount"`
	Type       string   `xml:"eventType"`
	State      string   `xml:"eventState"`
	Desc       string   `xml:"eventDescription"`
	Regions    []string `xml:"DetectionRegionList>DetectionRegionEntry>regionID"`
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05"}

// ParseAlert reads one EventNotificationAlert; false when it is none (no eventType).
func ParseAlert(b []byte) (Alert, bool) {
	var x xmlAlert
	if err := xml.Unmarshal(b, &x); err != nil {
		return Alert{}, false
	}
	a := Alert{Type: strings.TrimSpace(x.Type), State: strings.ToLower(strings.TrimSpace(x.State)),
		Description: strings.TrimSpace(x.Desc), Raw: oneLine(b)}
	if a.Type == "" {
		return Alert{}, false
	}
	for _, ch := range []string{x.Channel, x.DynChannel} {
		if n, err := strconv.Atoi(strings.TrimSpace(ch)); err == nil && n > 0 {
			a.Channel = n
			break
		}
	}
	a.Count, _ = strconv.Atoi(strings.TrimSpace(x.Count))
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, strings.TrimSpace(x.DateTime)); err == nil {
			a.Time = t
			break
		}
	}
	for _, r := range x.Regions {
		if r = strings.TrimSpace(r); r != "" {
			a.Regions = append(a.Regions, r)
		}
	}
	return a, true
}

// oneLine: the XML without its line breaks and indentation.
func oneLine(b []byte) string {
	var out strings.Builder
	for _, l := range strings.Split(string(b), "\n") {
		out.WriteString(strings.TrimSpace(l))
	}
	return strings.ToValidUTF8(out.String(), "")
}

var (
	alertOpen  = []byte("<EventNotificationAlert")
	alertClose = []byte("</EventNotificationAlert>")
)

// maxAlert: an alert longer than this is no alert (a stream gone wrong).
const maxAlert = 256 << 10

// ReadAlerts parses an alert stream (see above) until it ends.
func ReadAlerts(r io.Reader, fn func(Alert)) error {
	buf := make([]byte, 32<<10)
	var pending []byte
	in := false
	for {
		n, err := r.Read(buf)
		pending = append(pending, buf[:n]...)
		for {
			if !in {
				i := bytes.Index(pending, alertOpen)
				if i < 0 {
					// keep what may be the start of the tag, cut by the read
					if k := len(pending) - len(alertOpen) + 1; k > 0 {
						pending = append(pending[:0], pending[k:]...)
					}
					break
				}
				pending = append(pending[:0], pending[i:]...)
				in = true
			}
			j := bytes.Index(pending, alertClose)
			if j < 0 {
				if len(pending) > maxAlert {
					// no alert: start again at the next beginning, if one came
					if k := bytes.Index(pending[1:], alertOpen); k >= 0 {
						pending = append(pending[:0], pending[k+1:]...)
						continue
					}
					pending, in = pending[:0], false
				}
				break
			}
			end := j + len(alertClose)
			// an alert cut off (a broken part) is followed by the next one: the alert
			// begins at the last opening before its end
			start := bytes.LastIndex(pending[:j], alertOpen)
			if a, ok := ParseAlert(pending[start:end]); ok {
				fn(a)
			}
			pending = append(pending[:0], pending[end:]...)
			in = false
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// ---------------------------------------------------------------- events

// Event: an event of a kind on a channel begins or ends.
type Event struct {
	Alert           // the first alert of the event (a beginning), the last one (an end)
	Start bool      // begins; false: ends
	At    time.Time // this server's clock: when it began; for an end its last alert, or the inactive
}

// DefaultHold: an event whose alerts stop coming for this long has ended.
const DefaultHold = 5 * time.Second

// Tracker folds alerts into events (not safe for concurrent use).
type Tracker struct {
	Hold time.Duration // 0: DefaultHold
	open map[string]*openEvent
}

type openEvent struct {
	last   Alert
	lastAt time.Time
}

func trackKey(a Alert) string { return strings.ToLower(a.Type) + "\x00" + strconv.Itoa(a.Channel) }

func (t *Tracker) hold() time.Duration {
	if t.Hold <= 0 {
		return DefaultHold
	}
	return t.Hold
}

// Alert takes an alert that came at now: the beginning of an event, its end (inactive),
// or nothing (a repeated active, the heartbeat, an inactive of nothing begun).
func (t *Tracker) Alert(a Alert, now time.Time) []Event {
	if t.open == nil {
		t.open = map[string]*openEvent{}
	}
	k := trackKey(a)
	o := t.open[k]
	switch {
	case a.Active() && o == nil:
		t.open[k] = &openEvent{last: a, lastAt: now}
		return []Event{{Alert: a, Start: true, At: now}}
	case a.Active():
		o.last, o.lastAt = a, now
	case o != nil:
		delete(t.open, k)
		return []Event{{Alert: a, At: now}}
	}
	return nil
}

// Expire ends the events whose alerts stopped coming Hold ago; they end at their last
// alert.
func (t *Tracker) Expire(now time.Time) []Event {
	var out []Event
	for k, o := range t.open {
		if now.Sub(o.lastAt) >= t.hold() {
			delete(t.open, k)
			out = append(out, Event{Alert: o.last, At: o.lastAt})
		}
	}
	sortEvents(out)
	return out
}

// Close ends every open event at its last alert (the connection is gone).
func (t *Tracker) Close() []Event {
	var out []Event
	for k, o := range t.open {
		delete(t.open, k)
		out = append(out, Event{Alert: o.last, At: o.lastAt})
	}
	sortEvents(out)
	return out
}

// Open: the number of events going on.
func (t *Tracker) Open() int { return len(t.open) }

func sortEvents(evs []Event) {
	sort.Slice(evs, func(i, j int) bool {
		if !evs[i].At.Equal(evs[j].At) {
			return evs[i].At.Before(evs[j].At)
		}
		return trackKey(evs[i].Alert) < trackKey(evs[j].Alert)
	})
}

// ---------------------------------------------------------------- the subscription

// ErrStream: the answer is no event stream (wrong address, no permission, no ISAPI).
var ErrStream = errors.New("no event stream")

// HeartbeatIdle: once the camera sent a heartbeat, silence this long counts as a broken
// connection. (Before one, a silent stream is an idle one: TCP keepalive watches it.)
const HeartbeatIdle = 2 * time.Minute

// Events subscribes to the alert stream and calls fn for the beginning and end of every
// event (Tracker; hold 0: DefaultHold) until ctx ends or the connection breaks (the
// error says why; reconnecting is the caller's business). The events still open then
// end at their last alert, before Events returns. connected (may be nil) is called once
// the camera accepted the subscription. fn is never called concurrently.
func (c *Client) Events(ctx context.Context, hold time.Duration, connected func(), fn func(Event)) error {
	return c.events(ctx, hold, HeartbeatIdle, connected, fn)
}

func (c *Client) events(parent context.Context, hold, idle time.Duration, connected func(), fn func(Event)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/ISAPI/Event/notification/alertStream"), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w: HTTP 401: wrong username or password", ErrStream)
		}
		return fmt.Errorf("%w: HTTP %d %s", ErrStream, resp.StatusCode, responseStatus(body))
	}
	if connected != nil {
		connected()
	}

	tr := &Tracker{Hold: hold}
	var mu sync.Mutex
	emit := func(evs []Event) {
		for _, e := range evs {
			fn(e)
		}
	}
	var lastRead atomic.Int64 // unix ns
	var beating atomic.Bool   // a heartbeat came: silence means a broken connection
	var silent atomic.Bool
	lastRead.Store(time.Now().UnixNano())
	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				mu.Lock()
				emit(tr.Expire(now))
				mu.Unlock()
				if beating.Load() && now.Sub(time.Unix(0, lastRead.Load())) > idle {
					silent.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	err = ReadAlerts(&readWatch{r: resp.Body, last: &lastRead}, func(a Alert) {
		if a.Heartbeat() {
			beating.Store(true)
		}
		mu.Lock()
		emit(tr.Alert(a, time.Now()))
		mu.Unlock()
	})
	cancel()
	<-tickDone
	mu.Lock()
	emit(tr.Close())
	mu.Unlock()
	switch {
	case silent.Load():
		return fmt.Errorf("no heartbeat for %s", idle)
	case parent.Err() != nil:
		return parent.Err()
	case err == nil:
		return io.ErrUnexpectedEOF // the camera closed the stream
	}
	return err
}

// responseStatus: what an ISAPI refusal says (<ResponseStatus>), else the start of the
// answer.
func responseStatus(body []byte) string {
	var rs struct {
		StatusString  string `xml:"statusString"`
		SubStatusCode string `xml:"subStatusCode"`
	}
	if xml.Unmarshal(body, &rs) == nil && (rs.StatusString != "" || rs.SubStatusCode != "") {
		return strings.TrimSpace(strings.TrimSpace(rs.StatusString) + " " + strings.TrimSpace(rs.SubStatusCode))
	}
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = strings.ToValidUTF8(s[:120], "")
	}
	return strings.TrimSpace(s)
}

type readWatch struct {
	r    io.Reader
	last *atomic.Int64
}

func (r *readWatch) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.last.Store(time.Now().UnixNano())
	}
	return n, err
}
