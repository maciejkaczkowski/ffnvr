package web

import (
	"fmt"
	"html/template"
)

var _ = template.Must(systemTmpl.Funcs(template.FuncMap{
	// the place of an hour on a 24-hour strip, in percent
	"tickPct": func(h any) string { n, _ := h.(int); return fmt.Sprintf("%.3f", float64(n)*100/24) },
}).Parse(`
{{define "rsstrip"}}<div class="rsstrip">{{range .}}<i class="m-{{.Mode}}" style="left:{{.Left}}%;width:{{.Width}}%" title="{{.Title}}"></i>{{end}}</div>{{end}}

{{define "rsstyle"}}<style>
.rsstrip{position:relative;height:14px;border-radius:3px;background:var(--c-fill2);overflow:hidden;min-width:180px}
.rsstrip i{position:absolute;top:0;bottom:0}
.rsstrip.big{height:22px}
.m-regular{background:#33a9d6}.m-alarm{background:#d4483d}.m-none{background:repeating-linear-gradient(135deg,var(--c-fill2) 0 5px,var(--c-fill3) 5px 10px)}
.rsticks{position:relative;height:15px;font-size:11px;color:var(--c-muted);margin:0 0 3px;font-variant-numeric:tabular-nums;min-width:180px}
.rsticks span{position:absolute;top:0;transform:translateX(-50%)}
.rsticks span:first-child{transform:none}.rsticks span:last-child{transform:translateX(-100%)}
.rsweek{display:grid;grid-template-columns:28px 1fr;gap:3px 8px;align-items:center;font-size:11px;color:var(--c-muted2);min-width:240px}
.rslegend{display:flex;flex-wrap:wrap;gap:14px;font-size:12px;color:var(--c-muted2);align-items:center}
.rslegend b{display:inline-block;width:14px;height:14px;border-radius:3px;vertical-align:-3px;margin-right:6px}
.rsform{padding:16px;display:flex;flex-direction:column;gap:16px}
.rsform fieldset{border:0;margin:0;padding:0;display:flex;flex-direction:column;gap:16px;min-width:0}
.rsform label.f{display:flex;flex-direction:column;gap:4px;font-size:13px;font-weight:500;max-width:420px}
.rsmodes{display:flex;flex-wrap:wrap;gap:8px}
.rsmodes label{display:flex;align-items:center;gap:8px;border:1px solid var(--c-input-line);border-radius:6px;padding:6px 12px;font-size:13px;cursor:pointer;background:var(--c-card)}
.rsmodes label:has(input:checked){border-color:#066fd1;box-shadow:0 0 0 1px #066fd1}
.rsmodes input{margin:0}
.rsmodes b{width:16px;height:16px;border-radius:3px;display:inline-block}
.rsgridwrap{overflow-x:auto;padding-bottom:4px}
.rsgridbox{min-width:720px}
.rsgrid{display:grid;grid-template-columns:repeat(96,1fr);height:56px;border:1px solid var(--c-input-line);border-radius:6px;overflow:hidden;touch-action:none;user-select:none;cursor:crosshair}
.rsgrid span{border-right:1px solid rgba(255,255,255,.55)}
.rsgrid span:nth-child(4n){border-right-color:rgba(24,36,51,.25)}
.rsgrid span:nth-child(8n){border-right-color:rgba(24,36,51,.5)}
.rsgrid:focus-visible{outline:2px solid #066fd1;outline-offset:2px}
.rsrow{display:flex;flex-wrap:wrap;gap:8px;align-items:center}
.rsdays td{vertical-align:middle}
.rsdays select{min-width:200px}
.rschecks{display:flex;flex-wrap:wrap;gap:12px;font-size:13px}
.rsnvr{border-top:1px solid var(--c-line3)}
.rsnvr:first-of-type{border-top:0}
.rsnvr .bar2 h3{margin:0;font-size:14px;font-weight:600}
td .note{display:block;margin-top:2px}
</style>{{end}}

{{define "rsticks"}}<div class="rsticks" aria-hidden="true">{{range .}}<span style="left:{{tickPct .}}%">{{.}}</span>{{end}}</div>{{end}}

{{define "rslegend"}}<div class="rslegend"><span><b class="m-regular"></b>{{.rs_m_regular}}</span><span><b class="m-alarm"></b>{{.rs_m_alarm}}</span><span><b class="m-none"></b>{{.rs_m_none}}</span></div>{{end}}

{{define "schedule"}}{{template "rsstyle"}}{{with .Schedule}}
{{if $.Flash}}<div class="flash {{$.FlashKind}}" role="status">{{$.Flash}}</div>{{end}}
{{if .Day}}{{template "rsday" $}}{{else if .Week}}{{template "rsweekedit" $}}{{else}}
{{if not .CanEdit}}<div class="flash ok" style="background:var(--c-sub);border-color:var(--c-line);color:var(--c-muted2)">{{$.T.rs_view_only}}</div>{{end}}

<section><div class="sh"><h2>{{$.T.rs_step1}}</h2><span class="b">{{if .CanEdit}}<a class="btn sm" href="/?p=schedule&amp;day=%2B">{{$.T.rs_day_new}}</a>{{end}}</span></div>
 <div class="info-note">{{$.T.rs_step1_note}}</div>
 {{if .Days}}<div class="scroll"><table style="min-width:760px">
  <thead><tr><th>{{$.T.rs_name}}</th><th style="width:48%">{{template "rsticks" list 0 6 12 18 24}}</th><th>{{$.T.rs_used}}</th><th style="text-align:right">{{$.T.th_action}}</th></tr></thead>
  <tbody>{{range .Days}}<tr>
   <td style="font-weight:500">{{.Name}}{{if .Comment}}<span class="note">{{.Comment}}</span>{{end}}</td>
   <td>{{template "rsstrip" .Strip}}</td>
   <td>{{if .Used}}{{.Used}}{{else}}<span class="note">—</span>{{end}}</td>
   <td style="text-align:right;white-space:nowrap">{{if $.Schedule.CanEdit}}<a class="btn sm" href="/?p=schedule&amp;day={{.Name}}">{{$.T.rs_edit}}</a>{{end}}</td>
  </tr>{{end}}</tbody></table></div>
  <div style="padding:10px 16px;border-top:1px solid var(--c-line3)">{{template "rslegend" $.T}}</div>
 {{else}}<div class="empty">{{$.T.rs_no_days}}</div>{{end}}
</section>

<section><div class="sh"><h2>{{$.T.rs_step2}}</h2><span class="b">{{if .CanEdit}}<a class="btn sm" href="/?p=schedule&amp;week=%2B"{{if not .Days}} aria-disabled="true"{{end}}>{{$.T.rs_week_new}}</a>{{end}}</span></div>
 <div class="info-note">{{$.T.rs_step2_note}}</div>
 {{if .Weeks}}<div class="scroll"><table style="min-width:860px">
  <thead><tr><th>{{$.T.rs_name}}</th><th style="width:40%">{{$.T.rs_week}}</th><th>{{$.T.rs_alarm}}</th><th>{{$.T.rs_assigned_to}}</th><th style="text-align:right">{{$.T.th_action}}</th></tr></thead>
  <tbody>{{range .Weeks}}<tr>
   <td style="font-weight:500;vertical-align:top">{{.Name}}{{if .Comment}}<span class="note">{{.Comment}}</span>{{end}}</td>
   <td><div class="rsweek">{{range .Days}}<span title="{{or .Template $.T.rs_all_day}}">{{.Label}}</span>{{template "rsstrip" .Strip}}{{end}}</div></td>
   <td style="vertical-align:top;font-size:13px">{{printf $.T.rs_prepost .Pre .Post}}<span class="note">{{.Alarm}}</span><span class="note">{{$.T.rs_silence}}: {{.Silence}}</span></td>
   <td style="vertical-align:top">{{range .Used}}<div>{{.}}</div>{{else}}<span class="note">{{$.T.rs_nobody}}</span>{{end}}</td>
   <td style="text-align:right;white-space:nowrap;vertical-align:top">{{if $.Schedule.CanEdit}}<a class="btn sm" href="/?p=schedule&amp;week={{.Name}}">{{$.T.rs_edit}}</a>{{end}}</td>
  </tr>{{end}}</tbody></table></div>
 {{else}}<div class="empty">{{$.T.rs_no_weeks}}</div>{{end}}
</section>

<section><div class="sh"><h2>{{$.T.rs_step3}}</h2></div>
 <div class="info-note">{{$.T.rs_step3_note}}</div>
 {{range .Nvrs}}{{$n := .}}<form class="rsnvr" method="post" action="/record/save">
  <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="assign"><input type="hidden" name="nvr" value="{{.Name}}">
  <fieldset style="border:0;margin:0;padding:0;min-width:0"{{if not $.Schedule.CanEdit}} disabled{{end}}>
  <div class="bar2"><h3><a href="/?p=nvr&amp;n={{.Name}}">{{.Name}}</a></h3>
   <label class="note" style="display:flex;gap:8px;align-items:center">{{$.T.rs_nvr_schedule}}
    <select class="in" name="schedule"><option value="">{{$.T.rs_none_all_day}}</option>{{range $.Schedule.Names}}<option value="{{.}}"{{if eq . $n.Schedule}} selected{{end}}>{{.}}</option>{{end}}</select></label>
   {{if $.Schedule.CanEdit}}<button class="btn primary sm" type="submit">{{$.T.rs_save}}</button>{{end}}
  </div>
  <div class="scroll"><table style="min-width:900px">
   <thead><tr><th>{{$.T.camera}}</th><th>{{$.T.th_type}}</th><th>{{$.T.rs_cam_schedule}}</th><th>{{$.T.rs_alarms}}</th><th>{{$.T.rs_events}}</th><th>{{$.T.rs_today_h}}</th><th></th></tr></thead>
   <tbody>{{range .Cams}}<tr>
    <td style="font-weight:500">{{.Label}}{{if ne .Label .Name}}<span class="note">{{.Name}}</span>{{end}}</td>
    <td>{{or .Type "—"}}</td>
    <td><select class="in" name="cam_{{.Name}}" aria-label="{{$.T.rs_cam_schedule}}: {{.Label}}"><option value="">{{if $n.Schedule}}{{printf $.T.rs_inherit $n.Schedule}}{{else}}{{$.T.rs_inherit_none}}{{end}}</option>{{$own := .Own}}{{range $.Schedule.Names}}<option value="{{.}}"{{if eq . $own}} selected{{end}}>{{.}}</option>{{end}}</select></td>
    <td><span class="badge {{.Kind}}">{{.Status}}</span>{{if .Note}}<span class="note{{if eq .Kind "err"}} err{{else if eq .Kind "warn"}} warn{{end}}">{{.Note}}</span>{{end}}</td>
    <td style="font-size:13px">{{or .LastEvent "—"}}</td>
    <td style="font-size:13px">{{or .Today "—"}}{{if .Error}}<span class="note err">{{.Error}}</span>{{end}}</td>
    <td style="text-align:right">{{if .Details}}<a class="btn sm" href="{{.Details}}">{{$.T.details}}</a>{{end}}</td>
   </tr>{{end}}</tbody></table></div>
  </fieldset>
 </form>{{else}}<div class="empty">{{$.T.rs_no_local}}</div>{{end}}
</section>
{{end}}{{end}}{{end}}

{{define "rsday"}}{{with .Schedule.Day}}
<div style="display:flex;gap:8px;flex-wrap:wrap"><a class="btn" href="/?p=schedule">← {{$.T.schedule}}</a></div>
<section><form id="rsday" class="rsform" method="post" action="/record/save">
 <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="day-save"><input type="hidden" name="orig" value="{{.Orig}}">
 <div class="rsrow" style="align-items:flex-end;gap:16px">
  <label class="f">{{$.T.rs_name}}<input class="in" name="name" value="{{.Name}}" required maxlength="64" pattern="[A-Za-z0-9_][A-Za-z0-9_.\-]*" placeholder="{{$.T.rs_day_ph}}" autocomplete="off"></label>
  <label class="f">{{$.T.rs_default}}<select class="in" name="default">{{$d := .Default}}{{range $.Schedule.Modes}}<option value="{{.}}"{{if eq . $d}} selected{{end}}>{{index $.T (printf "rs_m_%s" .)}}</option>{{end}}</select></label>
 </div>
 <div>
  <div class="note" style="margin-bottom:8px" id="rshelp">{{$.T.rs_paint_help}}</div>
  <div class="rsmodes" role="radiogroup" aria-label="{{$.T.rs_paint}}">{{range $i, $m := $.Schedule.Modes}}<label><input type="radio" name="rsmode" value="{{$m}}"{{if eq $i 0}} checked{{end}}><b class="m-{{$m}}"></b>{{index $.T (printf "rs_m_%s" $m)}}</label>{{end}}
   <button type="button" class="btn sm" id="rsclear">{{$.T.rs_clear}}</button></div>
 </div>
 <div class="rsgridwrap"><div class="rsgridbox">
  {{template "rsticks" list 0 2 4 6 8 10 12 14 16 18 20 22 24}}
  <div class="rsgrid" id="rsgrid" aria-describedby="rshelp"></div>
 </div></div>
 <label class="f" style="max-width:none">{{$.T.rs_spans}}<input class="in mono" name="spans" value="{{.Spans}}" placeholder="08:00-16:00=regular,16:00-22:00=alarm" autocomplete="off" spellcheck="false"><span class="note" style="font-weight:400">{{$.T.rs_spans_note}}</span></label>
 <label class="f">{{$.T.rs_comment}}<input class="in" name="comment" value="{{.Comment}}" maxlength="200"></label>
 <div class="rsrow"><button class="btn primary" type="submit">{{$.T.rs_save}}</button><a class="btn" href="/?p=schedule">{{$.T.rs_cancel}}</a></div>
</form>
{{if not .New}}<form method="post" action="/record/save" class="bar2" style="border-bottom:0;border-top:1px solid var(--c-line4);background:var(--c-card)">
 <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="day-delete"><input type="hidden" name="name" value="{{.Orig}}">
 <span class="note">{{$.T.rs_delete_day_note}}</span><button class="btn danger sm" type="submit">{{$.T.rs_delete}}</button>
</form>{{end}}
</section>
<script nonce="{{$.Nonce}}">
(function(){
var f=document.getElementById('rsday'),grid=document.getElementById('rsgrid');if(!f||!grid)return;
var inp=f.elements.spans,def=f.elements['default'],N=96,modes=[],cells=[],cur='regular',painting=false,prevDef=def.value;
var names={ {{range $.Schedule.Modes}}{{.}}:{{index $.T (printf "rs_m_%s" .)}},{{end}} };
function pad(n){return(n<10?'0':'')+n}
function clock(m){return pad(Math.floor(m/60))+':'+pad(m%60)}
for(var i=0;i<N;i++){var c=document.createElement('span');c.dataset.i=i;grid.appendChild(c);cells.push(c)}
function paint(){for(var i=0;i<N;i++){cells[i].className='m-'+modes[i];cells[i].title=clock(i*15)+'–'+clock(i*15+15)+' '+names[modes[i]]}}
function parse(){
 for(var i=0;i<N;i++)modes[i]=def.value;
 inp.value.split(',').forEach(function(e){
  var m=e.trim().match(/^(\d{1,2}):(\d{2})-(\d{1,2}):(\d{2})=(\w+)$/);if(!m||!names[m[5]])return;
  var a=+m[1]*60+ +m[2],b=+m[3]*60+ +m[4];
  for(var i=0;i<N;i++){var mid=i*15+7.5;if(mid>=a&&mid<b)modes[i]=m[5]}
 });paint()}
function write(){
 var out=[];for(var i=0;i<N;){var j=i;while(j+1<N&&modes[j+1]===modes[i])j++;
  if(modes[i]!==def.value)out.push(clock(i*15)+'-'+clock((j+1)*15)+'='+modes[i]);i=j+1}
 inp.value=out.join(',')}
function set(el){if(!el||el.parentNode!==grid)return;var i=+el.dataset.i;if(modes[i]!==cur){modes[i]=cur;paint();write()}}
f.querySelectorAll('input[name=rsmode]').forEach(function(r){r.addEventListener('change',function(){cur=r.value})});
grid.addEventListener('pointerdown',function(e){painting=true;grid.setPointerCapture(e.pointerId);set(e.target);e.preventDefault()});
grid.addEventListener('pointermove',function(e){if(painting)set(document.elementFromPoint(e.clientX,e.clientY))});
['pointerup','pointercancel'].forEach(function(t){grid.addEventListener(t,function(){painting=false})});
def.addEventListener('change',function(){for(var i=0;i<N;i++)if(modes[i]===prevDef)modes[i]=def.value;prevDef=def.value;paint();write()});
inp.addEventListener('change',parse);
document.getElementById('rsclear').addEventListener('click',function(){for(var i=0;i<N;i++)modes[i]=def.value;paint();write()});
parse();
})();
</script>
{{end}}{{end}}

{{define "rsweekedit"}}{{with .Schedule.Week}}
<div style="display:flex;gap:8px;flex-wrap:wrap"><a class="btn" href="/?p=schedule">← {{$.T.schedule}}</a></div>
<section><form id="rsweek" class="rsform" method="post" action="/record/save">
 <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="week-save"><input type="hidden" name="orig" value="{{.Orig}}">
 <label class="f">{{$.T.rs_name}}<input class="in" name="name" value="{{.Name}}" required maxlength="64" pattern="[A-Za-z0-9_][A-Za-z0-9_.\-]*" placeholder="{{$.T.rs_week_ph}}" autocomplete="off"></label>
 <div>
  <div class="rsrow" style="justify-content:space-between;margin-bottom:8px"><h2 style="margin:0;font-size:14px;font-weight:600">{{$.T.rs_week}}</h2>
   <button type="button" class="btn sm" id="rscopy">{{$.T.rs_copy_mon}}</button></div>
  <div class="scroll"><table class="rsdays" style="min-width:640px">
   <thead><tr><th style="width:60px">{{$.T.rs_day_h}}</th><th>{{$.T.rs_template}}</th><th style="width:55%">{{template "rsticks" list 0 6 12 18 24}}</th></tr></thead>
   <tbody>{{range .Days}}{{$sel := .Template}}<tr>
    <td style="font-weight:500"><label for="rs-{{.Key}}">{{.Label}}</label></td>
    <td><select class="in" id="rs-{{.Key}}" name="{{.Key}}" data-day="{{.Key}}"><option value="">{{$.T.rs_all_day}}</option>{{range $.Schedule.Week.DayNames}}<option value="{{.}}"{{if eq . $sel}} selected{{end}}>{{.}}</option>{{end}}</select></td>
    <td><div id="rsprev-{{.Key}}">{{template "rsstrip" .Strip}}</div></td>
   </tr>{{end}}</tbody></table></div>
  <div style="margin-top:10px">{{template "rslegend" $.T}}</div>
 </div>
 <div>
  <h2 style="margin:0 0 4px;font-size:14px;font-weight:600">{{$.T.rs_alarm_h}}</h2>
  <p class="note" style="margin:0 0 12px">{{$.T.rs_alarm_note}}</p>
  <div class="rsrow" style="gap:16px;align-items:flex-start">
   <label class="f">{{$.T.rs_pre}}<input class="in" name="pre-alarm" value="{{.Pre}}" placeholder="{{.DefPre}}" style="width:120px" autocomplete="off"></label>
   <label class="f">{{$.T.rs_post}}<input class="in" name="post-alarm" value="{{.Post}}" placeholder="{{.DefPost}}" style="width:120px" autocomplete="off"></label>
   <label class="f">{{$.T.rs_silence}}<input class="in" name="alarm-silence" value="{{.Silence}}" placeholder="{{.DefSilence}}" style="width:120px" autocomplete="off"><span class="note" style="font-weight:400">{{$.T.rs_silence_note}}</span></label>
  </div>
  <div style="margin-top:12px"><div style="font-size:13px;font-weight:500;margin-bottom:6px">{{$.T.rs_filters}}</div>
   {{if .Filters}}<div class="rschecks">{{range .Filters}}<label><input type="checkbox" name="alarm-filters" value="{{.Name}}"{{if .On}} checked{{end}}> {{.Name}}</label>{{end}}</div>
   {{else}}<span class="note">{{$.T.rs_no_filters}}</span>{{end}}</div>
 </div>
 <label class="f">{{$.T.rs_comment}}<input class="in" name="comment" value="{{.Comment}}" maxlength="200"></label>
 <div class="rsrow"><button class="btn primary" type="submit">{{$.T.rs_save}}</button><a class="btn" href="/?p=schedule">{{$.T.rs_cancel}}</a></div>
</form>
{{if not .New}}<form method="post" action="/record/save" class="bar2" style="border-bottom:0;border-top:1px solid var(--c-line4);background:var(--c-card)">
 <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="action" value="week-delete"><input type="hidden" name="name" value="{{.Orig}}">
 <span class="note">{{$.T.rs_delete_week_note}}</span><button class="btn danger sm" type="submit">{{$.T.rs_delete}}</button>
</form>{{end}}
</section>
<script nonce="{{$.Nonce}}">
(function(){
var f=document.getElementById('rsweek');if(!f)return;
var strips={{.Strips}};
function draw(sel){var box=document.getElementById('rsprev-'+sel.dataset.day),s=strips[sel.value]||strips[''],h='<div class="rsstrip">';
 s.forEach(function(x){h+='<i class="m-'+x.Mode+'" style="left:'+x.Left+'%;width:'+x.Width+'%" title="'+x.Title+'"></i>'});box.innerHTML=h+'</div>'}
var sels=f.querySelectorAll('select[data-day]');
sels.forEach(function(s){s.addEventListener('change',function(){draw(s)})});
document.getElementById('rscopy').addEventListener('click',function(){for(var i=1;i<5;i++){sels[i].value=sels[0].value;draw(sels[i])}});
})();
</script>
{{end}}{{end}}
`))
