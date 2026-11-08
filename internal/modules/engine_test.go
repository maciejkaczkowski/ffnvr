package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/config"
	"ffnvr/internal/module"
	"ffnvr/internal/units"
)

func unitsOf(t *testing.T, s *config.Store) map[string]string {
	t.Helper()
	ctx := &module.Context{Store: s, Root: t.TempDir()}
	out := map[string]string{}
	for _, m := range []interface {
		Units(*module.Context) ([]units.Unit, error)
	}{cameraModule{}, liveModule{}} {
		us, err := m.Units(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range us {
			out[u.Name] = u.Content
		}
	}
	return out
}

func TestCameraEngineGo2rtc(t *testing.T) {
	s := newStore(t)
	// the configuration as written by the user
	for i, e := range run(t, s,
		`/storage add name=recordings path=/srv/rec`,
		`/nvr add name=test type=local storage=recordings retention=30d`,
		`/camera add nvr=test name=cam01 stream="dvrip://admin:@192.168.0.49?channel=0&subtype=0" enabled=yes stream-lq="dvrip://admin:@192.168.0.49?channel=0&subtype=1" engine="go2rtc"`,
		`/camera add nvr=test name=zwykla stream=rtsp://10.0.0.7/s stream-lq=rtsp://10.0.0.7/l`,
	) {
		if e != "" {
			t.Fatalf("line %d: %s", i, e)
		}
	}
	for _, c := range []struct{ line, want string }{
		{`/camera add nvr=test name=x stream="dvrip://admin:@10.0.0.1?channel=0&subtype=0"`, `expected rtsp://host[:port]/path (dvrip:// needs engine=go2rtc)`},
		{`/camera add nvr=test name=x stream="exec:ffmpeg -i x" engine=go2rtc`, "spaces, quotes"},
		{`/camera add nvr=test name=x stream=exec:/bin/sh engine=go2rtc`, "expected a network source of go2rtc"},
		{`/camera add nvr=test name=x stream=ffmpeg:rtsp://10.0.0.1/s engine=go2rtc`, "expected a network source of go2rtc"},
		{`/camera add nvr=test name=x stream=rtsp://10.0.0.1/s engine=turbo`, "engine"},
		{`/camera set [ find name=zwykla ] stream-lq=dvrip://10.0.0.7?channel=0`, "needs engine=go2rtc"},
		{`/nvr add type=dahua name=nvr02 host=10.0.0.3 username=a password=b`, ""},
		{`/camera add nvr=nvr02 name=ch01 channel=1 engine=go2rtc`, "engine is only for cameras of a type=local nvr"},
		{`/camera add nvr=nvr02 name=ch01 channel=1 engine=internal`, "engine is only for cameras of a type=local nvr"},
	} {
		expect(t, s, c.line, c.want)
	}

	us := unitsOf(t, s)
	rec := us[UnitName("test", "cam01")]
	for _, want := range []string{
		"-i rtsp://127.0.0.1:8554/test/cam01/hq -an -vcodec copy", // ffmpeg records go2rtc's copy
		"After=network-online.target\nWants=network-online.target\nAfter=ffnvr-go2rtc.service\nWants=ffnvr-go2rtc.service\n\n[Service]",
		"RestartSec=2\n",
	} {
		if !strings.Contains(rec, want) {
			t.Errorf("recording unit misses %q:\n%s", want, rec)
		}
	}
	if strings.Contains(rec, "dvrip") || strings.Contains(rec, "192.168.0.49") {
		t.Errorf("the camera's own address in the recording unit:\n%s", rec)
	}
	if plain := us[UnitName("test", "zwykla")]; !strings.Contains(plain, "-i rtsp://10.0.0.7/s ") || strings.Contains(plain, "go2rtc") || !strings.Contains(plain, "RestartSec=10\n") {
		t.Errorf("an engine=internal camera changed:\n%s", plain)
	}

	// go2rtc runs for the recording although the live view is off: only that camera,
	// RTSP on localhost
	conf, ok := us[GoConfig]
	if !ok || us[Go2rtcUnit] == "" {
		t.Fatalf("no go2rtc: %v", keys(us))
	}
	for _, want := range []string{"rtsp:\n  listen: '127.0.0.1:8554'\n",
		"  'test/cam01/hq': 'dvrip://admin:@192.168.0.49?channel=0&subtype=0'\n",
		"  'test/cam01/lq': 'dvrip://admin:@192.168.0.49?channel=0&subtype=1'\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("go2rtc config misses %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "zwykla") || strings.Contains(conf, "nvr02") {
		t.Errorf("live view off: only the go2rtc cameras:\n%s", conf)
	}

	// the live view on: every camera; another RTSP port
	run(t, s, `/system/live set enabled=yes rtsp-port=18554`)
	us = unitsOf(t, s)
	if c := us[GoConfig]; !strings.Contains(c, "'test/zwykla/hq'") || !strings.Contains(c, "listen: '127.0.0.1:18554'") {
		t.Errorf("live on:\n%s", c)
	}
	if !strings.Contains(us[UnitName("test", "cam01")], "-i rtsp://127.0.0.1:18554/test/cam01/hq ") {
		t.Errorf("rtsp-port not used:\n%s", us[UnitName("test", "cam01")])
	}
	expect(t, s, `/system/live set port=18554`, "port and rtsp-port must differ (both 18554)")

	// no go2rtc camera left: no RTSP server; live off: no go2rtc at all
	run(t, s, `/camera set [ find name=cam01 ] enabled=no`)
	if c := unitsOf(t, s)[GoConfig]; !strings.Contains(c, "rtsp:\n  listen: ''\n") {
		t.Errorf("RTSP still on:\n%s", c)
	}
	run(t, s, `/system/live set enabled=no`)
	if us := unitsOf(t, s); us[Go2rtcUnit] != "" {
		t.Errorf("go2rtc without a reason: %v", keys(us))
	}
	run(t, s, `/camera set [ find name=cam01 ] enabled=yes`, `/nvr set [ find name=test ] disabled=yes`)
	if us := unitsOf(t, s); us[Go2rtcUnit] != "" {
		t.Errorf("go2rtc for a disabled recorder: %v", keys(us))
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
