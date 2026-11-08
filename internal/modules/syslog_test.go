package modules

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/eventdb"
)

func TestStatusChanges(t *testing.T) {
	now := time.Now()
	ok := &NvrStatus{Checked: now, LastOK: now, Disks: []Disk{{Name: "sda", State: "Success"}},
		ChannelList: []Channel{{Number: 1, Title: "Brama", State: "Connected"}, {Number: 2, State: "Connected"}, {Number: 3, State: "Empty"}}}
	cams := map[int]string{1: "brama"}
	down := &NvrStatus{Checked: now, LastOK: now, Error: "dial tcp: timeout", ChannelList: ok.ChannelList}
	kinds := func(es []eventdb.Entry) string {
		var k []string
		for _, e := range es {
			k = append(k, e.Kind+"/"+e.Camera)
		}
		return strings.Join(k, " ")
	}
	if got := kinds(statusChanges("n", nil, ok, cams)); got != "" {
		t.Errorf("first check: %s", got)
	}
	if got := kinds(statusChanges("n", ok, down, cams)); got != "NvrOffline/" {
		t.Errorf("down: %s", got)
	}
	if got := kinds(statusChanges("n", down, down, cams)); got != "" {
		t.Errorf("still down: %s", got)
	}
	if got := kinds(statusChanges("n", down, ok, cams)); got != "NvrOnline/" {
		t.Errorf("back: %s", got)
	}
	lost := *ok
	lost.ChannelList = []Channel{{Number: 1, Title: "Brama", State: "Disconnected"}, {Number: 2, State: "Connected"}, {Number: 3, State: "Empty"}}
	lost.Disks = []Disk{{Name: "sda", State: "Error", Error: true}}
	if got := kinds(statusChanges("n", ok, &lost, cams)); got != "CameraOffline/brama DiskError/" {
		t.Errorf("camera and disk: %s", got)
	}
	if got := kinds(statusChanges("n", &lost, ok, cams)); got != "CameraOnline/brama" {
		t.Errorf("camera back: %s", got)
	}
}

func TestRetentionInSystemLog(t *testing.T) {
	root := t.TempDir()
	disk := t.TempDir()
	s := newStore(t)
	run(t, s, `/storage add name=d1 path=`+disk, `/nvr add type=local name=loc storage=d1 retention=1d`, `/camera add name=k nvr=loc stream=rtsp://10.0.0.9/s`)
	dir := filepath.Join(disk, "loc", "k")
	os.MkdirAll(dir, 0o755)
	for _, age := range []time.Duration{72 * time.Hour, 50 * time.Hour, time.Hour} {
		os.WriteFile(filepath.Join(dir, time.Now().Add(-age).Format("2006-01-02T15-04-05")+".mkv"), []byte("x"), 0o644)
	}
	MaintainLocalLog(s, root, time.Now(), t.Logf)
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	evs, _ := db.Query(context.Background(), eventdb.Filter{Origin: "system"})
	if len(evs) != 1 || evs[0].Kind != SysRecordingsDeleted || evs[0].Nvr != "loc" || !strings.Contains(evs[0].Text, "2 segments") {
		t.Fatalf("%+v", evs)
	}
	MaintainLocalLog(s, root, time.Now(), t.Logf) // nothing more to delete: no entry
	if n, _ := db.Count(context.Background(), eventdb.Filter{}); n != 1 {
		t.Errorf("entries: %d", n)
	}
}
