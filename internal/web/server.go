// Package web is the web panel (ffnvr web, run by ffnvr-www.service): sign in with
// the users of /user, then the recorders the user may see (valid-users of /nvr).
// The configuration is read from its file on every request, so changes saved by the
// shell apply at once; the panel itself does not change it.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
	"ffnvr/internal/profile"
	"ffnvr/internal/sysd"
)

// Options of the panel.
type Options struct {
	Load    func() (*config.Store, error) // reads the configuration
	Root    string                        // filesystem root (status files)
	Version string
	Allow   []netip.Prefix // who may connect; empty: everyone
	Proxies []netip.Prefix // reverse proxies in front: their X-Forwarded-For is believed
	Log     *log.Logger
	// Systemd tells the state and logs of the camera services; nil: not shown
	Systemd Systemd
	// Edit changes the configuration file (saved views, camera positions); nil: read only
	Edit func(func(*config.Store) error) error
}

// Systemd is what the panel needs of systemd (sysd.Systemd; a fake in tests).
type Systemd interface {
	Show(ctx context.Context, names ...string) (map[string]sysd.Unit, error)
	Logs(ctx context.Context, name string, lines int) (string, error)
}

const (
	cookieName  = "ffnvr_session"
	idleTimeout = 30 * time.Minute
	maxAge      = 12 * time.Hour
	maxFailures = 5
	blockFor    = time.Minute
	// the last use of a session is written to the profile at most this often
	saveEvery = time.Minute
)

// A session is kept in memory and in the user's profile (profile.Sessions), so that a
// restart of the panel signs nobody out.
type session struct {
	key             string // profile.SessionID of the cookie: the key of Server.sessions
	user, csrf      string
	created, active time.Time
	saved           time.Time // active as last written to the profile
}

func (se *session) expired(now time.Time) bool {
	return now.Sub(se.active) > idleTimeout || now.Sub(se.created) > maxAge
}

// stored: what the profile keeps of a session (s.mu held).
func (se *session) stored() profile.Session {
	return profile.Session{ID: se.key, CSRF: se.csrf, Created: se.created, Active: se.active}
}

type failures struct {
	n           int
	last, until time.Time
}

// Server is the panel's http.Handler.
type Server struct {
	opt      Options
	mux      *http.ServeMux
	mu       sync.Mutex
	sessions map[string]*session // by profile.SessionID of the cookie
	backups  backupRuns
	fw       fwRuns               // firmware updates (by recorder)
	failed   map[string]*failures // by client address
	play     *playCache           // the archive's player
	prof     *profile.Store       // the users' own settings (saved views)
	ptzc     *modules.PTZControl  // sessions of the PTZ control (live.ptz)
	tc       tcSlots              // transcoded live streams being watched
}

func New(opt Options) *Server {
	if opt.Log == nil {
		opt.Log = log.Default()
	}
	s := &Server{opt: opt, mux: http.NewServeMux(), sessions: map[string]*session{}, failed: map[string]*failures{}, play: newPlayCache("ffmpeg"), ptzc: modules.PTZ, prof: profile.New(opt.Root)}
	if s.ptzc.Logf == nil {
		s.ptzc.Logf = opt.Log.Printf
	}
	s.loadSessions()
	s.mux.HandleFunc("GET /login", s.loginPage)
	s.mux.HandleFunc("POST /login", s.login)
	s.mux.HandleFunc("POST /logout", s.logout)
	s.mux.HandleFunc("POST /account/password", s.accountPassword)
	s.mux.HandleFunc("POST /account/start", s.accountStart)
	s.mux.HandleFunc("POST /account/live", s.accountLive)
	s.mux.HandleFunc("POST /account/theme", s.accountTheme)
	s.mux.HandleFunc("GET /lang", s.setLang)
	s.mux.HandleFunc("GET /{$}", s.system)
	s.mux.HandleFunc("POST /tools/reboot", s.rebootTool)
	s.mux.HandleFunc("POST /events/ack", s.eventsAck)
	s.mux.HandleFunc("GET /events/picture", s.eventPicture)
	s.mux.HandleFunc("POST /tools/backup", s.backupTool)
	s.mux.HandleFunc("POST /tools/backup-to", s.backupTo)
	s.mux.HandleFunc("POST /tools/firmware", s.firmwareTool)
	s.mux.HandleFunc("POST /tools/clock", s.clockTool)
	s.mux.HandleFunc("POST /record/save", s.recordSave)
	s.mux.HandleFunc("POST /archive/range", s.archiveRange)
	s.mux.HandleFunc("POST /archive/files", s.archiveFiles)
	s.mux.HandleFunc("GET /archive/file", s.archiveFile)
	s.mux.HandleFunc("GET /archive/thumb", s.archiveThumb)
	s.mux.HandleFunc("GET /archive/motion", s.archiveMotion)
	s.mux.HandleFunc("POST /archive/cancel", s.archiveCancel)
	s.mux.HandleFunc("POST /archive/retry", s.archiveRetry)
	s.mux.HandleFunc("GET /archive/result", s.archiveResult)
	s.mux.HandleFunc("GET /live/", s.liveProxy)
	s.mux.HandleFunc("GET /static/live.js", s.static)
	s.mux.HandleFunc("GET /live/tiles/{z}/{x}/{y}", s.tile)
	s.mux.HandleFunc("GET /live/thumb", s.thumb)
	s.mux.HandleFunc("GET /live/transcode", s.transcodeCheck)
	s.mux.HandleFunc("GET /archive/play", s.archivePlay)
	s.mux.HandleFunc("GET /archive/segments", s.archiveSegments)
	s.mux.HandleFunc("POST /archive/export", s.archiveExport)
	s.mux.HandleFunc("GET /static/playback.js", s.static)
	s.mux.HandleFunc("POST /live/view/save", s.viewSave)
	s.mux.HandleFunc("POST /live/view/delete", s.viewDelete)
	s.mux.HandleFunc("POST /live/positions", s.positionsSave)
	s.mux.HandleFunc("POST /live/ptz", s.ptz)
	s.mux.HandleFunc("POST /live/ptz/swap", s.ptzSwap)
	return s
}

// peerIP: the address of the connection (behind a reverse proxy: the proxy's).
func peerIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

func inPrefixes(ip netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// viaProxy tells whether a request came through a reverse proxy of -proxy.
func (s *Server) viaProxy(r *http.Request) bool { return inPrefixes(peerIP(r), s.opt.Proxies) }

// clientIP: the browser's address. From a reverse proxy of -proxy, the one it names in
// X-Forwarded-For: the last address there that is not a proxy itself (what comes before
// it the browser could have written). From anyone else the header means nothing.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	ip := peerIP(r)
	if !inPrefixes(ip, s.opt.Proxies) {
		return ip
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if a = a.Unmap(); !inPrefixes(a, s.opt.Proxies) {
			return a
		}
	}
	return ip
}

// secure tells whether the browser's connection is encrypted: by the panel itself, or by
// a reverse proxy that says so (X-Forwarded-Proto: https). The cookies are Secure then.
// The header is believed from anyone: a browser that sends it over plain HTTP only
// keeps itself from signing in.
func secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	p, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(p), "https")
}

// allowed: who may connect (address= of /ip/service www): the connection's address,
// behind a reverse proxy the proxy's.
func (s *Server) allowed(r *http.Request) bool {
	if len(s.opt.Allow) == 0 {
		return true
	}
	ip := peerIP(r)
	for _, p := range s.opt.Allow {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	h := w.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

func token() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// lang: the cookie, else the browser's first known language.
func lang(r *http.Request) string {
	if c, err := r.Cookie("lang"); err == nil {
		if _, ok := texts[c.Value]; ok {
			return c.Value
		}
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		code := strings.ToLower(strings.TrimSpace(part))
		if len(code) >= 2 {
			if _, ok := texts[code[:2]]; ok {
				return code[:2]
			}
		}
	}
	return "en"
}

type page struct {
	Brand                       modules.Branding
	Lang, Title, CSRF, Nonce    string
	Theme                       string // the look (see theme.go): the cookie of the last signed in page
	T                           map[string]string
	Languages                   any
	Error, Info, Username, User string
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	p.Lang = lang(r)
	p.T = texts[p.Lang]
	p.Theme = cookieTheme(r)
	p.Brand = modules.Branding{Name: "ffnvr", LoginName: true, LoginLogo: true}
	if st, err := s.opt.Load(); err == nil {
		p.Brand = modules.WebBranding(st)
	}
	p.Languages = languages
	p.Nonce = token()
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+p.Nonce+"'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, p); err != nil {
		s.opt.Log.Printf("web: %s: %v", name, err)
	}
}

// the CSRF token of the sign in form: a cookie compared with the form
func (s *Server) loginCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("ffnvr_csrf"); err == nil && len(c.Value) > 20 {
		return c.Value
	}
	t := token()
	http.SetCookie(w, &http.Cookie{Name: "ffnvr_csrf", Value: t, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r)})
	return t
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.current(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p := page{Title: "Sign in", CSRF: s.loginCSRF(w, r)}
	if r.URL.Query().Get("forgot") != "" {
		p.Info = texts[lang(r)]["forgot_help"]
	}
	s.render(w, r, "login", p)
}

func (s *Server) setLang(w http.ResponseWriter, r *http.Request) {
	l := r.URL.Query().Get("lang")
	if _, ok := texts[l]; ok {
		http.SetCookie(w, &http.Cookie{Name: "lang", Value: l, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r).String()
	t := texts[lang(r)]
	fail := func(msg string, status int) {
		w.WriteHeader(status)
		s.render(w, r, "login", page{Title: "Sign in", CSRF: s.loginCSRF(w, r), Error: msg, Username: r.PostFormValue("username")})
	}
	c, err := r.Cookie("ffnvr_csrf")
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(r.PostFormValue("csrf"))) != 1 {
		fail(t["bad_login"], http.StatusForbidden)
		return
	}
	s.mu.Lock()
	f := s.failed[ip]
	blocked := f != nil && time.Now().Before(f.until)
	s.mu.Unlock()
	if blocked {
		fail(t["blocked"], http.StatusTooManyRequests)
		return
	}
	user := strings.TrimSpace(r.PostFormValue("username"))
	store, err := s.opt.Load()
	if err != nil {
		s.opt.Log.Printf("web: configuration: %v", err)
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	_, ok := modules.Authenticate(store, user, r.PostFormValue("password"))
	if ok && !modules.UserRights(store, user).Any() {
		// the password was right: no failure to count, but no session either
		s.opt.Log.Printf("web: %s has no rights in the panel (policy), sign in refused", user)
		s.syslog(eventdb.Entry{Kind: modules.SysLoginFailed, User: user, Address: ip, Text: "no rights in the panel (policy)"})
		fail(t["no_rights"], http.StatusForbidden)
		return
	}
	if !ok {
		s.mu.Lock()
		if f == nil || time.Since(f.last) > 10*time.Minute { // failures long ago are forgotten
			f = &failures{}
			s.failed[ip] = f
		}
		f.last = time.Now()
		blockedNow := false
		if f.n++; f.n >= maxFailures {
			f.until, f.n = time.Now().Add(blockFor), 0
			blockedNow = true
		}
		s.mu.Unlock()
		s.opt.Log.Printf("web: failed sign in of %q from %s", user, ip)
		s.syslog(eventdb.Entry{Kind: modules.SysLoginFailed, User: user, Address: ip, Text: "wrong user name or password"})
		if blockedNow {
			s.syslog(eventdb.Entry{Kind: modules.SysLoginBlocked, User: user, Address: ip, Text: fmt.Sprintf("%d failed sign ins: the address is refused for %s", maxFailures, blockFor)})
		}
		time.Sleep(300 * time.Millisecond) // slows guessing down
		fail(t["bad_login"], http.StatusUnauthorized)
		return
	}
	id := token()
	now := time.Now()
	se := &session{key: profile.SessionID(id), user: user, csrf: token(), created: now, active: now, saved: now}
	s.mu.Lock()
	delete(s.failed, ip)
	s.sessions[se.key] = se
	stored := se.stored()
	s.mu.Unlock()
	s.saveSession(user, stored)
	s.opt.Log.Printf("web: %s signed in from %s", user, ip)
	s.syslog(eventdb.Entry{Kind: modules.SysLogin, User: user, Address: ip, Text: "signed in"})
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r)})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// current returns the session of the request, if valid (and keeps it alive).
func (s *Server) current(r *http.Request) *session {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	key := profile.SessionID(c.Value)
	s.mu.Lock()
	now := time.Now()
	for k, se := range s.sessions { // drop the expired ones (the worker clears the profiles)
		if se.expired(now) {
			delete(s.sessions, k)
		}
	}
	se := s.sessions[key]
	var save *profile.Session
	if se != nil {
		se.active = now
		if now.Sub(se.saved) >= saveEvery {
			se.saved = now
			st := se.stored()
			save = &st
		}
	}
	s.mu.Unlock()
	if save != nil {
		s.saveSession(se.user, *save)
	}
	return se
}

// loadSessions takes the sessions of the profiles that are still valid (the panel
// starts).
func (s *Server) loadSessions() {
	now := time.Now()
	n := 0
	for user, list := range s.prof.AllSessions(s.opt.Log.Printf) {
		for _, st := range list {
			se := &session{key: st.ID, user: user, csrf: st.CSRF, created: st.Created, active: st.Active, saved: st.Active}
			if st.ID == "" || se.expired(now) {
				continue
			}
			s.sessions[se.key] = se
			n++
		}
	}
	if n > 0 {
		s.opt.Log.Printf("web: %d session(s) taken from the profiles", n)
	}
}

// saveSession writes a session to the user's profile; a failure only costs the
// session after a restart.
func (s *Server) saveSession(user string, st profile.Session) {
	if err := s.prof.SaveSession(user, st); err != nil {
		s.opt.Log.Printf("web: session of %s: %v", user, err)
	}
}

// forget removes ended sessions from the profiles of their users.
func (s *Server) forget(ses ...*session) {
	for _, se := range ses {
		if err := s.prof.DeleteSession(se.user, se.key); err != nil {
			s.opt.Log.Printf("web: session of %s: %v", se.user, err)
		}
	}
}

// syslog writes entries of the system log.
func (s *Server) syslog(entries ...eventdb.Entry) {
	modules.SystemLog(s.opt.Root, s.opt.Log.Printf, entries...)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if se := s.current(r); se != nil && subtle.ConstantTimeCompare([]byte(se.csrf), []byte(r.PostFormValue("csrf"))) == 1 {
		s.mu.Lock()
		delete(s.sessions, se.key)
		s.mu.Unlock()
		s.forget(se)
		s.syslog(eventdb.Entry{Kind: modules.SysLogout, User: se.user, Address: s.clientIP(r).String(), Text: "signed out"})
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// The limits of the server: a request has to come in time, an answer to go out in time
// (pages; the long answers lift them, see longAnswer).
var (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
)

// stallTimeout: a long answer (a download) goes on as long as data flows; a client
// that takes nothing for this long is dropped.
var stallTimeout = 60 * time.Second

// longAnswer lifts the server's limits for an answer that may take long (a recording
// to download on a slow connection, a file being made first): instead of the whole
// answer in writeTimeout, every write has stallTimeout; no read limit (it would end
// the request's context, and ffmpeg with it).
func longAnswer(w http.ResponseWriter) http.ResponseWriter {
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{})
	lw := &longWriter{ResponseWriter: w, rc: rc}
	lw.extend()
	return lw
}

type longWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func (l *longWriter) extend() { l.rc.SetWriteDeadline(time.Now().Add(stallTimeout)) }

func (l *longWriter) WriteHeader(code int) {
	l.extend()
	l.ResponseWriter.WriteHeader(code)
}

func (l *longWriter) Write(b []byte) (int, error) {
	l.extend()
	return l.ResponseWriter.Write(b)
}

// Unwrap: http.ResponseController reaches the connection through it.
func (l *longWriter) Unwrap() http.ResponseWriter { return l.ResponseWriter }

// HTTPServer is the server of the panel at addr.
func HTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout}
}

// can tells whether the user of a session has a right (see modules.Permissions).
func can(store *config.Store, se *session, perm string) bool {
	return modules.UserRights(store, se.user)[perm]
}

// denied answers a request the user's policy does not allow.
func (s *Server) denied(w http.ResponseWriter, se *session, perm string) {
	s.opt.Log.Printf("web: %s: no right %s", se.user, perm)
	http.Error(w, "forbidden: the right "+perm+" is missing (policy= of /user)", http.StatusForbidden)
}

// home is the first module the rights open: System, Live, Archive ("" for none).
// startPage: where the panel opens for a user (sign in, /): the start page the user
// chose (account) while the user may use it, else the panel's choice (home).
func (s *Server) startPage(user string, r modules.Rights) string {
	st, err := s.prof.Settings(user)
	if err != nil {
		s.opt.Log.Printf("web: settings of %s: %v", user, err)
	}
	if to := startURL(st.Start); to != "" && r[startRight(st.Start)] {
		return to
	}
	return home(r)
}

// startURL and startRight: a start page (profile.StartPages) as a link and the right
// it needs ("": not one).
func startURL(start string) string {
	return map[string]string{"live": "/?p=live", "archive": "/?p=archive", "system": "/"}[start]
}

func startRight(start string) string {
	return map[string]string{"live": modules.PermLive, "archive": modules.PermArchive, "system": modules.PermSystem}[start]
}

func home(r modules.Rights) string {
	switch {
	case r[modules.PermSystem]:
		return "/"
	case r[modules.PermLive]:
		return "/?p=live"
	case r[modules.PermArchive]:
		return "/?p=archive"
	}
	return ""
}

func findItem(s *config.Store, path, key, val string) *config.Item {
	for _, it := range s.Items(path) {
		if it.Val(key) == val {
			return it
		}
	}
	return nil
}
