package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
)

// Firmware updates of remote recorders (Dahua; right system.nvr.firmware): the page
// /?p=fwup&n=<nvr>, reached from the Firmware list and from the recorder's page. The
// browser sends the file to /tools/firmware; the panel keeps it in a temporary file and
// sends it to the recorder in the background, so the page may be closed meanwhile. The
// state of the latest update of every recorder is kept in memory.

// maxFirmware: the largest file accepted (Dahua recorders take 50-300 MB).
var maxFirmware int64 = 1 << 30

// fwRun is one firmware update.
type fwRun struct {
	Nvr, User, File string
	Size            int64
	Started, Ended  time.Time
	Phase           string // what the recorder told last ("" before it tells anything)
	Percent         int
	Sent            int64
	Err             string
	Done            bool
}

type fwRuns struct {
	mu sync.Mutex
	m  map[string]*fwRun // by recorder: the latest update
}

// start registers an update; false when one runs on the recorder already.
func (f *fwRuns) start(r *fwRun) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]*fwRun{}
	}
	if old := f.m[r.Nvr]; old != nil && !old.Done {
		return false
	}
	f.m[r.Nvr] = r
	return true
}

func (f *fwRuns) busy(nvr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.m[nvr]
	return r != nil && !r.Done
}

func (f *fwRuns) update(nvr string, fn func(*fwRun)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.m[nvr]; r != nil {
		fn(r)
	}
}

// fwRunView is an update as the pages show it.
type fwRunView struct {
	File, Size, Who, Phase, State, Kind, Err string
	Percent                                  int
	Active, OK                               bool
}

// views: the latest updates of the recorders in access.
func (f *fwRuns) views(access map[string]bool, t map[string]string) map[string]*fwRunView {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]*fwRunView{}
	for name, r := range f.m {
		if access[name] {
			out[name] = r.view(t)
		}
	}
	return out
}

func (r *fwRun) view(t map[string]string) *fwRunView {
	v := &fwRunView{File: r.File, Size: human(uint64(r.Size)), Percent: r.Percent, Active: !r.Done, Err: r.Err}
	v.Who = fmt.Sprintf(t["fwup_started"], r.Started.Format("2006-01-02 15:04:05"), r.User)
	if r.Done {
		v.Who += " · " + fmt.Sprintf(t["fwup_ended"], r.Ended.Format("15:04:05"))
	}
	phase := r.Phase
	if phase == "" && r.Sent < r.Size {
		phase = "sending"
	} else if phase == "" {
		phase = "waiting"
	}
	v.Phase = or(t["fwp_"+phase], phase)
	switch {
	case !r.Done:
		v.State, v.Kind = t["fwup_running"], kWarn
	case r.Err == "":
		v.State, v.Kind, v.OK = t["fwup_ok"], kOK, true
		v.Phase = t["fwp_Succeeded"]
	default:
		v.State, v.Kind = t["failed"], kErr
	}
	return v
}

// fwupView: the page of one recorder's update.
type fwupView struct {
	Nvr       *nvrView
	Supported bool
	Busy      bool
	Run       *fwRunView
	Max       string
}

// Upgradable: the panel can update the recorder's firmware (see modules.CanUpgrade).
func (v nvrView) Upgradable() bool { return !v.Local && v.Type == "dahua" }

// fwupPage fills the page; false when there is no such recorder.
func (s *Server) fwupPage(p *systemPage, store *config.Store, n *nvrView, q url.Values) bool {
	if n == nil {
		return false
	}
	t := p.T
	p.Title, p.Crumb = fmt.Sprintf(t["fwup_title"], n.Name), t["system"]+" › "+t["maint"]+" › "+t["firmware_p"]
	v := &fwupView{Nvr: n, Supported: modules.CanUpgrade(findItem(store, "/nvr", "name", n.Name)), Max: human(uint64(maxFirmware))}
	v.Run = p.FwRuns[n.Name]
	v.Busy = v.Run != nil && v.Run.Active
	p.Fwup = v
	if e := q.Get("err"); e != "" {
		p.Flash, p.FlashKind = e, "err"
	}
	return true
}

// firmwareTool: POST /tools/firmware, multipart: csrf, nvr, then file (the order of the
// form). The file is read straight into a temporary file, the update runs in the
// background; the answer leads to the recorder's update page.
func (s *Server) firmwareTool(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, modules.PermSystemFirmware) {
		s.denied(w, se, modules.PermSystemFirmware)
		return
	}
	// a large file over a slow link: not the limits of the pages
	w = longAnswer(w)
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(modules.UpgradeTimeout))
	t := texts[lang(r)]
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "multipart/form-data expected", http.StatusBadRequest)
		return
	}
	var csrf, name string
	back := func(msg string) {
		u := "/?p=fwup&n=" + url.QueryEscape(name)
		if msg != "" {
			u += "&err=" + url.QueryEscape(msg)
		}
		http.Redirect(w, r, u, http.StatusSeeOther)
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(csrf)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			back(t["fwup_bad_file"]) // no file
			return
		}
		switch part.FormName() {
		case "csrf", "nvr":
			b, _ := io.ReadAll(io.LimitReader(part, 1024))
			if part.FormName() == "csrf" {
				csrf = string(b)
			} else {
				name = string(b)
			}
			continue
		case "file":
		default:
			continue
		}
		// the file: everything else is known by now
		if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(csrf)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !modules.CanAccess(store, se.user, name) {
			http.Redirect(w, r, "/?p=firmware", http.StatusSeeOther)
			return
		}
		if !modules.CanUpgrade(findItem(store, "/nvr", "name", name)) {
			back(t["fwup_only_dahua"])
			return
		}
		if s.fw.busy(name) {
			back(fmt.Sprintf(t["fwup_busy"], name))
			return
		}
		file := path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		if !strings.EqualFold(path.Ext(file), ".bin") {
			back(t["fwup_bad_file"])
			return
		}
		tmp, size, err := saveUpload(part)
		if err != nil {
			s.opt.Log.Printf("web: %s: firmware for %s: %v", se.user, name, err)
			msg := t["fwup_bad_file"]
			if errors.Is(err, errTooBig) {
				msg = fmt.Sprintf(t["fwup_too_big"], human(uint64(maxFirmware)))
			} else if !errors.Is(err, errEmpty) {
				msg = fmt.Sprintf(t["action_failed"], name, err)
			}
			back(msg)
			return
		}
		run := &fwRun{Nvr: name, User: se.user, File: file, Size: size, Started: time.Now()}
		if !s.fw.start(run) {
			os.Remove(tmp)
			back(fmt.Sprintf(t["fwup_busy"], name))
			return
		}
		addr := s.clientIP(r).String()
		s.opt.Log.Printf("web: %s updates the firmware of %s: %s (%d bytes)", se.user, name, file, size)
		s.syslog(eventdb.Entry{Kind: modules.SysFirmwareUpgrade, Nvr: name, User: se.user, Address: addr,
			Text: fmt.Sprintf("firmware update started: %s (%s)", file, human(uint64(size)))})
		go s.upgrade(store, run, tmp, addr)
		back("")
		return
	}
}

var (
	errTooBig = errors.New("the file is too large")
	errEmpty  = errors.New("the file is empty")
)

// saveUpload copies an uploaded file into a temporary file: its path and size.
func saveUpload(r io.Reader) (string, int64, error) {
	f, err := os.CreateTemp("", "ffnvr-firmware-*.bin")
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxFirmware+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case n > maxFirmware:
		err = errTooBig
	case n == 0:
		err = errEmpty
	}
	if err != nil {
		os.Remove(f.Name())
		return "", 0, err
	}
	return f.Name(), n, nil
}

// upgrade sends the file to the recorder (in the background).
func (s *Server) upgrade(store *config.Store, run *fwRun, tmp, addr string) {
	defer os.Remove(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), modules.UpgradeTimeout)
	defer cancel()
	f, err := os.Open(tmp)
	if err == nil {
		defer f.Close()
		err = modules.UpgradeNvr(ctx, store, run.Nvr, f, run.File, run.Size, func(p modules.UpgradeProgress) {
			s.fw.update(run.Nvr, func(r *fwRun) { r.Phase, r.Percent, r.Sent = p.Phase, p.Percent, p.Sent })
		})
	}
	s.fw.update(run.Nvr, func(r *fwRun) {
		r.Done, r.Ended = true, time.Now()
		if err != nil {
			r.Err = err.Error()
		} else {
			r.Phase, r.Percent = "Succeeded", 100
		}
	})
	e := eventdb.Entry{Kind: modules.SysFirmwareUpgrade, Nvr: run.Nvr, User: run.User, Address: addr}
	if err != nil {
		s.opt.Log.Printf("web: %s: firmware update of %s failed: %v", run.User, run.Nvr, err)
		e.Text = fmt.Sprintf("firmware update failed: %s: %v", run.File, err)
	} else {
		s.opt.Log.Printf("web: %s: firmware %s installed on %s, it restarts", run.User, run.File, run.Nvr)
		e.Text = fmt.Sprintf("firmware installed: %s; the recorder restarts", run.File)
	}
	s.syslog(e)
}

// the page of the update
var _ = template.Must(systemTmpl.Parse(`
{{define "fwup"}}{{with .Fwup}}
{{if $.Flash}}<div class="flash {{$.FlashKind}}" role="status">{{$.Flash}}</div>{{end}}
<div style="display:flex;gap:8px;flex-wrap:wrap"><a class="btn" href="/?p=nvr&amp;n={{.Nvr.Name}}">← {{.Nvr.Name}}</a><a class="btn" href="/?p=firmware">{{$.T.fwup_list}}</a></div>
<div class="row">
 <section class="half"><div class="sh"><h2>{{$.T.information}}</h2><span class="b">{{template "badge" .Nvr}}</span></div>
  <dl><dt>{{$.T.th_type}}</dt><dd>{{.Nvr.Type}}</dd><dt>{{$.T.address}}</dt><dd class="mono">{{or .Nvr.Address "—"}}</dd>
   <dt>{{$.T.th_model}}</dt><dd>{{or .Nvr.Model "—"}}</dd><dt>{{$.T.fwup_current}}</dt><dd class="mono">{{or .Nvr.Firmware "—"}}</dd>
   <dt>{{$.T.th_build}}</dt><dd>{{or .Nvr.Build "—"}}</dd><dt>{{$.T.th_serial}}</dt><dd class="mono">{{or .Nvr.Serial "—"}}</dd>
   <dt>{{$.T.th_checked}}</dt><dd>{{or .Nvr.Checked "—"}}</dd></dl>
 </section>
 <section class="half"><div class="sh"><h2>{{$.T.fwup_h}}</h2></div>
 {{if not .Supported}}<div class="empty">{{$.T.fwup_only_dahua}}</div>{{else}}
  <div class="info-note">{{$.T.fwup_note}}</div>
  {{with .Run}}<div style="padding:12px 16px;display:flex;flex-direction:column;gap:6px;border-bottom:1px solid var(--c-line3)"{{if .Active}} id="fwrunning"{{end}}>
   <div style="display:flex;gap:8px;align-items:center"><span class="badge {{.Kind}}">{{.State}}</span><b style="font-weight:500">{{.Phase}}</b><span style="margin-left:auto;font-weight:600">{{.Percent}}%</span></div>
   <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="{{.Percent}}"><i{{if .Err}} class="err"{{end}} style="width:{{.Percent}}%"></i></div>
   <span class="note">{{$.T.ar_file}}: <span class="mono">{{.File}}</span> ({{.Size}}) · {{.Who}}</span>
   {{if .Err}}<span class="note err">{{.Err}}</span>{{end}}
   {{if .Active}}<span class="note">{{$.T.fwup_keep_open}}</span>{{else if .OK}}<span class="note">{{$.T.fwup_restart}}</span>{{end}}
  </div>{{end}}
  {{if not .Busy}}<form method="post" action="/tools/firmware" enctype="multipart/form-data" id="fwform" class="bar2" style="border-bottom:0;background:var(--c-card)" data-confirm="{{printf $.T.fwup_q .Nvr.Name}}">
   <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="nvr" value="{{.Nvr.Name}}">
   <div class="dest"><label>{{$.T.fwup_file}}<input class="in" type="file" name="file" accept=".bin" required style="padding-top:5px"></label></div>
   <button class="btn primary" type="submit" id="fwgo">{{$.T.fwup_start}}</button>
   <span class="note">{{printf $.T.fwup_max .Max}}</span>
   <div id="fwsend" hidden style="flex-basis:100%;display:flex;flex-direction:column;gap:6px"><div class="bar"><i id="fwsendbar" style="width:0%"></i></div><span class="note" id="fwsendtxt"></span></div>
  </form>{{end}}
 {{end}}
 </section>
</div>
<dialog id="fwconfirm" aria-labelledby="fwconfirm-q">
 <form method="dialog">
  <h2 id="fwconfirm-q"></h2>
  <p>{{$.T.fwup_warn}}</p>
  <div class="dbtn"><button class="btn" value="cancel" formnovalidate autofocus>{{$.T.cancel}}</button><button class="btn danger" value="yes">{{$.T.fwup_btn}}</button></div>
 </form>
</dialog>
<script nonce="{{$.Nonce}}">
(function(){
 if(document.getElementById('fwrunning')){setTimeout(function(){location.reload()},2000)}
 var f=document.getElementById('fwform');if(!f)return;
 var sending={{$.T.fwup_sending}},failed={{$.T.fwup_send_failed}};
 function send(){
  var box=document.getElementById('fwsend'),bar=document.getElementById('fwsendbar'),txt=document.getElementById('fwsendtxt');
  document.getElementById('fwgo').disabled=true;box.hidden=false;txt.textContent=sending.replace('%d','0');
  var x=new XMLHttpRequest();
  x.upload.onprogress=function(e){if(e.lengthComputable){var p=Math.floor(e.loaded*100/e.total);bar.style.width=p+'%';txt.textContent=sending.replace('%d',p)}};
  x.onload=function(){if(x.status<400){location.href=x.responseURL||location.href}else{txt.textContent=failed+' '+x.status+': '+x.responseText;document.getElementById('fwgo').disabled=false}};
  x.onerror=function(){txt.textContent=failed;document.getElementById('fwgo').disabled=false};
  x.open('POST',f.action);x.send(new FormData(f));
 }
 f.addEventListener('submit',function(e){
  e.preventDefault();
  var d=document.getElementById('fwconfirm');
  d.querySelector('h2').textContent=f.dataset.confirm;
  d.returnValue='';
  d.onclose=function(){if(d.returnValue==='yes'){window.XMLHttpRequest&&window.FormData?send():f.submit()}};
  d.showModal();
 });
})();
</script>
{{end}}{{end}}
`))
