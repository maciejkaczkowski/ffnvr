package alarm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/engine"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/tiandy"
	"ffnvr/internal/tiandy/tiandytest"
)

// Two Tiandy cameras of a local recorder subscribed to (events=subscribe of /camera),
// one with a filter of its own that keeps only the alarms, one with the default one;
// a third camera is not subscribed to (the default, as before).
func TestTiandyCameras(t *testing.T) {
	hold := tiandy.AlarmHold
	tiandy.AlarmHold = 300 * time.Millisecond
	defer func() { tiandy.AlarmHold = hold }()
	dev := tiandytest.New()
	defer dev.Close()
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	// the fake listens on a port of its own: given as ptz-host= (the address of the
	// camera's protocol)
	os.WriteFile(cfg, []byte(`/storage
add name=d1 path=`+root+`/rec
/nvr
add type=local name=nvr01 storage=d1
/event/filter
add name=alarms action=accept kinds=*\ALARM\*
/camera/profile
add name=td type=tiandy username=`+tiandytest.User+` password=`+tiandytest.Pass+` stream=rtsp://<host>/1/1 ptz=yes
/camera
add nvr=nvr01 name=brama profile=td host=127.0.0.1 ptz-host=`+dev.Addr()+` events=subscribe event-filter=alarms
add nvr=nvr01 name=plac profile=td host=127.0.0.1 ptz-host=`+dev.Addr()+` events=subscribe
add nvr=nvr01 name=hala profile=td host=127.0.0.1 ptz-host=`+dev.Addr()+`
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
	var logMu sync.Mutex
	var logged []string
	logf := func(f string, a ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(f, a...))
		logMu.Unlock()
		t.Logf(f, a...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- Run(ctx, Options{Load: load, Root: root, Logf: logf, Reload: 200 * time.Millisecond}) }()
	for i := 0; i < 200 && dev.Logged() < 2; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if n := dev.Logged(); n != 2 {
		t.Fatalf("%d cameras logged in, want 2", n)
	}
	time.Sleep(1800 * time.Millisecond) // the burst after the login is over

	const motion = "127.0.0.1\tPROXY\tINNER\tALARM\tMOTIONDETECT\t0\t1"
	for i := 0; i < 4; i++ {
		dev.Push(motion)
		time.Sleep(60 * time.Millisecond)
	}
	dev.Push("127.0.0.1\tPROXY\tINNER\tPARAGET\tDISK\tINFO\t0\t0\t0\t0\t0\t0", "INNER\tCMD\tREBOOT\t1")

	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var evs []eventdb.Event
	for i := 0; i < 100; i++ {
		evs, _ = db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		closed := 0
		for _, e := range evs {
			if !e.Open() {
				closed++
			}
		}
		if closed >= 3 {
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
	for _, cam := range []string{"brama", "plac"} {
		e, ok := got[cam+` INNER\ALARM\MOTIONDETECT`]
		if !ok || e.Nvr != "nvr01" || e.Channel != 1 || e.Source != "tiandy" || e.Open() || e.Count != 1 {
			t.Errorf("%s motion: %+v", cam, e)
		}
		if e.Text != `127.0.0.1\PROXY\INNER\ALARM\MOTIONDETECT\0\1` {
			t.Errorf("%s text: %q", cam, e.Text)
		}
		var raw map[string]any
		if json.Unmarshal([]byte(e.Raw), &raw) != nil || raw["kind"] != `INNER\ALARM\MOTIONDETECT` {
			t.Errorf("%s raw: %q", cam, e.Raw)
		}
	}
	// the default filter keeps the other news; the one of brama only the alarms
	if e, ok := got[`plac INNER\CMD\REBOOT`]; !ok || e.Text != `127.0.0.1\PROXY\INNER\CMD\REBOOT\1` {
		t.Errorf("plac news: %+v", e)
	}
	// the burst after the login and the parameter readouts are not kept (the count of
	// events); what comes after the burst is logged as JSON, once a kind, the burst not
	logMu.Lock()
	all := strings.Join(logged, "\n")
	logMu.Unlock()
	for _, want := range []string{`nvr01/plac: tiandy record INNER\PARAGET\DISK\INFO: {"tree":"127.0.0.1\\PROXY\\INNER\\PARAGET\\DISK\\INFO"`,
		`nvr01/brama: tiandy record INNER\CMD\REBOOT: {`} {
		if !strings.Contains(all, want) {
			t.Errorf("not logged: %s", want)
		}
	}
	if strings.Contains(all, `INNER\VERSION`) || strings.Contains(all, `INNER\CMDID`) {
		t.Errorf("the burst after the login is logged:\n%s", all)
	}

	// events=no: the subscription ends
	os.WriteFile(cfg, []byte(strings.Replace(mustRead(t, cfg), " events=subscribe event-filter=alarms", "", 1)), 0o644)
	for i := 0; i < 100 && dev.Logged() != 1; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if n := dev.Logged(); n != 1 {
		t.Errorf("%d connections after events=no on one camera, want 1", n)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end")
	}
}

// A Tiandy recorder (the same protocol): subscribed to only with events=subscribe of
// /nvr; the channel of a record gives the camera.
func TestTiandyRecorder(t *testing.T) {
	hold := tiandy.AlarmHold
	tiandy.AlarmHold = 300 * time.Millisecond
	defer func() { tiandy.AlarmHold = hold }()
	dev := tiandytest.New()
	defer dev.Close()
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	// first without events=: as before, nobody connects
	write := func(events string) {
		os.WriteFile(cfg, []byte(`/nvr
add type=tiandy name=nvr04 host=`+dev.Addr()+` username=`+tiandytest.User+` password=`+tiandytest.Pass+events+`
/camera
add nvr=nvr04 name=door channel=1
add nvr=nvr04 name=yard channel=3
/event
set gap=0s
`), 0o644)
	}
	write("")
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
	time.Sleep(600 * time.Millisecond)
	if n := dev.Logged(); n != 0 {
		t.Fatalf("subscribed without events=subscribe: %d", n)
	}
	write(" events=subscribe")
	for i := 0; i < 200 && dev.Logged() < 1; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if dev.Logged() != 1 {
		t.Fatal("not subscribed with events=subscribe")
	}
	time.Sleep(1800 * time.Millisecond) // the burst after the login is over
	for i := 0; i < 3; i++ {
		dev.Push("127.0.0.1\tIP\tINNER\tALARM\tMOTIONDETECT\t2\t1", "INNER\tALARM\tMOTIONDETECT\t0\t1")
		time.Sleep(60 * time.Millisecond)
	}
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var evs []eventdb.Event
	for i := 0; i < 100; i++ {
		evs, _ = db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		if len(evs) == 2 && !evs[0].Open() && !evs[1].Open() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := map[string]int{}
	for _, e := range evs {
		if e.Nvr != "nvr04" || e.Kind != `INNER\ALARM\MOTIONDETECT` || e.Open() || e.Source != "tiandy" {
			t.Errorf("event: %+v", e)
		}
		got[e.Camera] = e.Channel
	}
	if len(evs) != 2 || got["door"] != 1 || got["yard"] != 3 {
		t.Errorf("cameras by channel: %v (%d events)", got, len(evs))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end")
	}
}
