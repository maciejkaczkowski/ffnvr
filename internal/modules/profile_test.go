package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/config"
	"ffnvr/internal/script"
)

func profileStore(t *testing.T) *config.Store {
	t.Helper()
	s := newStore(t)
	for i, e := range run(t, s,
		`/storage add name=recordings path=/srv/rec`,
		`/nvr add name=test type=local storage=recordings`,
		// the user's example
		`/camera/profile add name="tiandy-secret" username="admin" password="secret" stream="rtsp://<host>/<ch>/1" stream-lq="rtsp://<host>/<ch>/2"`,
		`/camera/profile add name=xm-dvrip username=admin engine=go2rtc stream="dvrip://<host>?channel=<ch0>&subtype=0"`,
		`/camera/profile add name=xm-rtsp username=admin password="p@ss&x=1" stream="rtsp://<host>:554/user=<user>&password=<password>&channel=<ch>&stream=0.sdp"`,
		`/camera add nvr=test name=wejscie profile=tiandy-secret host=192.168.1.2 channel=1`,
		`/camera add nvr=test name=brama profile=tiandy-secret host=192.168.1.3 channel=1 password="secret"`,
		`/camera add nvr=test name=bez profile=tiandy-secret host=192.168.1.4 channel=3 password=""`,
		`/camera add nvr=test name=xm profile=xm-dvrip host=192.168.0.49 channel=1`,
		`/camera add nvr=test name=xr profile=xm-rtsp host=192.168.0.50 channel=2`,
		`/camera add nvr=test name=stara stream=rtsp://admin:x@10.0.0.9/live`,
	) {
		if e != "" {
			t.Fatalf("line %d: %s", i, e)
		}
	}
	return s
}

func camItem(t *testing.T, s *config.Store, name string) *config.Item {
	t.Helper()
	for _, c := range s.Items("/camera") {
		if c.Val("name") == name {
			return c
		}
	}
	t.Fatalf("no camera %s", name)
	return nil
}

func TestCameraProfileAddresses(t *testing.T) {
	s := profileStore(t)
	for _, c := range []struct{ cam, hq, lq, engine string }{
		{"wejscie", "rtsp://admin:secret@192.168.1.2/1/1", "rtsp://admin:secret@192.168.1.2/1/2", "internal"},
		{"brama", "rtsp://admin:secret@192.168.1.3/1/1", "rtsp://admin:secret@192.168.1.3/1/2", "internal"}, // the camera's own password
		{"bez", "rtsp://admin:@192.168.1.4/3/1", "rtsp://admin:@192.168.1.4/3/2", "internal"},                         // password="" overrides with none
		{"xm", "dvrip://admin:@192.168.0.49?channel=0&subtype=0", "", "go2rtc"},                                      // <ch0>, the engine from the profile
		{"xr", "rtsp://192.168.0.50:554/user=admin&password=p%40ss%26x%3D1&channel=2&stream=0.sdp", "", "internal"},  // the login in the path, encoded, not as user@
		{"stara", "rtsp://admin:x@10.0.0.9/live", "", "internal"},                                                     // no profile: as it was
	} {
		src := ResolveCamera(s, camItem(t, s, c.cam))
		hq, lq, err := src.Addresses()
		if err != nil || hq != c.hq || lq != c.lq || src.Engine != c.engine {
			t.Errorf("%s: %q %q %s %v\n want %q %q %s", c.cam, hq, lq, src.Engine, err, c.hq, c.lq, c.engine)
		}
	}
	// the recording and the live view use them
	us := unitsOf(t, s)
	for cam, want := range map[string]string{
		"brama": "-i rtsp://admin:secret@192.168.1.3/1/1 ", // % doubled for systemd
		"xm":    "-i rtsp://127.0.0.1:8554/test/xm/hq ",
		"stara": "-i rtsp://admin:x@10.0.0.9/live ",
	} {
		if u := us[UnitName("test", cam)]; !strings.Contains(u, want) {
			t.Errorf("%s unit misses %q:\n%s", cam, want, u)
		}
	}
	st := LiveStreams(s)
	for name, want := range map[string]string{
		"test/brama/lq": "rtsp://admin:secret@192.168.1.3/1/2",
		"test/xm/hq":    "dvrip://admin:@192.168.0.49?channel=0&subtype=0",
		"test/xm/lq":    "dvrip://admin:@192.168.0.49?channel=0&subtype=1", // made from HQ
	} {
		if st[name] != want {
			t.Errorf("%s: %q, want %q", name, st[name], want)
		}
	}
	if !strings.Contains(us[GoConfig], "'test/xm/hq': 'dvrip://admin:@192.168.0.49?channel=0&subtype=0'") {
		t.Errorf("go2rtc:\n%s", us[GoConfig])
	}
}

func TestCameraProfileOverrideAndInherit(t *testing.T) {
	s := profileStore(t)
	hq := func(cam string) string {
		h, _, err := ResolveCamera(s, camItem(t, s, cam)).Addresses()
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	run(t, s, `/camera set [ find name=wejscie ] password=Nowe123 stream="rtsp://<host>/<ch>/3"`)
	if got := hq("wejscie"); got != "rtsp://admin:Nowe123@192.168.1.2/1/3" {
		t.Errorf("override: %s", got)
	}
	run(t, s, `/camera unset [ find name=wejscie ] password stream`)
	if got := hq("wejscie"); got != "rtsp://admin:secret@192.168.1.2/1/1" {
		t.Errorf("back to the profile: %s", got)
	}
	run(t, s, `/camera/profile set [ find name=tiandy-secret ] password=zmienione`)
	if got := hq("wejscie"); got != "rtsp://admin:zmienione@192.168.1.2/1/1" {
		t.Errorf("profile changed: %s", got)
	}
	if got := hq("brama"); got != "rtsp://admin:secret@192.168.1.3/1/1" {
		t.Errorf("the camera's own value stays: %s", got)
	}
	// an address with a login keeps it
	run(t, s, `/camera set [ find name=brama ] stream=rtsp://inny:haslo@192.168.1.3/1/1`)
	if got := hq("brama"); got != "rtsp://inny:haslo@192.168.1.3/1/1" {
		t.Errorf("explicit login: %s", got)
	}
}

func TestCameraProfileChecks(t *testing.T) {
	s := profileStore(t)
	for _, c := range []struct{ line, want string }{
		// the typo of the user's example: no silent, dead camera
		{`/camera add nvr=test name=x profile="tiandy-admin" host=192.168.1.9 channel=1`, "tiandy-admin"},
		{`/camera add nvr=test name=x profile=tiandy-secret channel=1`, "stream (from profile tiandy-secret) uses <host>: set host= on the camera"},
		{`/camera add nvr=test name=x profile=tiandy-secret host=192.168.1.9`, "uses <ch>: set channel= on the camera"},
		{`/camera add nvr=test name=x profile=tiandy-secret host="10.0.0.1/x" channel=1`, "host \"10.0.0.1/x\": a host name or an address"},
		{`/camera add nvr=test name=x stream=rtsp://10.0.0.1/live password=abc`, "password without username"},
		{`/camera add nvr=test name=x host=10.0.0.1 channel=1`, "stream is required for a camera of a type=local nvr"},
		{`/camera add nvr=test name=x host=10.0.0.1 channel=1 stream="dvrip://<host>?channel=<ch0>"`, "dvrip:// needs engine=go2rtc"},
		{`/camera/profile add name=zly stream="rtsp://<host>/<chanel>/1"`, "unknown placeholder <chanel>"},
		{`/camera/profile add name=zly stream="dvrip://<host>?channel=<ch0>"`, "needs engine=go2rtc"},
		{`/camera/profile add name=zly stream="exec:ffmpeg" engine=go2rtc`, "expected a network source"},
		{`/camera/profile add name="z ly"`, "only letters, digits"},
		// in use: not removed or renamed
		{`/camera/profile remove [ find name=xm-dvrip ]`, "xm-dvrip"},
		{`/camera/profile set [ find name=xm-dvrip ] name=inny`, "xm-dvrip"},
		// wrong on its own: refused before the cameras are looked at
		{`/camera/profile set [ find name=xm-dvrip ] engine=internal`, "dvrip:// needs engine=go2rtc"},
		// right on its own, but not for a camera using it: refused, naming the camera
		{`/camera/profile add name=p2 username=admin stream="rtsp://<host>/live"`, ""},
		{`/camera add nvr=test name=c2 profile=p2 host=10.0.0.5`, ""},
		{`/camera/profile set [ find name=p2 ] stream="rtsp://<host>/<ch>/1"`, "profile p2 would break 1 camera(s):\n  test/c2: stream (from profile p2) uses <ch>: set channel= on the camera"},
	} {
		expect(t, s, c.line, c.want)
	}
	if p := find(s, "/camera/profile", "name", "xm-dvrip"); p.Val("engine") != "go2rtc" || p.Val("name") != "xm-dvrip" {
		t.Errorf("a refused change was kept: %v", p.Keys())
	}
	if p := find(s, "/camera/profile", "name", "p2"); p.Val("stream") != "rtsp://<host>/live" {
		t.Errorf("a refused change was kept: %s", p.Val("stream"))
	}
	// remote recorders: their own addresses and login
	run(t, s, `/nvr add type=dahua name=dh host=10.0.0.3 username=a password=b`)
	for _, k := range []string{"profile=tiandy-secret", "host=10.0.0.4", "username=x", `password=""`} {
		expect(t, s, `/camera add nvr=dh name=ch01 channel=1 `+k, "only for cameras of a type=local nvr")
	}
}

func TestCameraProfileExportRoundTrip(t *testing.T) {
	s := profileStore(t)
	exp := s.Export("/")
	if i, j := strings.Index(exp, "/camera/profile\n"), strings.Index(exp, "/camera\n"); i < 0 || j < 0 || i > j {
		t.Fatalf("profiles must come before the cameras:\n%s", exp)
	}
	if !strings.Contains(exp, `name=bez nvr=test profile=tiandy-secret host=192.168.1.4 channel=3 password=""`) &&
		!strings.Contains(exp, `password=""`) {
		t.Errorf("the empty password is lost:\n%s", exp)
	}
	cmds, err := script.Parse(strings.NewReader(exp), "export")
	if err != nil {
		t.Fatal(err)
	}
	s2 := newStore(t)
	for _, c := range cmds {
		if _, err := s2.Exec(c); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	if exp2 := s2.Export("/"); exp2 != exp {
		t.Errorf("round trip:\n%s\n---\n%s", exp, exp2)
	}
	if h, _, _ := ResolveCamera(s2, camItem(t, s2, "bez")).Addresses(); h != "rtsp://admin:@192.168.1.4/3/1" {
		t.Errorf("after the round trip: %s", h)
	}
}

func TestCameraPrintDetail(t *testing.T) {
	s := profileStore(t)
	out, err := s.Exec(script.Command{Path: "/camera", Verb: "print", Args: []string{"detail"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"username=admin  (profile tiandy-secret)",
		"password=*****  (camera)",
		"stream=rtsp://admin:*****@192.168.1.3/1/1  (profile tiandy-secret)",
		"stream-lq=rtsp://admin:*****@192.168.1.3/1/2  (profile tiandy-secret)",
		`password=""  (camera)`,
		"stream-lq=dvrip://admin:@192.168.0.49?channel=0&subtype=1  (made from stream)",
		"engine=go2rtc  (profile xm-dvrip)",
		"stream=rtsp://192.168.0.50:554/user=admin&password=*****&channel=2&stream=0.sdp  (profile xm-rtsp)",
		"engine=internal  (default)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("print detail misses %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{":secret@", "Admin123", "p%40ss", "p@ss"} { // (the profile name itself has secret in it)
		if strings.Contains(out, secret) {
			t.Errorf("a password in print detail (%s):\n%s", secret, out)
		}
	}
	plain, _ := s.Exec(script.Command{Path: "/camera", Verb: "print"})
	if !strings.Contains(plain, `nvr=test name=bez profile=tiandy-secret host=192.168.1.4 channel=3 password="" enabled=yes`) {
		t.Errorf("the empty password is not shown as such:\n%s", plain)
	}
	if strings.Contains(plain, "(profile") {
		t.Errorf("print without detail shows the details:\n%s", plain)
	}
	if p, _ := s.Exec(script.Command{Path: "/camera/profile", Verb: "print"}); strings.Contains(p, "password=secret") || !strings.Contains(p, "password=*****") {
		t.Errorf("/camera/profile print:\n%s", p)
	}
}
