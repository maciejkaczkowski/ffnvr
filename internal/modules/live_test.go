package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/module"
)

func TestLiveConfig(t *testing.T) {
	s := newStore(t)
	for _, l := range []string{
		`/storage add name=disk1 path=/srv/disk1`,
		`/nvr add type=local name=nvr01 storage=disk1`,
		`/nvr add type=dahua name=nvr02 host=192.168.0.175 username=admin password="p@ss:w/rd"`,
		`/nvr add type=dvrip name=nvr03 host=192.168.254.210 username=admin`,
		`/nvr add type=tiandy name=nvr04 host=192.168.0.82 username=Admin password=x`,
		`/camera add name=cam1 nvr=nvr01 stream="rtsp://u:p@192.168.10.21/s0" stream-lq="rtsp://u:p@192.168.10.21/s1"`,
		`/camera add name=cam2 nvr=nvr01 stream="rtsp://192.168.10.22/s0" enabled=no`,
		`/camera add name=ch01 nvr=nvr02 channel=1 stream="rtsp://192.168.0.175:554/cam/realmonitor?channel=1&subtype=0" stream-lq="rtsp://192.168.0.175:554/cam/realmonitor?channel=1&subtype=1"`,
		`/camera add name=ch01 nvr=nvr03 channel=1`,
		`/camera add name=ch01 nvr=nvr04 channel=1`,
	} {
		expect(t, s, l, "")
	}
	ctx := &module.Context{Store: s}
	if us, _ := (liveModule{}).Units(ctx); us != nil {
		t.Errorf("off by default: %v", us)
	}
	expect(t, s, `/system/live set enabled=yes port=19840`, "")
	expect(t, s, `/system/live set go2rtc=go2rtc`, "an absolute path")
	us, err := liveModule{}.Units(ctx)
	if err != nil || len(us) != 2 || us[0].Name != "ffnvr-go2rtc.service" || us[1].Name != "/etc/ffnvr/go2rtc.yaml" {
		t.Fatalf("%v %v", us, err)
	}
	conf := us[1].Content
	for _, want := range []string{"api:\n  listen: '127.0.0.1:19840'\n", "rtsp:\n  listen: ''\n", "webrtc:\n  listen: ''\n", "streams:\n",
		"  'nvr01/cam1/hq': 'rtsp://u:p@192.168.10.21/s0'\n", "  'nvr01/cam1/lq': 'rtsp://u:p@192.168.10.21/s1'\n",
		"  'nvr02/ch01/hq': 'rtsp://admin:p%40ss%3Aw%2Frd@192.168.0.175:554/cam/realmonitor?channel=1&subtype=0'\n",
		"  'nvr03/ch01/hq': 'dvrip://admin:@192.168.254.210:34567?channel=0&subtype=0'\n", "  'nvr03/ch01/lq': 'dvrip://admin:@192.168.254.210:34567?channel=0&subtype=1'\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("config misses %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "{") || strings.Contains(conf, "\\u") {
		t.Errorf("YAML, not JSON:\n%s", conf)
	}
	if got := yamlString("it's"); got != "'it''s'" {
		t.Error(got)
	}
	if !strings.Contains(goConfig(Live{Port: 1984}, nil, false), "streams: {}\n") {
		t.Error("no streams")
	}
	if strings.Contains(conf, "cam2") || strings.Contains(conf, "nvr04") {
		t.Errorf("disabled cameras and recorders without addresses are left out:\n%s", conf)
	}
	unit := us[0].Content
	for _, want := range []string{"ExecStart=/usr/local/bin/go2rtc -config %d/go2rtc.yaml", "LoadCredential=go2rtc.yaml:/etc/ffnvr/go2rtc.yaml",
		"DynamicUser=yes", "User=ffnvr-go2rtc\n", "SyslogIdentifier=ffnvr-go2rtc\n", "Environment=FFNVR_GO2RTC_CONFIG="} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit misses %s:\n%s", want, unit)
		}
	}
	// a change of a camera changes the unit (commit restarts go2rtc)
	expect(t, s, `/camera set [find name=cam1 nvr=nvr01] stream-lq="rtsp://u:p@192.168.10.21/s2"`, "")
	us2, _ := liveModule{}.Units(ctx)
	if us2[0].Content == unit {
		t.Error("the unit must change with the streams")
	}
	if hq, lq := HasLive(s, "nvr01", "cam1"); !hq || !lq {
		t.Error("HasLive")
	}
	if hq, _ := HasLive(s, "nvr04", "ch01"); hq {
		t.Error("tiandy: no address")
	}
	expect(t, s, `/camera add name=c9 nvr=nvr01 stream=rtsp://h/x stream-lq=http://h/y`, "stream-lq \"http://h/y\": expected rtsp://")
}
