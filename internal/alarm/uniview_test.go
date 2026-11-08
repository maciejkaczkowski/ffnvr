package alarm

import (
	"context"
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
	"ffnvr/internal/uniview/univiewtest"
)

// runAlarm starts the alarm server on the configuration cfg (in root) until the test ends.
func runAlarm(t *testing.T, root, cfg string, o Options) {
	t.Helper()
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
	o.Load, o.Root, o.Reload = load, root, 200*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- Run(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not end")
		}
	})
}

// Three Uniview cameras of a local recorder subscribed to on one device (LAPI: the
// camera pushes to alarm-lapi): brama keeps only the motion (its filter), plac has no
// filter - its alarms, no pushes of data - and wszystko keeps everything (kinds=*).
// MotionAlarmOn and MotionAlarmOff are one event MotionAlarm.
func TestUniviewCameras(t *testing.T) {
	dev := univiewtest.New()
	defer dev.Close()
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	os.WriteFile(cfg, []byte(`/storage
add name=d1 path=`+root+`/rec
/nvr
add type=local name=nvr01 storage=d1
/event/filter
add name=motion action=accept kinds=MotionAlarm
add name=all action=accept kinds=*
/camera/profile
add name=uv type=uniview username=`+univiewtest.User+` password=`+univiewtest.Pass+` stream=rtsp://<host>/unicast/c1/s0/live
/camera
add nvr=nvr01 name=brama profile=uv host=127.0.0.1 ptz-host=`+dev.Addr()+` events=subscribe event-filter=motion
add nvr=nvr01 name=plac profile=uv host=127.0.0.1 ptz-host=`+dev.Addr()+` events=subscribe
add nvr=nvr01 name=wszystko profile=uv host=127.0.0.1 ptz-host=`+dev.Addr()+` events=subscribe event-filter=all
/event
set gap=0s
`), 0o644)
	runAlarm(t, root, cfg, Options{Logf: t.Logf, LAPI: "127.0.0.1:0", Heartbeat: 100 * time.Millisecond})
	for i := 0; i < 800 && dev.Subscribed() < 3; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if n := dev.Subscribed(); n != 3 {
		t.Fatalf("%d cameras subscribed to, want 3", n)
	}
	for _, typ := range []string{"MotionAlarmOn", "LineDetectorCrossed", "MotionAlarmOff"} {
		if n := dev.PushAlarm(typ); n != 3 {
			t.Errorf("%s: %d took it", typ, n)
		}
	}
	dev.Push("PeopleCount/LineRuleData", map[string]any{"TimeStamp": time.Now().Unix(), "ChannelID": 0, "LineNum": 0})

	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := []string{"brama MotionAlarm", "plac LineDetectorCrossed", "plac MotionAlarm",
		`wszystko LineDetectorCrossed`, "wszystko MotionAlarm", `wszystko PeopleCount\LineRuleData`}
	var got map[string]eventdb.Event
	for i := 0; i < 200; i++ {
		evs, _ := db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		got = map[string]eventdb.Event{}
		for _, e := range evs {
			got[e.Camera+" "+e.Kind] = e
		}
		if len(got) >= len(want) && !got["brama MotionAlarm"].Open() {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(got) != len(want) {
		t.Fatalf("events %v, want %v", keys(got), want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("no %s: %v", k, keys(got))
		}
	}
	for _, c := range []string{"brama", "plac", "wszystko"} {
		e := got[c+" MotionAlarm"]
		if e.Open() || e.Nvr != "nvr01" || e.Channel != 1 || e.Source != "uniview" || e.Count != 1 ||
			e.Text != "MotionAlarmOn" || !strings.Contains(e.Raw, `"AlarmType":"MotionAlarmOn"`) {
			t.Errorf("%s motion: %+v", c, e)
		}
	}

	// events=no on plac: its subscription is ended on the device
	os.WriteFile(cfg, []byte(strings.Replace(mustRead(t, cfg), "name=plac profile=uv host=127.0.0.1 ptz-host="+dev.Addr()+" events=subscribe\n",
		"name=plac profile=uv host=127.0.0.1\n", 1)), 0o644)
	for i := 0; i < 200 && dev.Subscribed() != 2; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if n := dev.Subscribed(); n != 2 {
		t.Errorf("%d subscriptions after events=no, want 2", n)
	}
	// the device restarts and forgets the subscriptions: they are made anew; the pushes
	// of the old ones (if the device still sent them) are not kept twice
	dev.Mu.Lock()
	old := dev.NextSub
	dev.Forget, dev.Subs = true, nil
	dev.Mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	dev.Mu.Lock()
	dev.Forget = false
	dev.Mu.Unlock()
	for i := 0; i < 400 && dev.Subscribed() != 2; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	dev.Mu.Lock()
	renewed := dev.NextSub > old
	dev.Mu.Unlock()
	if dev.Subscribed() != 2 || !renewed {
		t.Fatalf("not subscribed again after the restart: %d", dev.Subscribed())
	}
	dev.PushAlarm("MaskImageAlarmOn")
	for i := 0; i < 200; i++ {
		evs, _ := db.Query(context.Background(), eventdb.Filter{Origin: "recorders", Kind: "MaskImageAlarm"})
		if len(evs) == 1 {
			if evs[0].Camera != "wszystko" || !evs[0].Open() {
				t.Errorf("masking: %+v", evs[0])
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Error("no masking after the restart")
}

// The IPC of the tcpdump: its subscription is 65280 and its pushes say Subscribers/0,
// come without Host, as text/plain Json={...}; MotionAlarmOn twice, then Off - one
// event MotionAlarm.
func TestUniviewQuirkyCamera(t *testing.T) {
	dev := univiewtest.New()
	defer dev.Close()
	dev.Quirky = true
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	os.WriteFile(cfg, []byte(`/storage
add name=d1 path=`+root+`/rec
/nvr
add type=local name=nvr01 storage=d1
/camera
add nvr=nvr01 name=ipc type=uniview username=`+univiewtest.User+` password=`+univiewtest.Pass+` host=127.0.0.1 ptz-host=`+dev.Addr()+` stream=rtsp://127.0.0.1/unicast/c1/s0/live events=subscribe
/event
set gap=0s
`), 0o644)
	runAlarm(t, root, cfg, Options{Logf: t.Logf, LAPI: "127.0.0.1:0", Heartbeat: 100 * time.Millisecond})
	for i := 0; i < 800 && dev.Subscribed() < 1; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // renewals of 65280 go through
	dev.Mu.Lock()
	subs, renewals := len(dev.Subs), dev.Renewals
	_, ok := dev.Subs[65280]
	dev.Mu.Unlock()
	if subs != 1 || !ok || renewals == 0 {
		t.Fatalf("subscriptions %d (65280: %v), renewals %d", subs, ok, renewals)
	}
	for _, typ := range []string{"MotionAlarmOn", "MotionAlarmOn", "MotionAlarmOff"} {
		if n := dev.PushAlarm(typ); n != 1 {
			t.Fatalf("%s: not answered 200", typ)
		}
	}
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 400; i++ {
		evs, _ := db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		if len(evs) == 1 && !evs[0].Open() {
			if e := evs[0]; e.Camera != "ipc" || e.Kind != "MotionAlarm" || e.Channel != 1 {
				t.Errorf("%+v", e)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	evs, _ := db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
	t.Errorf("events: %+v", evs)
}

// A Uniview recorder subscribed to (events=subscribe of /nvr): its pushes name the
// channel, which gives the camera (channel= of /camera); without a filter its alarms
// only. A Tiandy recorder next to it is not subscribed to: events= unset is no.
func TestUniviewRecorder(t *testing.T) {
	dev := univiewtest.New()
	defer dev.Close()
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	os.WriteFile(cfg, []byte(`/nvr
add type=uniview name=nvr03 host=`+dev.Addr()+` username=`+univiewtest.User+` password=`+univiewtest.Pass+` events=subscribe
add type=tiandy name=nvr04 host=127.0.0.1:1 username=Admin password=pw
/camera
add nvr=nvr03 name=brama channel=1
add nvr=nvr03 name=rampa channel=3
/event
set gap=0s
`), 0o644)
	runAlarm(t, root, cfg, Options{Logf: t.Logf, LAPI: "127.0.0.1:0", Heartbeat: 100 * time.Millisecond})
	for i := 0; i < 800 && dev.Subscribed() < 1; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	if n := dev.Subscribed(); n != 1 {
		t.Fatalf("%d subscriptions, want 1", n)
	}
	dev.PushChannel("MotionAlarmOn", 1)
	dev.PushChannel("MotionAlarmOn", 3)
	dev.PushChannel("MotionAlarmOff", 1)
	dev.PushChannel("VideoLossAlarmOn", 2)
	dev.Push("PeopleCount/LineRuleData", map[string]any{"TimeStamp": time.Now().Unix(), "ChannelID": 0, "LineNum": 0})
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got map[string]eventdb.Event
	for i := 0; i < 400; i++ {
		evs, _ := db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		got = map[string]eventdb.Event{}
		for _, e := range evs {
			got[fmt.Sprintf("%s ch%d %s %s", e.Nvr, e.Channel, e.Camera, e.Kind)] = e
		}
		if len(got) >= 3 && !got["nvr03 ch1 brama MotionAlarm"].Open() {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	want := []string{"nvr03 ch1 brama MotionAlarm", "nvr03 ch3 rampa MotionAlarm", "nvr03 ch2  VideoLossAlarm"}
	if len(got) != len(want) {
		t.Fatalf("events %q, want %q", keys(got), want)
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("no %q: %q", k, keys(got))
		}
	}
	if e := got["nvr03 ch1 brama MotionAlarm"]; e.Open() || e.Source != "uniview" {
		t.Errorf("brama: %+v", e)
	}
	if e := got["nvr03 ch3 rampa MotionAlarm"]; !e.Open() {
		t.Errorf("rampa: %+v", e)
	}
}

// Without alarm-lapi the camera has nowhere to push to: the subscription is not made
// and the reason is told.
func TestUniviewWithoutLAPI(t *testing.T) {
	dev := univiewtest.New()
	defer dev.Close()
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	os.WriteFile(cfg, []byte(`/storage
add name=d1 path=`+root+`/rec
/nvr
add type=local name=nvr01 storage=d1
/camera
add nvr=nvr01 name=brama type=uniview username=`+univiewtest.User+` password=`+univiewtest.Pass+` host=127.0.0.1 ptz-host=`+dev.Addr()+` stream=rtsp://<host>/unicast/c1/s0/live events=subscribe
`), 0o644)
	var mu sync.Mutex
	var log []string
	runAlarm(t, root, cfg, Options{Logf: func(f string, a ...any) {
		mu.Lock()
		log = append(log, fmt.Sprintf(f, a...))
		mu.Unlock()
	}})
	for i := 0; i < 800; i++ {
		mu.Lock()
		l := strings.Join(log, "\n")
		mu.Unlock()
		if strings.Contains(l, "nvr01/brama: events: alarm-lapi of /ip/service is not enabled") {
			if dev.Subscribed() != 0 {
				t.Error("subscribed without alarm-lapi")
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Errorf("no reason told:\n%s", strings.Join(log, "\n"))
}

func keys(m map[string]eventdb.Event) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
