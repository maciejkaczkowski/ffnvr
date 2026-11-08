package web

import "html/template"

// The design of the sign in page (colors, sizes, spacing) follows the mockup "1 · Sign
// in"; it is rendered on the server, without scripts but the password toggle.
const css = `
*{box-sizing:border-box}
body{margin:0;font-family:Inter,system-ui,-apple-system,'Segoe UI',sans-serif;color:#182433;background:#f2f4f7;font-size:14px}
a{color:#0559a8}a:hover{color:#033f78}
input:focus,select:focus,button:focus-visible,a:focus-visible{outline:2px solid #066fd1;outline-offset:1px}
button,input,select{font:inherit}
.page{min-height:100vh;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:20px;padding:24px}
.card{width:100%;max-width:480px;background:#fff;border:1px solid #d5dbe2;border-radius:8px;overflow:hidden;box-shadow:0 1px 3px rgba(24,36,51,.06)}
.card.wide{max-width:1100px}
.head{background:#182433;padding:20px 24px;display:flex;align-items:center;gap:12px;border-bottom:3px solid #066fd1}
.logo{width:34px;height:34px;border-radius:7px;background:#066fd1;color:#fff;display:flex;align-items:center;justify-content:center;flex:none}
.brand{display:flex;flex-direction:column;line-height:1.25}
.brand b{color:#fff;font-weight:700;font-size:17px}
.brand span{color:#b8c4d4;font-size:12px}
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0)}
.head select{margin-left:auto;height:32px;border:1px solid #33455e;border-radius:6px;background:#22324a;color:#fff;font-size:13px;padding:0 8px}
.head .who{margin-left:auto;color:#b8c4d4;font-size:13px;display:flex;align-items:center;gap:12px}
.head .who b{color:#fff}
.head .who button{height:32px;border:1px solid #33455e;border-radius:6px;background:#22324a;color:#fff;padding:0 12px;cursor:pointer}
.body{padding:28px 32px 32px;display:flex;flex-direction:column;gap:16px}
.field{display:flex;flex-direction:column;gap:6px}
.field label{font-weight:500}
.pfield{position:relative}
.forgot{position:absolute;top:1px;right:0;font-size:13px;text-decoration:none}
.in{height:40px;width:100%;border:1px solid #ccd3dc;border-radius:6px;padding:0 12px}
.pwd{position:relative}
.pwd .in{padding-right:44px}
.eye{position:absolute;right:0;top:0;width:44px;height:40px;border:0;background:transparent;cursor:pointer;color:#5b6878;display:flex;align-items:center;justify-content:center}
.buttons{display:flex;gap:10px;margin-top:8px}
.primary{flex:1;height:42px;border-radius:6px;border:0;background:#066fd1;color:#fff;font-weight:600;cursor:pointer}
.primary:hover{background:#0559a8}
.secondary{flex:0 0 120px;height:42px;border-radius:6px;border:1px solid #ccd3dc;background:#fff;color:#182433;font-weight:500;cursor:pointer}
.msg{border-radius:6px;padding:10px 12px;font-size:13px}
.msg.err{background:#fbebeb;color:#a12a2a;border:1px solid #f1c4c4}
.msg.info{background:#eaf2fb;color:#0b4a87;border:1px solid #c3daf3}
h2{margin:0;font-size:16px}
.nvr{border:1px solid #d5dbe2;border-radius:8px;overflow:hidden}
.nvr .t{background:#f7f8fa;padding:12px 16px;display:flex;gap:12px;align-items:baseline;flex-wrap:wrap;border-bottom:1px solid #d5dbe2}
.nvr .t span{color:#5b6878;font-size:13px}
.scroll{overflow-x:auto}
table{border-collapse:collapse;width:100%}
th,td{text-align:left;padding:8px 16px;border-bottom:1px solid #eef1f4;white-space:nowrap}
th{font-weight:500;color:#5b6878;font-size:12px}
.dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:#2fb344;margin-right:6px}
.dot.off{background:#aab4c0}
`

const eye = `<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"></path><circle cx="12" cy="12" r="3"></circle></svg>`

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{"eye": func() template.HTML { return template.HTML(eye) }}).Parse(`
{{define "head"}}<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — {{.Brand.Name}}</title>
<style>` + css + `</style>
</head>
<body>{{end}}

{{define "brand"}}{{if .Brand.LoginLogo}}<div class="logo">{{eye}}</div>{{end}}
{{if .Brand.LoginName}}<div class="brand"><b>{{.Brand.Name}}</b><span>{{or .Brand.Tagline .T.tagline}}</span></div>{{end}}{{end}}

{{define "login"}}{{template "head" .}}
<div class="page">
 <form class="card" method="post" action="/login" autocomplete="on">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <div class="head">
   {{template "brand" .}}
   <label for="lang" class="sr">{{.T.language}}</label>
   <select id="lang" name="lang">
    {{range .Languages}}<option value="{{.Code}}"{{if eq .Code $.Lang}} selected{{end}}>{{.Name}}</option>{{end}}
   </select>
  </div>
  <div class="body">
   {{if .Error}}<div class="msg err" role="alert">{{.Error}}</div>{{end}}
   {{if .Info}}<div class="msg info">{{.Info}}</div>{{end}}
   <div class="field">
    <label for="usr">{{.T.username}}</label>
    <input class="in" id="usr" name="username" type="text" autocomplete="username" value="{{.Username}}" required{{if not .Username}} autofocus{{end}}>
   </div>
   <div class="field pfield">
    <label for="pwd">{{.T.password}}</label>
    <div class="pwd">
     <input class="in" id="pwd" name="password" type="password" autocomplete="current-password"{{if .Username}} autofocus{{end}}>
     <button type="button" class="eye" id="eye" aria-label="{{.T.show_pwd}}">{{eye}}</button>
    </div>
    <a class="forgot" href="/login?forgot=1">{{.T.forgot}}</a>
   </div>
   <div class="buttons">
    <button class="primary" type="submit">{{.T.signin}}</button>
    <button class="secondary" type="reset">{{.T.clear}}</button>
   </div>
  </div>
 </form>
</div>
<script nonce="{{.Nonce}}">
document.getElementById('eye').onclick=function(){var p=document.getElementById('pwd');p.type=p.type==='password'?'text':'password'};
document.getElementById('lang').onchange=function(){location='/lang?lang='+this.value};
</script>
</body></html>{{end}}

`))
