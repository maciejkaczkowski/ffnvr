package dahua

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func collect(t *testing.T, stream string) []Event {
	t.Helper()
	var evs []Event
	if err := ReadEvents(strings.NewReader(stream), func(e Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestReadEventsWithHeaders(t *testing.T) {
	s := "--myboundary\r\nContent-Type: text/plain\r\nContent-Length: 36\r\n\r\nCode=VideoMotion;action=Start;index=0\r\n\r\n" +
		"--myboundary\r\nContent-Type: text/plain\r\nContent-Length: 9\r\n\r\nHeartbeat\r\n\r\n" +
		"--myboundary\r\nContent-Type: text/plain\r\nContent-Length: 35\r\n\r\nCode=VideoMotion;action=Stop;index=0\r\n\r\n"
	evs := collect(t, s)
	if len(evs) != 2 || evs[0].Action != "Start" || evs[1].Action != "Stop" || evs[0].Channel() != 1 {
		t.Fatalf("%+v", evs)
	}
}

// issue #46 of toxuin/alarmserver: parts without headers
func TestReadEventsWithoutHeaders(t *testing.T) {
	s := "--myboundary\r\nCode=VideoMotion;action=Start;index=0\r\n--myboundary\r\nCode=VideoMotion;action=Stop;index=0\r\n"
	if evs := collect(t, s); len(evs) != 2 || evs[1].Action != "Stop" {
		t.Fatalf("%+v", evs)
	}
}

func TestReadEventsWithData(t *testing.T) {
	s := "--myboundary\r\nContent-Type: text/plain\r\nContent-Length: 200\r\n\r\n" +
		"Code=CrossLineDetection;action=Pulse;index=3;data={\n   \"Name\" : \"Line } 1\",\n   \"Object\" : { \"ObjectType\" : \"Human\" },\n   \"Ids\" : [ 1, 2 ]\n}\r\n\r\n" +
		"--myboundary\r\nCode=StorageFailure;action=Start;index=0\r\n"
	evs := collect(t, s)
	if len(evs) != 2 {
		t.Fatalf("%d events: %+v", len(evs), evs)
	}
	if evs[0].Code != "CrossLineDetection" || evs[0].Channel() != 4 || !strings.Contains(evs[0].Data, `"Human"`) || !strings.HasSuffix(evs[0].Data, "}") {
		t.Errorf("%+v", evs[0])
	}
	if evs[1].Code != "StorageFailure" {
		t.Errorf("%+v", evs[1])
	}
}

func TestParseEventOddInput(t *testing.T) {
	for _, s := range []string{"Code", "Code=;action", "foo=bar", ";;;"} {
		ParseEvent(s) // must not panic (alarmserver indexed parts[1] without checking)
	}
	if e, ok := ParseEvent("Code=AlarmLocal;action=Start"); !ok || e.Index != -1 || e.Channel() != 0 {
		t.Errorf("%+v %v", e, ok)
	}
}

func TestEventsHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cgi-bin/eventManager.cgi" || r.URL.RawQuery != "action=attach&codes=[VideoMotion,VideoLoss]&heartbeat=1" {
			http.Error(w, "bad request "+r.URL.RawQuery, 400)
			return
		}
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=myboundary")
		fl := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "--myboundary\r\nContent-Type: text/plain\r\n\r\nCode=VideoMotion;action=Start;index=%d\r\n\r\n", i)
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "admin", "x", false)
	got := make(chan Event, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		errc <- c.Events(ctx, []string{"VideoMotion", "VideoLoss"}, time.Second, nil, func(e Event) { got <- e })
	}()
	for i := 0; i < 3; i++ {
		select {
		case e := <-got:
			if e.Index != i {
				t.Errorf("event %d: %+v", i, e)
			}
		case err := <-errc:
			t.Fatalf("stream ended: %v", err)
		}
	}
	// no more heartbeats: the watchdog ends it after 3 x 1 s + 5 s
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "heartbeat") {
			t.Errorf("want a heartbeat error, got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the watchdog did not end the silent stream")
	}
}
