package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/modules"
)

func TestAutoAddCameras(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "config.rsc")
	os.WriteFile(file, []byte(strings.Join([]string{
		`/storage add name=d path=/srv/d`,
		`/system/live set enabled=yes`,
		`/nvr add type=dahua name=dh host=10.0.0.29 username=admin password=x`,
		`/nvr add type=local name=loc storage=d`,
		`/camera add nvr=dh name=ch01 channel=1`,
		`/camera add nvr=loc name=cam stream=rtsp://10.0.0.5/s`, // a change nobody applied yet
	}, "\n")+"\n"), 0o644)
	st := modules.NvrStatus{Checked: time.Now(), Channels: 2,
		ChannelList: []modules.Channel{{Number: 1, State: "Connected"}, {Number: 2, State: "Connected"}}}
	b, _ := json.Marshal(st)
	os.MkdirAll(filepath.Join(root, modules.StatusDir), 0o755)
	os.WriteFile(filepath.Join(root, modules.StatusDir, "dh.json"), b, 0o644)

	opts := Options{Root: root}
	e, _ := New(opts)
	if err := e.LoadFiles([]string{file}); err != nil {
		t.Fatal(err)
	}
	var log []string
	logf := func(f string, a ...any) { log = append(log, fmt.Sprintf(f, a...)) }
	AutoAddCameras(e.Store, file, opts, logf)
	all := strings.Join(log, "\n")

	cfg, _ := os.ReadFile(file)
	if !strings.Contains(string(cfg), "name=ch02 nvr=dh channel=2") && !strings.Contains(string(cfg), "nvr=dh name=ch02") {
		t.Errorf("not in the configuration:\n%s\nlog:\n%s", cfg, all)
	}
	g2, err := os.ReadFile(filepath.Join(root, modules.GoConfig))
	if err != nil || !strings.Contains(string(g2), "'dh/ch02/hq'") {
		t.Errorf("go2rtc config: %v\n%s\nlog:\n%s", err, g2, all)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system", modules.Go2rtcUnit)); err != nil {
		t.Errorf("go2rtc unit: %v", err)
	}
	// only go2rtc: the local camera's service and the rest wait for "ffnvr apply"
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system", modules.UnitName("loc", "cam"))); err == nil {
		t.Errorf("an unrelated change was applied")
	}
	for _, want := range []string{"auto-add: dh: ch02: added", "not applied (waiting for ffnvr apply): ", modules.UnitName("loc", "cam"), "written    "} {
		if !strings.Contains(all, want) {
			t.Errorf("log misses %q:\n%s", want, all)
		}
	}
	// the next round: nothing new, nothing written
	mt, _ := os.Stat(file)
	e2, _ := New(opts)
	e2.LoadFiles([]string{file})
	log = nil
	AutoAddCameras(e2.Store, file, opts, logf)
	if mt2, _ := os.Stat(file); !mt2.ModTime().Equal(mt.ModTime()) || len(log) != 0 {
		t.Errorf("a round without news wrote the configuration or logged: %v", log)
	}
	// several configuration files: the worker cannot write, it says what to do
	os.WriteFile(filepath.Join(root, modules.StatusDir, "dh.json"), []byte(strings.Replace(string(b), `"channel_list":[`, `"channel_list":[{"Number":3,"Title":"","State":"Connected"},`, 1)), 0o644)
	log = nil
	AutoAddCameras(e2.Store, "", opts, logf)
	if len(log) != 1 || !strings.Contains(log[0], "run: /nvr scan dh") {
		t.Errorf("no file: %v", log)
	}
}
