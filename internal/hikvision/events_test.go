package hikvision

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"ffnvr/internal/digest"
)

func alertXML(typ, state string, ch, count int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
<ipAddress>192.168.1.64</ipAddress>
<portNo>80</portNo>
<protocol>HTTP</protocol>
<macAddress>44:19:b6:00:00:01</macAddress>
<channelID>%d</channelID>
<dateTime>2026-11-07T12:00:00+01:00</dateTime>
<activePostCount>%d</activePostCount>
<eventType>%s</eventType>
<eventState>%s</eventState>
<eventDescription>%s alarm</eventDescription>
</EventNotificationAlert>
`, ch, count, typ, state, typ)
}

// a part as most firmware send it
func part(body string) string {
	return fmt.Sprintf("--boundary\r\nContent-Type: application/xml; charset=\"UTF-8\"\r\nContent-Length: %d\r\n\r\n%s\r\n", len(body), body)
}

func collect(t *testing.T, stream string, oneByte bool) []Alert {
	t.Helper()
	var out []Alert
	r := strings.NewReader(stream)
	var err error
	if oneByte { // a tag cut by every read
		err = ReadAlerts(iotest.OneByteReader(r), func(a Alert) { out = append(out, a) })
	} else {
		err = ReadAlerts(r, func(a Alert) { out = append(out, a) })
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadAlerts(t *testing.T) {
	smart := `<EventNotificationAlert version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema"><channelID>1</channelID>` +
		`<dateTime>2026-11-07T12:00:05</dateTime><activePostCount>1</activePostCount><eventType>linedetection</eventType>` +
		`<eventState>active</eventState><eventDescription>linedetection alarm</eventDescription><DetectionRegionList>` +
		`<DetectionRegionEntry><regionID>1</regionID></DetectionRegionEntry><DetectionRegionEntry><regionID>3</regionID></DetectionRegionEntry>` +
		`</DetectionRegionList></EventNotificationAlert>`
	stream := part(alertXML("VMD", "active", 1, 1)) +
		part(alertXML("VMD", "active", 1, 2)) +
		// a picture after a smart event, then an alert stuck to the end of the previous part
		part(smart) + "--boundary\r\nContent-Type: image/jpeg\r\nContent-Length: 6\r\n\r\n\xff\xd8\x00\n\xff\xd9\r\n" +
		"--boundary\r\n" + alertXML("videoloss", "inactive", 0, 0) + "HTTP/1.1 200 OK" +
		// no part headers at all, \n only
		strings.ReplaceAll(alertXML("tamperdetection", "inactive", 2, 3), "\r\n", "\n") +
		"<EventNotificationAlert><eventState>active</eventState></EventNotificationAlert>" // no type: no alert
	for _, oneByte := range []bool{false, true} {
		got := collect(t, stream, oneByte)
		if len(got) != 5 {
			t.Fatalf("one byte %v: %d alerts: %+v", oneByte, len(got), got)
		}
		v := got[0]
		if v.Type != "VMD" || !v.Active() || v.Channel != 1 || v.Count != 1 || v.Description != "VMD alarm" ||
			!v.Time.Equal(time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)) || v.Heartbeat() {
			t.Errorf("VMD: %+v", v)
		}
		if strings.Contains(v.Raw, "\n") || !strings.HasPrefix(v.Raw, "<EventNotificationAlert") || !strings.HasSuffix(v.Raw, "</EventNotificationAlert>") {
			t.Errorf("raw: %q", v.Raw)
		}
		if s := got[2]; s.Type != "linedetection" || s.Text() != "linedetection alarm (region 1,3)" || s.Time.IsZero() {
			t.Errorf("smart: %+v %q", s, s.Text())
		}
		if h := got[3]; !h.Heartbeat() || h.Channel != 0 {
			t.Errorf("heartbeat: %+v", h)
		}
		if d := got[4]; d.Type != "tamperdetection" || d.Active() || d.Channel != 2 || d.Heartbeat() {
			t.Errorf("tamper: %+v", d)
		}
	}
}

func TestReadAlertsTooLong(t *testing.T) {
	stream := "<EventNotificationAlert>" + strings.Repeat("x", maxAlert+10) + alertXML("VMD", "active", 1, 1)
	if got := collect(t, stream, false); len(got) != 1 || got[0].Type != "VMD" {
		t.Fatalf("%+v", got)
	}
	// a part cut off in the middle of its alert, then a whole one
	stream = "--boundary\r\n<EventNotificationAlert><eventType>IO</eventType><eventSta\r\n" + part(alertXML("VMD", "active", 1, 1))
	if got := collect(t, stream, false); len(got) != 1 || got[0].Type != "VMD" {
		t.Fatalf("cut off: %+v", got)
	}
}

func TestTracker(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	vmd := Alert{Type: "VMD", State: "active", Channel: 1}
	tr := &Tracker{Hold: 3 * time.Second}
	expectEvs := func(what string, evs []Event, want ...string) {
		t.Helper()
		var got []string
		for _, e := range evs {
			s := "stop"
			if e.Start {
				s = "start"
			}
			got = append(got, fmt.Sprintf("%s %s/%d %s", s, e.Type, e.Channel, e.At.Sub(t0)))
		}
		if strings.Join(got, ";") != strings.Join(want, ";") {
			t.Errorf("%s: %q, want %q", what, got, want)
		}
	}
	expectEvs("first", tr.Alert(vmd, at(0)), "start VMD/1 0s")
	expectEvs("again", tr.Alert(vmd, at(1)))
	expectEvs("heartbeat", tr.Alert(Alert{Type: "videoloss", State: "inactive"}, at(1.5)))
	expectEvs("again", tr.Alert(vmd, at(2)))
	expectEvs("too soon", tr.Expire(at(4.9)))
	// the alerts stopped: the event ends at the last one
	expectEvs("expired", tr.Expire(at(5)), "stop VMD/1 2s")
	expectEvs("begins anew", tr.Alert(vmd, at(6)), "start VMD/1 6s")
	// another channel and another kind are events of their own; inactive ends one now
	expectEvs("ch2", tr.Alert(Alert{Type: "VMD", State: "active", Channel: 2}, at(6.5)), "start VMD/2 6.5s")
	expectEvs("io", tr.Alert(Alert{Type: "IO", State: "active", Channel: 1}, at(7)), "start IO/1 7s")
	expectEvs("io ends", tr.Alert(Alert{Type: "IO", State: "inactive", Channel: 1}, at(7.5)), "stop IO/1 7.5s")
	expectEvs("io ends again", tr.Alert(Alert{Type: "io", State: "inactive", Channel: 1}, at(7.6)))
	if tr.Open() != 2 {
		t.Errorf("%d open", tr.Open())
	}
	expectEvs("close", tr.Close(), "stop VMD/1 6s", "stop VMD/2 6.5s")
	if tr.Open() != 0 {
		t.Errorf("%d open after close", tr.Open())
	}
}

// fakeCamera answers alertStream with Digest authentication (qop=auth) and writes the
// parts it is given, then waits.
type fakeCamera struct {
	user, pass string
	parts      chan string
	mu         sync.Mutex
	conns      int
	refuse     int // the status of the answer (0: 200)
	body       string
}

func (f *fakeCamera) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ISAPI/Event/notification/alertStream" {
		http.NotFound(w, r)
		return
	}
	auth := r.Header.Get("Authorization")
	const realm, nonce = "IP Camera(C1234)", "4e6a4d324e7a5130"
	p := digest.ParseParams(strings.TrimPrefix(auth, "Digest "))
	ha1 := digest.MD5Hex(f.user + ":" + realm + ":" + f.pass)
	ha2 := digest.MD5Hex(r.Method + ":" + p["uri"])
	want := digest.MD5Hex(ha1 + ":" + nonce + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	if !strings.HasPrefix(auth, "Digest ") || p["username"] != f.user || p["response"] != want {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest qop="auth", realm="%s", nonce="%s", stale="FALSE"`, realm, nonce))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.refuse != 0 {
		w.WriteHeader(f.refuse)
		fmt.Fprint(w, f.body)
		return
	}
	f.mu.Lock()
	f.conns++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "multipart/mixed; boundary=boundary")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case p, ok := <-f.parts:
			if !ok {
				return // the camera closes the stream
			}
			fmt.Fprint(w, p)
			w.(http.Flusher).Flush()
		}
	}
}

type recorder struct {
	mu  sync.Mutex
	evs []Event
}

func (r *recorder) add(e Event) { r.mu.Lock(); r.evs = append(r.evs, e); r.mu.Unlock() }
func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evs {
		s := "stop"
		if e.Start {
			s = "start"
		}
		out = append(out, s+" "+e.Type)
	}
	return out
}

func (r *recorder) wait(t *testing.T, n int) []string {
	t.Helper()
	for i := 0; i < 200; i++ {
		if l := r.list(); len(l) >= n {
			return l
		}
		time.Sleep(10 * time.Millisecond)
	}
	return r.list()
}

func TestEvents(t *testing.T) {
	f := &fakeCamera{user: "admin", pass: "hik12345", parts: make(chan string, 10)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c, err := NewClient(strings.TrimPrefix(srv.URL, "http://"), "admin", "hik12345")
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	connected := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Events(context.Background(), 300*time.Millisecond, func() { connected <- struct{}{} }, rec.add)
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("not connected")
	}
	f.parts <- part(alertXML("VMD", "active", 1, 1))
	f.parts <- part(alertXML("videoloss", "inactive", 1, 0))
	f.parts <- part(alertXML("VMD", "active", 1, 2))
	// the motion ends by itself: no more alerts for the hold
	if got := rec.wait(t, 2); strings.Join(got, ",") != "start VMD,stop VMD" {
		t.Fatalf("%q", got)
	}
	// an event still going on ends when the camera closes the stream
	f.parts <- part(alertXML("fielddetection", "active", 1, 1))
	rec.wait(t, 3)
	close(f.parts)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "EOF") {
			t.Errorf("end of the stream: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Events did not end")
	}
	if got := rec.list(); strings.Join(got, ",") != "start VMD,stop VMD,start fielddetection,stop fielddetection" {
		t.Errorf("%q", got)
	}
}

func TestEventsRefused(t *testing.T) {
	f := &fakeCamera{user: "admin", pass: "hik12345"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	c, _ := NewClient(addr, "admin", "wrong")
	err := c.Events(context.Background(), 0, nil, func(Event) {})
	if !errors.Is(err, ErrStream) || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: %v", err)
	}
	f.refuse = http.StatusForbidden
	f.body = `<?xml version="1.0" encoding="UTF-8"?>
<ResponseStatus version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
<requestURL>/ISAPI/Event/notification/alertStream</requestURL>
<statusCode>4</statusCode>
<statusString>Invalid Operation</statusString>
<subStatusCode>notSupport</subStatusCode>
</ResponseStatus>`
	c, _ = NewClient(addr, "admin", "hik12345")
	err = c.Events(context.Background(), 0, nil, func(Event) {})
	if !errors.Is(err, ErrStream) || !strings.Contains(err.Error(), "HTTP 403 Invalid Operation notSupport") {
		t.Errorf("refused: %v", err)
	}
}

// After a heartbeat, silence is a broken connection; without one it is an idle stream.
func TestEventsHeartbeat(t *testing.T) {
	f := &fakeCamera{user: "a", pass: "b", parts: make(chan string, 10)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c, _ := NewClient(strings.TrimPrefix(srv.URL, "http://"), "a", "b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.events(ctx, 0, 400*time.Millisecond, nil, func(Event) {}) }()
	select {
	case err := <-done:
		t.Fatalf("ended without a heartbeat: %v", err)
	case <-time.After(800 * time.Millisecond):
	}
	f.parts <- part(alertXML("videoloss", "inactive", 1, 0))
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no heartbeat") {
			t.Errorf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silence after a heartbeat did not end the subscription")
	}
	// the caller's end is no failure of the camera
	go func() { done <- c.events(ctx, 0, time.Minute, nil, func(Event) {}) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Events did not end with its context")
	}
}

func TestNewClient(t *testing.T) {
	for addr, want := range map[string]string{
		"192.168.1.64":          "http://192.168.1.64/ISAPI/x",
		"192.168.1.64:8080":     "http://192.168.1.64:8080/ISAPI/x",
		"192.168.1.64:443":      "https://192.168.1.64:443/ISAPI/x",
		"https://cam.lan":       "https://cam.lan/ISAPI/x",
		"[fe80::1]":             "http://[fe80::1]/ISAPI/x",
		"http://u:p@cam.lan/zz": "http://cam.lan/ISAPI/x",
	} {
		c, err := NewClient(addr, "u", "p")
		if err != nil || c.url("/ISAPI/x") != want {
			t.Errorf("%s: %v %v, want %s", addr, c, err, want)
		}
	}
	if _, err := NewClient("rtsp://cam.lan", "u", "p"); err == nil {
		t.Error("rtsp:// accepted")
	}
}
