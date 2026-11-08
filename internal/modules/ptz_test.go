package modules

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/dahua/dahuatest"
	"ffnvr/internal/tiandy"
	"ffnvr/internal/uniview"
)

// fakePTZ records the commands in lg; lg.fail makes the next command fail once.
type fakePTZ struct {
	id int
	lg *ptzLog
}

func (f *fakePTZ) add(line string) {
	f.lg.mu.Lock()
	defer f.lg.mu.Unlock()
	f.lg.l = append(f.lg.l, line)
}

func (f *fakePTZ) do(_ context.Context, ch int, a string, on bool, speed int) error {
	f.lg.mu.Lock()
	fail := f.lg.fail
	f.lg.fail = false
	f.lg.mu.Unlock()
	if fail {
		return errors.New("session dropped")
	}
	f.add(fmt.Sprintf("d%d ch%d %s %v %d", f.id, ch, a, on, speed))
	return nil
}

func (f *fakePTZ) close() { f.add(fmt.Sprintf("d%d closed", f.id)) }

type fakeStopper struct{ *fakePTZ }

func (f fakeStopper) allStop(_ context.Context, ch int) error {
	f.add(fmt.Sprintf("d%d ch%d ALLSTOP", f.id, ch))
	return nil
}

func item(kv map[string]string) *config.Item {
	it := config.NewItem()
	for k, v := range kv {
		it.Set(k, v)
	}
	return it
}

type ptzLog struct {
	mu   sync.Mutex
	l    []string
	fail bool
	n    int
}

func (p *ptzLog) get() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.l...)
}

func testPTZ(t *testing.T, withStop bool) (*PTZControl, *ptzLog) {
	t.Helper()
	lg := &ptzLog{}
	p := NewPTZControl()
	p.connect = func(nvr *config.Item) (ptzDriver, error) {
		lg.mu.Lock()
		defer lg.mu.Unlock()
		lg.n++
		f := &fakePTZ{id: lg.n, lg: lg}
		if withStop {
			return fakeStopper{f}, nil
		}
		return f, nil
	}
	return p, lg
}

func ptzStore(t *testing.T) *config.Store {
	t.Helper()
	s := newStore(t)
	for i, e := range run(t, s,
		`/storage add name=d1 path=/tmp`,
		`/nvr add type=local name=loc storage=d1`,
		`/camera add name=l1 nvr=loc stream=rtsp://10.0.0.9/s`,
		`/nvr add type=dahua name=dh host=10.0.0.1 username=admin password=x`,
		`/camera add name=ch02 nvr=dh channel=2`,
		`/nvr add type=uniview name=uv host=10.0.0.2 username=admin password=x`,
		`/camera add name=ch08 nvr=uv channel=8`,
	) {
		if e != "" {
			t.Fatalf("line %d: %s", i, e)
		}
	}
	return s
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n got  %q\n want %q", what, got, want)
	}
}

func TestPTZCapabilities(t *testing.T) {
	for typ, aux := range map[string]string{"dahua": "", "dvrip": "light", "tiandy": "light wiper(hold)", "uniview": "light wiper heater deicing"} {
		c, ok := PTZCapabilities(typ)
		var names []string
		for _, a := range c.Aux {
			n := a.Name
			if a.Hold {
				n += "(hold)"
			}
			names = append(names, n)
		}
		if !ok || !c.Move || !c.Zoom || !c.Focus || !c.Iris || strings.Join(names, " ") != aux {
			t.Errorf("%s: %+v %v", typ, c, ok)
		}
	}
	if _, ok := PTZCapabilities("local"); ok {
		t.Error("local has PTZ")
	}
}

func TestPTZRequests(t *testing.T) {
	s := ptzStore(t)
	p, lg := testPTZ(t, false)
	for _, c := range []struct{ n, c, a, err string }{
		{"loc", "l1", PTZUp, "camera l1 has no PTZ control (ptz=yes of /camera)"},
		{"dh", "nope", PTZUp, "no such camera"},
		{"zz", "x", PTZUp, "no such nvr"},
		{"dh", "ch02", "dance", `dh cannot do "dance"`},
		{"dh", "ch02", PTZWiper, `dh cannot do "wiper"`}, // no auxiliaries captured for Dahua
	} {
		err := p.Do(s, c.n, c.c, c.a, true, 5)
		if err == nil || !errors.Is(err, ErrPTZ) || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%v: %v", c, err)
		}
	}
	if lg.n != 0 {
		t.Errorf("connected for wrong requests: %d", lg.n)
	}
}

func TestPTZSessionReuseRetryAndStop(t *testing.T) {
	s := ptzStore(t)
	p, lg := testPTZ(t, false)
	must := func(a string, on bool, speed int) {
		t.Helper()
		if err := p.Do(s, "dh", "ch02", a, on, speed); err != nil {
			t.Fatal(err)
		}
	}
	must(PTZUp, true, 0)  // default speed 5
	must(PTZUp, false, 5) // the same session
	lg.mu.Lock()
	lg.fail = true // the recorder dropped the session
	lg.mu.Unlock()
	must(PTZZoomIn, true, 99) // a new session, the command again; speed capped
	must(PTZStop, false, 3)   // no "stop all" on Dahua: the stop of what runs
	must(PTZStop, false, 3)   // nothing runs: the stop of a movement
	eq(t, "commands", lg.get(), []string{
		"d1 ch2 up true 5", "d1 ch2 up false 5", "d1 closed",
		"d2 ch2 zoomin true 8", "d2 ch2 zoomin false 3", "d2 ch2 up false 3",
	})
}

func TestPTZAllStopAndAux(t *testing.T) {
	s := ptzStore(t)
	p, lg := testPTZ(t, true)
	for _, c := range []struct {
		a  string
		on bool
	}{{PTZLeft, true}, {PTZStop, false}, {PTZLight, true}, {PTZLight, false}} {
		if err := p.Do(s, "uv", "ch08", c.a, c.on, 5); err != nil {
			t.Fatal(err)
		}
	}
	eq(t, "commands", lg.get(), []string{"d1 ch8 left true 5", "d1 ch8 ALLSTOP", "d1 ch8 light true 5", "d1 ch8 light false 5"})
	p.mu.Lock()
	e := p.m["uv"]
	p.mu.Unlock()
	if len(e.dog) != 0 || len(e.active) != 0 {
		t.Errorf("an auxiliary switch is no movement: %v %v", e.dog, e.active)
	}
}

func TestPTZWatchdogAndIdle(t *testing.T) {
	s := ptzStore(t)
	p, lg := testPTZ(t, false)
	p.Watchdog, p.Idle = 60*time.Millisecond, 150*time.Millisecond
	var logged []string
	var lmu sync.Mutex
	p.Logf = func(f string, a ...any) { lmu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); lmu.Unlock() }
	if err := p.Do(s, "dh", "ch02", PTZRight, true, 4); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := p.Do(s, "dh", "ch02", PTZRight, true, 4); err != nil { // the browser's repeat while held
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	eq(t, "still held", lg.get(), []string{"d1 ch2 right true 4", "d1 ch2 right true 4"})
	time.Sleep(60 * time.Millisecond) // no news: stopped
	eq(t, "watchdog", lg.get(), []string{"d1 ch2 right true 4", "d1 ch2 right true 4", "d1 ch2 right false 4"})
	time.Sleep(250 * time.Millisecond) // unused: closed
	eq(t, "idle", lg.get(), []string{"d1 ch2 right true 4", "d1 ch2 right true 4", "d1 ch2 right false 4", "d1 closed"})
	lmu.Lock()
	defer lmu.Unlock()
	if len(logged) != 1 || !strings.Contains(logged[0], "ptz: dh channel 2: right stopped after 60ms without news") {
		t.Errorf("log: %q", logged)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.m) != 0 {
		t.Errorf("entries left: %v", p.m)
	}
}

func TestPTZSettingsChanged(t *testing.T) {
	s := ptzStore(t)
	p, lg := testPTZ(t, false)
	p.Do(s, "dh", "ch02", PTZUp, false, 5)
	if e := run(t, s, `/nvr set [ find name=dh ] password=y`)[0]; e != "" {
		t.Fatal(e)
	}
	p.Do(s, "dh", "ch02", PTZUp, false, 5)
	eq(t, "new session", lg.get(), []string{"d1 ch2 up false 5", "d1 closed", "d2 ch2 up false 5"})
}

// ---------- the makes, against fakes answering like the captured recorders ----------

func TestPTZDahua(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	nvr := item(map[string]string{"type": "dahua", "host": fake.Addr(), "username": dahuatest.User, "password": dahuatest.Pass})
	d, err := newPTZDriver(nvr)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range []struct {
		ch int
		a  string
		on bool
	}{{2, PTZUp, true}, {2, PTZUp, false}, {2, PTZRightDown, true}, {3, PTZZoomIn, true}, {3, PTZIrisClose, false}, {3, PTZFocusNear, true}} {
		if err := d.do(ctx, c.ch, c.a, c.on, 5); err != nil {
			t.Fatal(err)
		}
	}
	if fake.Instances() != 2 {
		t.Errorf("instances: %d", fake.Instances())
	}
	d.close()
	fake.Mu.Lock()
	eq(t, "rpc", fake.PTZ, []string{"rpc start Up ch2 5", "rpc stop Up ch2 5", "rpc start RightDown ch2 5",
		"rpc start ZoomTele ch3 5", "rpc stop IrisSmall ch3 5", "rpc start FocusNear ch3 5"})
	fake.PTZ, fake.NoRPCPTZ = nil, true // firmware without RPC PTZ: the CGI
	fake.Mu.Unlock()
	if fake.Instances() != 0 {
		t.Errorf("instances after close: %d", fake.Instances())
	}
	d, _ = newPTZDriver(nvr)
	if err := d.do(ctx, 4, PTZLeftUp, true, 6); err != nil {
		t.Fatal(err)
	}
	if err := d.do(ctx, 4, PTZLeftUp, false, 6); err != nil {
		t.Fatal(err)
	}
	fake.Mu.Lock()
	eq(t, "cgi", fake.PTZ, []string{"cgi start LeftUp ch4 6", "cgi stop LeftUp ch4 6"})
	fake.Mu.Unlock()
}

// a DVRIP recorder answering the login and OPPTZControl (message 1400)
type dvripConf struct {
	mu       sync.Mutex
	listOnly bool              // firmware without Camera.ParamEx.[n]
	written  []string          // the configurations written (raw)
	paramEx  []json.RawMessage // per channel
}

func fakeDVRIP(t *testing.T) (addr string, got func() []map[string]any) {
	a, g, _ := fakeDVRIPConf(t)
	return a, g
}

func fakeDVRIPConf(t *testing.T) (addr string, got func() []map[string]any, conf *dvripConf) {
	conf = &dvripConf{paramEx: []json.RawMessage{
		json.RawMessage(`{ "Big" : 1000000, "WhiteLight" : { "Brightness" : 50, "WorkMode" : "Auto" } }`),
		json.RawMessage(`{ "Big" : 2000000, "WhiteLight" : { "Brightness" : 70, "WorkMode" : "Close" }, "BroadTrends" : { "AutoGain" : 0 } }`),
		json.RawMessage(`{ "Big" : 3 }`),
	}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var cmds []map[string]any
	send := func(c net.Conn, id uint16, v any) {
		b, _ := json.Marshal(v)
		h := make([]byte, 20)
		h[0] = 0xFF
		binary.LittleEndian.PutUint32(h[4:], 0x260)
		binary.LittleEndian.PutUint16(h[14:], id)
		binary.LittleEndian.PutUint32(h[16:], uint32(len(b)))
		c.Write(append(h, b...))
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					h := make([]byte, 20)
					if _, err := io.ReadFull(c, h); err != nil {
						return
					}
					body := make([]byte, binary.LittleEndian.Uint32(h[16:]))
					io.ReadFull(c, body)
					id := binary.LittleEndian.Uint16(h[14:])
					switch id {
					case 1000:
						send(c, 1001, map[string]any{"Ret": 100, "SessionID": "0x00000260"})
					case 1042:
						var m map[string]any
						json.Unmarshal([]byte(strings.TrimRight(string(body), "\x00\n")), &m)
						name, _ := m["Name"].(string)
						conf.mu.Lock()
						switch {
						case name == "Camera.ParamEx":
							send(c, 1043, map[string]any{"Name": name, name: conf.paramEx, "Ret": 100, "SessionID": "0x00000260"})
						case strings.HasPrefix(name, "Camera.ParamEx.[") && !conf.listOnly:
							n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "Camera.ParamEx.["), "]"))
							send(c, 1043, map[string]any{"Name": name, name: conf.paramEx[n], "Ret": 100, "SessionID": "0x00000260"})
						default:
							send(c, 1043, map[string]any{"Name": name, "Ret": 607, "SessionID": "0x00000260"})
						}
						conf.mu.Unlock()
					case 1040:
						conf.mu.Lock()
						conf.written = append(conf.written, strings.TrimRight(string(body), "\x00\n"))
						conf.mu.Unlock()
						send(c, 1041, map[string]any{"Name": "", "Ret": 100, "SessionID": "0x00000260"})
					case 1400:
						var m map[string]any
						json.Unmarshal([]byte(strings.TrimRight(string(body), "\x00\n")), &m)
						if !strings.Contains(string(body), `"Command" : "`) {
							t.Errorf("not spaced like XMeye: %s", body)
						}
						mu.Lock()
						cmds = append(cmds, m)
						mu.Unlock()
						send(c, 1401, map[string]any{"Name": "", "Ret": 100, "SessionID": "0x00000260"})
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() []map[string]any { mu.Lock(); defer mu.Unlock(); return cmds }, conf
}

func TestPTZDVRIP(t *testing.T) {
	addr, got := fakeDVRIP(t)
	d, err := newPTZDriver(item(map[string]string{"type": "dvrip", "host": addr, "username": "admin", "password": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	ctx := context.Background()
	for _, c := range []struct {
		a  string
		on bool
	}{{PTZUp, true}, {PTZUp, false}, {PTZRightDown, true}, {PTZZoomIn, true}, {PTZZoomOut, false}, {PTZFocusFar, true}, {PTZIrisOpen, true}} {
		if err := d.do(ctx, 2, c.a, c.on, 5); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	for _, m := range got() {
		op := m["OPPTZControl"].(map[string]any)
		par := op["Parameter"].(map[string]any)
		seen = append(seen, fmt.Sprintf("%v ch%v preset%v step%v %v", op["Command"], par["Channel"], par["Preset"], par["Step"], m["SessionID"]))
	}
	eq(t, "dvrip", seen, []string{
		"DirectionUp ch1 preset65535 step5 0x00000260", "DirectionUp ch1 preset-1 step5 0x00000260",
		"DirectionRightDown ch1 preset65535 step5 0x00000260", "ZoomTile ch1 preset65535 step5 0x00000260",
		"ZoomWide ch1 preset-1 step5 0x00000260", "FocusFar ch1 preset65535 step5 0x00000260", "IrisLarge ch1 preset65535 step5 0x00000260",
	})
}

// an HTTP recorder that wants Digest: the first try of every request gets 401, so the
// body must be sent again
func fakeHTTP(t *testing.T, answer string) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="NVRDVR", qop="auth", nonce="112274192"`)
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(b))
		mu.Unlock()
		fmt.Fprint(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestPTZUniview(t *testing.T) {
	srv, seen := fakeHTTP(t, `{"Response":{"ResponseURL":"/LAPI/V1.0/Channels/8/PTZ/PTZCtrl","ResponseCode":0,"ResponseString":"Succeed","StatusCode":0,"Data":{}}}`)
	c, _ := uniview.NewClient(srv.URL, "admin", "x", true)
	u := univiewPTZ{c}
	ctx := context.Background()
	for _, x := range []struct {
		a  string
		on bool
	}{{PTZUp, true}, {PTZUp, false}, {PTZRight, true}, {PTZRightUp, true}, {PTZZoomIn, true}, {PTZZoomIn, false},
		{PTZFocusNear, true}, {PTZIrisOpen, true}, {PTZLight, true}, {PTZLight, false}, {PTZWiper, true}, {PTZHeater, false}, {PTZDeicing, true}} {
		if err := u.do(ctx, 8, x.a, x.on, 5); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.allStop(ctx, 8); err != nil {
		t.Fatal(err)
	}
	p := "PUT /LAPI/V1.0/Channels/8/PTZ/PTZCtrl "
	eq(t, "uniview (as captured)", seen(), []string{
		p + `{"PTZCmd":1026,"Para1":0,"Para2":5}`, p + `{"PTZCmd":1025,"Para1":5,"Para2":5}`,
		p + `{"PTZCmd":1282,"Para1":5,"Para2":0}`, p + `{"PTZCmd":2050,"Para1":5,"Para2":5}`,
		p + `{"PTZCmd":770,"Para1":5,"Para2":5}`, p + `{"PTZCmd":769,"Para1":5,"Para2":5}`,
		p + `{"PTZCmd":514,"Para1":5,"Para2":5}`, p + `{"PTZCmd":260,"Para1":5,"Para2":5}`,
		p + `{"PTZCmd":2817}`, p + `{"PTZCmd":2818}`, p + `{"PTZCmd":2561}`, p + `{"PTZCmd":3074}`, p + `{"PTZCmd":4609}`,
		p + `{"PTZCmd":2305}`,
	})
}

func TestPTZTiandy(t *testing.T) {
	srv, seen := fakeHTTP(t, `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus version="2.0"><statusCode>1</statusCode><statusString>OK</statusString><subStatusCode>ok</subStatusCode></ResponseStatus>`)
	c, err := tiandy.NewClient("127.0.0.1", "admin", "x")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPPort, _ = strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	d := tiandyPTZ{c}
	ctx := context.Background()
	for _, x := range []struct {
		a  string
		on bool
	}{{PTZLeftUp, true}, {PTZLeftUp, false}, {PTZDown, true}, {PTZZoomIn, true}, {PTZZoomIn, false}, {PTZFocusNear, true}, {PTZFocusNear, false},
		{PTZIrisClose, true}, {PTZIrisClose, false}, {PTZWiper, true}, {PTZWiper, false}, {PTZLight, true}, {PTZLight, false}} {
		if err := d.do(ctx, 4, x.a, x.on, 5); err != nil {
			t.Fatal(err)
		}
	}
	const ch = "/ISAPI/PTZCtrl/channels/4/"
	eq(t, "tiandy (as captured)", seen(), []string{
		"PUT " + ch + "continuous <PTZData><pan>-50</pan><tilt>50</tilt><zoom/></PTZData>",
		"PUT " + ch + "continuous <PTZData><pan>0</pan><tilt>0</tilt><zoom/></PTZData>",
		"PUT " + ch + "continuous <PTZData><pan>0</pan><tilt>-50</tilt><zoom/></PTZData>",
		"PUT " + ch + "continuous <PTZData><pan/><tilt/><zoom>50</zoom></PTZData>",
		"PUT " + ch + "continuous <PTZData><pan/><tilt/><zoom>0</zoom></PTZData>",
		"PUT /ISAPI/System/Video/inputs/channels/4/focus <FocusData><focus>-100</focus></FocusData>",
		"PUT /ISAPI/System/Video/inputs/channels/4/focus <FocusData><focus>0</focus></FocusData>",
		"PUT /ISAPI/System/Video/inputs/channels/4/iris <IrisData><iris>-100</iris></IrisData>", // no stop for the iris
		"PUT " + ch + "auxcontrols/0 <PTZAux><id>0</id><type>WIPER</type><status>on</status></PTZAux>",
		"PUT " + ch + "auxcontrols/0 <PTZAux><id>0</id><type>WIPER</type><status>off</status></PTZAux>",
		`PUT /CGI/PTZCtrl/channels/4/manuallaser <?xml version="1.0" encoding="UTF-8"?><LaserData><LaserEnable>1</LaserEnable></LaserData>`,
		`PUT /CGI/PTZCtrl/channels/4/manuallaser <?xml version="1.0" encoding="UTF-8"?><LaserData><LaserEnable>0</LaserEnable></LaserData>`,
	})
}

func TestPTZDVRIPWhiteLight(t *testing.T) {
	addr, _, conf := fakeDVRIPConf(t)
	d, _ := newPTZDriver(item(map[string]string{"type": "dvrip", "host": addr, "username": "admin", "password": "x"}))
	defer d.close()
	ctx := context.Background()
	if err := d.do(ctx, 2, PTZLight, true, 5); err != nil { // channel 2 = Camera.ParamEx.[1]
		t.Fatal(err)
	}
	if err := d.do(ctx, 1, PTZLight, false, 5); err != nil {
		t.Fatal(err)
	}
	err := d.do(ctx, 3, PTZLight, true, 5)
	if err == nil || !strings.Contains(err.Error(), "no white light") {
		t.Errorf("a camera without the light: %v", err)
	}
	conf.mu.Lock()
	conf.listOnly = true // firmware without the channel's own part: the whole list
	conf.mu.Unlock()
	if err := d.do(ctx, 2, PTZLight, true, 5); err != nil {
		t.Fatal(err)
	}
	conf.mu.Lock()
	defer conf.mu.Unlock()
	eq(t, "written (other values untouched, numbers as they were)", conf.written, []string{
		`{ "Camera.ParamEx.[1]" : { "Big" : 2000000, "BroadTrends" : { "AutoGain" : 0 }, "WhiteLight" : { "Brightness" : 70, "WorkMode" : "Open" } }, "Name" : "Camera.ParamEx.[1]", "SessionID" : "0x00000260" }`,
		`{ "Camera.ParamEx.[0]" : { "Big" : 1000000, "WhiteLight" : { "Brightness" : 50, "WorkMode" : "Close" } }, "Name" : "Camera.ParamEx.[0]", "SessionID" : "0x00000260" }`,
		`{ "Camera.ParamEx" : [{ "Big" : 1000000, "WhiteLight" : { "Brightness" : 50, "WorkMode" : "Auto" } }, { "Big" : 2000000, "BroadTrends" : { "AutoGain" : 0 }, "WhiteLight" : { "Brightness" : 70, "WorkMode" : "Open" } }, { "Big" : 3 }], "Name" : "Camera.ParamEx", "SessionID" : "0x00000260" }`,
	})
}

func TestLocalCameraPTZ(t *testing.T) {
	s := newStore(t)
	for i, e := range run(t, s,
		`/storage add name=d1 path=/tmp`,
		`/nvr add type=local name=loc storage=d1`,
		`/camera/profile add name=xm username=admin password=pw type=dvrip ptz=yes stream="rtsp://<host>:554/user=<user>&password=<password>&channel=<ch>&stream=0.sdp"`,
		// host from host= (its RTSP port dropped), login, type and ptz from the profile
		`/camera add name=k1 nvr=loc profile=xm host=192.168.1.21:554 channel=1`,
		// another make on the same recorder, everything from the stream address
		`/camera add name=k2 nvr=loc stream=rtsp://op:sec@192.168.1.22:8554/live type=dahua ptz=yes`,
		// the profile's ptz switched off for one camera
		`/camera add name=k3 nvr=loc profile=xm host=192.168.1.23 channel=1 ptz=no`,
		// explicit address and channel: an encoder behind a port forward
		`/camera add name=k4 nvr=loc profile=xm host=10.0.0.5 channel=3 ptz-host=10.0.0.5:34570`,
		// go2rtc's dvrip:// carries the protocol's port
		`/camera add name=k5 nvr=loc engine=go2rtc stream="dvrip://u:p@192.168.1.25:34599?channel=0&subtype=0" type=dvrip ptz=yes`,
	) {
		if e != "" {
			t.Fatalf("line %d: %s", i, e)
		}
	}
	for cam, want := range map[string]string{
		"k1": "dvrip 192.168.1.21 admin pw ch1 loc/k1",
		"k2": "dahua 192.168.1.22 op sec ch1 loc/k2",
		"k4": "dvrip 10.0.0.5:34570 admin pw ch3 loc/k4",
		"k5": "dvrip 192.168.1.25:34599 u p ch1 loc/k5",
	} {
		tg, err := targetOf(s, "loc", cam)
		if err != nil {
			t.Errorf("%s: %v", cam, err)
			continue
		}
		got := fmt.Sprintf("%s %s %s %s ch%d %s", tg.drv.Val("type"), tg.drv.Val("host"), tg.drv.Val("username"), tg.drv.Val("password"), tg.ch, tg.session)
		if got != want {
			t.Errorf("%s: %s, want %s", cam, got, want)
		}
	}
	if _, ok := CameraPTZ(s, "loc", "k3"); ok {
		t.Error("k3: ptz=no overrides the profile")
	}
	if c, ok := CameraPTZ(s, "loc", "k1"); !ok || !c.Move || len(c.Aux) != 1 {
		t.Errorf("k1 caps: %+v %v", c, ok)
	}
	// every camera has its own session: two cameras, two drivers
	p, lg := testPTZ(t, false)
	if err := p.Do(s, "loc", "k1", PTZLeft, true, 3); err != nil {
		t.Fatal(err)
	}
	if err := p.Do(s, "loc", "k2", PTZZoomIn, true, 3); err != nil {
		t.Fatal(err)
	}
	eq(t, "commands", lg.get(), []string{"d1 ch1 left true 3", "d2 ch1 zoomin true 3"})

	for _, bad := range []string{
		`/camera add name=b1 nvr=loc stream=rtsp://192.168.1.30/s ptz-host=192.168.1.31`,             // ptz-host without ptz
		`/camera add name=b2 nvr=loc stream=rtsp://192.168.1.30/s type=tiandy ptz=yes ptz-channel=0`, // channel 0
		`/camera add name=b3 nvr=loc stream=rtsp://192.168.1.30/s type=dvrip ptz=yes ptz-host="a b"`,
		`/camera add name=b4 nvr=loc stream=rtsp://192.168.1.30/s ptz=yes`, // ptz without type
	} {
		if e := run(t, s, bad)[0]; e == "" {
			t.Errorf("accepted: %s", bad)
		}
	}
	// a camera of a remote recorder is of its recorder's type, steered through it
	run(t, s, `/nvr add type=dahua name=dh host=10.0.0.1 username=admin password=x`)
	if e := run(t, s, `/camera add name=r1 nvr=dh channel=1 type=dvrip`)[0]; !strings.Contains(e, "type=local") {
		t.Errorf("type on a remote recorder's camera: %q", e)
	}
	run(t, s, `/camera add name=r2 nvr=dh channel=2`)
	if _, ok := CameraPTZ(s, "dh", "r2"); !ok {
		t.Error("r2: steered through the recorder, by default")
	}
	if e := run(t, s, `/camera add name=r3 nvr=dh channel=3 ptz=false`)[0]; e != "" {
		t.Errorf("ptz=no on a remote recorder's camera: %q", e)
	}
	if _, ok := CameraPTZ(s, "dh", "r3"); ok {
		t.Error("r3: ptz=no, no PTZ")
	}
}
