package alarm

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/engine"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
)

// An XMeye camera of a local recorder pushes its alarms to alarm-dvrip (15002): it is
// known by its address (host= of /camera, else the host of its stream address) and
// its events are of its recorder and named by it; its event-filter= applies.
func TestPushFromLocalCamera(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config.rsc")
	os.WriteFile(cfg, []byte(`/storage
add name=d1 path=`+root+`/rec
/nvr
add type=local name=nvr01 storage=d1
/event/filter
add name=motion action=accept kinds=MotionDetect
/camera
add nvr=nvr01 name=other type=dvrip stream=rtsp://10.9.9.9/x
add nvr=nvr01 name=xm type=dvrip stream=rtsp://admin:@127.0.0.1:554/user=admin_password=_channel=1_stream=0.sdp event-filter=motion
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
	defer cancel()
	done := make(chan error)
	go func() {
		done <- Run(ctx, Options{Load: load, Root: root, DVRIP: "127.0.0.1:0", Logf: t.Logf, Reload: time.Hour})
	}()
	var st *modules.AlarmState
	for i := 0; i < 200 && (st == nil || st.Listeners["alarm-dvrip"] == ""); i++ {
		time.Sleep(25 * time.Millisecond)
		st = modules.ReadAlarmState(root)
	}
	c, err := net.Dial("tcp", st.Listeners["alarm-dvrip"])
	if err != nil {
		t.Fatal(err)
	}
	// a camera tells no address of its own here (or a wrong one): the connection's counts
	c.Write(pushFrame(`{"Address":"0x0","Channel":0,"Event":"MotionDetect","SerialID":"abc","Status":"Start","Type":"Alarm"}`))
	c.Write(pushFrame(`{"Address":"0x0","Channel":0,"Event":"VideoLoss","SerialID":"abc","Status":"Start","Type":"Alarm"}`))
	c.Close()
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var evs []eventdb.Event
	for i := 0; i < 60 && len(evs) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		evs, _ = db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
	}
	time.Sleep(300 * time.Millisecond)
	evs, _ = db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
	if len(evs) != 1 {
		t.Fatalf("%d events (VideoLoss is dropped by the filter of the camera): %+v", len(evs), evs)
	}
	if e := evs[0]; e.Nvr != "nvr01" || e.Camera != "xm" || e.Kind != "MotionDetect" || e.Source != "dvrip" || e.Device != "" {
		t.Errorf("event: %+v", e)
	}
	cancel()
	<-done
}
