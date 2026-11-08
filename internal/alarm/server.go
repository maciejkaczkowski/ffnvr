// Package alarm is the alarm server of ffnvr (ffnvr alarm, ffnvr-alarm.service): it
// takes the events of the recorders and keeps them in the event database.
//
//	alarm-dvrip  (/ip/service) XMeye recorders connect and push their alarms (TCP 15002)
//	alarm-smtp   (/ip/service) recorders and cameras send e-mails
//	alarm-lapi   (/ip/service) Uniview cameras subscribed to push their events (HTTP)
//	events=subscribe (/nvr)    ffnvr connects to the recorder: Dahua eventManager,
//	                           DVRIP guard, Tiandy native protocol (port 3001),
//	                           Uniview LAPI (the recorder pushes to alarm-lapi)
//	events=subscribe (/camera) ffnvr connects to a camera of a local recorder: Tiandy
//	                           native protocol (port 3001), Dahua eventManager (HTTP),
//	                           Hikvision ISAPI alertStream (HTTP), Uniview LAPI
//	                           (the camera pushes to alarm-lapi: uniview.go)
//
// One process writes the database (eventdb.Writer). Every recorder subscribed to has
// its own client, its own authentication state and its own goroutine: a recorder that
// fails (wrong password, gone) touches none of the others.
//
// A pushed notice is matched to a recorder of the configuration by, in this order:
// event-id= of /nvr (serial number, SMTP login or sender), the serial number the worker
// read from the recorder (status), the address it claims (DVRIP Address), the address
// it connects from - and, after the recorders, to a camera of a local recorder by these
// addresses (the address of its protocol: ptz-host=, host=, the host of its stream
// address): its events are of its recorder, named by the camera. A notice of nobody known is kept too, with the sender, so that it
// can be given an event-id.
//
// Which kinds are kept is decided after the match: event-filter= of the recorder or of
// the camera subscribed to (/event/filter), else default-filter= of /event (also for
// unknown senders).
//
// A Tiandy recorder or camera (the same protocol) sends records of a tree, IP\SYSTEM\DESCRIPTION (internal/tiandy): the
// kind is the description, the text the record with \ for its tabs, the raw notice its
// JSON. Its parameter readouts (PARAGET) and the burst after the login are its state,
// not news: they are not kept. A kind of record that comes after the burst is written to
// the log the first time, as JSON (what the cameras send can be learned so).
package alarm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/dahua"
	"ffnvr/internal/dvrip"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/hikvision"
	"ffnvr/internal/modules"
	"ffnvr/internal/tiandy"
)

// Options of Run.
type Options struct {
	Load func() (*config.Store, error)
	Root string
	// listen addresses ("" off) and who may connect (nil: everyone)
	DVRIP, SMTP, LAPI                string
	DVRIPAllow, SMTPAllow, LAPIAllow []netip.Prefix
	Logf                             func(string, ...any)
	// Reload: how often the configuration is read again (0: 30 s).
	Reload time.Duration
	// Heartbeat of Dahua subscriptions, the renewal of Uniview ones (0: 20 s).
	Heartbeat time.Duration
}

type server struct {
	o    Options
	db   *eventdb.DB
	w    *eventdb.Writer
	mu   sync.RWMutex
	dir  *directory
	subs map[string]*sub
	lapi lapiPushes // Uniview: the subscriptions awaiting pushes

	stMu    sync.Mutex
	st      modules.AlarmState
	stDirty bool
	dns     map[string][]string
}

// Run serves until ctx ends.
func Run(ctx context.Context, o Options) error {
	if o.Reload <= 0 {
		o.Reload = 30 * time.Second
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = 20 * time.Second
	}
	db, err := eventdb.Open(eventdb.Path(o.Root))
	if err != nil {
		return err
	}
	defer db.Close()
	s := &server{o: o, db: db, subs: map[string]*sub{}, dns: map[string][]string{},
		st: modules.AlarmState{Started: time.Now(), Listeners: map[string]string{}, Subs: map[string]modules.SubscribeState{}, Unknown: map[string]time.Time{}}}
	s.w = eventdb.NewWriter(db, eventdb.PicturesPath(o.Root), o.Logf)
	if o.LAPI != "" {
		s.lapi.ready = make(chan struct{})
	}
	if err := s.reload(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	wctx, stopWriter := context.WithCancel(context.Background()) // the writer ends last
	wg.Add(1)
	go func() { defer wg.Done(); s.w.Run(wctx) }()

	lctx, stopListeners := context.WithCancel(ctx)
	if o.DVRIP != "" {
		s.listen(lctx, &wg, "alarm-dvrip", o.DVRIP, func(ln net.Listener) error { return s.serveDVRIP(lctx, ln) })
	}
	if o.SMTP != "" {
		s.listen(lctx, &wg, "alarm-smtp", o.SMTP, func(ln net.Listener) error { return s.serveSMTP(lctx, ln) })
	}
	if o.LAPI != "" {
		s.listen(lctx, &wg, "alarm-lapi", o.LAPI, func(ln net.Listener) error { return s.serveLAPI(lctx, ln) })
	}
	o.Logf("alarm server: started (dvrip %q, smtp %q, lapi %q)", o.DVRIP, o.SMTP, o.LAPI)
	s.syslog(eventdb.Entry{Kind: modules.SysAlarmServer, Text: fmt.Sprintf("started: dvrip %s, smtp %s, lapi %s, %d subscribed", or(o.DVRIP, "off"), or(o.SMTP, "off"), or(o.LAPI, "off"), len(s.subs))})

	reload := time.NewTicker(o.Reload)
	defer reload.Stop()
	state := time.NewTicker(time.Minute)
	defer state.Stop()
	purge := time.NewTimer(time.Minute)
	defer purge.Stop()
	s.writeState()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-reload.C:
			if err := s.reload(ctx); err != nil {
				o.Logf("configuration: %v", err)
			}
		case <-state.C:
			s.writeState()
		case <-purge.C:
			s.purge(ctx)
			purge.Reset(time.Hour)
		}
	}
	stopListeners()
	s.mu.Lock()
	for _, sb := range s.subs {
		sb.cancel()
	}
	s.mu.Unlock()
	for _, sb := range s.subs {
		<-sb.done
	}
	stopWriter()
	wg.Wait()
	s.syslog(eventdb.Entry{Kind: modules.SysAlarmServer, Text: "stopped"})
	s.writeState()
	return nil
}

// syslog writes entries of the system log (in the database the server keeps open).
func (s *server) syslog(entries ...eventdb.Entry) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.db.Log(ctx, entries...); err != nil {
		s.o.Logf("system log: %v", err)
	}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *server) listen(ctx context.Context, wg *sync.WaitGroup, name, addr string, serve func(net.Listener) error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.o.Logf("%s: %v", name, err)
		s.setListener(name, "error: "+err.Error())
		if name == "alarm-lapi" {
			s.lapi.setErr(err.Error())
		}
		return
	}
	s.setListener(name, ln.Addr().String())
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := serve(ln); err != nil && ctx.Err() == nil {
			s.o.Logf("%s: %v", name, err)
			s.setListener(name, "error: "+err.Error())
		}
	}()
}

// ---------------------------------------------------------------- the recorders

type nvrEntry struct {
	name    string
	ips     map[string]bool
	ids     map[string]bool // event-id and serial number, lower case
	cams    map[int]string  // channel: camera name
	filter  *modules.EventFilter
	filters map[string]*modules.EventFilter // camera: its own (events=subscribe of /camera)
}

type directory struct {
	nvrs     []*nvrEntry // the remote recorders: matched to senders
	locals   []*nvrEntry // the local ones: their cameras send or are subscribed to
	cams     []*camEntry // the cameras of the local ones: matched to senders after nvrs
	settings modules.EventSettings
}

// camEntry: a camera of a local recorder, matched to senders by its addresses.
type camEntry struct {
	nvr, camera string
	ips         map[string]bool
}

// entry is the recorder of that name (nil: none).
func (d *directory) entry(name string) *nvrEntry {
	for _, list := range [][]*nvrEntry{d.nvrs, d.locals} {
		for _, n := range list {
			if n.name == name {
				return n
			}
		}
	}
	return nil
}

// filter of the events of a recorder (name "": an unknown sender), or of a camera of it
// subscribed to with a filter of its own.
func (d *directory) filter(name, camera string) *modules.EventFilter {
	e := d.entry(name)
	if e != nil && camera != "" && e.filters[camera] != nil {
		return e.filters[camera]
	}
	if e != nil && e.filter != nil {
		return e.filter
	}
	return d.settings.Default
}

// matchCamera finds the camera of a local recorder that sent: by its addresses (nil:
// none). Asked after match: a recorder's own address wins.
func (d *directory) matchCamera(ips []string) *camEntry {
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		for _, c := range d.cams {
			if c.ips[ip] {
				return c
			}
		}
	}
	return nil
}

// match finds the recorder of a sender: ids first (they are certain), then addresses.
func (d *directory) match(ids, ips []string) *nvrEntry {
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		for _, n := range d.nvrs {
			if n.ids[id] {
				return n
			}
		}
	}
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		for _, n := range d.nvrs {
			if n.ips[ip] {
				return n
			}
		}
	}
	return nil
}

// hostOnly: the host of host= of /nvr ("192.168.1.2:8080", "https://nvr.lan/").
func hostOnly(h string) string {
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil {
			return u.Hostname()
		}
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.Trim(h, "[]")
}

func (s *server) resolve(ctx context.Context, host string) []string {
	if host == "" {
		return nil
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return []string{a.Unmap().String()}
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(lctx, host)
	if err != nil {
		return s.dns[host] // keep what we knew
	}
	s.dns[host] = addrs
	return addrs
}

func (s *server) reload(ctx context.Context) error {
	store, err := s.o.Load()
	if err != nil {
		return err
	}
	d := &directory{settings: modules.EventConfig(store)}
	byName := map[string]*nvrEntry{}
	for _, n := range store.Items("/nvr") {
		e := &nvrEntry{name: n.Val("name"), ips: map[string]bool{}, ids: map[string]bool{}, cams: map[int]string{},
			filter: d.settings.FilterFor(n.Val("event-filter")), filters: map[string]*modules.EventFilter{}}
		byName[e.name] = e
		if n.Val("type") == "local" { // nobody sends for it: only its cameras subscribed to
			d.locals = append(d.locals, e)
			continue
		}
		for _, ip := range s.resolve(ctx, hostOnly(n.Val("host"))) {
			e.ips[ip] = true
		}
		if id := strings.ToLower(n.Val("event-id")); id != "" {
			e.ids[id] = true
		}
		if st := modules.ReadStatus(s.o.Root, e.name); st != nil && st.Device.Serial != "" {
			e.ids[strings.ToLower(st.Device.Serial)] = true
		}
		d.nvrs = append(d.nvrs, e)
	}
	for _, c := range modules.LocalCameras(store) {
		e := byName[c.Nvr]
		if e == nil {
			continue
		}
		e.filters[c.Camera] = d.settings.FilterFor(c.Filter)
		ce := &camEntry{nvr: c.Nvr, camera: c.Camera, ips: map[string]bool{}}
		for _, ip := range s.resolve(ctx, hostOnly(c.Host)) {
			ce.ips[ip] = true
		}
		d.cams = append(d.cams, ce)
	}
	for _, c := range store.Items("/camera") {
		if e := byName[c.Val("nvr")]; e != nil {
			var ch int
			if _, err := fmt.Sscan(c.Val("channel"), &ch); err == nil && ch > 0 {
				e.cams[ch] = c.Val("name")
			}
		}
	}
	s.w.SetGap(d.settings.Gap)
	s.mu.Lock()
	s.dir = d
	s.mu.Unlock()
	s.reconcile(ctx, store)
	return nil
}

func (s *server) directory() *directory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dir
}

// notice completes a notice (recorder, camera) and queues it.
func (s *server) notice(n eventdb.Notice, ids, ips []string, sender string) {
	d := s.directory()
	if n.Nvr == "" {
		if e := d.match(ids, ips); e != nil {
			n.Nvr = e.name
		} else if c := d.matchCamera(ips); c != nil {
			n.Nvr, n.Camera = c.nvr, c.camera
		}
	}
	// filtered once the recorder is known: every recorder has its own filter
	if !d.filter(n.Nvr, n.Camera).Keeps(n.Kind) {
		return
	}
	if n.Nvr == "" {
		n.Device = sender
		s.stMu.Lock()
		if _, known := s.st.Unknown[sender]; !known {
			s.o.Logf("events from an unknown sender %s (%s): give the recorder event-id= or host= (a camera of a local recorder: host=)", sender, n.Source)
		}
		if len(s.st.Unknown) < 200 {
			s.st.Unknown[sender] = time.Now()
		}
		s.stMu.Unlock()
	} else {
		for _, e := range d.nvrs {
			if e.name == n.Nvr && n.Camera == "" {
				n.Camera = e.cams[n.Channel]
			}
		}
	}
	s.w.Add(n)
}

// ---------------------------------------------------------------- push: DVRIP

func allowed(list []netip.Prefix, a net.Addr) bool {
	if list == nil {
		return true
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	for _, p := range list {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *server) serveDVRIP(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	slots := make(chan struct{}, 512) // a flood of connections must not exhaust memory
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if !allowed(s.o.DVRIPAllow, c.RemoteAddr()) {
			c.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		go func() {
			defer func() { <-slots; c.Close() }()
			remote := hostOf(c.RemoteAddr())
			dvrip.ReadPushed(c, 30*time.Second, func(a dvrip.Alarm, err error) {
				if err != nil {
					s.o.Logf("alarm-dvrip %s: %v", remote, err)
					return
				}
				s.notice(dvripNotice(a), []string{a.Serial}, []string{a.Address, remote}, dvripSender(a, remote))
			})
		}()
	}
}

func dvripNotice(a dvrip.Alarm) eventdb.Notice {
	st := eventdb.Pulse
	if a.Type != "Log" {
		switch strings.ToLower(a.Status) {
		case "start":
			st = eventdb.Start
		case "stop":
			st = eventdb.Stop
		}
	}
	return eventdb.Notice{Channel: a.Channel, Kind: a.Event, Source: "dvrip", State: st, Text: a.Descrip, Raw: a.Raw}
}

func dvripSender(a dvrip.Alarm, remote string) string {
	if a.Serial != "" {
		return "serial " + a.Serial + " at " + remote
	}
	return remote
}

// ---------------------------------------------------------------- push: SMTP

func (s *server) serveSMTP(ctx context.Context, ln net.Listener) error {
	tc, err := selfSigned("ffnvr")
	if err != nil {
		s.o.Logf("alarm-smtp: no STARTTLS: %v", err)
	}
	srv := &SMTPServer{Hostname: "ffnvr", TLS: tc, Logf: s.o.Logf,
		Accept: func(a net.Addr) bool { return allowed(s.o.SMTPAllow, a) },
		Handle: func(m Mail) {
			text := m.Subject
			if m.Text != "" && !strings.Contains(m.Subject, m.Text) {
				text = strings.TrimSpace(m.Subject + " | " + m.Text)
			}
			n := eventdb.Notice{Channel: MailChannel(m.Subject, m.Text), Kind: MailKind(m.Subject, m.Text), Source: "smtp",
				State: eventdb.Pulse, Text: clip(text), Raw: m.Raw, Picture: m.Picture}
			sender := m.From
			if m.User != "" {
				sender = "login " + m.User
			}
			s.notice(n, []string{m.User, m.From}, []string{m.Remote}, sender+" at "+m.Remote)
		}}
	return srv.Serve(ctx, ln)
}

// ---------------------------------------------------------------- subscriptions

type sub struct {
	sig    string
	cancel func()
	done   chan struct{}
}

// target is what is subscribed to: a recorder (/nvr) or a camera of a local one
// (/camera); key names it in the state ("nvr" or "nvr/camera").
type target struct {
	key, nvr, camera      string
	typ, host, user, pass string
}

func (t target) sig() string {
	return strings.Join([]string{t.typ, t.host, t.user, t.pass}, "\x00")
}

// what tells who it is in the log and the system log
func (t target) what() string {
	if t.camera != "" {
		return "camera " + t.camera
	}
	return "the recorder"
}

// targets: the subscriptions the configuration wants.
func targets(store *config.Store) map[string]target {
	want := map[string]target{}
	for _, n := range store.Items("/nvr") {
		if n.Val("events") == "subscribe" && n.Val("disabled") != "yes" && modules.NvrSubscribeTypes[n.Val("type")] {
			want[n.Val("name")] = target{key: n.Val("name"), nvr: n.Val("name"), typ: n.Val("type"),
				host: n.Val("host"), user: n.Val("username"), pass: n.Val("password")}
		}
	}
	for _, c := range modules.SubscribedCameras(store) {
		k := c.Nvr + "/" + c.Camera
		want[k] = target{key: k, nvr: c.Nvr, camera: c.Camera, typ: c.Type, host: c.Host, user: c.Username, pass: c.Password}
	}
	return want
}

// reconcile starts the subscriptions the configuration wants and stops the others; a
// changed recorder or camera (address, password) is subscribed to anew.
func (s *server) reconcile(ctx context.Context, store *config.Store) {
	want := targets(store)
	// stopped without the lock held: a subscription still running reads the directory
	var stale []*sub
	s.mu.Lock()
	for key, sb := range s.subs {
		if t, ok := want[key]; !ok || t.sig() != sb.sig {
			stale = append(stale, sb)
			delete(s.subs, key)
			s.stMu.Lock()
			delete(s.st.Subs, key)
			s.stMu.Unlock()
		}
	}
	s.mu.Unlock()
	for _, sb := range stale {
		sb.cancel()
		<-sb.done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, t := range want {
		if s.subs[key] != nil {
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		sb := &sub{sig: t.sig(), cancel: cancel, done: make(chan struct{})}
		s.subs[key] = sb
		go s.subscribe(sctx, sb, t)
	}
}

func (s *server) setSub(name string, fn func(*modules.SubscribeState)) {
	s.stMu.Lock()
	st := s.st.Subs[name]
	fn(&st)
	s.st.Subs[name] = st
	s.stDirty = true
	s.stMu.Unlock()
}

func (s *server) setListener(name, v string) {
	s.stMu.Lock()
	s.st.Listeners[name] = v
	s.stDirty = true
	s.stMu.Unlock()
}

// subscribe keeps one recorder or camera subscribed until ctx ends, reconnecting after
// failures (5 s, doubling up to 2 min).
func (s *server) subscribe(ctx context.Context, sb *sub, t target) {
	defer close(sb.done)
	wait := 5 * time.Second
	lastErr := ""
	seen := map[string]bool{} // tiandy, hikvision: the kinds logged already
	for ctx.Err() == nil {
		began := time.Now()
		connected := func() {
			s.setSub(t.key, func(st *modules.SubscribeState) { st.Connected, st.Since, st.Error = true, time.Now(), "" })
			s.o.Logf("%s: subscribed to events", t.key)
			s.syslog(eventdb.Entry{Nvr: t.nvr, Kind: modules.SysEventsConnection, Text: "subscribed to the events of " + t.what() + " (" + t.typ + ")"})
			lastErr = ""
		}
		event := func() { s.setSub(t.key, func(st *modules.SubscribeState) { st.LastEvent = time.Now() }) }
		var err error
		switch t.typ {
		case "dahua":
			var c *dahua.Client
			if c, err = dahua.NewClient(t.host, t.user, t.pass, true); err == nil {
				err = c.Events(ctx, nil, s.o.Heartbeat, connected, func(e dahua.Event) {
					event()
					s.notice(dahuaNotice(t, e), nil, nil, t.nvr)
				})
			}
		case "dvrip":
			var c *dvrip.Client
			if c, err = dvrip.NewClient(t.host, t.user, t.pass); err == nil {
				err = c.Alarms(ctx, connected, func(a dvrip.Alarm) {
					event()
					n := dvripNotice(a)
					n.Nvr = t.nvr
					s.notice(n, nil, nil, t.nvr)
				})
			}
		case "tiandy":
			var c *tiandy.Client
			if c, err = tiandy.NewClient(t.host, t.user, t.pass); err == nil {
				err = c.Events(ctx, connected, func(e tiandy.Event) {
					if !e.Alarm && !e.Burst && !seen[e.Kind()] && len(seen) < 1000 { // what comes later, once a kind
						seen[e.Kind()] = true
						s.o.Logf("%s: tiandy record %s: %s", t.key, e.Kind(), clipLog(e.JSON()))
					}
					n, ok := tiandyNotice(t, e)
					if !ok {
						return
					}
					event()
					s.notice(n, nil, nil, t.nvr)
				})
			}
		case "uniview":
			err = s.subscribeUniview(ctx, t, connected, event)
		case "hikvision":
			var c *hikvision.Client
			if c, err = hikvision.NewClient(t.host, t.user, t.pass); err == nil {
				err = c.Events(ctx, 0, connected, func(e hikvision.Event) {
					if e.Start && !seen[e.Type] && len(seen) < 1000 { // what the camera sends, once a kind
						seen[e.Type] = true
						body := e.Raw // without the root element's namespace: the log line is short
						if i := strings.IndexByte(body, '>'); i >= 0 {
							body = strings.TrimSuffix(body[i+1:], "</EventNotificationAlert>")
						}
						s.o.Logf("%s: hikvision alert %s: %s", t.key, e.Type, clipLog(body))
					}
					event()
					s.notice(hikvisionNotice(t, e), nil, nil, t.nvr)
				})
			}
		default:
			err = fmt.Errorf("type=%s cannot be subscribed to", t.typ)
		}
		if t.camera != "" {
			s.w.LostCamera(t.nvr, t.camera) // nobody will send the Stop of what was going
		} else {
			s.w.Lost(t.nvr)
		}
		if ctx.Err() != nil {
			return
		}
		msg := "connection ended"
		if err != nil {
			msg = err.Error()
		}
		s.setSub(t.key, func(st *modules.SubscribeState) {
			if st.Connected || st.Since.IsZero() {
				st.Since = time.Now()
			}
			st.Connected, st.Error = false, msg
		})
		if msg != lastErr { // one line per kind of failure, not one a minute
			s.o.Logf("%s: events: %s", t.key, msg)
			s.syslog(eventdb.Entry{Nvr: t.nvr, Kind: modules.SysEventsConnection, Text: "no events of " + t.what() + ": " + msg})
			lastErr = msg
		}
		if time.Since(began) > 2*time.Minute {
			wait = 5 * time.Second // it worked for a while: try again soon
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, 2*time.Minute)
	}
}

// tiandyNotice: an event of a Tiandy recorder or camera as a notice; false for what is
// not kept (its state: the burst after the login, parameter readouts). The channel of a
// recorder's record (from 1) gives the camera (/camera channel=, in notice); a camera
// subscribed to by itself is named by the target.
func tiandyNotice(t target, e tiandy.Event) (eventdb.Notice, bool) {
	if !e.Alarm && (e.Burst || e.Param()) {
		return eventdb.Notice{}, false
	}
	st := eventdb.Pulse
	if e.Alarm {
		st = eventdb.Stop
		if e.Active {
			st = eventdb.Start
		}
	}
	return eventdb.Notice{Nvr: t.nvr, Camera: t.camera, Channel: e.Channel, Kind: e.Kind(), Source: "tiandy",
		State: st, Time: e.Time, Text: clip(e.String()), Raw: e.JSON()}, true
}

// clipLog: a line of the log is not endless (a log readout of a device is long).
func clipLog(s string) string {
	if len(s) > 400 {
		return strings.ToValidUTF8(s[:400], "") + "…"
	}
	return s
}

// dahuaNotice: an event of a Dahua recorder or camera (the same eventManager). The
// channel of a recorder's event (index from 0) gives the camera (/camera channel=, in
// notice); a camera subscribed to by itself is named by the target, its channel stays
// as it came (1: the camera itself).
func dahuaNotice(t target, e dahua.Event) eventdb.Notice {
	st := eventdb.Pulse
	switch strings.ToLower(e.Action) {
	case "start":
		st = eventdb.Start
	case "stop":
		st = eventdb.Stop
	}
	ch := e.Channel()
	return eventdb.Notice{Nvr: t.nvr, Camera: t.camera, Channel: ch, Kind: e.Code, Source: "dahua", State: st, Text: dataSummary(e.Data), Raw: e.Raw}
}

// hikvisionNotice: the beginning or the end of an event of a Hikvision camera (the
// alerts folded by hikvision.Tracker). The time is this server's: when it began, for an
// end its last alert. Its channel stays as it came (1: the camera itself).
func hikvisionNotice(t target, e hikvision.Event) eventdb.Notice {
	st := eventdb.Stop
	if e.Start {
		st = eventdb.Start
	}
	return eventdb.Notice{Nvr: t.nvr, Camera: t.camera, Channel: e.Channel, Kind: e.Type, Source: "hikvision",
		State: st, Time: e.At, Text: clip(e.Text()), Raw: e.Raw}
}

// dataSummary: the start of the data= JSON on one line, enough for the list.
func dataSummary(data string) string {
	if data == "" {
		return ""
	}
	d := clip(data)
	if len(d) > 200 {
		d = strings.ToValidUTF8(d[:200], "") // the cut may split a character
	}
	return d
}

// ---------------------------------------------------------------- upkeep

func (s *server) purge(ctx context.Context) {
	ret := s.directory().settings.Retention
	n, err := s.db.Purge(ctx, time.Now().Add(-ret), eventdb.PicturesPath(s.o.Root))
	if err != nil {
		s.o.Logf("events: retention: %v", err)
	}
	if n > 0 {
		eventdb.RemoveEmptyDirs(eventdb.PicturesPath(s.o.Root))
		s.o.Logf("events: %d older than %s deleted", n, ret)
		s.syslog(eventdb.Entry{Kind: modules.SysEventsPurged, Text: fmt.Sprintf("%d events older than %s deleted (retention of /event)", n, ret)})
	}
}

func (s *server) writeState() {
	s.stMu.Lock()
	s.st.Updated = time.Now()
	for k, t := range s.st.Unknown {
		if time.Since(t) > 24*time.Hour {
			delete(s.st.Unknown, k)
		}
	}
	b := s.st // the maps are only read while the lock is held
	err := modules.WriteAlarmState(s.o.Root, &b)
	s.stDirty = false
	s.stMu.Unlock()
	if err != nil {
		s.o.Logf("state: %v", err)
	}
}
