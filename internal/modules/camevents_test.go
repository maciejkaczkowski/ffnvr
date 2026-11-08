package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/eventdb"
	"ffnvr/internal/module"
)

func TestCameraEventsConfig(t *testing.T) {
	c := withNvr(t)
	s := c.Store
	expect(t, s, `/event/filter add name=alarms action=accept kinds=*\ALARM\*`, "")
	expect(t, s, `/camera/profile add name=td type=tiandy username=Admin password=pw stream=rtsp://<host>/1/1`, "")
	// by default nothing is subscribed to, as before
	expect(t, s, `/camera add name=a nvr=nvr01 profile=td host=192.168.0.53`, "")
	if got := SubscribedCameras(s); len(got) != 0 {
		t.Fatalf("subscribed by default: %+v", got)
	}
	// the make from the profile; the login too; host= without its RTSP port
	expect(t, s, `/camera add name=b nvr=nvr01 profile=td host=192.168.0.54:8554 events=subscribe event-filter=alarms`, "")
	expect(t, s, `/camera add name=c nvr=nvr01 stream=rtsp://u:p@192.168.0.55/1/1 events=subscribe`, "events=subscribe needs type= (dahua, hikvision, tiandy, uniview)")
	expect(t, s, `/camera add name=c nvr=nvr01 stream=rtsp://u:p@192.168.0.55/1/1 type=dvrip events=subscribe`, "cameras of type=dvrip cannot be subscribed to")
	expect(t, s, `/camera add name=c nvr=nvr01 stream=rtsp://u:p@192.168.0.55/1/1 type=tiandy events=subscribe`, "")
	expect(t, s, `/camera add name=d nvr=nvr01 stream=rtsp://192.168.0.56/1/1 type=tiandy events=subscribe enabled=no`, "")
	// a Dahua camera: the same eventManager as its recorders
	expect(t, s, `/camera add name=dh nvr=nvr01 stream=rtsp://adm:pw@192.168.0.57:554/cam/realmonitor type=dahua events=subscribe`, "")
	// a Hikvision camera: ISAPI on the port of ptz-host=; no PTZ control yet
	expect(t, s, `/camera add name=hk nvr=nvr01 stream=rtsp://admin:hik@192.168.0.58:554/Streaming/Channels/101 type=hikvision ptz=yes`, "cameras of type=hikvision have no PTZ control yet")
	expect(t, s, `/camera add name=hk nvr=nvr01 stream=rtsp://admin:hik@192.168.0.58:554/Streaming/Channels/101 type=hikvision events=subscribe`, "")
	// a Uniview camera: LAPI, it pushes to alarm-lapi; its motion is MotionAlarm
	expect(t, s, `/camera add name=uv nvr=nvr01 stream=rtsp://admin:uv@192.168.0.59:554/unicast/c1/s0/live type=uniview events=subscribe`, "")
	if kinds, det := MotionKinds(s, findCamera(s, "nvr01", "uv")); det || len(kinds) != 1 || kinds[0] != "MotionAlarm" {
		t.Errorf("uv motion: %v %v", kinds, det)
	}
	if ok, why := AlarmCapable(s, findCamera(s, "nvr01", "uv")); !ok {
		t.Errorf("uv alarms: %s", why)
	}
	if uv := SubscribedCameras(s)[4]; uv.Camera != "uv" || uv.Type != "uniview" || uv.Host != "192.168.0.59" || uv.Username != "admin" || uv.Password != "uv" {
		t.Errorf("uv: %+v", SubscribedCameras(s)[4])
	}
	expect(t, s, `/camera set uv events=no`, "")
	if _, det := MotionKinds(s, findCamera(s, "nvr01", "uv")); !det {
		t.Error("uv without events=subscribe: not the detector")
	}
	expect(t, s, `/camera remove uv`, "")
	got := SubscribedCameras(s)
	if len(got) != 4 {
		t.Fatalf("subscribed: %+v", got)
	}
	if dh := got[2]; dh.Camera != "dh" || dh.Type != "dahua" || dh.Host != "192.168.0.57" || dh.Username != "adm" || dh.Password != "pw" {
		t.Errorf("dh (the host of the stream address without its RTSP port): %+v", dh)
	}
	if hk := got[3]; hk.Camera != "hk" || hk.Type != "hikvision" || hk.Host != "192.168.0.58" || hk.Username != "admin" || hk.Password != "hik" {
		t.Errorf("hk: %+v", hk)
	}
	// its motion is its alarms VMD, once subscribed to; without it the detector
	if kinds, det := MotionKinds(s, findCamera(s, "nvr01", "hk")); det || len(kinds) != 1 || kinds[0] != "VMD" {
		t.Errorf("hk motion: %v %v", kinds, det)
	}
	if ok, why := AlarmCapable(s, findCamera(s, "nvr01", "hk")); !ok {
		t.Errorf("hk alarms: %s", why)
	}
	expect(t, s, `/camera set hk events=no`, "")
	if _, det := MotionKinds(s, findCamera(s, "nvr01", "hk")); !det {
		t.Error("hk without events=subscribe: not the detector")
	}
	if ok, _ := AlarmCapable(s, findCamera(s, "nvr01", "hk")); ok {
		t.Error("hk without events=subscribe: alarm capable")
	}
	expect(t, s, `/camera set hk events=subscribe`, "")
	// ptz-host= is the address of the camera's protocol: for the events too
	expect(t, s, `/camera set hk ptz-host=192.168.0.58:8080`, "")
	if hk := SubscribedCameras(s)[3]; hk.Host != "192.168.0.58:8080" {
		t.Errorf("hk with ptz-host=: %+v", hk)
	}
	expect(t, s, `/camera set hk events=no`, "ptz-host is used with ptz=yes or events=subscribe")
	expect(t, s, `/camera set hk ptz-channel=2`, "ptz-channel is used with ptz=yes")
	if b := got[0]; b.Camera != "b" || b.Host != "192.168.0.54" || b.Username != "Admin" || b.Password != "pw" || b.Filter != "alarms" || b.Type != "tiandy" {
		t.Errorf("b: %+v", b)
	}
	if c := got[1]; c.Camera != "c" || c.Host != "192.168.0.55" || c.Username != "u" || c.Password != "p" || c.Filter != "" {
		t.Errorf("c (login of the stream address): %+v", c)
	}
	if !strings.Contains(s.Export("/camera"), `event-filter=alarms`) {
		t.Errorf("export: %s", s.Export("/camera"))
	}
	if u := alarmUnit(&module.Context{Store: s}); u == nil {
		t.Error("a camera subscribed to starts the alarm server")
	}
	// /nvr stays as it was: a local recorder is not subscribed to
	expect(t, s, `/nvr set nvr01 events=subscribe`, "events is not used by type=local")

	// a Tiandy recorder: not subscribed to unless told (events= unset is no)
	expect(t, s, `/nvr add type=tiandy name=nvr04 host=10.0.0.4 username=Admin password=pw`, "")
	if u := alarmUnit(&module.Context{Store: s}); u == nil || strings.Contains(s.Export("/nvr"), "events=") {
		t.Errorf("tiandy recorder by default: %s", s.Export("/nvr"))
	}
	expect(t, s, `/nvr set nvr04 events=subscribe`, "")
	// a Uniview recorder: the same, not subscribed to unless told; with events=subscribe
	// it pushes to alarm-lapi
	expect(t, s, `/nvr add type=uniview name=nvr05 host=10.0.0.5 username=a password=b`, "")
	if ex := s.Export("/nvr"); !strings.Contains(ex, "name=nvr05 host=10.0.0.5 username=a password=b\n") {
		t.Errorf("uniview recorder by default: %s", ex)
	}
	expect(t, s, `/nvr set nvr05 events=subscribe`, "")
	expect(t, s, `/nvr add type=dahua name=nvr06 host=10.0.0.6 username=a password=b events=subscribe`, "")
	expect(t, s, `/nvr remove nvr06`, "")

	// a camera of a remote recorder cannot be
	expect(t, s, `/nvr add type=dahua name=nvr02 host=10.0.0.2 username=a password=b`, "")
	expect(t, s, `/camera add name=e nvr=nvr02 channel=1 events=subscribe`, "events is only for cameras of a type=local nvr")
}

func TestKindPatterns(t *testing.T) {
	for _, c := range []struct {
		pat, kind string
		ok        bool
	}{
		{`*\ALARM\*`, `INNER\ALARM\MOTIONDETECT`, true},
		{`*/alarm/*`, `INNER\ALARM\MOTIONDETECT`, true},
		{`INNER\ALARM\*`, `INNER\ALARM\VIDEOLOST`, true},
		{`*\ALARM\*`, `INNER\PARAGET\DISK\INFO`, false},
		{`*MOTION*`, `INNER\ALARM\MOTIONDETECT`, true},
		{`Video*`, `VideoMotion`, true},
		{`Video*`, `NewFile`, false},
		{`*`, ``, true},
		{`a*b*c`, `axxbyyc`, true},
		{`a*b*c`, `axxbyy`, false},
	} {
		if got := KindMatch(c.pat, c.kind); got != c.ok {
			t.Errorf("%q ~ %q: %v", c.pat, c.kind, got)
		}
	}
	acc := &EventFilter{Accept: true, Kinds: filterKinds(`*\ALARM\*,Mail`)}
	if !acc.Keeps(`INNER\ALARM\MOTIONDETECT`) || !acc.Keeps("Mail") || acc.Keeps(`INNER\CMD\X`) {
		t.Error("accept with a pattern")
	}
	drop := &EventFilter{Kinds: filterKinds(`*\PARAGET\*`)}
	if drop.Keeps(`INNER\PARAGET\DISK\INFO`) || !drop.Keeps(`INNER\ALARM\MOTIONDETECT`) {
		t.Error("drop with a pattern")
	}
}

func TestPrintRaw(t *testing.T) {
	evs := []eventdb.Event{{ID: 2, Kind: `INNER\ALARM\MOTIONDETECT`, Raw: `{"kind":"INNER\\ALARM\\MOTIONDETECT"}`}, {ID: 1, Kind: "Mail"}}
	out := withRaw(FormatEvents(evs), evs)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[1], "2 ") || lines[2] != `       raw: {"kind":"INNER\\ALARM\\MOTIONDETECT"}` || !strings.HasPrefix(lines[3], "1 ") {
		t.Errorf("print raw=yes:\n%s", out)
	}
}
