package alarm

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/eventdb"
	"ffnvr/internal/uniview"
)

// Uniview (LAPI) pushes its events: the subscription (uniview.Client.Events) gives the
// recorder or camera this server's address and the port of alarm-lapi (/ip/service),
// and the device POSTs every event there. A push is matched to its subscription by the
// address it comes from; the subscription named by its Reference only chooses between
// devices behind one address, it cannot be trusted further (an IPC got ID 65280 and
// pushes with Subscribers/0). A push of nobody subscribed to is matched like the other
// pushed notices: a recorder, else a camera of a local recorder by its address, else an
// unknown sender.
//
// The devices speak HTTP their own way, so the server is not net/http's: a push comes
// without Host (HTTP/1.1 requires it, net/http answers 400), as text/plain with the body
// Json={...}, and the device closes its side right after the request.
//
// A recorder's push names its channel (AlarmSrcID, uniview.Notification.RecorderChannel):
// the camera is the one of that channel= (/camera); a camera's own pushes are channel 1.
//
// What is kept: the alarms (Notification/Alarm: motion, masking, audio, inputs, line
// crossing, intrusion...). Pushes of data (structured objects, people counting, ...)
// only when a filter is set for the device (event-filter= of /camera or /nvr, else
// default-filter= of /event): with action=accept kinds=* everything is kept.

// lapiReg is one recorder or camera subscribed to whose pushes are awaited.
type lapiReg struct {
	t  target
	id int64
	// event: a push of it came (the state of the subscription)
	event func()
	mu    sync.Mutex
	seen  map[string]bool // the kinds logged already
}

// first: the kind is new for the log (once a kind, pushes come in parallel).
func (r *lapiReg) first(kind string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[kind] || len(r.seen) >= 1000 {
		return false
	}
	r.seen[kind] = true
	return true
}

// lapiPushes: the subscriptions by the address of the device.
type lapiPushes struct {
	mu   sync.Mutex
	byIP map[string][]*lapiReg
	port int // of the listener, 0 while it is not listening
	err  string
	// ready is closed once the listener listens or failed (nil: alarm-lapi is off): the
	// first subscriptions are made before it is opened
	ready chan struct{}
	once  sync.Once
}

// settled: the listener listens or failed.
func (l *lapiPushes) settled() {
	if l.ready != nil {
		l.once.Do(func() { close(l.ready) })
	}
}

func (l *lapiPushes) add(ips []string, r *lapiReg) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byIP == nil {
		l.byIP = map[string][]*lapiReg{}
	}
	for _, ip := range ips {
		l.byIP[ip] = append(l.byIP[ip], r)
	}
}

func (l *lapiPushes) remove(r *lapiReg) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, list := range l.byIP {
		out := list[:0]
		for _, x := range list {
			if x != r {
				out = append(out, x)
			}
		}
		if len(out) == 0 {
			delete(l.byIP, ip)
		} else {
			l.byIP[ip] = out
		}
	}
}

func (l *lapiPushes) setErr(e string) {
	l.mu.Lock()
	l.err = e
	l.mu.Unlock()
	l.settled()
}

// find the subscription of a push from ip: the one its Reference names, else the
// first of that address (nil: none).
func (l *lapiPushes) find(ip, ref string) *lapiReg {
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.byIP[ip]
	if len(list) == 0 {
		return nil
	}
	if id, ok := uniview.ReferenceID(ref); ok {
		for _, x := range list {
			if x.id == id {
				return x
			}
		}
	}
	return list[0]
}

// lapiListening: the port pushes come to; an error when alarm-lapi is off or failed.
func (s *server) lapiListening(ctx context.Context) (int, error) {
	if s.lapi.ready != nil {
		select {
		case <-s.lapi.ready:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	s.lapi.mu.Lock()
	defer s.lapi.mu.Unlock()
	if s.lapi.port == 0 {
		if s.lapi.err != "" {
			return 0, fmt.Errorf("alarm-lapi of /ip/service does not listen (%s): the device has nowhere to push its events to", s.lapi.err)
		}
		return 0, errors.New("alarm-lapi of /ip/service is not enabled: the device has nowhere to push its events to")
	}
	return s.lapi.port, nil
}

// subscribeUniview keeps one recorder or camera subscribed (one connection's worth: it returns when
// the subscription is lost).
func (s *server) subscribeUniview(ctx context.Context, t target, connected, event func()) error {
	port, err := s.lapiListening(ctx)
	if err != nil {
		return err
	}
	c, err := uniview.NewClient(t.host, t.user, t.pass, true)
	if err != nil {
		return err
	}
	ips := s.targetIPs(ctx, t, c.Host())
	if len(ips) == 0 {
		return fmt.Errorf("the address %q is unknown", c.Host())
	}
	reg := &lapiReg{t: t, event: event, seen: map[string]bool{}}
	defer s.lapi.remove(reg)
	return c.Events(ctx, "", port, s.o.Heartbeat, func(sub uniview.Subscription) {
		reg.id = sub.ID
		s.lapi.add(ips, reg)
		s.o.Logf("%s: %s pushes to %s (subscription %d)", t.key, t.what(), sub.Address, sub.ID)
		connected()
	})
}

// targetIPs: the addresses the pushes of a recorder or camera come from - as the
// directory knows it, else looked up now.
func (s *server) targetIPs(ctx context.Context, t target, host string) []string {
	var out []string
	d := s.directory()
	if t.camera == "" {
		if e := d.entry(t.nvr); e != nil {
			for ip := range e.ips {
				out = append(out, ip)
			}
		}
	}
	for _, c := range d.cams {
		if t.camera != "" && c.nvr == t.nvr && c.camera == t.camera {
			for ip := range c.ips {
				out = append(out, ip)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if a, err := netipAddr(host); err == nil {
		return []string{a}
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, _ := net.DefaultResolver.LookupHost(lctx, host)
	for _, a := range addrs {
		if n, err := netipAddr(a); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// serveLAPI takes the pushes of Uniview devices.
func (s *server) serveLAPI(ctx context.Context, ln net.Listener) error {
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscan(p, &port)
	s.lapi.mu.Lock()
	s.lapi.port, s.lapi.err = port, ""
	s.lapi.mu.Unlock()
	s.lapi.settled()
	defer func() {
		s.lapi.mu.Lock()
		s.lapi.port = 0
		s.lapi.mu.Unlock()
	}()
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveLAPIConn(conn)
		}()
	}
}

// serveLAPIConn answers the requests of one connection (one, as the devices send them:
// Connection: close; more when they keep it alive).
func (s *server) serveLAPIConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	for {
		conn.SetDeadline(time.Now().Add(60 * time.Second))
		// http.ReadRequest, unlike http.Server, takes a request without Host
		req, err := http.ReadRequest(br)
		if err != nil {
			if err != io.EOF && !errors.Is(err, net.ErrClosed) && !isTimeout(err) {
				s.o.Logf("alarm-lapi %s: %v", conn.RemoteAddr(), err)
			}
			return
		}
		req.RemoteAddr = conn.RemoteAddr().String()
		w := &lapiResponse{h: http.Header{}, code: http.StatusOK}
		s.handleLAPI(w, req)
		io.Copy(io.Discard, io.LimitReader(req.Body, 16<<20))
		req.Body.Close()
		keep := !req.Close && req.ProtoAtLeast(1, 1)
		w.h.Set("Content-Length", strconv.Itoa(w.body.Len()))
		if w.h.Get("Content-Type") == "" {
			w.h.Set("Content-Type", "text/plain; charset=utf-8")
		}
		if !keep {
			w.h.Set("Connection", "close")
		}
		var b bytes.Buffer
		fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", w.code, http.StatusText(w.code))
		w.h.Write(&b)
		b.WriteString("\r\n")
		b.Write(w.body.Bytes())
		// the device may have closed its side already (FIN after the request): the
		// answer is written anyway, what it does not read is lost
		if _, err := conn.Write(b.Bytes()); err != nil || !keep {
			return
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// lapiResponse: the answer to a push, written by serveLAPIConn.
type lapiResponse struct {
	h    http.Header
	code int
	body bytes.Buffer
}

func (r *lapiResponse) Header() http.Header         { return r.h }
func (r *lapiResponse) WriteHeader(code int)        { r.code = code }
func (r *lapiResponse) Write(b []byte) (int, error) { return r.body.Write(b) }

func (s *server) handleLAPI(w http.ResponseWriter, r *http.Request) {
	if !allowed(s.o.LAPIAllow, addrOf(r.RemoteAddr)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "only POST", http.StatusMethodNotAllowed)
		return
	}
	// a push may carry pictures (base64, up to 3 MB each)
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return
	}
	remote := hostOnly(r.RemoteAddr)
	if a, err := netipAddr(remote); err == nil {
		remote = a
	}
	n, err := uniview.ParseNotification(r.URL.Path, body)
	if err != nil {
		s.o.Logf("alarm-lapi %s %s: %v", remote, r.URL.Path, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(uniview.NotificationAnswer(r.URL.Path))

	reg := s.lapi.find(remote, n.Reference)
	if reg == nil {
		s.lapiNotice(target{}, n, remote)
		return
	}
	t := reg.t
	if n.State() != "stop" && reg.first(n.Path+" "+n.Type) { // what the camera sends, once a kind
		s.o.Logf("%s: uniview %s: %s", t.key, or(n.Type, n.Path), clipLog(n.Raw))
	}
	if s.lapiNotice(t, n, remote) {
		reg.event()
	}
}

// lapiNotice queues a push; t is the recorder or camera subscribed to (zero: none,
// matched by the address remote). False when it is not kept as a push of data without
// a filter.
func (s *server) lapiNotice(t target, n uniview.Notification, remote string) bool {
	d := s.directory()
	nvr, camera, recorder := t.nvr, t.camera, t.nvr != "" && t.camera == ""
	if nvr == "" {
		if e := d.match(nil, []string{remote}); e != nil {
			nvr, recorder = e.name, true
		} else if c := d.matchCamera([]string{remote}); c != nil {
			nvr, camera = c.nvr, c.camera
		}
	}
	ch := 1 // a camera itself
	if recorder {
		ch = n.RecorderChannel()
	}
	if !n.Alarm() && d.filter(nvr, camera).Name == "" {
		return false // data, and no filter of the user's asks for it
	}
	st := eventdb.Pulse
	switch n.State() {
	case "start":
		st = eventdb.Start
	case "stop":
		st = eventdb.Stop
	}
	// the time is this server's: the camera's clock may be off, and the alarm mode of the
	// schedule cuts the recordings by it
	s.notice(eventdb.Notice{Nvr: nvr, Camera: camera, Channel: ch, Kind: n.Kind(), Source: "uniview",
		State: st, Text: clip(n.Text()), Raw: n.Raw, Picture: n.Picture}, nil, []string{remote}, remote)
	return true
}

// addrOf: a net.Addr of "host:port" (for allowed).
type tcpAddrString string

func (a tcpAddrString) Network() string { return "tcp" }
func (a tcpAddrString) String() string  { return string(a) }

func addrOf(s string) net.Addr { return tcpAddrString(s) }

// netipAddr: an address without its zone and IPv4 not mapped into IPv6.
func netipAddr(h string) (string, error) {
	ip := net.ParseIP(strings.Trim(h, "[]"))
	if ip == nil {
		return "", fmt.Errorf("not an address: %q", h)
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String(), nil
	}
	return ip.String(), nil
}
