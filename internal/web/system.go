package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/modules"
)

type navItem struct {
	Name, Href, Dot, Note string
	Current, Header       bool // Header: a heading inside the group, not a link
}

type navGroup struct {
	Name, Icon, Badge string
	Open              bool
	Items             []navItem
}

type camTable struct {
	All  bool
	Cams []camView
	T    map[string]string
}

type systemPage struct {
	Lang, Title, Crumb, Updated, Self, CSRF, User string
	Module, Page                                  string
	T                                             map[string]string
	Sys                                           *system
	Groups                                        []navGroup
	Nvr                                           *nvrView
	CamTable                                      camTable
	CamNvr                                        string
	CamInfo                                       []kv
	Log                                           string
	Brand                                         string
	Jobs                                          int
	JobTable                                      jobTable
	Server                                        []kv
	Nonce, Flash, FlashKind                       string
	Archive                                       *archiveView
	BackupStorages                                []string
	BackupRun                                     *backupRun
	BackupItems                                   []backupItem
	BackupNote                                    string
	Live                                          *liveView
	Events                                        *eventsView
	NvrEvents                                     *eventsView           // the latest events on a recorder's page
	Can                                           modules.Rights        // the user's rights (policy= of /user)
	Fwup                                          *fwupView             // the firmware update of a recorder
	FwRuns                                        map[string]*fwRunView // the latest firmware updates, by recorder
	Clock                                         *clockView            // the clock of a recorder
	Schedule                                      *scheduleView         // the recording schedule
	CamBack, CamFrom                              string                // the camera's page: where Back leads, the page it was opened from ("": its recorder)
	CamRec                                        *camRecView           // the camera's page: its latest recordings
	Account                                       *accountView          // the user's own page
	Theme                                         string                // the look of the panel: profile.Theme* (see theme.go)
}

type jobTable struct {
	All  bool
	Jobs []jobView
	T    map[string]string
	// Actions: cancel, retry, download buttons (forms with CSRF, back to Back), as far as
	// the rights Manage (archive.jobs) and Download (archive.download) allow
	Actions          bool
	Manage, Download bool
	CSRF, Back       string
}

// pages that are not designed yet: the section and their title
var placeholders = map[string][2]string{
	"email":    {"events", "email"},
	"accounts": {"users_g", "accounts"}, "perms": {"users_g", "perms"}, "netcfg": {"network_g", "netcfg"},
	"ports": {"network_g", "ports"},
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		s.opt.Log.Printf("web: configuration: %v", err)
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	// a user removed or disabled meanwhile loses the session
	if u := findItem(store, "/user", "user", se.user); u == nil || u.Val("disabled") == "yes" {
		s.drop(se)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	rights := modules.UserRights(store, se.user)
	q := r.URL.Query()
	// the bare / opens the user's start page (the dashboard is ?p=dash)
	if r.URL.RawQuery == "" {
		if to := s.startPage(se.user, rights); to != "/" && to != "" {
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
	}
	need := pageRight(q.Get("p"))
	if need == "" && !rights.Any() {
		need = modules.PermSystem // no rights at all: as any other page denied
	}
	if need != "" && !rights[need] {
		to := home(rights)
		if to == "" { // the policy was taken away meanwhile
			s.drop(se)
			to = "/login"
		} else if q.Get("p") != "" && to == "/" {
			to = "/?p=dash"
		}
		if to == r.URL.RequestURI() {
			to = "/login" // never a loop
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	lng := lang(r)
	t := texts[lng]
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	sys := s.gather(ctx, store, se.user, t)
	p := systemPage{Lang: lng, T: t, Sys: sys, CSRF: se.csrf, User: se.user, Module: "system", Page: q.Get("p"), Can: rights,
		Updated: time.Now().Format("15:04:05"), Self: r.URL.RequestURI(), Brand: modules.WebBranding(store).Name}
	// the user's theme; the sign in page follows it through the cookie
	// (a profile that cannot be read: the day)
	st, _ := s.prof.Settings(se.user)
	p.Theme = st.ThemeOf()
	rememberTheme(w, r, p.Theme)
	access := map[string]bool{}
	for _, n := range sys.Nvrs {
		access[n.Name] = true
	}
	p.FwRuns = s.fw.views(access, t)
	allJobs := s.jobsOf(access, t)
	active, history := splitJobs(allJobs)
	p.Jobs = len(active)
	if p.Page == "" {
		p.Page = "dash"
	}
	crumb := func(group string) string { return t["system"] + " › " + t[group] }
	nvrOf := func(name string) *nvrView {
		for i := range sys.Nvrs {
			if sys.Nvrs[i].Name == name {
				return &sys.Nvrs[i]
			}
		}
		return nil
	}
	switch p.Page {
	case "live":
		s.livePage(&p, store)
	case "archive":
		s.archivePage(ctx, &p, store, q, allJobs)
	case "dash":
		p.Title, p.Crumb = t["dash"], crumb("overview")
	case "nvr":
		if p.Nvr = nvrOf(q.Get("n")); p.Nvr == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		p.Title, p.Crumb = p.Nvr.Name, crumb("recorders")
		p.CamTable = camTable{Cams: p.Nvr.Cameras, T: t}
		p.NvrEvents = s.nvrEvents(ctx, &p, p.Nvr.Name, "")
		var mine []jobView
		finished := 0
		for _, j := range allJobs {
			if j.Nvr != p.Nvr.Name {
				continue
			}
			if !j.Active {
				if finished == 10 {
					continue // the queue, and the latest finished ones
				}
				finished++
			}
			mine = append(mine, j)
		}
		p.JobTable = jobTable{Jobs: mine, T: t}
	case "jobs":
		p.Title, p.Crumb = t["job_queue"], crumb("jobs_g")
		p.JobTable = jobTable{All: true, Jobs: active, T: t}
	case "history":
		p.Title, p.Crumb = t["job_history"], crumb("jobs_g")
		p.JobTable = jobTable{All: true, Jobs: history, T: t}
	case "reboot", "backup":
		p.Title, p.Crumb = t[p.Page], crumb("maint")
		if v := q.Get("ok"); v != "" {
			p.Flash, p.FlashKind = fmt.Sprintf(t["reboot_done"], v), "ok"
		}
		if v := q.Get("err"); v != "" {
			p.Flash, p.FlashKind = v, "err"
		}
		if p.Page == "backup" {
			s.backupPage(&p, modules.StoragesOf(store, "backup"), q.Get("run"))
		}
	case "firmware":
		p.Title, p.Crumb = t["firmware_p"], crumb("maint")
		p.Server = s.serverInfo(t)
		if l := modules.LiveSettings(store); l.Enabled || modules.RecordsViaGo2rtc(store) {
			p.Server = append(p.Server, kv{"go2rtc", go2rtcVersion(r.Context(), l)})
		}
	case "clock":
		if !s.clockPage(r, &p, store, nvrOf, q) {
			http.Redirect(w, r, "/?p=clock", http.StatusSeeOther)
			return
		}
	case "fwup":
		if !s.fwupPage(&p, store, nvrOf(q.Get("n")), q) {
			http.Redirect(w, r, "/?p=firmware", http.StatusSeeOther)
			return
		}
	case "disks", "retention":
		p.Title, p.Crumb = t[map[string]string{"disks": "disks", "retention": "retention_page"}[p.Page]], crumb("storage_g")
	case "cams":
		p.Title, p.Crumb = t["camlist"], crumb("cameras_g")
		p.CamTable = camTable{All: true, T: t}
		for _, n := range sys.Nvrs {
			for _, c := range n.Cameras {
				if c.Details != "" {
					c.Details += "&from=cams" // Back leads to the list
				}
				p.CamTable.Cams = append(p.CamTable.Cams, c)
			}
		}
	case "schedule":
		if !s.schedulePage(ctx, &p, store, q) {
			http.Redirect(w, r, "/?p=schedule", http.StatusSeeOther)
			return
		}
	case "alerts":
		p.Title, p.Crumb = t["active_alerts"], crumb("events")
	case "account":
		s.accountPage(&p, store, q)
	case "event_log":
		p.Title, p.Crumb = t["event_log"], crumb("events")
		s.eventsPage(ctx, &p, store, q)
	case "cam":
		n := nvrOf(q.Get("n"))
		var cam *camView
		if n != nil && n.Local {
			for i := range n.Cameras {
				if n.Cameras[i].Name == q.Get("c") {
					cam = &n.Cameras[i]
				}
			}
		}
		if cam == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		p.Title, p.Crumb, p.CamNvr = or(cam.Alias, cam.Name), crumb("recorders")+" › "+n.Name, n.Name
		// Back leads where the page was opened: the recorder, the camera list or the schedule
		switch p.CamFrom = camFrom(q); p.CamFrom {
		case "cams":
			p.Crumb, p.CamBack = crumb("cameras_g")+" › "+n.Name, "/?p=cams"
		case "schedule":
			p.Crumb, p.CamBack = crumb("storage_g")+" › "+t["schedule"], "/?p=schedule"
		default:
			p.CamBack = "/?p=nvr&n=" + url.QueryEscape(n.Name)
		}
		s.camDetails(ctx, &p, store, n.Name, cam, rights[modules.PermSystemLogs])
		p.NvrEvents = s.nvrEvents(ctx, &p, n.Name, cam.Name)
		p.CamRec = s.camRecent(ctx, &p, store, n.Name, cam)
	default:
		ph, ok := placeholders[p.Page]
		if !ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		p.Title, p.Crumb = t[ph[1]], crumb(ph[0])
	}
	p.Groups = groups(&p, sys, t, q)
	p.Nonce = token()
	imgs := "'self' data: blob:"
	if p.Live != nil && p.Live.TileHost != "" {
		imgs += " " + p.Live.TileHost // the map's tiles
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+p.Nonce+"'; img-src "+imgs+"; "+
		"media-src 'self' blob:; connect-src 'self' ws://"+r.Host+" wss://"+r.Host+"; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := systemTmpl.ExecuteTemplate(w, "layout", p); err != nil {
		s.opt.Log.Printf("web: system: %v", err)
	}
}

func (s *Server) camDetails(ctx context.Context, p *systemPage, store *config.Store, nvr string, cam *camView, logs bool) {
	t := p.T
	unit := modules.UnitName(nvr, cam.Name)
	p.CamInfo = []kv{{t["th_nvr"], nvr}, {t["th_ip"], or(cam.Address, "—")}}
	p.CamInfo = append(p.CamInfo, camSettings(store, nvr, cam.Name, t)...)
	p.CamInfo = append(p.CamInfo, kv{t["th_recording"], cam.Recording}, kv{t["unit"], unit}, kv{t["th_status"], cam.Status})
	if s.opt.Systemd == nil {
		return
	}
	if us, err := s.opt.Systemd.Show(ctx, unit); err == nil {
		if u, ok := us[unit]; ok {
			p.CamInfo = append(p.CamInfo, kv{t["service_state"], u.ActiveState + " / " + u.SubState + " (" + or(u.Result, "—") + ")"},
				kv{t["th_restarts"], strconv.Itoa(u.Restarts)}, kv{t["memory"], cam.Memory})
			if !u.Since.IsZero() {
				p.CamInfo = append(p.CamInfo, kv{t["started"], u.Since.Format("2006-01-02 15:04:05") + " (" + cam.Uptime + ")"})
			}
			if u.PID > 0 {
				p.CamInfo = append(p.CamInfo, kv{t["pid"], strconv.Itoa(u.PID)})
			}
		}
	}
	if !logs {
		return
	}
	if log, err := s.opt.Systemd.Logs(ctx, unit, 500); err == nil {
		p.Log = maskSecrets(log)
	}
}

// camFrom: the page a camera's page was opened from ("cams", "schedule"; "": its recorder's).
func camFrom(q url.Values) string {
	if f := q.Get("from"); f == "cams" || f == "schedule" {
		return f
	}
	return ""
}

// camSettings: the make, the profile and the PTZ control of a camera, as its page shows
// them; what comes from the profile says so.
func camSettings(store *config.Store, nvr, name string, t map[string]string) []kv {
	var it *config.Item
	for _, c := range store.Items("/camera") {
		if c.Val("nvr") == nvr && c.Val("name") == name {
			it = c
		}
	}
	if it == nil {
		return nil
	}
	src := modules.ResolveCamera(store, it)
	yes := func(b bool) string {
		if b {
			return t["cd_yes"]
		}
		return t["cd_no"]
	}
	from := func(key, v string) string {
		if f := src.From[key]; strings.HasPrefix(f, "profile ") {
			return v + " " + fmt.Sprintf(t["cd_from_profile"], strings.TrimPrefix(f, "profile "))
		}
		return v
	}
	var out []kv
	if src.Type != "" {
		out = append(out, kv{t["th_type"], from("type", src.Type)})
	}
	if src.Profile != "" {
		out = append(out, kv{t["cd_profile"], src.Profile})
	}
	out = append(out, kv{t["cd_ptz"], from("ptz", yes(src.PTZ))})
	h, v := it.Val("ptz-swap-horizontal") == "yes", it.Val("ptz-swap-vertical") == "yes"
	if src.PTZ || h || v {
		out = append(out, kv{t["cd_swap_h"], yes(h)}, kv{t["cd_swap_v"], yes(v)})
	}
	return out
}

func groups(p *systemPage, sys *system, t map[string]string, q url.Values) []navGroup {
	from := ""
	if p.Page == "cam" {
		from = camFrom(q)
	}
	item := func(page, name string) navItem {
		// a recorder's firmware update belongs to the Firmware page; a camera's page to
		// the page it was opened from
		return navItem{Name: name, Href: "/?p=" + page, Current: p.Page == page || (page == "firmware" && p.Page == "fwup") || (from != "" && from == page)}
	}
	nvrs := navGroup{Name: t["recorders"], Icon: "nvr"}
	bad := 0
	for _, local := range []bool{true, false} { // as in the Archive: local, then external
		head := t["ar_external"]
		if local {
			head = t["ar_local"]
		}
		first := true
		for _, n := range sys.Nvrs {
			if n.Local != local {
				continue
			}
			if first {
				nvrs.Items = append(nvrs.Items, navItem{Name: head, Header: true})
				first = false
			}
			note := fmt.Sprintf("%d %s", n.CamsTotal, t["ar_cams"])
			if n.Kind == kErr && !n.Local {
				note = t["offline"]
			}
			nvrs.Items = append(nvrs.Items, navItem{Name: n.Name, Href: "/?p=nvr&n=" + url.QueryEscape(n.Name), Dot: n.Kind, Note: note,
				Current: (p.Page == "nvr" || p.Page == "cam" && from == "") && q.Get("n") == n.Name})
			if n.Kind == kWarn || n.Kind == kErr {
				bad++
			}
		}
	}
	if bad > 0 {
		nvrs.Badge = strconv.Itoa(bad)
	}
	storage := navGroup{Name: t["storage_g"], Icon: "storage",
		Items: []navItem{item("disks", t["disks"]), item("retention", t["retention_page"]), item("schedule", t["schedule"])}}
	diskBad := 0
	for _, d := range sys.Disks {
		if d.Kind == kWarn || d.Kind == kErr {
			diskBad++
		}
	}
	if diskBad > 0 {
		storage.Badge = strconv.Itoa(diskBad)
	}
	jobsGroup := navGroup{Name: t["jobs_g"], Icon: "archive", Items: []navItem{item("jobs", t["job_queue"]), item("history", t["job_history"])}}
	if p.Jobs > 0 {
		jobsGroup.Badge = strconv.Itoa(p.Jobs)
	}
	gs := []navGroup{
		{Name: t["overview"], Icon: "system", Items: []navItem{item("dash", t["dash"])}},
		nvrs, storage,
		{Name: t["cameras_g"], Icon: "cam", Items: []navItem{item("cams", t["camlist"])}},
		jobsGroup,
		{Name: t["events"], Icon: "bell", Items: []navItem{item("alerts", t["active_alerts"])}},
		// Users, Network and e-mail notifications: hidden until they are designed
		{Name: t["maint"], Icon: "srv", Items: []navItem{item("firmware", t["firmware_p"])}},
	}
	if p.Can[modules.PermSystemEvents] {
		gs[5].Items = append(gs[5].Items, item("event_log", t["event_log"]))
	}
	if p.Can[modules.PermSystemClock] {
		gs[6].Items = append(gs[6].Items, item("clock", t["ck_title"]))
	}
	if p.Can[modules.PermSystemBackup] {
		gs[6].Items = append(gs[6].Items, item("backup", t["backup"]))
	}
	if p.Can[modules.PermSystemReboot] {
		gs[6].Items = append(gs[6].Items, item("reboot", t["reboot"]))
	}
	if n := len(sys.Alerts); n > 0 {
		gs[5].Badge = strconv.Itoa(n) // Events & alerts
	}
	// only the section of the page is open (the others can be opened by hand); a page
	// of no section opens the overview
	open := false
	for i := range gs {
		for _, it := range gs[i].Items {
			if it.Current {
				gs[i].Open, open = true, true
			}
		}
	}
	if !open {
		gs[0].Open = true
	}
	return gs
}

// pageRight: the right a page of the panel needs.
func pageRight(page string) string {
	switch page {
	case "live":
		return modules.PermLive
	case "archive":
		return modules.PermArchive
	case "reboot":
		return modules.PermSystemReboot
	case "backup":
		return modules.PermSystemBackup
	case "account": // the user's own page: every signed in user
		return ""
	case "event_log":
		return modules.PermSystemEvents
	case "fwup":
		return modules.PermSystemFirmware
	case "clock":
		return modules.PermSystemClock
	}
	return modules.PermSystem
}

// drop ends a session.
func (s *Server) drop(se *session) {
	s.mu.Lock()
	if s.sessions[se.key] == se {
		delete(s.sessions, se.key)
	}
	s.mu.Unlock()
	s.forget(se)
}

// serverInfo: what runs on this server (the firmware page).
func (s *Server) serverInfo(t map[string]string) []kv {
	out := []kv{{t["ffnvr_version"], s.opt.Version}}
	if exe, err := os.Executable(); err == nil {
		out = append(out, kv{t["binary"], exe})
	}
	if v := ffmpegVersion(); v != "" {
		out = append(out, kv{t["ffmpeg_version"], v})
	}
	return out
}

var ffmpegCache struct {
	sync.Mutex
	at time.Time
	v  string
}

// ffmpegVersion: the first line of ffmpeg -version (asked at most every 10 minutes).
func ffmpegVersion() string {
	ffmpegCache.Lock()
	defer ffmpegCache.Unlock()
	if !ffmpegCache.at.IsZero() && time.Since(ffmpegCache.at) < 10*time.Minute {
		return ffmpegCache.v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-version").Output()
	ffmpegCache.at, ffmpegCache.v = time.Now(), ""
	if err == nil {
		line, _, _ := strings.Cut(string(out), "\n")
		line, _, _ = strings.Cut(strings.TrimPrefix(line, "ffmpeg version "), " Copyright")
		ffmpegCache.v = strings.TrimSpace(line)
	}
	return ffmpegCache.v
}

// tool runs an action on a recorder for a signed in user with access to it (POST, with
// the session's CSRF token); it returns the store and the recorder's name.
func (s *Server) toolRequest(w http.ResponseWriter, r *http.Request, back, perm string) (store *config.Store, se *session, name string, ok bool) {
	se = s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(r.PostFormValue("csrf"))) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, perm) {
		s.denied(w, se, perm)
		return
	}
	name = r.PostFormValue("nvr")
	if !modules.CanAccess(store, se.user, name) {
		http.Redirect(w, r, "/?p="+back, http.StatusSeeOther)
		return
	}
	return store, se, name, true
}

func (s *Server) failTool(w http.ResponseWriter, r *http.Request, back, name string, err error) {
	t := texts[lang(r)]
	http.Redirect(w, r, "/?p="+back+"&err="+url.QueryEscape(fmt.Sprintf(t["action_failed"], name, err)), http.StatusSeeOther)
}

func (s *Server) rebootTool(w http.ResponseWriter, r *http.Request) {
	store, se, name, ok := s.toolRequest(w, r, "reboot", modules.PermSystemReboot)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := modules.RebootNvr(ctx, store, name); err != nil {
		s.opt.Log.Printf("web: %s: reboot of %s failed: %v", se.user, name, err)
		s.failTool(w, r, "reboot", name, err)
		return
	}
	s.opt.Log.Printf("web: %s rebooted %s", se.user, name)
	http.Redirect(w, r, "/?p=reboot&ok="+url.QueryEscape(name), http.StatusSeeOther)
}

func (s *Server) backupTool(w http.ResponseWriter, r *http.Request) {
	store, se, name, ok := s.toolRequest(w, r, "backup", modules.PermSystemBackup)
	if !ok {
		return
	}
	w = longAnswer(w) // the recorder may take a while, the connection too
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	body, file, err := modules.BackupNvr(ctx, store, name)
	if err != nil {
		s.opt.Log.Printf("web: %s: backup of %s failed: %v", se.user, name, err)
		s.failTool(w, r, "backup", name, err)
		return
	}
	defer body.Close()
	s.opt.Log.Printf("web: %s downloads the configuration of %s", se.user, name)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	io.Copy(w, body)
}

// passwords in addresses (rtsp://admin:secret@host) and in key=value forms
var (
	urlPassword = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]*:)[^\s/]*@`)
	kvPassword  = regexp.MustCompile(`(?i)\b(password|passwd|pwd|pass)=([^\s&"';,]+)`)
)

// maskSecrets hides passwords before a log is shown.
func maskSecrets(s string) string {
	s = urlPassword.ReplaceAllString(s, "${1}*****@")
	return kvPassword.ReplaceAllString(s, "${1}=*****")
}

// go2rtcVersion asks the running go2rtc (its API answers {"version": ...}).
func go2rtcVersion(ctx context.Context, l modules.Live) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+l.API()+"/api", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "not running (" + l.Binary + ")"
	}
	defer resp.Body.Close()
	var info struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&info) != nil || info.Version == "" {
		return fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return info.Version + " (" + l.Binary + ", " + l.API() + ")"
}
