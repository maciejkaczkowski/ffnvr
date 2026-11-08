package alarm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/digest"
	"ffnvr/internal/engine"
	"ffnvr/internal/eventdb"
)

func hikAlert(typ, state string, count int) string {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<EventNotificationAlert version="2.0" xmlns="http://www.hikvision.com/ver20/XMLSchema">
<ipAddress>192.168.1.64</ipAddress>
<channelID>1</channelID>
<dateTime>2026-11-07T12:00:00+01:00</dateTime>
<activePostCount>%d</activePostCount>
<eventType>%s</eventType>
<eventState>%s</eventState>
<eventDescription>%s alarm</eventDescription>
</EventNotificationAlert>`, count, typ, state, typ)
	return fmt.Sprintf("--boundary\r\nContent-Type: application/xml; charset=\"UTF-8\"\r\nContent-Length: %d\r\n\r\n%s\r\n", len(body), body)
}

// Two Hikvision cameras of a local recorder subscribed to (events=subscribe of /camera,
// ISAPI alertStream with Digest): one with a filter of its own that keeps only the
// motion, one with the default one. The motion is sent as VMD active once a second and
// ends when the alerts stop; the heartbeat (videoloss inactive) is no event; a smart
// event comes with a picture part.
func TestHikvisionCameras(t *testing.T) {
	var conns, open atomic.Int32
	release := make(chan struct{})
	const realm, nonce = "IP Camera(C1234)", "4e6a4d324e7a5130"
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ISAPI/Event/notification/alertStream" {
			http.NotFound(w, r)
			return
		}
		p := digest.ParseParams(strings.TrimPrefix(r.Header.Get("Authorization"), "Digest "))
		ha1 := digest.MD5Hex("admin:" + realm + ":hik12345")
		want := digest.MD5Hex(ha1 + ":" + nonce + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + digest.MD5Hex(r.Method+":"+p["uri"]))
		if p["response"] != want {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest qop="auth", realm="%s", nonce="%s"`, realm, nonce))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		conns.Add(1)
		open.Add(1)
		defer open.Add(-1)
		w.Header().Set("Content-Type", "multipart/mixed; boundary=boundary")
		w.WriteHeader(http.StatusOK)
		send := func(s string) {
			fmt.Fprint(w, s)
			w.(http.Flusher).Flush()
		}
		send(hikAlert("videoloss", "inactive", 0))
		send(hikAlert("VMD", "active", 1))
		send(hikAlert("VMD", "active", 2))
		send(hikAlert("videoloss", "inactive", 0))
		send(hikAlert("VMD", "active", 3))
		send(hikAlert("linedetection", "active", 1) + "--boundary\r\nContent-Type: image/jpeg\r\nContent-Length: 4\r\n\r\n\xff\xd8\xff\xd9\r\n")
		send(hikAlert("tamperdetection", "active", 1))
		// the tampering goes on: its alerts keep coming until the camera is let go of
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for n := 2; ; n++ {
			select {
			case <-r.Context().Done():
				return
			case <-release:
				return
			case <-tick.C:
				send(hikAlert("tamperdetection", "active", n))
			}
		}
	}))
	defer cam.Close()
	defer close(release)
	addr := strings.TrimPrefix(cam.URL, "http://")
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	os.WriteFile(cfg, []byte(`/storage
add name=d1 path=`+root+`/rec
/nvr
add type=local name=nvr01 storage=d1
/event/filter
add name=motion action=accept kinds=VMD
/camera/profile
add name=hk type=hikvision username=admin password=hik12345 stream=rtsp://<host>/Streaming/Channels/101
/camera
add nvr=nvr01 name=brama profile=hk host=127.0.0.1 ptz-host=`+addr+` events=subscribe event-filter=motion
add nvr=nvr01 name=plac profile=hk host=127.0.0.1 ptz-host=`+addr+` events=subscribe
add nvr=nvr01 name=hala profile=hk host=127.0.0.1
/event
set gap=0s
`), 0o644)
	load := func() (*config.Store, error) {
		e, err := engine.New(engine.Options{Root: root})
		if err != nil {
			return nil, err
		}
		if err := e.LoadFiles([]string{cfg}); err != nil {
			return nil, err
		}
		return e.Store, nil
	}
	if _, err := load(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- Run(ctx, Options{Load: load, Root: root, Logf: t.Logf, Reload: 200 * time.Millisecond})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not end")
		}
	}()
	for i := 0; i < 200 && conns.Load() < 2; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if n := conns.Load(); n != 2 {
		t.Fatalf("%d cameras subscribed to, want 2", n)
	}
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// brama: VMD; plac: VMD, linedetection, tamperdetection; the motion ends 5 s after
	// its last alert
	got := map[string]eventdb.Event{}
	for i := 0; i < 200; i++ {
		evs, _ := db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		got = map[string]eventdb.Event{}
		for _, e := range evs {
			got[e.Camera+" "+e.Kind] = e
		}
		if len(evs) >= 4 && !got["brama VMD"].Open() && !got["plac VMD"].Open() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(got) != 4 {
		t.Fatalf("%d events, want 4: %+v", len(got), got)
	}
	for _, c := range []string{"brama", "plac"} {
		e, ok := got[c+" VMD"]
		if !ok || e.Nvr != "nvr01" || e.Channel != 1 || e.Source != "hikvision" || e.Open() || e.Count != 1 ||
			e.Text != "VMD alarm" || !strings.HasPrefix(e.Raw, "<EventNotificationAlert") {
			t.Errorf("%s motion: %+v", c, e)
		}
	}
	if _, ok := got["plac videoloss"]; ok {
		t.Error("the heartbeat was kept")
	}
	if e := got["plac linedetection"]; e.Open() || e.Text != "linedetection alarm" {
		t.Errorf("plac line: %+v", e)
	}
	tamper, ok := got["plac tamperdetection"]
	if !ok || !tamper.Open() {
		t.Fatalf("plac tamper: %+v", tamper)
	}
	// events=no on plac: its subscription ends and its open event with it
	os.WriteFile(cfg, []byte(strings.Replace(mustRead(t, cfg), "name=plac profile=hk host=127.0.0.1 ptz-host="+addr+" events=subscribe\n", "name=plac profile=hk host=127.0.0.1\n", 1)), 0o644)
	closed := false
	for i := 0; i < 100 && !closed; i++ {
		time.Sleep(50 * time.Millisecond)
		e, _ := db.Get(context.Background(), tamper.ID)
		closed = !e.Open() && open.Load() == 1
		if closed && !e.Ended.After(e.Started) {
			t.Errorf("tamper ended at its start, not at its last alert: %+v", e)
		}
	}
	if !closed {
		t.Errorf("after events=no: the event stayed open or the connection too (%d open)", open.Load())
	}
	if n := conns.Load(); n != 2 {
		t.Errorf("%d subscriptions in all, want 2 (one ended, none anew)", n)
	}
}
