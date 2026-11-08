package modules

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/dahua"
	"ffnvr/internal/dvrip"
	"ffnvr/internal/tiandy"
	"ffnvr/internal/uniview"
)

// PTZ of the cameras behind remote recorders (the live view's control panel, right
// live.ptz). The commands are the ones the makers' web interfaces send, as captured
// from them (see the ptz.go files of the protocol packages). A movement runs from its
// start (on) until its stop (off): the browser sends the start when a button is
// pressed and the stop when it is released.

// PTZ actions.
const (
	PTZUp        = "up"
	PTZDown      = "down"
	PTZLeft      = "left"
	PTZRight     = "right"
	PTZLeftUp    = "leftup"
	PTZRightUp   = "rightup"
	PTZLeftDown  = "leftdown"
	PTZRightDown = "rightdown"
	PTZZoomIn    = "zoomin"
	PTZZoomOut   = "zoomout"
	PTZFocusNear = "focusnear"
	PTZFocusFar  = "focusfar"
	PTZIrisOpen  = "irisopen"
	PTZIrisClose = "irisclose"
	PTZStop      = "stop" // everything of the channel
	PTZLight     = "light"
	PTZWiper     = "wiper"
	PTZHeater    = "heater"
	PTZDeicing   = "deicing"
)

var ptzMoves = []string{PTZUp, PTZDown, PTZLeft, PTZRight, PTZLeftUp, PTZRightUp, PTZLeftDown, PTZRightDown}

// PTZAux is an auxiliary of a camera: Hold ones run while their button is pressed
// (like a movement), the others are switched on and off.
type PTZAux struct {
	Name string `json:"name"`
	Hold bool   `json:"hold,omitempty"`
}

// PTZCaps: what the panel offers for the cameras of a recorder type.
type PTZCaps struct {
	Move  bool     `json:"move"`
	Zoom  bool     `json:"zoom"`
	Focus bool     `json:"focus"`
	Iris  bool     `json:"iris"`
	Aux   []PTZAux `json:"aux"`
}

// PTZSpeedMax: speeds are 1..PTZSpeedMax (5 is what the web interfaces send).
const PTZSpeedMax = 8

// PTZCapabilities of a recorder type; false: no PTZ (local recorders).
func PTZCapabilities(nvrType string) (PTZCaps, bool) {
	all := PTZCaps{Move: true, Zoom: true, Focus: true, Iris: true, Aux: []PTZAux{}}
	switch nvrType {
	case "dahua":
		return all, true
	case "dvrip":
		all.Aux = []PTZAux{{Name: PTZLight}} // the camera's white light (Camera.ParamEx)
		return all, true
	case "tiandy":
		all.Aux = []PTZAux{{Name: PTZLight}, {Name: PTZWiper, Hold: true}}
		return all, true
	case "uniview":
		all.Aux = []PTZAux{{Name: PTZLight}, {Name: PTZWiper}, {Name: PTZHeater}, {Name: PTZDeicing}}
		return all, true
	}
	return PTZCaps{}, false
}

// supports reports whether the caps allow an action.
func (c PTZCaps) supports(a string) bool {
	switch a {
	case PTZStop:
		return c.Move || c.Zoom || c.Focus
	case PTZZoomIn, PTZZoomOut:
		return c.Zoom
	case PTZFocusNear, PTZFocusFar:
		return c.Focus
	case PTZIrisOpen, PTZIrisClose:
		return c.Iris
	}
	for _, m := range ptzMoves {
		if a == m {
			return c.Move
		}
	}
	for _, x := range c.Aux {
		if a == x.Name {
			return true
		}
	}
	return false
}

// running: an action that lasts until its stop (movements, held auxiliaries).
func (c PTZCaps) running(a string) bool {
	if a == PTZStop {
		return false
	}
	for _, x := range c.Aux {
		if x.Name == a {
			return x.Hold
		}
	}
	return true
}

// PTZSwap: whether a camera's PTZ directions are swapped (/camera ptz-swap-horizontal,
// ptz-swap-vertical) - the panel turns left into right, up into down.
func PTZSwap(s *config.Store, nvrName, camName string) (horizontal, vertical bool) {
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvrName && c.Val("name") == camName {
			return c.Val("ptz-swap-horizontal") == "yes", c.Val("ptz-swap-vertical") == "yes"
		}
	}
	return false, false
}

// ErrPTZ: the request itself is wrong (unknown action, no PTZ on that recorder).
var ErrPTZ = errors.New("ptz")

// ptzDriver sends commands to one recorder; channel from 1.
type ptzDriver interface {
	do(ctx context.Context, channel int, action string, on bool, speed int) error
	close()
}

// allStopper: a recorder with a command stopping everything of a channel.
type allStopper interface {
	allStop(ctx context.Context, channel int) error
}

// PTZControl keeps a session per recorder for a while after its last command (logging
// in for every press would be slow), serializes the commands of a recorder and stops a
// movement nobody stopped (the browser went away) after Watchdog.
type PTZControl struct {
	Idle     time.Duration // a session unused this long is closed (default 30 s)
	Watchdog time.Duration // a movement without news this long is stopped (default 15 s)
	Logf     func(string, ...any)
	// connect makes the driver of a recorder (tests replace it)
	connect func(nvr *config.Item) (ptzDriver, error)

	mu sync.Mutex
	m  map[string]*ptzEntry
}

type ptzEntry struct {
	mu     sync.Mutex
	key    string // the recorder's settings: another key, another session
	drv    ptzDriver
	idle   *time.Timer
	active map[int]string      // channel -> the running action
	dog    map[int]*time.Timer // channel -> its watchdog
}

// NewPTZControl returns an empty pool.
func NewPTZControl() *PTZControl {
	return &PTZControl{Idle: 30 * time.Second, Watchdog: 15 * time.Second, connect: newPTZDriver, m: map[string]*ptzEntry{}}
}

// PTZ is the pool of the web panel.
var PTZ = NewPTZControl()

func ptzKey(nvr *config.Item) string {
	k := ""
	for _, f := range []string{"type", "host", "username", "password", "api", "http-port"} {
		k += f + "=" + nvr.Val(f) + "\x00"
	}
	return k
}

// CameraTypes: the makes of cameras of local recorders (type= of /camera and
// /camera/profile), the protocol their PTZ control speaks. Cameras of these makes speak
// their recorders' protocol: an XMeye camera answers DVRIP on 34567, a Tiandy one its
// native port 3001 and ISAPI on 80, Dahua and Uniview their HTTP API - channel 1 is the
// camera itself. Hikvision cameras speak ISAPI on 80 (for now only their events; no
// PTZ control).
var CameraTypes = []string{"dahua", "dvrip", "hikvision", "tiandy", "uniview"}

// ptzTarget is what a PTZ command goes to.
type ptzTarget struct {
	session string       // the pool's key: the recorder, or nvr/camera for a camera of its own
	drv     *config.Item // what newPTZDriver connects to: type, host, username, password, api, http-port
	ch      int          // from 1
	caps    PTZCaps
}

func findCamera(s *config.Store, nvrName, camName string) *config.Item {
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvrName && c.Val("name") == camName {
			return c
		}
	}
	return nil
}

// targetOf finds a camera and its PTZ: through its remote recorder (unless ptz=no),
// or (a camera of a local recorder with ptz=yes, by its type=) directly.
func targetOf(s *config.Store, nvrName, camName string) (ptzTarget, error) {
	nvr := find(s, "/nvr", "name", nvrName)
	if nvr == nil {
		return ptzTarget{}, fmt.Errorf("%w: no such nvr: %s", ErrPTZ, nvrName)
	}
	cam := findCamera(s, nvrName, camName)
	if cam == nil {
		return ptzTarget{}, fmt.Errorf("%w: no such camera: %s %s", ErrPTZ, nvrName, camName)
	}
	if !remote(nvr) {
		if !ResolveCamera(s, cam).PTZ {
			return ptzTarget{}, fmt.Errorf("%w: camera %s has no PTZ control (ptz=yes of /camera)", ErrPTZ, camName)
		}
		drv, ch, err := localPTZ(s, cam)
		if err != nil {
			return ptzTarget{}, err
		}
		caps, _ := PTZCapabilities(drv.Val("type"))
		return ptzTarget{session: nvrName + "/" + camName, drv: drv, ch: ch, caps: caps}, nil
	}
	// a camera of a remote recorder is of the recorder's type and steered through it,
	// unless ptz=no
	if cam.Val("ptz") == "no" {
		return ptzTarget{}, fmt.Errorf("%w: camera %s: ptz=no", ErrPTZ, camName)
	}
	caps, ok := PTZCapabilities(nvr.Val("type"))
	if !ok {
		return ptzTarget{}, fmt.Errorf("%w: %s (type=%s) has no PTZ control", ErrPTZ, nvrName, nvr.Val("type"))
	}
	ch, err := strconv.Atoi(cam.Val("channel"))
	if err != nil || ch < 1 {
		return ptzTarget{}, fmt.Errorf("%w: camera %s has no channel", ErrPTZ, camName)
	}
	return ptzTarget{session: nvrName, drv: nvr, ch: ch, caps: caps}, nil
}

// localPTZ: the driver settings of a camera of a local recorder with ptz=yes, by its
// type=.
//
//	host      ptz-host=, else host= (of the camera or the address scheme, without its
//	          RTSP port), else the host of the stream address; the protocol's own
//	          port is the default (ptz-host=192.168.1.5:34568 gives another)
//	login     username= and password= (camera or profile), else those in the address
//	channel   ptz-channel=, else channel=, else 1 (the camera itself)
func localPTZ(s *config.Store, cam *config.Item) (*config.Item, int, error) {
	src := ResolveCamera(s, cam)
	typ := src.Type
	if !src.PTZ {
		return nil, 0, fmt.Errorf("%w: camera %s has no PTZ control (ptz=yes of /camera)", ErrPTZ, cam.Val("name"))
	}
	if typ == "" {
		return nil, 0, fmt.Errorf("%w: camera %s: ptz=yes needs type= (dahua, dvrip, tiandy, uniview)", ErrPTZ, cam.Val("name"))
	}
	host, user, pass := cameraDevice(cam, src)
	if host == "" {
		return nil, 0, fmt.Errorf("%w: camera %s: the PTZ control (type=%s) needs an address: ptz-host=, host= or a stream address", ErrPTZ, cam.Val("name"), typ)
	}
	ch := 1
	for _, k := range []string{"ptz-channel", "channel"} {
		if v := cam.Val(k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return nil, 0, fmt.Errorf("%w: camera %s: %s must be 1 or more", ErrPTZ, cam.Val("name"), k)
			}
			ch = n
			break
		}
	}
	drv := config.NewItem()
	for k, v := range map[string]string{"type": typ, "host": host, "username": user, "password": pass} {
		drv.Set(k, v)
	}
	return drv, ch, nil
}

// cameraDevice: where the protocol of a camera of a local recorder (its type=) is
// reached, and its login - for the PTZ control and the events. The address: ptz-host=,
// else host= without its port (that one is the stream's), else the host of the stream
// address (with its port when the address is of the protocol itself: dvrip://host:34567
// of go2rtc); "" when none tells. The login: the camera's or its profile's, else the
// one in the stream address.
func cameraDevice(cam *config.Item, src CameraSource) (host, user, pass string) {
	typ := src.Type
	var streamURL *url.URL
	if hq, _, err := src.Addresses(); err == nil && hq != "" {
		streamURL, _ = url.Parse(hq)
	}
	host = cam.Val("ptz-host")
	if host == "" && src.Host != "" {
		host = hostname(src.Host)
	}
	if host == "" && streamURL != nil {
		host = streamURL.Hostname()
		if streamURL.Scheme == typ && streamURL.Port() != "" { // dvrip://host:34567 of go2rtc: that port is the protocol's
			host = net.JoinHostPort(host, streamURL.Port())
		} else if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	}
	user, pass = src.Username, src.Password
	if user == "" && pass == "" && streamURL != nil && streamURL.User != nil {
		user = streamURL.User.Username()
		pass, _ = streamURL.User.Password()
	}
	return host, user, pass
}

// hostname: a host without its port ("192.168.1.2:8554" -> "192.168.1.2").
func hostname(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return h
}

// CameraPTZ: what the control panel offers for a camera (false: no PTZ).
func CameraPTZ(s *config.Store, nvrName, camName string) (PTZCaps, bool) {
	t, err := targetOf(s, nvrName, camName)
	if err != nil {
		return PTZCaps{}, false
	}
	return t.caps, true
}

// Do runs an action on a camera: on starts it (or switches an auxiliary on), off
// stops it; speed 1..PTZSpeedMax (0: 5).
func (p *PTZControl) Do(s *config.Store, nvrName, camName, action string, on bool, speed int) error {
	tg, err := targetOf(s, nvrName, camName)
	if err != nil {
		return err
	}
	nvr, ch, caps, name := tg.drv, tg.ch, tg.caps, tg.session
	if !caps.supports(action) {
		return fmt.Errorf("%w: %s cannot do %q", ErrPTZ, name, action)
	}
	if speed <= 0 {
		speed = 5
	}
	speed = min(speed, PTZSpeedMax)
	e := p.entry(name, ptzKey(nvr))
	e.mu.Lock()
	defer e.mu.Unlock()
	p.touch(name, e)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // not the request's: a half sent command breaks a session
	defer cancel()
	run := func() error {
		if action == PTZStop {
			return p.stop(ctx, e, ch, speed)
		}
		return e.drv.do(ctx, ch, action, on, speed)
	}
	fresh := e.drv == nil
	if fresh {
		if e.drv, err = p.connect(nvr); err != nil {
			e.drv = nil
			return err
		}
	}
	if err = run(); err != nil && !fresh {
		// a session the recorder has dropped meanwhile: once more with a new one
		e.drv.close()
		if e.drv, err = p.connect(nvr); err != nil {
			e.drv = nil
			return err
		}
		err = run()
	}
	if err != nil {
		return err
	}
	p.watch(name, e, ch, action, on && caps.running(action), speed)
	return nil
}

// stop ends what runs on a channel: the recorder's own "stop all", else the stop of
// the running action (of a movement when none is known: it halts the motors).
func (p *PTZControl) stop(ctx context.Context, e *ptzEntry, ch, speed int) error {
	if a, ok := e.drv.(allStopper); ok {
		return a.allStop(ctx, ch)
	}
	act := e.active[ch]
	if act == "" {
		act = PTZUp
	}
	return e.drv.do(ctx, ch, act, false, speed)
}

func (p *PTZControl) entry(name, key string) *ptzEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.m[name]
	if e != nil && e.key != key { // the recorder's settings changed
		e.mu.Lock()
		if e.drv != nil {
			e.drv.close()
			e.drv = nil
		}
		e.mu.Unlock()
		e = nil
	}
	if e == nil {
		e = &ptzEntry{key: key, active: map[int]string{}, dog: map[int]*time.Timer{}}
		p.m[name] = e
	}
	return e
}

// touch restarts the idle timer of a recorder's session (e.mu held).
func (p *PTZControl) touch(name string, e *ptzEntry) {
	if e.idle != nil {
		e.idle.Stop()
	}
	e.idle = time.AfterFunc(p.Idle, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.dog) > 0 {
			return // a watchdog will come and touch again
		}
		if e.drv != nil {
			e.drv.close()
			e.drv = nil
		}
		p.mu.Lock()
		if p.m[name] == e {
			delete(p.m, name)
		}
		p.mu.Unlock()
	})
}

// watch remembers what runs on a channel and arms its watchdog (e.mu held).
func (p *PTZControl) watch(name string, e *ptzEntry, ch int, action string, running bool, speed int) {
	if t := e.dog[ch]; t != nil {
		t.Stop()
		delete(e.dog, ch)
	}
	if !running {
		if action == PTZStop || e.active[ch] == action {
			delete(e.active, ch)
		}
		return
	}
	e.active[ch] = action
	e.dog[ch] = time.AfterFunc(p.Watchdog, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.active[ch] != action || e.drv == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := e.drv.do(ctx, ch, action, false, speed)
		delete(e.active, ch)
		delete(e.dog, ch)
		p.touch(name, e)
		if p.Logf != nil {
			p.Logf("ptz: %s channel %d: %s stopped after %s without news (err: %v)", name, ch, action, p.Watchdog, err)
		}
	})
}

// ---------- the makes ----------

func newPTZDriver(nvr *config.Item) (ptzDriver, error) {
	switch nvr.Val("type") {
	case "dahua":
		c, err := dahua.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"), true)
		if err != nil {
			return nil, err
		}
		return &dahuaPTZ{c: c, cgi: nvr.Val("api") == "cgi"}, nil
	case "dvrip":
		c, err := dvrip.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"))
		if err != nil {
			return nil, err
		}
		return &dvripPTZ{c: c}, nil
	case "uniview":
		c, err := uniview.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"), true)
		if err != nil {
			return nil, err
		}
		return univiewPTZ{c}, nil
	case "tiandy":
		c, err := tiandy.NewClient(nvr.Val("host"), nvr.Val("username"), nvr.Val("password"))
		if err != nil {
			return nil, err
		}
		c.HTTPPort, _ = strconv.Atoi(nvr.Val("http-port"))
		return tiandyPTZ{c}, nil
	}
	return nil, fmt.Errorf("%w: type=%s has no PTZ control", ErrPTZ, nvr.Val("type"))
}

// Dahua and DVRIP name the actions the same way, nearly.
var dahuaCodes = map[string]string{
	PTZUp: "Up", PTZDown: "Down", PTZLeft: "Left", PTZRight: "Right",
	PTZLeftUp: "LeftUp", PTZRightUp: "RightUp", PTZLeftDown: "LeftDown", PTZRightDown: "RightDown",
	PTZZoomIn: "ZoomTele", PTZZoomOut: "ZoomWide", PTZFocusNear: "FocusNear", PTZFocusFar: "FocusFar",
	PTZIrisOpen: "IrisLarge", PTZIrisClose: "IrisSmall",
}

// dahuaPTZ: JSON-RPC (instances per channel), the CGI with api=cgi or when the RPC
// login fails.
type dahuaPTZ struct {
	c   *dahua.Client
	cgi bool
	rpc *dahua.PTZ
}

func (d *dahuaPTZ) do(ctx context.Context, ch int, action string, on bool, speed int) error {
	code, ok := dahuaCodes[action]
	if !ok {
		return fmt.Errorf("%w: dahua: no %q", ErrPTZ, action)
	}
	if !d.cgi && d.rpc == nil {
		p, err := d.c.LoginPTZ(ctx)
		if err != nil {
			d.cgi = true // older firmware: the CGI
		} else {
			d.rpc = p
		}
	}
	if !d.cgi {
		err := d.rpc.Do(ctx, ch, code, on, speed)
		if !errors.Is(err, dahua.ErrNoRPCPTZ) {
			return err
		}
		d.close() // older firmware: the CGI from now on
		d.cgi = true
	}
	return d.c.PTZCGI(ctx, ch, code, on, speed)
}

func (d *dahuaPTZ) close() {
	if d.rpc != nil {
		d.rpc.Close()
		d.rpc = nil
	}
}

// dvripPTZ: one logged in session kept alive while it is used.
type dvripPTZ struct {
	c      *dvrip.Client
	s      *dvrip.Session
	cancel context.CancelFunc
}

func (d *dvripPTZ) do(ctx context.Context, ch int, action string, on bool, speed int) error {
	cmd, ok := dahuaCodes[action]
	if !ok && action != PTZLight {
		return fmt.Errorf("%w: dvrip: no %q", ErrPTZ, action)
	}
	switch {
	case action == PTZZoomIn:
		cmd = "ZoomTile" // sic, as the recorder wants it
	case !isZoomFocusIris(action):
		cmd = "Direction" + cmd // DirectionUp, DirectionLeftDown, ...
	}
	if d.s == nil {
		s, err := d.c.Login(ctx)
		if err != nil {
			return err
		}
		kctx, cancel := context.WithCancel(context.Background())
		d.s, d.cancel = s, cancel
		go s.KeepAlive(kctx)
	}
	if action == PTZLight {
		return d.s.WhiteLight(ch-1, on)
	}
	return d.s.PTZ(ch-1, cmd, on, speed)
}

func isZoomFocusIris(a string) bool {
	switch a {
	case PTZZoomIn, PTZZoomOut, PTZFocusNear, PTZFocusFar, PTZIrisOpen, PTZIrisClose:
		return true
	}
	return false
}

func (d *dvripPTZ) close() {
	if d.cancel != nil {
		d.cancel()
	}
	if d.s != nil {
		d.s.Close()
		d.s = nil
	}
}

// univiewPTZ: LAPI, a command and its stop (command - 1); its own "stop all".
type univiewPTZ struct{ c *uniview.Client }

var univiewCodes = map[string]int{
	PTZUp: uniview.PTZUp, PTZDown: uniview.PTZDown, PTZLeft: uniview.PTZLeft, PTZRight: uniview.PTZRight,
	PTZLeftUp: uniview.PTZLeftUp, PTZRightUp: uniview.PTZRightUp, PTZLeftDown: uniview.PTZLeftDown, PTZRightDown: uniview.PTZRightDown,
	PTZZoomIn: uniview.PTZZoomTele, PTZZoomOut: uniview.PTZZoomWide, PTZFocusNear: uniview.PTZFocusNear, PTZFocusFar: uniview.PTZFocusFar,
	PTZIrisOpen: uniview.PTZIrisOpen, PTZIrisClose: uniview.PTZIrisClose,
	PTZLight: uniview.PTZLightOn, PTZWiper: uniview.PTZWiperOn, PTZHeater: uniview.PTZHeaterOn, PTZDeicing: uniview.PTZDeicingOn,
}

func (u univiewPTZ) do(ctx context.Context, ch int, action string, on bool, speed int) error {
	cmd, ok := univiewCodes[action]
	if !ok {
		return fmt.Errorf("%w: uniview: no %q", ErrPTZ, action)
	}
	switch action {
	case PTZLight, PTZWiper, PTZHeater, PTZDeicing:
		if !on {
			cmd++ // off
		}
		return u.c.PTZ(ctx, ch, cmd, nil, nil)
	}
	pan, tilt := speed, speed
	if on {
		switch action { // the web interface: no speed on the axis that does not move
		case PTZUp, PTZDown:
			pan = 0
		case PTZLeft, PTZRight:
			tilt = 0
		}
	} else {
		cmd-- // stop
	}
	return u.c.PTZ(ctx, ch, cmd, &pan, &tilt)
}

func (u univiewPTZ) allStop(ctx context.Context, ch int) error {
	return u.c.PTZ(ctx, ch, uniview.PTZAllStop, nil, nil)
}

func (univiewPTZ) close() {}

// tiandyPTZ: ISAPI (continuous movement, focus, iris), auxiliaries; speed 1-8 is
// 10-80 (the web interface sends 50).
type tiandyPTZ struct{ c *tiandy.Client }

func (t tiandyPTZ) do(ctx context.Context, ch int, action string, on bool, speed int) error {
	v := 0
	if on {
		v = speed * 10
	}
	n := func(x int) *int { return &x }
	switch action {
	case PTZUp, PTZDown, PTZLeft, PTZRight, PTZLeftUp, PTZRightUp, PTZLeftDown, PTZRightDown:
		pan, tilt := 0, 0
		switch action {
		case PTZLeft, PTZLeftUp, PTZLeftDown:
			pan = -v
		case PTZRight, PTZRightUp, PTZRightDown:
			pan = v
		}
		switch action {
		case PTZUp, PTZLeftUp, PTZRightUp:
			tilt = v
		case PTZDown, PTZLeftDown, PTZRightDown:
			tilt = -v
		}
		return t.c.PTZContinuous(ctx, ch, &pan, &tilt, nil)
	case PTZZoomIn:
		return t.c.PTZContinuous(ctx, ch, nil, nil, n(v))
	case PTZZoomOut:
		return t.c.PTZContinuous(ctx, ch, nil, nil, n(-v))
	case PTZFocusFar, PTZFocusNear:
		f := 0
		if on {
			f = 100
			if action == PTZFocusNear {
				f = -100
			}
		}
		return t.c.PTZFocus(ctx, ch, f)
	case PTZIrisOpen, PTZIrisClose:
		if !on {
			return nil // a step per press: nothing to stop
		}
		i := 100
		if action == PTZIrisClose {
			i = -100
		}
		return t.c.PTZIris(ctx, ch, i)
	case PTZWiper:
		return t.c.PTZAux(ctx, ch, "WIPER", on)
	case PTZLight:
		return t.c.PTZLaser(ctx, ch, on)
	}
	return fmt.Errorf("%w: tiandy: no %q", ErrPTZ, action)
}

func (t tiandyPTZ) allStop(ctx context.Context, ch int) error {
	zero := 0
	err := t.c.PTZContinuous(ctx, ch, &zero, &zero, &zero)
	if ferr := t.c.PTZFocus(ctx, ch, 0); err == nil {
		err = ferr
	}
	return err
}

func (tiandyPTZ) close() {}
