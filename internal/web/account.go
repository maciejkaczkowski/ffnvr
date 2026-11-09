package web

import (
	"crypto/subtle"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
	"ffnvr/internal/profile"
	"ffnvr/internal/script"
)

// The user's own page (?p=account, the name in the header): the account as the
// configuration has it and the change of the password. Every signed in user has it,
// whatever the policy allows.

// minPassword: the shortest new password the panel takes (in characters)
const minPassword = 8

type accountView struct {
	User, Groups, Policy string
	Nvrs, Rights         string
	Sessions             int  // the user's sessions of the panel (this one included)
	CanEdit              bool // the configuration can be changed (Options.Edit)
	MinLen               int
	Starts               []startOpt // the start pages the user may choose (the modules of the rights)
	Live                 bool       // the user may use the live view: its own settings
	ZoomHQ               bool       // a zoomed live tile switches to HQ
	Themes               []startOpt // the looks of the panel, the user's one checked
}

type startOpt struct {
	Value, Label string
	Checked      bool
}

func (s *Server) accountPage(p *systemPage, store *config.Store, q url.Values) {
	t := p.T
	p.Module, p.Title, p.Crumb = "account", t["acc_title"], p.User
	v := &accountView{User: p.User, CanEdit: s.opt.Edit != nil, MinLen: minPassword, Groups: "—", Policy: "—", Nvrs: "—", Rights: "—"}
	if u := findItem(store, "/user", "user", p.User); u != nil {
		v.Groups = or(strings.ReplaceAll(u.Val("groups"), ",", ", "), "—")
		v.Policy = or(u.Val("policy"), "—")
	}
	if n := modules.AccessibleNvrs(store, p.User); len(n) > 0 {
		v.Nvrs = strings.Join(n, ", ")
	}
	var rights []string
	for _, x := range modules.Permissions {
		if p.Can[x.Name] {
			rights = append(rights, x.Name)
		}
	}
	if len(rights) > 0 {
		v.Rights = strings.Join(rights, ", ")
	}
	s.mu.Lock()
	for _, se := range s.sessions {
		if se.user == p.User {
			v.Sessions++
		}
	}
	s.mu.Unlock()
	// the start pages: only the modules the user may use; the one the panel opens with checked
	cur := s.startPage(p.User, p.Can)
	for _, m := range profile.StartPages {
		if p.Can[startRight(m)] {
			v.Starts = append(v.Starts, startOpt{Value: m, Label: t[m], Checked: startURL(m) == cur})
		}
	}
	// the look: every user chooses their own (the day unless chosen)
	for _, th := range profile.Themes {
		v.Themes = append(v.Themes, startOpt{Value: th, Label: t["acc_theme_"+th], Checked: th == p.Theme})
	}
	if p.Can[modules.PermLive] {
		st, _ := s.prof.Settings(p.User)
		v.Live, v.ZoomHQ = true, st.ZoomToHQ()
	}
	switch q.Get("ok") {
	case "live":
		p.Flash, p.FlashKind = t["acc_live_saved"], "ok"
	case "password":
		p.Flash, p.FlashKind = t["acc_changed"], "ok"
	case "start":
		p.Flash, p.FlashKind = t["acc_start_saved"], "ok"
	case "theme":
		p.Flash, p.FlashKind = t["acc_theme_saved"], "ok"
	}
	if e := q.Get("err"); e != "" {
		p.Flash, p.FlashKind = e, "err"
	}
	p.Account = v
}

// POST /account/password: the signed in user changes their password; the current one
// is asked for. The user's other sessions end (who knew the old password is signed out).
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(r.PostFormValue("csrf"))) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	t := texts[lang(r)]
	ip := s.clientIP(r).String()
	fail := func(msg string) {
		http.Redirect(w, r, "/?p=account&err="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if s.opt.Edit == nil {
		fail(t["acc_readonly"])
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	cur, nw := r.PostFormValue("current"), r.PostFormValue("new")
	if _, ok := modules.Authenticate(store, se.user, cur); !ok {
		s.opt.Log.Printf("web: %s: password change refused, wrong current password", se.user)
		s.syslog(eventdb.Entry{Kind: modules.SysLoginFailed, User: se.user, Address: ip, Text: "password change: wrong current password"})
		time.Sleep(300 * time.Millisecond) // slows guessing down
		fail(t["acc_wrong"])
		return
	}
	switch {
	case len([]rune(nw)) < minPassword:
		fail(fmt.Sprintf(t["acc_short"], minPassword))
		return
	case nw != r.PostFormValue("repeat"):
		fail(t["acc_mismatch"])
		return
	case nw == cur:
		fail(t["acc_same"])
		return
	}
	err = s.opt.Edit(func(st *config.Store) error {
		if findItem(st, "/user", "user", se.user) == nil {
			return fmt.Errorf("/user: no %s", se.user)
		}
		return run(st, "/user", "set", []script.Param{p("user", se.user)}, []script.Param{p("password", nw)})
	})
	if err != nil {
		s.opt.Log.Printf("web: %s: password change: %v", se.user, err)
		fail(err.Error())
		return
	}
	// the other sessions of the user end; this one goes on
	var ended []*session
	s.mu.Lock()
	for k, o := range s.sessions {
		if o.user == se.user && o != se {
			delete(s.sessions, k)
			ended = append(ended, o)
		}
	}
	s.mu.Unlock()
	s.forget(ended...)
	text := "password changed"
	if len(ended) > 0 {
		text += fmt.Sprintf(", %d other session(s) ended", len(ended))
	}
	s.opt.Log.Printf("web: %s: %s", se.user, text)
	s.syslog(eventdb.Entry{Kind: modules.SysPasswordChanged, User: se.user, Address: ip, Text: text})
	http.Redirect(w, r, "/?p=account&ok=password", http.StatusSeeOther)
}

// POST /account/start: the user's start page, one of the modules the user may use.
func (s *Server) accountStart(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
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
	start := r.PostFormValue("start")
	if startURL(start) == "" || !modules.UserRights(store, se.user)[startRight(start)] {
		http.Error(w, "no such start page", http.StatusBadRequest)
		return
	}
	if err := s.prof.SetStart(se.user, start); err != nil {
		s.opt.Log.Printf("web: %s: start page: %v", se.user, err)
		http.Redirect(w, r, "/?p=account&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.opt.Log.Printf("web: %s: start page %s", se.user, start)
	http.Redirect(w, r, "/?p=account&ok=start", http.StatusSeeOther)
}

// POST /account/live: the user's own settings of the live view (zoom_hq=1: a zoomed
// tile switches to HQ).
func (s *Server) accountLive(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
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
	if !modules.UserRights(store, se.user)[modules.PermLive] {
		s.denied(w, se, modules.PermLive)
		return
	}
	on := r.PostFormValue("zoom_hq") == "1"
	if err := s.prof.SetZoomHQ(se.user, on); err != nil {
		s.opt.Log.Printf("web: %s: live settings: %v", se.user, err)
		http.Redirect(w, r, "/?p=account&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.opt.Log.Printf("web: %s: live view: zoom to HQ %v", se.user, on)
	http.Redirect(w, r, "/?p=account&ok=live", http.StatusSeeOther)
}

// POST /account/theme: the look of the panel for the user (auto, day or night); every
// signed in user may choose it, whatever the policy allows.
func (s *Server) accountTheme(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(r.PostFormValue("csrf"))) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	theme := r.PostFormValue("theme")
	if profile.NormTheme(theme) != theme {
		http.Error(w, "no such theme", http.StatusBadRequest)
		return
	}
	if err := s.prof.SetTheme(se.user, theme); err != nil {
		s.opt.Log.Printf("web: %s: theme: %v", se.user, err)
		http.Redirect(w, r, "/?p=account&err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	rememberTheme(w, r, theme)
	s.opt.Log.Printf("web: %s: theme %s", se.user, theme)
	http.Redirect(w, r, "/?p=account&ok=theme", http.StatusSeeOther)
}

var _ = template.Must(systemTmpl.Parse(`
{{define "account"}}{{with .Account}}
{{if $.Flash}}<div class="flash {{$.FlashKind}}" role="status">{{$.Flash}}</div>{{end}}
<div class="row">
 <section class="half"><div class="sh"><h2>{{$.T.information}}</h2></div>
  <dl><dt>{{$.T.acc_user}}</dt><dd>{{.User}}</dd><dt>{{$.T.acc_groups}}</dt><dd>{{.Groups}}</dd><dt>{{$.T.acc_policy}}</dt><dd>{{.Policy}}</dd>
   <dt>{{$.T.acc_rights}}</dt><dd>{{.Rights}}</dd><dt>{{$.T.recorders}}</dt><dd>{{.Nvrs}}</dd><dt>{{$.T.acc_sessions}}</dt><dd>{{.Sessions}}</dd></dl>
 </section>
 <div style="flex:1 1 340px;display:flex;flex-direction:column;gap:16px;min-width:0">
 <section><div class="sh"><h2>{{$.T.acc_start}}</h2></div>
  {{if gt (len .Starts) 1}}<form method="post" action="/account/start" style="padding:16px">
   <input type="hidden" name="csrf" value="{{$.CSRF}}">
   <fieldset style="border:0;margin:0 0 12px;padding:0"><legend class="note" style="padding:0;margin-bottom:6px">{{$.T.acc_start_note}}</legend>
   {{range .Starts}}<label class="chk"><input type="radio" name="start" value="{{.Value}}"{{if .Checked}} checked{{end}}> {{.Label}}</label>{{end}}</fieldset>
   <button class="btn primary" type="submit">{{$.T.acc_save}}</button>
  </form>{{else}}<div class="empty">{{with index .Starts 0}}{{printf $.T.acc_start_only .Label}}{{end}}</div>{{end}}
 </section>
 <section><div class="sh"><h2>{{$.T.acc_theme}}</h2></div>
  <form method="post" action="/account/theme" style="padding:16px">
   <input type="hidden" name="csrf" value="{{$.CSRF}}">
   <fieldset style="border:0;margin:0 0 12px;padding:0"><legend class="note" style="padding:0;margin-bottom:6px">{{$.T.acc_theme_note}}</legend>
   {{range .Themes}}<label class="chk"><input type="radio" name="theme" value="{{.Value}}"{{if .Checked}} checked{{end}}> {{.Label}}</label>{{end}}</fieldset>
   <button class="btn primary" type="submit">{{$.T.acc_save}}</button>
  </form>
 </section>
 {{if .Live}}<section><div class="sh"><h2>{{$.T.acc_live}}</h2></div>
  <form method="post" action="/account/live" style="padding:16px">
   <input type="hidden" name="csrf" value="{{$.CSRF}}">
   <label class="chk" style="margin-bottom:4px"><input type="checkbox" name="zoom_hq" value="1"{{if .ZoomHQ}} checked{{end}}> {{$.T.acc_zoom_hq}}</label>
   <p class="note" style="margin:0 0 12px">{{$.T.acc_zoom_hq_note}}</p>
   <button class="btn primary" type="submit">{{$.T.acc_save}}</button>
  </form>
 </section>{{end}}
 <section><div class="sh"><h2>{{$.T.acc_password}}</h2></div>
  {{if .CanEdit}}<form method="post" action="/account/password" id="pwform" style="padding:16px;max-width:380px">
   <input type="hidden" name="csrf" value="{{$.CSRF}}">
   <input type="text" name="username" value="{{.User}}" autocomplete="username" hidden>
   <label class="fl">{{$.T.acc_current}}<input class="in" type="password" name="current" autocomplete="current-password" required></label>
   <label class="fl">{{$.T.acc_new}}<input class="in" type="password" name="new" id="pwnew" autocomplete="new-password" minlength="{{.MinLen}}" required></label>
   <label class="fl">{{$.T.acc_repeat}}<input class="in" type="password" name="repeat" id="pwrepeat" autocomplete="new-password" minlength="{{.MinLen}}" required></label>
   <p class="note" style="margin:0 0 12px">{{printf $.T.acc_rules .MinLen}}</p>
   <button class="btn primary" type="submit">{{$.T.acc_change}}</button>
  </form>
  <script nonce="{{$.Nonce}}">
  (function(){var n=document.getElementById('pwnew'),r=document.getElementById('pwrepeat'),msg={{$.T.acc_mismatch}};
   function check(){r.setCustomValidity(r.value&&r.value!==n.value?msg:'')}
   n.addEventListener('input',check);r.addEventListener('input',check)})();
  </script>{{else}}<div class="empty">{{$.T.acc_readonly}}</div>{{end}}
 </section>
 </div>
</div>
{{end}}{{end}}
`))
