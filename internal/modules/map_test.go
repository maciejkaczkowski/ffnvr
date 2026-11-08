package modules

import (
	"strings"
	"testing"
)

func TestMapConfig(t *testing.T) {
	s := newStore(t)
	for _, l := range []string{
		`/user add user=u1 password=x`,
		`/storage add name=d path=/srv/d`,
		`/nvr add type=local name=nvr01 storage=d`,
		`/camera add name=ch01 nvr=nvr01 stream=rtsp://h/1`,
		`/camera/position add nvr="nvr01" camera="cam22" long=17.0 lat=49.4 direction=90 angle=104 range=50`,
	} {
		expect(t, s, l, "")
	}
	// the saved views are in the users' profiles now; the places of the map are gone
	expect(t, s, `/dashboard add id=1 name=x user=u1`, "/dashboard")
	expect(t, s, `/dashboard/site add name=X lat=1 long=1`, "/dashboard/site")
	expect(t, s, `/dashboard/camera add nvr=nvr01 camera=ch01 position=1`, "/dashboard/camera")
	expect(t, s, `/camera/position add nvr=nvr01 camera=cam22 lat=1 long=1`, "has a position already")
	expect(t, s, `/camera/position add nvr=nvr01 camera=x lat=91 long=1`, "lat \"91\": a number from -90 to 90")
	if p := Positions(s); len(p) != 1 || p[0].Long != 17 || p[0].Direction != 90 || p[0].RangeMeter != 50 {
		t.Errorf("%+v", p)
	}
	exp := s.Export("/")
	for _, want := range []string{"/camera/position\nadd nvr=nvr01 camera=cam22 long=17 lat=49.4 direction=90 angle=104 range=50"} {
		if !strings.Contains(exp, want) {
			t.Errorf("export misses %q:\n%s", want, exp)
		}
	}
	// removing the camera leaves the entries (ignored), it does not block
	expect(t, s, `/camera remove [find name=ch01 nvr=nvr01]`, "")
}
