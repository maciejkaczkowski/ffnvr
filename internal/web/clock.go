package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
)

// The clock of remote recorders (right system.nvr.clock): the list /?p=clock (the
// offsets as last read, a sync button for each) and the page of one recorder
// /?p=clock&n=<nvr> (its clock read live, setting it, the NTP settings), reached from
// the list and from the recorder's page. The changes go to /tools/clock.

// ClockSettable: the panel can set the recorder's clock (see modules.CanSetClock).
func (v nvrView) ClockSettable() bool { return !v.Local && modules.ClockType(v.Type) }

// clockView: the page of one recorder's clock.
type clockView struct {
	Nvr                *nvrView
	Supported          bool
	Err                string
	Time, Server, Zone string
	Offset, OffsetKind string
	NTPKnown           bool
	NTP                modules.NTPSettings
	Input              string // the time field: this server's time
	Port, Interval     string
}

const clockInputLayout = "2006-01-02T15:04:05"

// clockPage fills the list or, with n=, a recorder's page; false: no such recorder.
func (s *Server) clockPage(r *http.Request, p *systemPage, store *config.Store, nvrOf func(string) *nvrView, q url.Values) bool {
	t := p.T
	if v := q.Get("ok"); v != "" {
		p.Flash, p.FlashKind = v, "ok"
	}
	if v := q.Get("err"); v != "" {
		p.Flash, p.FlashKind = v, "err"
	}
	name := q.Get("n")
	if name == "" {
		p.Title, p.Crumb = t["ck_title"], t["system"]+" › "+t["maint"]
		return true
	}
	n := nvrOf(name)
	if n == nil {
		return false
	}
	p.Title, p.Crumb = fmt.Sprintf(t["ck_title_nvr"], n.Name), t["system"]+" › "+t["maint"]+" › "+t["ck_title"]
	v := &clockView{Nvr: n, Supported: modules.CanSetClock(findItem(store, "/nvr", "name", name)),
		Input: time.Now().Format(clockInputLayout), Time: "—", Zone: "—", Offset: "—", OffsetKind: kMute}
	p.Clock = v
	if !v.Supported {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	c, err := modules.NvrClock(ctx, store, name)
	if err != nil {
		v.Err = err.Error()
		return true
	}
	modules.NoteClock(s.opt.Root, name, c)
	off := c.Offset()
	v.Time, v.Server = c.Time.In(time.Local).Format("2006-01-02 15:04:05"), c.Read.Format("2006-01-02 15:04:05")
	v.Offset, v.OffsetKind = fmt.Sprintf("%+d s", int(off/time.Second)), kOK
	if off.Abs() >= time.Minute {
		v.OffsetKind = kWarn
	}
	v.Zone = or(c.Zone, "—")
	v.NTPKnown, v.NTP = c.NTPKnown, c.NTP
	if c.NTP.Port > 0 {
		v.Port = strconv.Itoa(c.NTP.Port)
	}
	if c.NTP.Interval > 0 {
		v.Interval = strconv.Itoa(c.NTP.Interval)
	}
	return true
}

// clockTool: POST /tools/clock - action sync (this server's time), set (time=) or ntp
// (ntp, ntp_server, ntp_port, ntp_interval); back=list returns to the list.
func (s *Server) clockTool(w http.ResponseWriter, r *http.Request) {
	store, se, name, ok := s.toolRequest(w, r, "clock", modules.PermSystemClock)
	if !ok {
		return
	}
	t := texts[lang(r)]
	back := "/?p=clock&n=" + url.QueryEscape(name)
	if r.PostFormValue("back") == "list" {
		back = "/?p=clock"
	}
	answer := func(kind, msg string) {
		http.Redirect(w, r, back+"&"+kind+"="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if !modules.CanSetClock(findItem(store, "/nvr", "name", name)) {
		answer("err", fmt.Sprintf(t["action_failed"], name, t["ck_unsupported"]))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var c modules.ClockInfo
	var err error
	var done, logText string
	switch r.PostFormValue("action") {
	case "sync":
		c, err = modules.SyncNvrTime(ctx, store, name)
		done = fmt.Sprintf(t["ck_synced"], name)
		logText = "clock set to this server's time"
	case "set":
		var at time.Time
		if at, err = modules.ParseClock(r.PostFormValue("time")); err != nil {
			answer("err", t["ck_bad_time"])
			return
		}
		c, err = modules.SetNvrTime(ctx, store, name, at)
		done = fmt.Sprintf(t["ck_set"], name, at.Format("2006-01-02 15:04:05"))
		logText = "clock set to " + at.Format("2006-01-02 15:04:05")
	case "ntp":
		n := modules.NTPSettings{Enabled: r.PostFormValue("ntp") == "on", Server: strings.TrimSpace(r.PostFormValue("ntp_server"))}
		for _, f := range []struct {
			key string
			to  *int
		}{{"ntp_port", &n.Port}, {"ntp_interval", &n.Interval}} {
			if v := strings.TrimSpace(r.PostFormValue(f.key)); v != "" {
				if *f.to, err = strconv.Atoi(v); err != nil || *f.to <= 0 {
					answer("err", t["ck_bad_ntp"])
					return
				}
			}
		}
		if err = modules.CheckNTP(n); err != nil {
			answer("err", fmt.Sprintf(t["action_failed"], name, err))
			return
		}
		c, err = modules.SetNvrNTP(ctx, store, name, n)
		done = fmt.Sprintf(t["ck_ntp_saved"], name)
		state := "off"
		if n.Enabled {
			state = "on"
		}
		logText = fmt.Sprintf("NTP %s, server %q", state, n.Server)
		if n.Port > 0 {
			logText += fmt.Sprintf(", port %d", n.Port)
		}
		if n.Interval > 0 {
			logText += fmt.Sprintf(", every %d min", n.Interval)
		}
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	entry := eventdb.Entry{Kind: modules.SysClockSet, Nvr: name, User: se.user, Address: s.clientIP(r).String()}
	if err != nil {
		s.opt.Log.Printf("web: %s: clock of %s: %s failed: %v", se.user, name, logText, err)
		entry.Text = logText + " failed: " + err.Error()
		s.syslog(entry)
		answer("err", fmt.Sprintf(t["action_failed"], name, err))
		return
	}
	modules.NoteClock(s.opt.Root, name, c)
	s.opt.Log.Printf("web: %s: clock of %s: %s", se.user, name, logText)
	entry.Text = fmt.Sprintf("%s (offset now %+ds)", logText, int(c.Offset()/time.Second))
	s.syslog(entry)
	answer("ok", done)
}

var _ = template.Must(systemTmpl.Parse(`
{{define "clocklist"}}
{{if .Flash}}<div class="flash {{.FlashKind}}" role="status">{{.Flash}}</div>{{end}}
<section><div class="info-note">{{.T.ck_note}}</div><div class="scroll"><table style="min-width:760px">
 <thead><tr><th>{{.T.th_nvr}}</th><th>{{.T.th_type}}</th><th>{{.T.th_model}}</th><th>{{.T.ck_offset}}</th><th>{{.T.th_checked}}</th><th style="text-align:right">{{.T.th_action}}</th></tr></thead>
 <tbody>{{range .Sys.Nvrs}}<tr>
  <td style="font-weight:500"><a href="/?p=nvr&amp;n={{.Name}}">{{.Name}}</a></td><td>{{.Type}}</td><td>{{or .Model "—"}}</td>
  <td>{{if .ClockKnown}}<span class="badge {{.ClockKind}}">{{.Clock}}</span>{{else}}<span class="note">—</span>{{end}}</td>
  <td style="color:#44505f">{{or .Checked "—"}}</td>
  <td style="text-align:right;white-space:nowrap">{{if .Local}}<span class="note">{{$.T.only_remote}}</span>{{else if .ClockSettable}}
   <form method="post" action="/tools/clock" class="inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="nvr" value="{{.Name}}"><input type="hidden" name="action" value="sync"><input type="hidden" name="back" value="list">
    <button type="submit" class="btn sm act">{{$.T.ck_sync_btn}}</button></form>
   <a class="btn sm" href="/?p=clock&amp;n={{.Name}}">{{$.T.ck_settings}}</a>{{else}}<span class="note">{{$.T.ck_unsupported}}</span>{{end}}</td>
 </tr>{{end}}</tbody>
</table></div></section>
{{end}}

{{define "clockpage"}}{{with .Clock}}
{{if $.Flash}}<div class="flash {{$.FlashKind}}" role="status">{{$.Flash}}</div>{{end}}
<div style="display:flex;gap:8px;flex-wrap:wrap"><a class="btn" href="/?p=nvr&amp;n={{.Nvr.Name}}">← {{.Nvr.Name}}</a><a class="btn" href="/?p=clock">{{$.T.ck_list}}</a></div>
{{if not .Supported}}<section><div class="empty">{{$.T.ck_unsupported_long}}</div></section>{{else}}
{{if .Err}}<div class="banner">{{printf $.T.ck_read_failed .Err}}</div>{{end}}
<div class="row">
 <section class="half"><div class="sh"><h2>{{$.T.ck_clock}}</h2><span class="b"><span class="badge {{.OffsetKind}}">{{.Offset}}</span></span></div>
  <dl><dt>{{$.T.ck_recorder_time}}</dt><dd class="mono">{{.Time}}</dd><dt>{{$.T.ck_server_time}}</dt><dd class="mono">{{or .Server "—"}}</dd>
   <dt>{{$.T.ck_offset}}</dt><dd>{{.Offset}}</dd><dt>{{$.T.ck_zone}}</dt><dd class="mono">{{.Zone}}</dd></dl>
  <form method="post" action="/tools/clock" class="bar2" style="border-bottom:0;border-top:1px solid #eef1f4;background:#fff">
   <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="nvr" value="{{.Nvr.Name}}"><input type="hidden" name="action" value="sync">
   <span class="note" style="flex:1 1 200px">{{$.T.ck_sync_note}}</span>
   <button class="btn primary" type="submit">{{$.T.ck_sync_long}}</button>
  </form>
  <form method="post" action="/tools/clock" class="bar2" style="border-bottom:0;border-top:1px solid #eef1f4;background:#fff">
   <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="nvr" value="{{.Nvr.Name}}"><input type="hidden" name="action" value="set">
   <div class="dest"><label>{{$.T.ck_new_time}}<input class="in" type="datetime-local" name="time" step="1" value="{{.Input}}" required></label></div>
   <button class="btn" type="submit">{{$.T.ck_set_btn}}</button>
   <span class="note">{{$.T.ck_set_note}}</span>
  </form>
 </section>
 <section class="half"><div class="sh"><h2>{{$.T.ck_ntp}}</h2>{{if .NTPKnown}}<span class="b"><span class="badge {{if .NTP.Enabled}}ok{{else}}mute{{end}}">{{if .NTP.Enabled}}{{$.T.ck_on}}{{else}}{{$.T.ck_off}}{{end}}</span></span>{{end}}</div>
  {{if and (not .NTPKnown) (not .Err)}}<div class="info-note">{{$.T.ck_ntp_unknown}}</div>{{end}}
  <form method="post" action="/tools/clock" style="padding:12px 16px;display:flex;flex-direction:column;gap:12px">
   <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="nvr" value="{{.Nvr.Name}}"><input type="hidden" name="action" value="ntp">
   <label style="display:flex;gap:8px;align-items:center;font-size:13px;font-weight:500"><input type="checkbox" name="ntp"{{if .NTP.Enabled}} checked{{end}}> {{$.T.ck_ntp_enable}}</label>
   <div class="dest">
    <label>{{$.T.ck_ntp_server}}<input class="in" name="ntp_server" value="{{.NTP.Server}}" maxlength="127" placeholder="pool.ntp.org" autocomplete="off"></label>
    <label>{{$.T.ck_ntp_port}}<input class="in" type="number" name="ntp_port" min="1" max="65535" value="{{.Port}}" placeholder="123" style="width:100px"></label>
    <label>{{$.T.ck_ntp_interval}}<input class="in" type="number" name="ntp_interval" min="1" max="10080" value="{{.Interval}}" placeholder="60" style="width:120px"></label>
   </div>
   <div><button class="btn primary" type="submit">{{$.T.ck_ntp_save}}</button></div>
  </form>
 </section>
</div>
{{end}}
{{end}}{{end}}
`))
