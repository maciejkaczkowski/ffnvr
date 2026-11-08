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
	"ffnvr/internal/engine"
	"ffnvr/internal/eventdb"
)

// Two Dahua cameras of a local recorder subscribed to (events=subscribe of /camera), the
// same eventManager as the Dahua recorders: one with a filter of its own that keeps only
// the motion, one with the default one; a third camera is not subscribed to. The events
// are of the camera (its name), not of a channel of the recorder.
func TestDahuaCameras(t *testing.T) {
	var conns, open atomic.Int32
	release := make(chan struct{})
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cgi-bin/eventManager.cgi" || r.URL.Query().Get("action") != "attach" {
			http.NotFound(w, r)
			return
		}
		conns.Add(1)
		open.Add(1)
		defer open.Add(-1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=myboundary")
		for _, ev := range []string{"Code=VideoMotion;action=Start;index=0", "Code=VideoMotion;action=Stop;index=0",
			"Code=VideoBlind;action=Start;index=0"} {
			fmt.Fprintf(w, "--myboundary\r\nContent-Type: text/plain\r\n\r\n%s\r\n\r\n", ev)
		}
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer cam.Close()
	defer close(release)
	addr := strings.TrimPrefix(cam.URL, "http://")
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	// the fake listens on a port of its own: given as ptz-host= (the address of the
	// camera's protocol; without it port 80 of host=)
	os.WriteFile(cfg, []byte(`/storage
add name=d1 path=`+root+`/rec
/nvr
add type=local name=nvr01 storage=d1
/event/filter
add name=motion action=accept kinds=VideoMotion
/camera/profile
add name=dh type=dahua username=admin password=pw stream=rtsp://<host>/cam/realmonitor ptz=yes
/camera
add nvr=nvr01 name=brama profile=dh host=127.0.0.1 ptz-host=`+addr+` events=subscribe event-filter=motion
add nvr=nvr01 name=plac profile=dh host=127.0.0.1 ptz-host=`+addr+` events=subscribe
add nvr=nvr01 name=hala profile=dh host=127.0.0.1 ptz-host=`+addr+`
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
	// the database is opened once the alarm server made it (both would migrate it)
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
	var evs []eventdb.Event
	for i := 0; i < 100; i++ {
		evs, _ = db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		if len(evs) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := map[string]eventdb.Event{}
	for _, e := range evs {
		got[e.Camera+" "+e.Kind] = e
	}
	if len(evs) != 3 {
		t.Fatalf("%d events, want 3: %+v", len(evs), evs)
	}
	for _, c := range []string{"brama", "plac"} {
		if e, ok := got[c+" VideoMotion"]; !ok || e.Nvr != "nvr01" || e.Channel != 1 || e.Source != "dahua" || e.Open() {
			t.Errorf("%s motion: %+v", c, e)
		}
	}
	// the filter of brama keeps only the motion; the default one keeps the blinding
	blind, ok := got["plac VideoBlind"]
	if !ok || !blind.Open() {
		t.Errorf("plac blind: %+v", blind)
	}
	// events=no on plac: its subscription ends and its open event with it
	os.WriteFile(cfg, []byte(strings.Replace(mustRead(t, cfg), "ptz-host="+addr+" events=subscribe\n", "ptz-host="+addr+"\n", 1)), 0o644)
	closed := false
	for i := 0; i < 100 && !closed; i++ {
		time.Sleep(50 * time.Millisecond)
		e, _ := db.Get(context.Background(), blind.ID)
		closed = !e.Open() && open.Load() == 1
	}
	if !closed {
		t.Errorf("after events=no: the event stayed open or the connection too (%d open)", open.Load())
	}
	if n := conns.Load(); n != 2 {
		t.Errorf("%d subscriptions in all, want 2 (one ended, none anew)", n)
	}
}
