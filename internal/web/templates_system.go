package web

import (
	"ffnvr/internal/modules"
	"fmt"
	"html/template"
)

// The System screen follows the mockup "4 · System": a dark header with the modules,
// a side menu of sections and the content. Rendered on the server; the side menu
// folds with <details>, without scripts.
const systemCSS = `
*{box-sizing:border-box}
body{margin:0;font-family:Inter,system-ui,-apple-system,'Segoe UI',sans-serif;color:#182433;background:#f6f8fb;font-size:14px}
a{color:#0559a8}a:hover{color:#033f78}
button{font:inherit}
th{font-weight:600}
.mono{font-family:'JetBrains Mono',ui-monospace,SFMono-Regular,Menlo,monospace}
:root{--hh:56px;--vh:100vh}
@supports (height:100dvh){:root{--vh:100dvh}}
.app{min-height:var(--vh);display:flex;flex-direction:column}
header{position:sticky;top:0;z-index:40;background:#182433;color:#fff;display:flex;flex-wrap:nowrap;align-items:stretch;gap:0 28px;padding:0 20px;height:var(--hh)}
.menubtn{display:none;position:relative;flex:none;width:var(--hh);border:0;background:none;color:#c5d0de;cursor:pointer;align-items:center;justify-content:center}
.menubtn:hover{color:#fff;background:#22324a}
.menubtn:focus-visible,nav.mods a:focus-visible{outline:2px solid #4ea1f0;outline-offset:-2px}
.menubtn .mdot{position:absolute;top:14px;right:13px;width:9px;height:9px;border-radius:50%;background:#d63939;border:2px solid #182433}
.logo{display:flex;align-items:center;gap:10px}
.logo i{width:30px;height:30px;border-radius:7px;background:#066fd1;display:flex;align-items:center;justify-content:center}
.logo b{font-weight:700;font-size:16px}
nav.mods{display:flex;flex-wrap:nowrap;min-width:0}
nav.mods a{position:relative;display:flex;align-items:center;gap:8px;padding:0 16px;height:var(--hh);white-space:nowrap;text-decoration:none;color:#c5d0de;font-weight:500;border-bottom:3px solid transparent}
nav.mods a[aria-current=page]{color:#fff;font-weight:600;border-bottom-color:#066fd1;background:#22324a}
.count{background:#d63939;color:#fff;font-size:11px;font-weight:600;padding:1px 7px;border-radius:10px}
.right{margin-left:auto;display:flex;align-items:center;gap:14px;min-width:0}
.right .who{min-width:0}.right .who span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.right .who button{flex:none}
.bell{position:relative;width:36px;height:36px;color:#c5d0de;display:flex;align-items:center;justify-content:center}
.bell span{position:absolute;top:6px;right:7px;width:8px;height:8px;border-radius:50%;background:#d63939;border:2px solid #182433}
.who{color:#b8c4d4;font-size:13px;display:flex;align-items:center;gap:12px;margin:0}
.who b{color:#fff}
.who a.me{color:inherit;text-decoration:none;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.who a.me:hover b,.who a.me[aria-current=page] b{text-decoration:underline}
.who button{height:32px;border:1px solid #33455e;border-radius:6px;background:#22324a;color:#fff;padding:0 12px;cursor:pointer}
.cols{flex:1;display:flex;align-items:flex-start}
/* the side menu: as high as the screen under the header, it scrolls on its own */
.cols>aside{flex:0 0 272px;width:272px;position:sticky;top:var(--hh);height:calc(var(--vh) - var(--hh));overflow-y:auto;overscroll-behavior:contain;background:#fff;border-right:1px solid #dce1e7}
.cols>aside.lside{overflow:hidden}
details{border-bottom:1px solid #e3e7ec}
summary{list-style:none;min-height:42px;padding:0 14px;background:#f6f8fb;cursor:pointer;display:flex;align-items:center;gap:10px;font-weight:600}
summary::-webkit-details-marker{display:none}
summary svg.ic{color:#44505f}
summary .chev{flex:none;transition:transform .15s}
details[open] summary .chev{transform:rotate(180deg)}
.gbadge{background:#f8dadb;color:#a3222b;font-size:11px;font-weight:600;padding:1px 7px;border-radius:10px}
.items{padding:4px 8px 8px;display:flex;flex-direction:column;gap:1px}
.items a{display:flex;align-items:center;gap:8px;min-height:34px;padding:0 10px 0 36px;border-radius:5px;font-size:13px;color:#182433;text-decoration:none}
.items a:hover{background:#f1f4f8}
.items a[aria-current=page]{background:#e6f0fb;color:#0559a8;font-weight:600;box-shadow:inset 3px 0 0 #066fd1}
.items .ihead{padding:10px 10px 4px;font-size:11px;font-weight:700;letter-spacing:.06em;text-transform:uppercase;color:#5b6878}
.items .ihead:first-child{padding-top:4px}
.items a .inote{margin-left:auto;font-size:12px;color:#5b6878;font-weight:400}
.items a.nvr{padding-left:14px}
.btn.act{min-width:96px;justify-content:center}
.dot{width:8px;height:8px;border-radius:50%;flex:none;display:inline-block}
.dot.ok{background:#2fb344}.dot.warn{background:#f59f00}.dot.err{background:#d63939}.dot.mute{background:#aab4c0}
main{flex:1 1 auto;align-self:stretch;min-width:0;padding:16px 20px 24px;display:flex;flex-direction:column;gap:16px}
.top{display:flex;flex-wrap:wrap;align-items:center;gap:12px}
.crumb{font-size:12px;color:#5b6878}
h1{margin:0;font-size:20px;font-weight:700}
.upd{margin-left:auto;display:flex;align-items:center;gap:8px;font-size:13px;color:#5b6878}
.btn{height:34px;padding:0 12px;border:1px solid #ccd3dc;border-radius:6px;background:#fff;color:#182433;cursor:pointer;display:inline-flex;align-items:center;text-decoration:none}
.btn:hover{color:#182433;background:#f6f8fb}
.kpis{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:12px}
.kpi{background:#fff;border:1px solid #dce1e7;border-radius:8px;padding:14px 16px;display:flex;flex-direction:column;gap:2px}
.kpi .l{font-size:13px;color:#5b6878}
.kpi .v{font-size:22px;font-weight:700}
.kpi .v span{font-size:14px;font-weight:500;color:#5b6878}
.note{font-size:12px;color:#5b6878}.note.err{color:#a3222b}.note.warn{color:#8a4b08}
.tabs.sm a{padding:4px 10px;font-size:12px}
.thsel{padding:8px 16px;border-bottom:1px solid #eef1f4}
.thumbs{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:12px;padding:12px 16px}
.thumb{display:flex;flex-direction:column;border:1px solid #dce1e7;border-radius:8px;overflow:hidden;background:#fff;cursor:pointer}
.thumb:hover{border-color:#066fd1}
.thumb img{width:100%;aspect-ratio:16/9;object-fit:cover;background:#1a2433 linear-gradient(90deg,#1a2433,#243247,#1a2433);display:block}
.tcap{display:flex;flex-wrap:wrap;align-items:center;gap:4px 8px;padding:8px 10px 2px;font-size:13px}
.tcap .note{flex-basis:100%}
.tdl{display:flex;gap:6px;padding:6px 10px 10px}
.mbar{display:flex;gap:1px;height:7px;margin:6px 10px 0}
.mbar i{flex:1;border-radius:1px;background:#d5dbe2}
.mbar i.still{background:#2fb344}.mbar i.mv{background:#d63939}
.mbar i.wait{background:#d5dbe2;animation:pulse 1.2s ease-in-out infinite}
@keyframes pulse{50%{opacity:.45}}
.reach{display:flex;align-items:center;gap:8px;font-size:12px;color:#1a6b2c}
.reachbar{width:28px;height:6px;border-radius:3px;background:#2fb344}
.row{display:flex;flex-wrap:wrap;gap:16px;align-items:flex-start}
section{background:#fff;border:1px solid #dce1e7;border-radius:8px}
section.wide{flex:999 1 560px;min-width:0}
section.side{flex:1 1 280px}
section.half{flex:1 1 340px}
section h2{margin:0;font-size:15px;font-weight:600}
.sh{padding:12px 16px;border-bottom:1px solid #e8ebef;display:flex;align-items:center;gap:8px}
.sh .b{margin-left:auto}
.scroll{overflow-x:auto}
table{width:100%;border-collapse:collapse;font-size:13px}
thead tr{background:#f6f8fb;color:#44505f;text-align:left;font-size:12px;text-transform:uppercase;letter-spacing:.03em}
th{padding:9px 8px;white-space:nowrap}
td{padding:10px 8px;border-top:1px solid #eef1f4;vertical-align:middle}
tbody tr:hover td{background:#f5f8fc}
th:first-child,td:first-child{padding-left:16px}
th:last-child,td:last-child{padding-right:16px}
.nl{display:flex;flex-direction:column;gap:2px;text-decoration:none}
.nl b{font-weight:600}
.nl span{font-size:12px;color:#5b6878}
.badge{display:inline-block;font-size:11px;font-weight:600;padding:2px 8px;border-radius:10px;white-space:nowrap}
.badge.ok{background:#d7f0dc;color:#1a6b2c}.badge.warn{background:#fdebd0;color:#8a4b08}
.badge.err{background:#f8dadb;color:#a3222b}.badge.mute{background:#e8ebef;color:#44505f}
.bar{height:6px;background:#e8ebef;border-radius:3px;overflow:hidden}
.bar i{display:block;height:100%;background:#066fd1}
.bar i.warn{background:#f59f00}.bar i.err{background:#d63939}
.sto{min-width:130px}
.sto .t{display:flex;justify-content:space-between;font-size:12px;margin-bottom:4px;gap:8px}
.sto .t span{color:#5b6878}
.alert{padding:11px 16px;border-top:1px solid #eef1f4;display:flex;gap:12px}
.alert .dot{width:10px;height:10px;margin-top:4px}
.alert div{display:flex;flex-direction:column;gap:2px}
.alert b{font-size:13px}.alert span{font-size:12px;color:#44505f}.alert em{font-size:12px;color:#5b6878;font-style:normal}
.empty{padding:14px 16px;color:#5b6878;font-size:13px}
dl{margin:0;display:grid;grid-template-columns:repeat(2,minmax(0,1fr));font-size:13px}
dt{padding:9px 16px;border-top:1px solid #eef1f4;color:#5b6878}
dd{margin:0;padding:9px 16px;border-top:1px solid #eef1f4;font-weight:500;overflow-wrap:anywhere}
.disk{padding:11px 16px;border-top:1px solid #eef1f4;display:flex;flex-direction:column;gap:6px}
.disk .t{display:flex;align-items:center;gap:8px}
.disk .t b{font-size:13px}.disk .t .badge{margin-left:auto}
.disk>span{font-size:12px;color:#5b6878}
.banner{background:#fdf1f1;border:1px solid #f1c4c6;border-radius:8px;padding:14px 16px;color:#7f1d22}
.placeholder{background:#fff;border:1px dashed #c3ccd6;border-radius:8px;padding:48px 24px;text-align:center;color:#44505f}
.placeholder p{margin:0;font-size:13px}.placeholder p:first-child{margin-bottom:6px;font-weight:600;color:#182433;font-size:14px}
.logbox{max-height:540px;overflow:auto;display:flex;flex-direction:column-reverse;background:#0f1722;border-radius:0 0 8px 8px}
pre.log{margin:0;padding:12px 16px;font-size:12px;line-height:1.5;white-space:pre;color:#d5dde8}
.info-note{font-size:13px;color:#44505f;padding:12px 16px;border-bottom:1px solid #e8ebef}
form.inline{display:inline;margin:0}
.ltree summary,.lside summary{background:#fff;min-height:38px;font-size:13px}
aside.lside{display:flex;flex-direction:column}
.lgridside,.lmapside{display:flex;flex-direction:column;min-height:0;flex:1}
.lgridside[hidden],.lmapside[hidden]{display:none}
.lscroll{flex:1;min-height:0;overflow:auto}
.lviews{flex:none;max-height:35%;overflow:auto;border-top:1px solid #e3e7ec}
.lview{display:flex;align-items:center;gap:6px;margin:1px 8px;padding:0 4px 0 10px;border-radius:6px}
.lview.cur{background:#e6f0fb;box-shadow:inset 3px 0 0 #066fd1}
.lview .lvn{flex:1;text-align:left;border:0;background:none;font:inherit;font-size:13px;min-height:32px;cursor:pointer;color:#182433}
.lvdel{border:0;background:none;color:#8a96a5;cursor:pointer;width:26px;height:26px;border-radius:4px}
.lvdel:hover{background:#fdf1f1;color:#a3222b}
.mlist{display:flex;flex-direction:column;padding:4px 0 8px}
.mrow{display:flex;align-items:center;gap:8px;margin:1px 8px;padding:0 10px;min-height:32px;border:0;border-radius:6px;background:none;font:inherit;font-size:13px;text-align:left;cursor:pointer;color:#182433}
.mrow:hover{background:#f1f4f8}.mrow.cur{background:#e6f0fb;box-shadow:inset 3px 0 0 #066fd1}
.mrow .ln{flex:1}
.mlayers{padding:6px 16px 12px}
main.mapmode{height:calc(var(--vh) - var(--hh));overflow:hidden}
main.mapmode .lmap{flex:1 1 auto;height:auto;min-height:200px}
main.mapmode .mstrip{flex:none;max-height:42%;overflow:auto;margin-top:auto}
main.mapmode .lthumbs{flex:1 1 auto;min-height:200px;overflow:auto}
.lthumbs{display:grid;grid-template-columns:repeat(3,1fr);grid-auto-rows:max-content;align-content:start;gap:4px;background:#0f1722;padding:4px;border-radius:8px;overflow-y:auto;overscroll-behavior:contain}
.lthumbs[hidden],.lthumbside[hidden]{display:none}
.lthumbs:fullscreen{border-radius:0;overflow-y:auto}
.lthumbs .tile{aspect-ratio:auto}
.lthumbs .tile img,.lthumbs .tile .none{display:block;width:100%;height:auto;aspect-ratio:16/9;object-fit:contain;background:#111a26}
.lthumbs .tile.nolive{cursor:default}
.lthumbs .tile .none{display:flex;align-items:center;justify-content:center;color:#8a96a5;font-size:13px}
.lthumbs .tile.instrip{border-color:#066fd1}
.lthumbs .tile .tn small{color:#b8c4d4;font-size:11px;margin-left:4px}
.lthumbs .tile .tf{position:absolute;inset:auto 0 0;display:flex;align-items:center;gap:8px;padding:3px 8px;background:rgba(15,23,34,.75);color:#dfe6ee;font-size:11px;z-index:2}
.lthumbs .tile .tf .badge{margin-left:auto}
.lthumbs .empty-note{grid-column:1/-1;color:#b8c4d4;padding:24px;text-align:center}
dialog.player{width:min(1280px,96vw);max-width:96vw;max-height:96vh;padding:0;border:0;border-radius:10px;background:#0f1722;color:#dfe6ee;overflow:hidden}
dialog.player::backdrop{background:rgba(10,15,24,.75)}
.plhead{display:flex;align-items:center;gap:12px;padding:10px 14px}
.plhead h2{margin:0;font-size:15px;font-weight:600;color:#fff;flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.plclock{font-size:14px;color:#ffd166}
.plvid{position:relative;background:#000}
.plvid video{display:block;width:100%;max-height:calc(96vh - 120px);background:#000}
.plmsg[hidden]{display:none}
.plmsg{position:absolute;inset:0;display:flex;align-items:center;justify-content:center;text-align:center;padding:24px;color:#dfe6ee;background:rgba(0,0,0,.6);pointer-events:none}
.plmotion{display:flex;gap:2px;padding:8px 14px 0}
.plmotion[hidden]{display:none}
.plmotion button{flex:1;height:14px;min-width:0;padding:0;border:0;border-radius:2px;background:#2fb344;cursor:pointer;opacity:.85;transition:transform .08s,opacity .08s}
.plmotion button.mv{background:#d63939}
.plmotion button:hover,.plmotion button:focus-visible{opacity:1;transform:scaleY(1.45);outline:2px solid #fff;outline-offset:1px}
.plmotion button.now{opacity:1;box-shadow:inset 0 -3px 0 #ffd166}
.plbar{display:flex;flex-wrap:wrap;align-items:center;gap:10px;padding:10px 14px}
.plbar .chk{color:#dfe6ee}.plbar .note{color:#b8c4d4;margin-left:auto}
.tinfo{padding:8px 16px 12px;font-size:13px;display:flex;flex-direction:column;gap:6px}
.tinfo .row{display:flex;gap:8px}.tinfo .row span:first-child{color:#5b6878}.tinfo .row span:last-child{margin-left:auto;text-align:right}
.tinfo .err{color:#a3222b}
.mprev.loading img{visibility:hidden}
.thumb .timg{position:relative;display:block}
.mprev.loading::after{top:58%}
.thumb .timg.loading::after{top:50%}
.mprev.loading::after,.thumb .timg.loading::after{content:"";position:absolute;left:50%;width:22px;height:22px;margin:-11px;border:3px solid #44505f;border-top-color:#fff;border-radius:50%;animation:spin .8s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
.lmap{position:relative;height:calc(100vh - 230px);min-height:360px;overflow:hidden;border:1px solid #dce1e7;border-radius:8px;background:#e9edf1;touch-action:none;cursor:grab;user-select:none}
.lmap[hidden]{display:none}
.lmap.pan{cursor:grabbing}
.mtiles img{position:absolute;width:256px;height:256px;max-width:none;pointer-events:none}
.mfov{position:absolute;inset:0;pointer-events:none}
.mfov .fov{fill:rgba(6,111,209,.18);stroke:rgba(6,111,209,.6);stroke-width:1.2}
.mfov .fov.bad{fill:rgba(214,57,57,.14);stroke:rgba(214,57,57,.6)}
.mfov .fov.sel{fill:rgba(6,111,209,.3);stroke:#066fd1;stroke-width:2}
.mk{position:absolute;transform:translate(-50%,-50%);width:28px;height:28px;border-radius:50%;background:#fff;border:3px solid #2fb344;color:#1a6b2c;display:flex;align-items:center;justify-content:center;cursor:pointer;box-shadow:0 1px 4px rgba(0,0,0,.3)}
.mk.err,.mk.warn{border-color:#d63939;color:#a3222b}.mk.mute{border-color:#8a96a5;color:#44505f}
.mk.sel{box-shadow:0 0 0 4px rgba(6,111,209,.35)}
.mk.edit{cursor:move}
.mk.nonames span{display:none}
.mh[hidden]{display:none}
.mk span{position:absolute;left:50%;top:30px;transform:translateX(-50%);white-space:nowrap;font-size:11px;font-weight:600;color:#182433;background:rgba(255,255,255,.85);padding:0 4px;border-radius:3px}
.mh{position:absolute;transform:translate(-50%,-50%);width:14px;height:14px;border-radius:50%;background:#066fd1;border:2px solid #fff;cursor:crosshair;box-shadow:0 1px 3px rgba(0,0,0,.4)}
.mzoom{position:absolute;left:12px;top:12px;display:flex;flex-direction:column;background:#fff;border-radius:6px;box-shadow:0 1px 4px rgba(0,0,0,.2)}
.mzoom button{width:32px;height:32px;border:0;background:none;font-size:18px;cursor:pointer}
.mattr{position:absolute;right:0;bottom:0;background:rgba(255,255,255,.8);font-size:11px;padding:1px 6px}
.msel{position:absolute;right:12px;top:12px;background:#fff;border-radius:8px;box-shadow:0 2px 8px rgba(0,0,0,.2);padding:10px 12px;display:flex;flex-direction:column;gap:6px;font-size:12px;cursor:default}
.msel[hidden],.mprev[hidden]{display:none}
.msel input{width:70px;height:26px;border:1px solid #ccd3dc;border-radius:4px;padding:0 6px}
.msel label{display:flex;justify-content:space-between;gap:8px;align-items:center}
.mprev{position:absolute;width:240px;background:#0f1722;color:#fff;border-radius:6px;overflow:hidden;box-shadow:0 2px 10px rgba(0,0,0,.4);pointer-events:none;font-size:12px}
.mprev b{display:block;padding:4px 8px}.mprev img{display:block;width:100%;aspect-ratio:16/9;object-fit:cover;background:#1a2433}
.mstrip[hidden]{display:none}
#march[hidden],.tf .arch[hidden]{display:none}
.strip{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,240px));gap:8px;padding:10px 12px}
.tile.over{border-color:#2fb344}
.fl{display:flex;flex-direction:column;gap:4px;font-size:13px;font-weight:500;margin-bottom:10px}
.addlist{max-height:300px;overflow:auto;margin-bottom:14px}
.gridtools,.maptools{display:flex;gap:8px;align-items:center;flex-wrap:nowrap}
.maptools[hidden],.gridtools[hidden],.lgrid[hidden],#meditbar[hidden],#medit[hidden]{display:none}
.lnvr{border-bottom:1px solid #eef1f4}
.lcam{display:flex;align-items:center;gap:8px;width:calc(100% - 16px);margin:1px 8px;padding:0 10px 0 18px;min-height:32px;border:0;border-radius:6px;background:transparent;color:#182433;font:inherit;font-size:13px;text-align:left;cursor:pointer}
.lcam:hover{background:#f1f4f8}
.lcam .ln{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.lcam .shown{color:#066fd1;visibility:hidden;display:flex}
.lcam.on{background:#eef5fd}.lcam.on .shown{visibility:visible}
.ltool{display:flex;flex-wrap:wrap;align-items:center;gap:10px}
.ltool .note{flex:1}
.lgrp{display:flex;gap:4px}
.lstage{display:flex;gap:12px;align-items:flex-start;position:relative}
.lstage>.lgrid{flex:1 1 auto;min-width:0}
.lptz{flex:none;width:224px;background:#fff;border:1px solid #dce1e7;border-radius:8px;padding:0 0 10px;display:flex;flex-direction:column;user-select:none}
.lptz[hidden],.pzaux[hidden],.pzbody [hidden]{display:none}
.pzh{display:flex;align-items:center;padding:10px 8px 6px 14px}
.pzh h2{margin:0;flex:1;font-size:14px;font-weight:600}
.pzx{width:28px;height:28px;border:0;border-radius:6px;background:none;color:#5b6878;font-size:18px;cursor:pointer}
.pzx:hover{background:#f1f4f8}
.pzcam{padding:0 14px 10px;font-size:12px;color:#5b6878;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;border-bottom:1px solid #eef1f4}
.pzcam b{color:#182433;font-weight:600}
.pzbody{display:flex;flex-direction:column;gap:6px;padding:10px 12px 0}
.pzbody.off{opacity:.45;pointer-events:none}
/* the lens: zoom, focus and iris side by side, each with + above and − below */
.pzctl{display:flex;flex-wrap:wrap;align-items:center;justify-content:center;gap:10px 14px}
.pzlens{display:flex;gap:4px}
.pzl{display:flex;flex-direction:column;width:54px;border:1px solid #dce1e7;border-radius:8px;overflow:hidden;background:#fff}
.pzl button{height:36px;border:0;background:#f6f8fb;color:#182433;font-size:18px;line-height:1;cursor:pointer;touch-action:none}
.pzl button:hover{background:#e6f0fb}
.pzli{display:flex;flex-direction:column;align-items:center;gap:2px;padding:6px 0 5px;border-block:1px solid #eef1f4;color:#44505f}
.pzli svg{width:18px;height:18px;fill:none;stroke:currentColor;stroke-width:1.4;stroke-linecap:round;stroke-linejoin:round}
.pzli small{max-width:100%;font-size:10px;letter-spacing:-.01em;line-height:1.1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.lptz button.held{background:#066fd1;color:#fff}
.pzmove{display:flex;align-items:center;gap:8px}
.pzpad{display:grid;grid-template-columns:repeat(3,44px);grid-template-rows:repeat(3,44px);border-radius:50%;background:radial-gradient(circle,#e8ebef 0 56%,#f1f4f8 57%);border:1px solid #dce1e7;padding:6px}
.pzd{border:0;background:none;color:#44505f;cursor:pointer;display:flex;align-items:center;justify-content:center;border-radius:50%;touch-action:none}
.pzd svg{width:16px;height:16px;fill:currentColor;transform:rotate(var(--r))}
.pzd:hover{color:#066fd1;background:rgba(6,111,209,.08)}
.pzd.held{color:#fff}
.pzstop{border:0;border-radius:50%;background:#44505f;color:#fff;font-size:11px;font-weight:700;letter-spacing:.02em;cursor:pointer;box-shadow:0 1px 3px rgba(24,36,51,.3)}
.pzstop:hover{background:#d63939}
.pzspeed{display:flex;flex-direction:column;align-items:center;gap:4px;font-size:11px;color:#5b6878}
.pzspeed input{writing-mode:vertical-lr;direction:rtl;width:20px;height:120px;margin:0;accent-color:#066fd1}
.pzspeed output{font-weight:600;color:#182433}
.pzspi{display:none;flex:none;width:20px;height:20px;fill:none;stroke:#5b6878;stroke-width:1.4;stroke-linecap:round;stroke-linejoin:round}
.pzauxs{display:grid;grid-template-columns:repeat(4,1fr);gap:4px;margin-top:6px}
.pzauxs:empty,.pzauxs.none{display:none}
.pzaux{height:32px;border:1px solid #dce1e7;border-radius:6px;background:#f6f8fb;color:#44505f;cursor:pointer;display:flex;align-items:center;justify-content:center;touch-action:none}
.pzaux svg{width:17px;height:17px;fill:none;stroke:currentColor;stroke-width:1.4;stroke-linecap:round;stroke-linejoin:round}
.pzaux:hover{border-color:#066fd1;color:#066fd1}
.pzaux[aria-pressed=true]{background:#fff4d6;border-color:#f59f00;color:#8a4b08}
.lptz button:focus-visible{outline:2px solid #066fd1;outline-offset:1px}
.pzmsg{margin:8px 12px 0;font-size:12px;color:#5b6878;min-height:16px;overflow-wrap:anywhere}
.pzmsg.err{color:#a3222b}
.pzorient{margin-top:4px;border-top:1px solid #eef1f4;padding-top:6px;font-size:12px}
.pzorient summary{cursor:pointer;color:#44505f;font-weight:600;display:flex;align-items:center;gap:6px;min-height:24px}
.pzorient .chk{display:flex;align-items:center;gap:6px;margin:6px 0 0;font-size:12px;color:#182433}
.pzflag:not(:empty){background:#fff4d6;color:#8a4b08;border-radius:10px;padding:0 6px;font-size:11px;font-weight:600}
.pznote{margin:6px 0 0;color:#5b6878;font-size:11px}
.pznote:empty{display:none}
#pzswapsave{margin-top:6px;width:100%}
#pzswapsave[hidden],#pzshow[hidden]{display:none}
@media (max-width:900px){.lstage{flex-wrap:wrap}.lptz{width:100%}.pzmove{justify-content:center}}
.lgrid{display:grid;grid-template-columns:repeat(var(--cols,2),1fr);gap:4px;background:#0f1722;padding:4px;border-radius:8px;min-height:300px}
/* full screen: the biggest 16:9 tiles that let the whole grid fit both ways, centred
   (a wide screen gets bars left and right, a tall one above and below) */
.lgrid:fullscreen{border-radius:0;padding:4px;
 --tw:min(calc((100vw - 8px - (var(--cols) - 1) * 4px) / var(--cols)), calc((100vh - 8px - (var(--cols) - 1) * 4px) / var(--cols) * 16 / 9));
 grid-template-columns:repeat(var(--cols),var(--tw));grid-auto-rows:calc(var(--tw) * 9 / 16);place-content:center}
.lgrid:fullscreen .tile{aspect-ratio:auto;height:100%}
.lgrid.big>.tile:not(.zoomed){display:none}
/* the zoomed tile: ‹ › to the previous and the next camera of the grid, a sideways swipe too */
.lgrid.big>.tile.zoomed{touch-action:pan-y pinch-zoom}
.tile .znav{position:absolute;top:50%;transform:translateY(-50%);z-index:3;width:40px;height:64px;padding:0;border:0;border-radius:6px;background:rgba(15,23,34,.55);color:#fff;font-size:30px;line-height:1;cursor:pointer;display:flex;align-items:center;justify-content:center}
.tile .znav.prev{left:6px}.tile .znav.next{right:6px}
.tile .znav:active{background:rgba(6,111,209,.85)}
@media (hover:hover) and (pointer:fine){.tile .znav{opacity:0;transition:opacity .15s}.tile.zoomed:hover .znav,.tile .znav:focus-visible{opacity:1}}
.tile{position:relative;aspect-ratio:16/9;background:#1a2433;border:2px solid transparent;border-radius:4px;overflow:hidden;cursor:pointer}
.tile.sel{border-color:#066fd1}
.tile video{width:100%;height:100%;object-fit:contain;background:#111a26;display:block}
.tile video.back{position:absolute;inset:0;opacity:0;pointer-events:none;z-index:0}
.tile .th{position:absolute;inset:0 0 auto;display:flex;align-items:center;gap:8px;padding:4px 8px;background:rgba(15,23,34,.75);color:#fff;font-size:12px;z-index:2}
.tile .th .tr{margin-left:auto;color:#b8c4d4;font-size:11px}
.tile .live{background:#d63939;border-radius:3px;padding:0 5px;font-size:10px;font-weight:700}
.tile .tcb{border:0;background:#f59f00;color:#1a1300;border-radius:3px;padding:0 5px;font:700 10px/1.5 inherit;cursor:help;white-space:nowrap}
.tile .tcb[hidden]{display:none}
.tile .tf{position:absolute;inset:auto 0 0;display:flex;align-items:center;padding:4px 8px;color:#fff;font-size:11px;z-index:2;font-family:ui-monospace,monospace;background:linear-gradient(transparent,rgba(15,23,34,.7))}
.tile .tb{margin-left:auto;display:flex;gap:4px}
.tile .tb button,.tile .tb a{width:28px;height:26px;border:0;border-radius:4px;background:rgba(15,23,34,.8);color:#fff;font-size:12px;cursor:pointer;display:flex;align-items:center;justify-content:center;text-decoration:none}
.tile .tb button.q{width:auto;padding:0 6px;font-weight:700;font-family:inherit}
.tile .tb button:disabled{opacity:.5;cursor:default}
.tile .nsg{position:absolute;inset:0;display:none;align-items:center;justify-content:center;color:#f1a5a9;font-weight:600;font-size:13px;text-align:center;padding:24px}
.tile.nosig{background:#2a1a1e}.tile.nosig .nsg,.tile.empty .nsg{display:flex}
.tile.empty .nsg{color:#6b7a8c;font-weight:400}
.btn.sm{height:28px;padding:0 10px;font-size:12px}
.btn.primary{background:#066fd1;border-color:#066fd1;color:#fff;font-weight:600}
.btn.primary:hover{background:#0559a8;color:#fff}
.btn:disabled{opacity:.5;cursor:not-allowed}
aside.srcs{padding-bottom:12px}
.srch{padding:14px 14px 8px;font-weight:700;font-size:15px;background:#f6f8fb;border-bottom:1px solid #e3e7ec}
.srcg{padding:12px 16px 4px;font-size:11px;font-weight:700;letter-spacing:.06em;text-transform:uppercase;color:#5b6878}
.src{display:flex;align-items:center;gap:10px;margin:1px 8px;padding:0 10px;min-height:36px;border-radius:6px;text-decoration:none;color:#182433;font-size:13px}
.src:hover{background:#f1f4f8}
.src[aria-current=page]{background:#e6f0fb;color:#0559a8;font-weight:600;box-shadow:inset 3px 0 0 #066fd1}
.tabs{display:flex;gap:2px;background:#e8ebef;border-radius:8px;padding:3px;align-self:flex-start}
.tabs a{padding:7px 14px;border-radius:6px;text-decoration:none;color:#44505f;font-size:13px;font-weight:500}
.tabs a[aria-selected=true]{background:#fff;color:#182433;font-weight:600;box-shadow:0 1px 2px rgba(24,36,51,.12)}
.card-form{margin:0}
.fbody{display:flex;flex-wrap:wrap;gap:20px;padding:16px}
fieldset.cams{flex:1 1 240px;max-width:340px;border:1px solid #e3e7ec;border-radius:8px;margin:0;padding:8px 12px 10px;display:flex;flex-direction:column;gap:2px;max-height:340px;overflow:auto}
fieldset.cams legend{font-weight:600;font-size:13px;padding:0 4px}
.chk{display:flex;align-items:center;gap:8px;min-height:30px;font-size:13px;cursor:pointer}
.chk.all{border-bottom:1px solid #eef1f4;margin-bottom:4px}
.chk input{width:16px;height:16px;accent-color:#066fd1}
.fcol{flex:999 1 420px;display:flex;flex-direction:column;gap:14px}
.range,.dest{display:flex;flex-wrap:wrap;gap:12px;align-items:flex-end}
.range label,.dest label,.blocks label{display:flex;flex-direction:column;gap:4px;font-size:13px;font-weight:500}
.in{height:36px;border:1px solid #ccd3dc;border-radius:6px;padding:0 10px;font:inherit;background:#fff;color:#182433}
.in.sm{height:30px}
.quick{display:flex;gap:6px}
.split{display:flex;flex-wrap:wrap;gap:16px;align-items:center}
.blocks{display:flex;gap:8px;align-items:flex-end}
.blocks label{flex-direction:row;align-items:center;gap:8px}
.actions{display:flex;justify-content:space-between;align-items:center;gap:12px;border-top:1px solid #eef1f4;padding-top:12px}
.bar2{display:flex;flex-wrap:wrap;align-items:flex-end;justify-content:space-between;gap:12px;padding:12px 16px;background:#f6f8fb;border-bottom:1px solid #e8ebef}
.note.warn{color:#8a4b08}
.btn.danger{border-color:#e7b4b8;color:#a3222b}
.btn.danger:hover{background:#fdf1f1;color:#a3222b}
.flash{border-radius:8px;padding:12px 16px;font-size:13px}
.flash.ok{background:#e9f7ec;border:1px solid #b9e2c2;color:#1a6b2c}
.flash.err{background:#fdf1f1;border:1px solid #f1c4c6;color:#7f1d22}
dialog{border:1px solid #dce1e7;border-radius:10px;padding:20px 22px;max-width:440px;box-shadow:0 10px 30px rgba(24,36,51,.25);color:#182433}
dialog::backdrop{background:rgba(24,36,51,.45)}
dialog h2{margin:0 0 8px;font-size:16px}
dialog p{margin:0 0 18px;font-size:13px;color:#44505f}
.dbtn{display:flex;justify-content:flex-end;gap:8px}
/* the synchronous player (Archive › Playback): the grid of Live over a timeline */
.pbstage{display:flex;gap:12px;align-items:flex-start}
.pbmain{flex:1 1 auto;min-width:0;background:#0f1722;border-radius:8px;overflow:hidden;display:flex;flex-direction:column}
.pbmain>.lgrid{border-radius:0;min-height:240px}
.pbmain:fullscreen{border-radius:0}
.pbmain:fullscreen>.lgrid{flex:1 1 auto;min-height:0;grid-auto-rows:1fr}
.pbmain:fullscreen>.lgrid .tile{aspect-ratio:auto;min-height:0}
.pbbar{border-top:1px solid #22324a;color:#dfe6ee;padding:8px 10px 8px;display:flex;flex-direction:column;gap:8px}
.pbctl{display:flex;flex-wrap:wrap;align-items:center;gap:8px 14px}
.pbgrp{display:flex;align-items:center;gap:4px}
.pbzoom{margin-left:auto}
.pbbtn{height:30px;min-width:34px;padding:0 8px;border:1px solid #33455e;border-radius:6px;background:#1a2433;color:#dfe6ee;cursor:pointer;display:inline-flex;align-items:center;justify-content:center;font-size:13px;white-space:nowrap}
.pbbtn:hover{background:#22324a;border-color:#4b6283}
.pbbtn:focus-visible,.pbbar .tabs a:focus-visible,#pbcanvas:focus-visible{outline:2px solid #4ea1f0;outline-offset:1px}
.pbbtn.main{min-width:46px;background:#066fd1;border-color:#066fd1;color:#fff;font-size:15px}
.pbbtn.main:hover{background:#0559a8}
.pbclock{font-size:15px;color:#ffd166;min-width:160px}
.pbspan{min-width:52px;text-align:center;font-size:12px;color:#b8c4d4}
.pbbar .tabs{background:#1a2433;align-self:center}
.pbbar .tabs a{color:#b8c4d4;padding:4px 9px}
.pbbar .tabs a[aria-selected=true]{background:#33455e;color:#fff;box-shadow:none}
.pbtl{display:flex;align-items:flex-start}
.pbnames{flex:none;width:150px;padding-top:24px}
.pbnames div{height:20px;margin-bottom:4px;padding:0 8px;font-size:12px;line-height:20px;color:#b8c4d4;overflow:hidden;white-space:nowrap;text-overflow:ellipsis;cursor:pointer;border-radius:3px}
.pbnames div.sel{color:#fff;background:#1a2433;box-shadow:inset 3px 0 0 #066fd1}
.pbnames div.empty{color:#5b6878;cursor:default}
.pbtlc{flex:1 1 auto;min-width:0;position:relative}
#pbcanvas{display:block;width:100%;height:60px;touch-action:none;cursor:pointer}
#pbcanvas.pan{cursor:grabbing}
.pbtip{position:absolute;top:0;transform:translateX(-50%);background:#ffd166;color:#182433;font-size:11px;font-weight:600;padding:1px 6px;border-radius:3px;pointer-events:none;white-space:nowrap}
.pbtip[hidden]{display:none}
.pbleg{display:flex;flex-wrap:wrap;align-items:center;gap:4px 16px;font-size:11px;color:#8a96a5}
.pbleg i{display:inline-block;width:14px;height:8px;border-radius:2px;margin-right:6px;vertical-align:-1px}
.pbleg .lrec{background:#2fb344}.pbleg .lmv{background:#d63939}.pbleg .lsel{background:rgba(78,161,240,.45);box-shadow:inset 0 0 0 1px #4ea1f0}.pbleg .lcur{width:3px;background:#ffd166}
.pbleg .pbkeys{margin-left:auto}
.tile .pbtag{background:#066fd1;border-radius:3px;padding:0 5px;font-size:10px;font-weight:700;font-family:ui-monospace,monospace}
.tile.norec .nsg,.tile.wait .nsg{display:flex;color:#8a96a5;font-weight:400}
.tile.norec video,.tile.wait video{visibility:hidden}
.tile.wait .nsg::before{content:"";width:22px;height:22px;margin-right:10px;border:3px solid #44505f;border-top-color:#fff;border-radius:50%;animation:spin .8s linear infinite;flex:none}
.tile .tb .snd.on{background:#066fd1}
.tile .tb a[hidden],.tile .tb button[hidden]{display:none}
.pbcal{flex:none;width:224px;background:#fff;border:1px solid #dce1e7;border-radius:8px;padding-bottom:12px}
.calnav{display:flex;align-items:center;padding:0 8px 4px}
.calnav b{flex:1;text-align:center;font-size:13px;font-weight:600}
.cal{display:grid;grid-template-columns:repeat(7,1fr);gap:2px;padding:0 8px}
.cal span{font-size:11px;color:#5b6878;text-align:center;padding:4px 0 2px}
.cal button{position:relative;height:28px;padding:0;border:0;border-radius:6px;background:none;font:inherit;font-size:12px;color:#182433;cursor:pointer}
.cal button:hover{background:#f1f4f8}
.cal button.out{color:#aab4c0}
.cal button.rec::after{content:"";position:absolute;left:50%;bottom:3px;width:4px;height:4px;margin-left:-2px;border-radius:50%;background:#2fb344}
.cal button.today{box-shadow:inset 0 0 0 1px #066fd1}
.cal button.cur{background:#066fd1;color:#fff;font-weight:600}
.cal button.cur::after{background:#fff}
.calfoot{display:flex;flex-direction:column;gap:10px;padding:10px 12px 0;margin-top:8px;border-top:1px solid #eef1f4}
.calfoot .btn{align-self:flex-start}
.calfoot .fl{margin:0}
.calfoot p{margin:0}
#pbxlist{display:flex;flex-direction:column;gap:2px}
#pbexport .flash[hidden]{display:none}
@media (max-width:1100px){.pbstage{flex-wrap:wrap}.pbcal{width:100%}.pbnames{width:110px}}
/* the drawer of narrow screens: the brand, who is signed in, the alerts and signing out */
dialog.drawer{position:fixed;inset:0 auto 0 0;margin:0;width:min(340px,88vw);max-width:none;height:var(--vh);max-height:none;padding:0;border:0;border-radius:0 12px 12px 0;background:#fff;color:#182433;box-shadow:6px 0 28px rgba(10,15,24,.35);overflow:hidden}
dialog.drawer::backdrop{background:rgba(10,15,24,.55)}
dialog.drawer[open]{animation:drin .22s cubic-bezier(.2,.8,.2,1)}
dialog.drawer[open]::backdrop{animation:drfade .22s ease-out}
dialog.drawer.closing{animation:drout .2s ease-in forwards}
dialog.drawer.closing::backdrop{animation:drfade .2s ease-in reverse forwards}
@keyframes drin{from{transform:translateX(-100%)}}
@keyframes drout{to{transform:translateX(-100%)}}
@keyframes drfade{from{opacity:0}}
html:has(dialog.drawer[open]){overflow:hidden}
.dwin{display:flex;flex-direction:column;height:100%}
.dhead{flex:none;display:flex;align-items:center;gap:8px;height:var(--hh);padding:0 6px 0 16px;background:#182433;color:#fff}
.dhead .logo{flex:1;min-width:0}.dhead .logo b{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dclose{width:44px;height:44px;border:0;border-radius:8px;background:none;color:#c5d0de;cursor:pointer;display:flex;align-items:center;justify-content:center}
.dclose:hover{background:#22324a;color:#fff}
/* the side menu of the page, moved here on narrow screens: it scrolls on its own */
.dsec{flex:1;min-height:0;display:flex;flex-direction:column}
.dsec>aside{flex:1;min-height:0;overflow-y:auto;overscroll-behavior:contain;background:#fff}
.dsec>aside.lside{overflow:hidden}
.dsec .lcam,.dsec .mrow,.dsec .lview .lvn,.dsec .items a,.dsec .src{min-height:42px}
.dfoot{flex:none;border-top:1px solid #dce1e7;background:#f6f8fb;padding:6px 8px 10px}
.dalert{display:flex;align-items:center;gap:12px;min-height:44px;padding:0 10px;border-radius:8px;color:#182433;text-decoration:none;font-weight:500}
.dalert:hover{background:#e8ebef;color:#182433}
.dalert[aria-current=page]{background:#e6f0fb;color:#0559a8}
.dalert svg{flex:none;color:#44505f}.dalert .count{margin-left:auto}
.dout{display:flex;align-items:center;gap:10px;margin:4px 0 0;padding:0 2px 0 10px}
.duser{flex:1;min-width:0;display:flex;align-items:center;gap:12px;color:#44505f;font-size:13px}
a.duser{text-decoration:none}a.duser:hover b{text-decoration:underline}
.duser svg{flex:none}.duser b{color:#182433;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dout button{flex:none;height:40px;padding:0 16px;border:1px solid #ccd3dc;border-radius:8px;background:#fff;color:#a3222b;font-weight:600;cursor:pointer}
.dout button:hover{background:#fdf1f1;border-color:#e7b4b8}
.dclose:focus-visible,.dalert:focus-visible,.dout button:focus-visible{outline:2px solid #066fd1;outline-offset:1px}
@media (prefers-reduced-motion:reduce){dialog.drawer,dialog.drawer::backdrop{animation:none!important}}
/* touch screens have no keys to learn */
@media (hover:none){.pbleg .pbkeys{display:none}}
/* narrow screens: the header keeps the menu button and the modules; the side menu goes
   into the drawer (without <dialog>: over the content, half the screen high at most) */
@media (max-width:900px){
 header{padding:0;gap:0}
 header>.logo,header>.right{display:none}
 .menubtn{display:flex}
 nav.mods{flex:1}
 nav.mods a{flex:1 1 0;justify-content:center;padding:0 8px;min-width:0}
 .cols{flex-direction:column;align-items:stretch}
 .cols>aside{position:static;flex:none;width:auto;height:auto;max-height:calc(var(--vh) * .5);border-right:0;border-bottom:1px solid #dce1e7;overscroll-behavior:auto}
 .drawer-ok .cols>aside{display:none}
 main{flex:1 0 auto;padding:12px 12px 20px}
 .gridtools{flex-wrap:wrap}
 .tabs{max-width:100%;overflow-x:auto}
 .tabs a{white-space:nowrap}
 .upd{margin-left:0}
}
/* PTZ on narrow screens: the lens buttons (icons only, named by their hint) left of the
   pad, the speed as a horizontal slider under both */
@media (max-width:900px){
 .pzctl{display:grid;grid-template-columns:auto auto;justify-content:center;align-items:center;gap:14px 16px}
 .pzmove:not([hidden]){display:contents}
 .pzlens{gap:5px}
 .pzl{width:46px}.pzl button{height:44px;font-size:20px}
 .pzli{padding:9px 0}.pzli small{display:none}.pzli svg{width:20px;height:20px}
 .pzspeed{grid-column:1/-1;flex-direction:row;align-items:center;gap:10px;width:100%;padding:0 4px;font-size:13px}
 .pzspeed input{writing-mode:horizontal-tb;direction:ltr;flex:1;min-width:0;width:auto;height:28px}
 .pzspeed output{min-width:2ch;text-align:right}
 .pzspi{display:block}
}
@media (max-width:400px){
 .pzpad{grid-template-columns:repeat(3,40px);grid-template-rows:repeat(3,40px)}
 .pzl{width:42px}.pzctl{column-gap:12px}
}
@media (max-width:560px){
 nav.mods a{flex-direction:column;gap:3px;padding:7px 2px 4px;font-size:12px}
 nav.mods a .count{position:absolute;top:6px;left:calc(50% + 6px);font-size:10px;padding:0 5px;line-height:15px}
 .pbnames{width:84px}

 .pbclock{min-width:0}
 .lstage>.lgrid{min-height:0}
}
/* the toolbar on narrow screens: the tabs and the full-screen button on the first line,
   the hint on its own full-width line under them, the other tools below */
@media (max-width:900px){
 .ltool{gap:8px 10px}
 .ltool>.tabs{flex:0 1 auto;min-width:0}
 #lfull,#pbfull{order:2;margin-left:auto}
 .ltool>.note{order:3;flex:1 1 100%;line-height:1.4}
 .ltool>.gridtools,.ltool>.maptools{order:4}
}
/* thumbnails on narrow screens: two (or more) columns of cards, the picture on top and
   the caption under it instead of over it; the state is a coloured dot before the time */
@media (max-width:900px){
 .lthumbs{grid-template-columns:repeat(auto-fill,minmax(140px,1fr));gap:6px;padding:6px}
 .lthumbs .tile{display:flex;flex-direction:column;border-radius:6px}
 .lthumbs .tile .th,.lthumbs .tile .tf{position:static;inset:auto;background:none}
 .lthumbs .tile .th{order:2;padding:6px 8px 0;font-size:13px;font-weight:600;line-height:1.3}
 .lthumbs .tile .tn{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .lthumbs .tile .tn small{font-weight:400}
 .lthumbs .tile .tr{display:none}
 .lthumbs .tile img,.lthumbs .tile .none{order:1}
 .lthumbs .tile .tf{order:3;gap:6px;padding:2px 8px 7px;font-family:inherit;font-size:11px;color:#b8c4d4}
 .lthumbs .tile .tt{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
 .lthumbs .tile .tf .badge{order:-1;flex:none;width:8px;height:8px;margin:0;padding:0;border-radius:50%;font-size:0;line-height:0;overflow:hidden}
 .lthumbs .tile .tf .badge.ok{background:#2fb344}
 .lthumbs .tile .tf .badge.err{background:#d63939}
 .lthumbs .tile .tf .badge.mute{background:#8a96a5}
}
.lthumbs .tile .badge[hidden]{display:none}
`

// icons of the mockup (paths of 24×24 stroke icons)
var icons = map[string]string{
	"live":    `<rect x="3" y="3" width="8" height="8" rx="1"></rect><rect x="13" y="3" width="8" height="8" rx="1"></rect><rect x="3" y="13" width="8" height="8" rx="1"></rect><rect x="13" y="13" width="8" height="8" rx="1"></rect>`,
	"archive": `<rect x="3" y="4" width="18" height="5" rx="1"></rect><path d="M5 9v10a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9M10 13h4"></path>`,
	"system":  `<path d="M3 12h4l3 8 4-16 3 8h4"></path>`,
	"bell":    `<path d="M10 5a2 2 0 1 1 4 0 7 7 0 0 1 4 6v3l2 3H4l2-3v-3a7 7 0 0 1 4-6"></path><path d="M9 17v1a3 3 0 0 0 6 0v-1"></path>`,
	"nvr":     `<path d="M3 5h18v6H3zM3 13h18v6H3z"></path>`,
	"storage": `<path d="M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"></path>`,
	"cam":     `<path d="M15 10l5-3v10l-5-3zM3 7h12v10H3z"></path>`,
	"usr":     `<path d="M9 4a4 4 0 1 1 0 8 4 4 0 0 1 0-8zM3 21v-1a6 6 0 0 1 12 0v1"></path>`,
	"net":     `<path d="M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM3 12h18M12 3c3 3 3 15 0 18"></path>`,
	"srv":     `<path d="M14 7l3-3 3 3-3 3M4 20l9-9"></path>`,
	"chev":    `<path d="M6 9l6 6 6-6"></path>`,
	"menu":    `<path d="M4 7h16M4 12h16M4 17h16"></path>`,
	"x":       `<path d="M6 6l12 12M18 6L6 18"></path>`,
	"eye":     `<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"></path><circle cx="12" cy="12" r="3"></circle>`,
}

func icon(name string, size int, class string) template.HTML {
	sw := "2"
	if name == "chev" {
		sw = "2.5"
	}
	return template.HTML(fmt.Sprintf(`<svg class="%s" width="%d" height="%d" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="%s" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">%s</svg>`,
		class, size, size, sw, icons[name]))
}

var systemTmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"icon": icon,
	"list": func(v ...any) []any { return v },
	"pair": func(a, b any) map[string]any { return map[string]any{"A": a, "B": b} },
	// the parts of a motion bar: "1" motion, "0" none; "" while it is made
	"bar": func(parts string) []string {
		out := make([]string, modules.MotionParts)
		for i := range out {
			out[i] = "wait"
			if i < len(parts) {
				out[i] = map[byte]string{'1': "mv", '0': "still"}[parts[i]]
			}
		}
		return out
	},
	"pct": func(f float64) string { return fmt.Sprintf("%.0f", f) },
	// the table of events: on the event log (full: every column, acknowledging) or a recorder's page
	"evTable": func(t map[string]string, v *eventsView, full bool) map[string]any {
		return map[string]any{"T": t, "Rows": v.Rows, "Full": full, "Ack": full && v.CanAck, "Play": !full && v.Archive}
	},
	// a tile of a recorded file (the Archive's search, a camera's page)
	"tile": func(o tileOpts, f fileRow) map[string]any { return map[string]any{"O": o, "F": f} },
	// a kind as the panel names it: the system log's are translated
	"kindLabel": func(t map[string]string, k string) string {
		if l := t["sk_"+k]; l != "" {
			return l
		}
		return k
	},
	"barKind": func(p float64, kind string) string {
		switch {
		case kind == kErr:
			return "err"
		case p >= 85:
			return "warn"
		}
		return ""
	},
}).Parse(`
{{define "layout"}}<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — {{.Brand}}</title>
<style>` + systemCSS + `</style>
<script nonce="{{.Nonce}}">
// narrow screens: the side menu lives in the drawer (see the end of the page)
window.ffnvrNav={mq:matchMedia('(max-width: 900px)'),narrow:function(){return !!window.HTMLDialogElement&&this.mq.matches},open:function(){}};
if(window.HTMLDialogElement)document.documentElement.classList.add('drawer-ok');
</script>
</head>
<body>
<div class="app">
 <header>
  <button type="button" class="menubtn" id="menubtn" aria-label="{{.T.menu_open}}" aria-controls="drawer" aria-expanded="false" aria-haspopup="dialog">{{icon "menu" 22 ""}}{{if .Sys.Alerts}}<span class="mdot" aria-hidden="true"></span>{{end}}</button>
  <div class="logo"><i>{{icon "eye" 17 ""}}</i><b>{{.Brand}}</b></div>
  <nav class="mods" aria-label="Modules">
   {{if index .Can "live"}}<a href="/?p=live"{{if eq .Module "live"}} aria-current="page"{{end}}>{{icon "live" 18 ""}}{{.T.live}}</a>{{end}}
   {{if index .Can "archive"}}<a href="/?p=archive"{{if eq .Module "archive"}} aria-current="page"{{end}}>{{icon "archive" 18 ""}}{{.T.archive}}</a>{{end}}
   {{if index .Can "system"}}<a href="/?p=dash"{{if eq .Module "system"}} aria-current="page"{{end}}>{{icon "system" 18 ""}}{{.T.system}}{{with len .Sys.Alerts}} <span class="count">{{.}}</span>{{end}}</a>{{end}}
  </nav>
  <div class="right">
   <a class="bell" href="/?p=alerts" aria-label="{{.T.active_alerts}}">{{icon "bell" 20 ""}}{{if .Sys.Alerts}}<span></span>{{end}}</a>
   <form class="who" method="post" action="/logout">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <a class="me" href="/?p=account" title="{{.T.acc_title}}"{{if eq .Page "account"}} aria-current="page"{{end}}>{{.T.signed_in_as}} <b>{{.User}}</b></a>
    <button type="submit">{{.T.signout}}</button>
   </form>
  </div>
 </header>
 <dialog class="drawer" id="drawer" aria-label="{{.T.menu_open}}">
  <div class="dwin">
   <div class="dhead"><div class="logo"><i>{{icon "eye" 17 ""}}</i><b>{{.Brand}}</b></div><button type="button" class="dclose" id="drawerx" aria-label="{{.T.menu_close}}">{{icon "x" 20 ""}}</button></div>
   <div class="dsec" id="dsec"></div>
   <div class="dfoot">
    <a class="dalert" href="/?p=alerts"{{if eq .Page "alerts"}} aria-current="page"{{end}}>{{icon "bell" 18 ""}}<span>{{.T.active_alerts}}</span>{{with len .Sys.Alerts}}<span class="count">{{.}}</span>{{end}}</a>
    <form class="dout" method="post" action="/logout">
     <input type="hidden" name="csrf" value="{{.CSRF}}">
     <a class="duser" href="/?p=account" title="{{.T.acc_title}}">{{icon "usr" 18 ""}}<b>{{.User}}</b></a>
     <button type="submit">{{.T.signout}}</button>
    </form>
   </div>
  </div>
 </dialog>
 <div class="cols">
  {{if eq .Module "system"}}<aside aria-label="{{.T.system}}">
   {{range .Groups}}<details{{if .Open}} open{{end}}>
    <summary>{{icon .Icon 16 "ic"}}<span style="flex:1">{{.Name}}</span>{{if .Badge}}<span class="gbadge">{{.Badge}}</span>{{end}}{{icon "chev" 14 "chev"}}</summary>
    <div class="items">{{range .Items}}{{if .Header}}<div class="ihead">{{.Name}}</div>{{else}}<a href="{{.Href}}"{{if .Dot}} class="nvr"{{end}}{{if .Current}} aria-current="page"{{end}}>{{if .Dot}}<span class="dot {{.Dot}}" aria-hidden="true"></span>{{end}}<span>{{.Name}}</span>{{if .Note}}<span class="inote">{{.Note}}</span>{{end}}</a>{{end}}{{end}}</div>
   </details>{{end}}
  </aside>{{else if eq .Module "live"}}{{with .Live}}<aside aria-label="{{$.T.lv_tree}}" class="srcs lside">
   <div class="lgridside">
    <div class="srch" style="display:flex;align-items:center">{{$.T.lv_tree}}<span class="note" style="margin-left:auto">{{.Online}} / {{.Total}}</span></div>
    <div style="padding:10px 10px 6px"><input class="in" id="lsearch" type="search" placeholder="{{$.T.lv_search}}" style="width:100%"></div>
    <div class="lscroll">
    {{range .Nvrs}}<details open class="lnvr">
     <summary><span style="flex:1">{{.Name}}</span><span class="badge {{.Kind}}">{{.Status}}</span>{{icon "chev" 14 "chev"}}</summary>
     {{range .Cams}}<button type="button" class="lcam" draggable="true" data-n="{{.Nvr}}" data-c="{{.Cam}}" data-label="{{.Label}}" title="{{.State}}">
      <span class="dot {{.Kind}}" aria-hidden="true"></span><span class="ln">{{.Label}}</span><span class="note">{{.Ch}}</span><span class="shown" aria-hidden="true">{{icon "eye" 14 ""}}</span></button>{{end}}
     {{if not .Cams}}<div class="note" style="padding:4px 16px 8px">{{$.T.lv_no_live}}</div>{{end}}
    </details>{{end}}
    </div>
    <details class="lviews" open><summary><span style="flex:1">{{$.T.lv_views}}</span>{{icon "chev" 14 "chev"}}</summary><div id="lviews"></div></details>
   </div>
   <div class="lthumbside" hidden>
    <div style="padding:10px 10px 6px"><input class="in" id="tsearch" type="search" placeholder="{{$.T.lv_search}}" style="width:100%"></div>
    <label class="chk" style="padding:2px 16px 8px"><input type="checkbox" id="tbad"> {{$.T.tb_only_bad}}</label>
    <div class="srch">{{$.T.tb_sched}}</div>
    <div class="tinfo" id="tinfo"></div>
   </div>
   <div class="lmapside" hidden>
    <div class="srch" style="display:flex">{{$.T.mp_on_map}}<span class="note" style="margin-left:auto" id="moncount"></span></div><div id="mcams" class="mlist lscroll"></div>
    <div class="srch">{{$.T.mp_layers}}</div>
    <div class="mlayers"><label class="chk"><input type="checkbox" id="lyfov" checked> {{$.T.mp_fov}}</label><label class="chk"><input type="checkbox" id="lynames" checked> {{$.T.mp_names}}</label><label class="chk"><input type="checkbox" id="lystreet" checked> {{$.T.mp_street}}</label></div>
   </div>
  </aside>{{end}}{{else if and (eq .Module "archive") .Archive (eq .Archive.Tab "play")}}{{with .Archive}}<aside aria-label="{{$.T.lv_tree}}" class="srcs lside">
   <div class="lgridside">
    <div class="srch">{{$.T.lv_tree}}</div>
    <div style="padding:10px 10px 6px"><input class="in" id="lsearch" type="search" placeholder="{{$.T.lv_search}}" style="width:100%"></div>
    <div class="lscroll">
    {{if .PlayNvrs}}<div class="srcg">{{$.T.ar_local}}</div>{{end}}
    {{range .PlayNvrs}}{{template "pbnvr" (pair $ .)}}{{end}}
    {{if or .PlayRemote .PlayExt}}<div class="srcg">{{$.T.ar_external}}</div>{{end}}
    {{range .PlayRemote}}{{template "pbnvr" (pair $ .)}}{{end}}
    {{if .PlayExt}}{{range .PlayExt}}<a class="src" href="/?p=archive&amp;n={{.Name}}&amp;tab=search" title="{{$.T.pb_ext_note}}"><span class="dot {{.Kind}}" aria-hidden="true"></span><span style="flex:1">{{.Name}}</span><span class="note">{{$.T.ar_search}}</span></a>{{end}}{{end}}
    </div>
   </div>
  </aside>{{end}}{{else if eq .Module "archive"}}<aside aria-label="{{.T.archive}}" class="srcs">
   <div class="srch">{{.T.ar_source}}</div>
   {{$cur := ""}}{{$tab := ""}}{{with .Archive}}{{$cur = .Nvr.Name}}{{$tab = .Tab}}{{end}}
   {{range $i, $local := list true false}}{{if $.Sys.HasNvrs $local}}
    <div class="srcg">{{if $local}}{{$.T.ar_local}}{{else}}{{$.T.ar_external}}{{end}}</div>
    {{range $.Sys.Nvrs}}{{if eq .Local $local}}<a class="src" href="/?p=archive&amp;n={{.Name}}{{with $tab}}&amp;tab={{.}}{{end}}"{{if eq .Name $cur}} aria-current="page"{{end}}>
     <span class="dot {{.Kind}}" aria-hidden="true"></span><span style="flex:1">{{.Name}}</span><span class="note">{{if eq .Kind "err"}}{{$.T.offline}}{{else}}{{.CamsTotal}} {{$.T.ar_cams}}{{end}}</span></a>{{end}}{{end}}
   {{end}}{{end}}
  </aside>{{end}}
  <main>
   <div class="top">
    <div style="display:flex;flex-direction:column">
     <span class="crumb">{{.Crumb}}</span>
     <h1>{{.Title}}</h1>
    </div>
    {{if not (or (eq .Module "live") (and .Archive (eq .Archive.Tab "play")))}}<div class="upd"><span>{{.T.updated}} {{.Updated}}</span><a class="btn" href="{{.Self}}">{{.T.refresh}}</a></div>{{end}}
   </div>
   {{template "content" .}}
  </main>
 </div>
</div>
<script nonce="{{.Nonce}}">
(function(){
 var d=document.getElementById('drawer'),b=document.getElementById('menubtn'),sec=document.getElementById('dsec'),nav=window.ffnvrNav;
 if(!d||!d.showModal)return;
 // the side menu of the page: beside the content on wide screens, in the drawer on narrow ones
 var aside=document.querySelector('.cols>aside'),mark=document.createComment('aside');
 if(aside)aside.parentNode.insertBefore(mark,aside);
 var calm=matchMedia('(prefers-reduced-motion: reduce)');
 function done(){d.classList.remove('closing');if(d.open)d.close();b.setAttribute('aria-expanded','false')}
 function hide(){
  if(!d.open||d.classList.contains('closing'))return;
  if(calm.matches)return done();
  d.classList.add('closing');
  setTimeout(done,220);
 }
 function show(){if(d.open)return;d.showModal();b.setAttribute('aria-expanded','true');
  var cur=sec.querySelector('[aria-current=page],.lview.cur');if(cur)cur.scrollIntoView({block:'nearest'})}
 function place(){
  if(!aside)return;
  if(nav.mq.matches){if(aside.parentNode!==sec)sec.appendChild(aside)}
  else{done();if(aside.parentNode!==mark.parentNode)mark.parentNode.insertBefore(aside,mark.nextSibling)}
 }
 place();
 nav.mq.addEventListener('change',place);
 nav.open=show;
 b.addEventListener('click',show);
 document.getElementById('drawerx').addEventListener('click',hide);
 d.addEventListener('cancel',function(e){e.preventDefault();hide()});
 d.addEventListener('click',function(e){
  if(e.target===d)return hide();
  // a choice in the menu closes it: a link, a camera put on the tile, a saved view, a camera of the map
  if(e.target.closest('a[href],.lcam,.lvn,.mrow'))setTimeout(hide,60);
 });
})();
</script>
</body></html>{{end}}

{{define "badge"}}<span class="badge {{.Kind}}">{{.Status}}</span>{{end}}

{{define "content"}}
{{if eq .Page "dash"}}{{template "dash" .}}
{{else if eq .Page "nvr"}}{{template "nvr" .}}
{{else if eq .Page "disks"}}{{template "disks" .}}
{{else if eq .Page "retention"}}{{template "retention" .}}
{{else if eq .Page "schedule"}}{{template "schedule" .}}
{{else if eq .Page "cams"}}{{template "cams" .}}
{{else if eq .Page "alerts"}}{{template "alerts" .}}
{{else if eq .Page "event_log"}}{{template "events" .}}
{{else if eq .Page "account"}}{{template "account" .}}
{{else if eq .Page "cam"}}{{template "cam" .}}
{{else if eq .Page "jobs"}}<section>{{template "jobtable" .JobTable}}</section>
{{else if eq .Page "history"}}<section>{{template "jobtable" .JobTable}}</section>
{{else if eq .Page "firmware"}}{{template "firmware" .}}
{{else if eq .Page "fwup"}}{{template "fwup" .}}
{{else if eq .Page "clock"}}{{if .Clock}}{{template "clockpage" .}}{{else}}{{template "clocklist" .}}{{end}}
{{else if eq .Page "reboot"}}{{template "tools" .}}
{{else if eq .Page "archive"}}{{template "archive" .}}
{{else if eq .Page "live"}}{{template "live" .}}
{{else if eq .Page "backup"}}{{template "tools" .}}
{{else}}<div class="placeholder"><p>{{.Title}}</p><p>{{.T.next_iteration}}</p></div>{{end}}
{{end}}

{{define "storagecell"}}<div class="sto"><div class="t"><b style="font-weight:400">{{.DiskTxt}}</b><span>{{.DiskNote}}</span></div><div class="bar"><i class="{{barKind .DiskPct .Kind}}" style="width:{{pct .DiskPct}}%"></i></div></div>{{end}}

{{define "alertlist"}}{{range .}}<div class="alert"><span class="dot {{.Kind}}" aria-hidden="true"></span><div><b>{{.Title}}</b><span>{{.Desc}}</span>{{if .Time}}<em>{{.Time}}</em>{{end}}</div></div>{{end}}{{end}}

{{define "dash"}}
<div class="kpis">{{range .Sys.KPIs}}<div class="kpi"><span class="l">{{.Label}}</span><span class="v">{{.Value}}<span> {{.Of}}</span></span><span class="note {{.Kind}}">{{.Note}}</span></div>{{end}}</div>
<div class="row">
 <section class="wide"><div class="sh"><h2>{{.T.recorders}}</h2></div>
  {{if .Sys.Nvrs}}<div class="scroll"><table style="min-width:640px">
   <thead><tr><th>{{.T.th_recorder}}</th><th>{{.T.th_status}}</th><th>{{.T.th_cameras}}</th><th>{{.T.th_storage}}</th><th>{{.T.th_retention}}</th><th>{{.T.th_uptime}}</th></tr></thead>
   <tbody>{{range .Sys.Nvrs}}<tr>
    <td><a class="nl" href="/?p=nvr&amp;n={{.Name}}"><b>{{.Name}}</b><span class="mono">{{.Address}}</span></a></td>
    <td>{{template "badge" .}}</td>
    <td>{{if .CamsKnown}}{{.CamsOnline}}{{else}}—{{end}} / {{.CamsTotal}}</td>
    <td>{{template "storagecell" .}}</td>
    <td>{{.Retention}}</td><td style="color:#44505f">{{.Uptime}}</td>
   </tr>{{end}}</tbody></table></div>
  {{else}}<div class="empty">{{.T.no_recorders}}</div>{{end}}
 </section>
 <section class="side"><div class="sh"><h2>{{.T.active_alerts}}</h2></div>
  {{template "alertlist" .Sys.Alerts}}{{if not .Sys.Alerts}}<div class="empty">{{.T.no_alerts}}</div>{{end}}
 </section>
</div>
{{end}}

{{define "camtable"}}
<div class="scroll"><table style="min-width:780px">
 <thead><tr>{{if .All}}<th>{{.T.th_nvr}}</th>{{end}}<th>{{.T.th_ch}}</th><th>{{.T.th_name}}</th><th>{{.T.th_ip}}</th><th>{{.T.th_detail}}</th><th>{{.T.th_recording}}</th><th>{{.T.th_uptime}}</th><th>{{.T.th_restarts}}</th><th>{{.T.th_memory}}</th><th>{{.T.th_status}}</th><th></th></tr></thead>
 <tbody>{{range .Cams}}<tr>
  {{if $.All}}<td><a href="/?p=nvr&amp;n={{.Nvr}}">{{.Nvr}}</a></td>{{end}}
  <td style="color:#5b6878">{{.Ch}}</td>
  <td style="font-weight:500">{{or .Alias .Name}}{{if and .Alias .Name}} <span style="color:#5b6878;font-weight:400">({{.Name}})</span>{{end}}</td>
  <td class="mono" style="font-size:12px">{{or .Address "—"}}</td>
  <td>{{or .Detail "—"}}</td><td>{{.Recording}}</td><td>{{.Uptime}}</td><td>{{.Restarts}}</td><td>{{.Memory}}</td>
  <td>{{template "badge" .}}</td>
  <td style="text-align:right">{{if .Details}}<a class="btn sm" href="{{.Details}}">{{$.T.details}}</a>{{end}}</td>
 </tr>{{end}}</tbody>
</table></div>{{if not .Cams}}<div class="empty">{{.T.no_cameras}}</div>{{end}}
{{end}}

{{define "nvr"}}{{with .Nvr}}
{{if .Offline}}<div class="banner">{{.Offline}}</div>{{end}}
<div class="row">
 <section class="half"><div class="sh"><h2>{{$.T.information}}</h2><span class="b">{{template "badge" .}}</span></div>
  <dl>{{range .Info}}<dt>{{.K}}</dt><dd>{{.V}}</dd>{{end}}</dl>
  {{$fw := and (index $.Can "system.nvr.firmware") .Upgradable}}{{$ck := and (index $.Can "system.nvr.clock") .ClockSettable}}
  {{if or $fw $ck}}<div class="bar2" style="border-bottom:0;border-top:1px solid #eef1f4;background:#fff;align-items:center">
   {{with and $fw (index $.FwRuns .Name)}}<span class="badge {{.Kind}}">{{.State}}{{if .Active}} · {{.Percent}}%{{end}}</span>{{else}}<span></span>{{end}}
   <span style="display:flex;gap:8px;flex-wrap:wrap">{{if $ck}}<a class="btn sm" href="/?p=clock&amp;n={{.Name}}">{{$.T.ck_nvr_btn}}</a>{{end}}{{if $fw}}<a class="btn sm" href="/?p=fwup&amp;n={{.Name}}">{{$.T.fwup_nvr_btn}}</a>{{end}}</span></div>{{end}}
 </section>
 <section class="half"><div class="sh"><h2>{{if .Local}}{{$.T.storage}}{{else}}{{$.T.disks}}{{end}}</h2></div>
  {{range .Disks}}<div class="disk"><div class="t"><b>{{.Name}}</b>{{if or .Model .Serial}}<span class="note">{{.Model}}{{if .Serial}} · {{$.T.sn}} {{.Serial}}{{end}}</span>{{end}}<span class="badge {{.Kind}}">{{.State}}</span></div>
   <div class="bar"><i class="{{barKind .Pct .Kind}}" style="width:{{pct .Pct}}%"></i></div><span>{{.Used}}{{if .Known}} · {{.Cap}}{{end}}</span></div>{{end}}
  {{if not .Disks}}<div class="empty">{{$.T.no_data}}</div>{{end}}
 </section>
</div>
<section><div class="sh"><h2>{{$.T.cameras_g}}</h2></div>{{template "camtable" $.CamTable}}</section>
{{template "latestevents" $}}
<section><div class="sh"><h2>{{$.T.jobs_of_nvr}}</h2></div>{{template "jobtable" $.JobTable}}</section>
{{end}}{{end}}

{{/* the latest events of a recorder, or of a camera (its page) */}}
{{define "latestevents"}}{{with .NvrEvents}}<section><div class="sh"><h2>{{$.T.ev_latest}}</h2>{{if not .Missing}}<span class="b note">{{printf $.T.ev_total .Total .Unread}}</span>{{end}}</div>
 {{if .Missing}}<div class="empty">{{$.T.ev_missing}}</div>{{else}}{{template "eventtable" (evTable $.T . false)}}
 {{if .Rows}}<div class="bar2" style="border-bottom:0"><a class="btn sm" href="{{.More}}">{{$.T.ev_more}}</a>{{if .Unread}} <a class="btn sm" href="{{.MoreUnacked}}">{{$.T.ev_unacked_only}}</a>{{end}}</div>{{end}}{{end}}
</section>{{end}}{{end}}

{{define "jobtable"}}
<div class="scroll"><table style="min-width:900px">
 <thead><tr><th>#</th>{{if .All}}<th>{{.T.th_nvr}}</th>{{end}}<th>{{.T.camera}}</th><th>{{.T.th_range}}</th><th>{{.T.th_files}}</th><th>{{.T.th_status}}</th><th style="min-width:140px">{{.T.th_progress}}</th><th>{{.T.th_when}}</th><th>{{.T.th_dest}}</th>{{if .Actions}}<th style="text-align:right">{{.T.th_action}}</th>{{end}}</tr></thead>
 <tbody>{{range .Jobs}}<tr>
  <td style="color:#5b6878">{{.ID}}</td>
  {{if $.All}}<td><a href="/?p=nvr&amp;n={{.Nvr}}">{{.Nvr}}</a></td>{{end}}
  <td style="font-weight:500">{{.Camera}}</td><td style="white-space:nowrap">{{.Range}}</td><td style="white-space:nowrap">{{.Files}}</td>
  <td><span class="badge {{.Kind}}">{{.State}}</span></td>
  <td>{{if .Active}}<div class="bar"><i style="width:{{pct .Pct}}%"></i></div>{{end}}<span class="note">{{.Progress}}</span></td>
  <td style="color:#44505f;white-space:nowrap">{{.When}}</td>
  <td class="mono" style="font-size:12px">{{.Target}}{{if .Error}}<div class="note err" style="font-family:inherit">{{.Error}}</div>{{end}}{{if .Comment}}<div class="note" style="font-family:inherit">{{.Comment}}</div>{{end}}</td>
  {{if $.Actions}}<td style="text-align:right;white-space:nowrap">
   {{if and .Download $.Download}}<a class="btn primary sm act" href="/archive/result?id={{.ID}}">{{$.T.backup_btn}}</a>{{end}}
   {{if and .Active $.Manage}}<form method="post" action="/archive/cancel" class="inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="back" value="{{$.Back}}"><button class="btn sm act" type="submit">{{$.T.cancel}}</button></form>{{end}}
   {{if and .Retry $.Manage}}<form method="post" action="/archive/retry" class="inline"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="id" value="{{.ID}}"><input type="hidden" name="back" value="{{$.Back}}"><button class="btn sm act" type="submit">{{$.T.ar_retry}}</button></form>{{end}}
  </td>{{end}}
 </tr>{{end}}</tbody>
</table></div>{{if not .Jobs}}<div class="empty">{{.T.no_jobs}}</div>{{end}}
{{end}}

{{define "live"}}
{{if not .Live.Enabled}}<div class="placeholder"><p>{{.T.live}}</p><p>{{.T.lv_off}}</p></div>{{else}}
<div class="ltool">
 <div class="tabs" role="tablist"><a href="#" role="tab" id="tabgrid" aria-selected="true">{{.T.mp_grid}}</a><a href="#" role="tab" id="tabmap">{{.T.mp_map}}</a><a href="#" role="tab" id="tabthumbs">{{.T.tb_tab}}</a></div>
 <span class="note" id="lhint"></span>
 <span class="gridtools">
  <span class="lgrp" role="group" aria-label="{{.T.lv_layout}}">{{range list 1 4 9 16 25}}<button type="button" class="btn sm" data-layout="{{.}}">{{.}}</button>{{end}}</span>
  <button type="button" class="btn sm" id="lsave">{{.T.lv_save}}</button>
  <button type="button" class="btn sm" id="pzshow" hidden title="{{.T.pz_show}}">PTZ</button>
 </span>
 <span class="maptools" hidden>
  <button type="button" class="btn sm" id="medit">{{.T.mp_edit}}</button>
  <span id="meditbar" hidden><button type="button" class="btn sm" id="madd">{{.T.mp_add}}</button> <button type="button" class="btn sm" id="mcancel">{{.T.cancel}}</button> <button type="button" class="btn sm primary" id="msave">{{.T.mp_save}}</button></span>
 </span>
 <button type="button" class="btn sm primary" id="lfull" title="{{.T.lv_full}}">⛶</button>
</div>
<div class="lstage">
<div class="lgrid" id="lgrid"></div>
<aside class="lptz" id="lptz" aria-label="{{.T.pz_title}}" hidden>
 <div class="pzh"><h2>{{.T.pz_title}}</h2><button type="button" class="pzx" id="pzhide" title="{{.T.pz_hide}}" aria-label="{{.T.pz_hide}}">›</button></div>
 <div class="pzcam" id="pzcam"></div>
 <div class="pzbody" id="pzbody">
  <div class="pzctl">
   <div class="pzlens"><div class="pzl" data-need="zoom" role="group" aria-label="{{.T.pz_zoom}}"><button type="button" data-a="zoomin" data-hold title="{{.T.pz_zoom}} +" aria-label="{{.T.pz_zoom}} +">+</button><span class="pzli" title="{{.T.pz_zoom}}" aria-hidden="true"><svg viewBox="0 0 16 16"><circle cx="7" cy="7" r="4.5"/><path d="M10.4 10.4 14 14M5 7h4M7 5v4"/></svg><small>{{.T.pz_zoom_s}}</small></span><button type="button" data-a="zoomout" data-hold title="{{.T.pz_zoom}} −" aria-label="{{.T.pz_zoom}} −">−</button></div><div class="pzl" data-need="focus" role="group" aria-label="{{.T.pz_focus}}"><button type="button" data-a="focusnear" data-hold title="{{.T.pz_focus}} +" aria-label="{{.T.pz_focus}} +">+</button><span class="pzli" title="{{.T.pz_focus}}" aria-hidden="true"><svg viewBox="0 0 16 16"><path d="M2 5.5V2h3.5M10.5 2H14v3.5M14 10.5V14h-3.5M5.5 14H2v-3.5"/><circle cx="8" cy="8" r="2"/></svg><small>{{.T.pz_focus}}</small></span><button type="button" data-a="focusfar" data-hold title="{{.T.pz_focus}} −" aria-label="{{.T.pz_focus}} −">−</button></div><div class="pzl" data-need="iris" role="group" aria-label="{{.T.pz_iris}}"><button type="button" data-a="irisopen" data-hold title="{{.T.pz_iris}} +" aria-label="{{.T.pz_iris}} +">+</button><span class="pzli" title="{{.T.pz_iris}}" aria-hidden="true"><svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="6"/><path d="M8 2l2.6 4.5M14 8l-5.2.1M10.9 13.2 8.3 8.7M2.6 11l2.7-4.4M5.1 2.8l2.5 4.5M13.3 5.2 8.2 7.5"/></svg><small>{{.T.pz_iris}}</small></span><button type="button" data-a="irisclose" data-hold title="{{.T.pz_iris}} −" aria-label="{{.T.pz_iris}} −">−</button></div></div>
  <div class="pzmove" data-need="move">
   <div class="pzpad"><button type="button" class="pzd" data-a="leftup" data-hold style="--r:-45deg" title="{{.T.pz_leftup}}" aria-label="{{.T.pz_leftup}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button><button type="button" class="pzd" data-a="up" data-hold style="--r:0deg" title="{{.T.pz_up}}" aria-label="{{.T.pz_up}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button><button type="button" class="pzd" data-a="rightup" data-hold style="--r:45deg" title="{{.T.pz_rightup}}" aria-label="{{.T.pz_rightup}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button><button type="button" class="pzd" data-a="left" data-hold style="--r:-90deg" title="{{.T.pz_left}}" aria-label="{{.T.pz_left}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button><button type="button" class="pzstop" data-a="stop">{{.T.pz_stop}}</button><button type="button" class="pzd" data-a="right" data-hold style="--r:90deg" title="{{.T.pz_right}}" aria-label="{{.T.pz_right}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button><button type="button" class="pzd" data-a="leftdown" data-hold style="--r:-135deg" title="{{.T.pz_leftdown}}" aria-label="{{.T.pz_leftdown}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button><button type="button" class="pzd" data-a="down" data-hold style="--r:180deg" title="{{.T.pz_down}}" aria-label="{{.T.pz_down}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button><button type="button" class="pzd" data-a="rightdown" data-hold style="--r:135deg" title="{{.T.pz_rightdown}}" aria-label="{{.T.pz_rightdown}}"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3 13 11H3z"/></svg></button></div>
   <label class="pzspeed" title="{{.T.pz_speed}}"><svg class="pzspi" viewBox="0 0 16 16" aria-hidden="true"><path d="M2.5 11.5a5.5 5.5 0 1 1 11 0"/><path d="M8 11.5 11 7"/><circle cx="8" cy="11.5" r="1"/></svg><input type="range" id="pzspeed" min="1" step="1" aria-label="{{.T.pz_speed}}"><output id="pzspeedv"></output></label>
  </div>
  </div>
  <div class="pzauxs" id="pzauxs"><button type="button" class="pzaux" data-aux="light" title="{{.T.pz_light}}" aria-label="{{.T.pz_light}}" hidden><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M6 13h4M6.5 15h3M8 1.5a4.5 4.5 0 0 0-2.6 8.2c.4.3.6.8.6 1.3h4c0-.5.2-1 .6-1.3A4.5 4.5 0 0 0 8 1.5z"/></svg></button><button type="button" class="pzaux" data-aux="wiper" title="{{.T.pz_wiper}}" aria-label="{{.T.pz_wiper}}" hidden><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2 12A8 8 0 0 1 14 12M8 14 12.5 4.5"/><circle cx="8" cy="14" r="1"/></svg></button><button type="button" class="pzaux" data-aux="heater" title="{{.T.pz_heater}}" aria-label="{{.T.pz_heater}}" hidden><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 3c-1 1.5 1 2.5 0 4s1 2.5 0 4M8 3c-1 1.5 1 2.5 0 4s1 2.5 0 4M12 3c-1 1.5 1 2.5 0 4s1 2.5 0 4M2 14h12"/></svg></button><button type="button" class="pzaux" data-aux="deicing" title="{{.T.pz_deicing}}" aria-label="{{.T.pz_deicing}}" hidden><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 1.5v13M2.4 4.75l11.2 6.5M2.4 11.25l11.2-6.5M6 2.5 8 4l2-1.5M6 13.5 8 12l2 1.5"/></svg></button></div>
  <details class="pzorient" data-need="move"><summary>{{.T.pz_orient}}<span class="pzflag" id="pzflag" aria-hidden="true"></span></summary>
   <label class="chk"><input type="checkbox" id="pzswaph"> {{.T.pz_swaph}}</label>
   <label class="chk"><input type="checkbox" id="pzswapv"> {{.T.pz_swapv}}</label>
   <p class="pznote" id="pzswapnote"></p>
   <button type="button" class="btn sm" id="pzswapsave" hidden>{{.T.pz_swap_save}}</button>
  </details>
 </div>
 <p class="pzmsg" id="pzmsg" role="status"></p>
</aside>
</div>
<div id="lthumbs" class="lthumbs" hidden></div>
<div id="lmap" class="lmap" hidden>
 <div class="mtiles"></div><svg class="mfov"></svg><div class="mmarks"></div>
 <div class="mzoom"><button type="button" data-z="1">+</button><button type="button" data-z="-1">−</button></div>
 <div class="mattr"></div>
 <div class="msel" hidden></div>
 <div class="mprev" hidden><b></b><img alt=""></div>
</div>
<section id="mstrip" class="mstrip" hidden><div class="sh"><h2>{{.T.mp_strip}}</h2><span class="note" style="margin-left:8px" id="mstripn"></span>
 <span class="b"><button type="button" class="btn sm" id="mgrid">{{.T.mp_open_grid}}</button>{{if index .Can "archive"}} <a class="btn sm" id="march" href="#">{{.T.lv_open_archive}}</a>{{end}} <button type="button" class="btn sm danger" id="mclear">{{.T.mp_clear}}</button></span></div><div class="strip" id="mstriptiles"></div></section>
<dialog id="dsave"><form method="dialog"><h2>{{.T.lv_save}}</h2>
 <label class="fl">{{.T.name}}<input class="in" id="dsname" maxlength="60" required></label>
 <label class="chk" id="dsnewl"><input type="checkbox" id="dsnew"> {{.T.lv_save_new}}</label>
 <div class="dbtn"><button class="btn" value="cancel" formnovalidate>{{.T.cancel}}</button><button class="btn primary" value="ok">{{.T.lv_save}}</button></div></form></dialog>
<dialog id="ddel"><form method="dialog"><h2></h2><p>{{.T.lv_del_warn}}</p>
 <div class="dbtn"><button class="btn" value="cancel" formnovalidate autofocus>{{.T.cancel}}</button><button class="btn danger" value="ok">{{.T.lv_delete}}</button></div></form></dialog>
<dialog id="dadd"><form method="dialog"><h2>{{.T.mp_add}}</h2><p>{{.T.mp_add_hint}}</p>
 <input class="in" id="daddq" type="search" placeholder="{{.T.lv_search}}" style="width:100%;margin-bottom:8px"><div id="daddlist" class="addlist"></div>
 <div class="dbtn"><button class="btn" value="cancel" formnovalidate>{{.T.cancel}}</button><button class="btn primary" value="ok">{{.T.mp_add_btn}}</button></div></form></dialog>
<script type="application/json" id="ldata">{{.Live.Data}}</script>
<script src="/static/live.js" nonce="{{.Nonce}}"></script>
{{end}}
{{end}}

{{define "camchecks"}}
<fieldset class="cams"><legend>{{.T.cameras_g}}</legend>
 <label class="chk all"><input type="checkbox" data-all="c"> {{.T.ar_select_all}}</label>
 {{range .Archive.Cameras}}<label class="chk"><input type="checkbox" name="c" value="{{.Name}}"{{if .Checked}} checked{{end}}> <span style="flex:1">{{.Label}}</span><span class="note">{{.Ch}}</span></label>{{end}}
 {{if not .Archive.Cameras}}<span class="note">{{.T.no_cameras}}</span>{{end}}
</fieldset>{{end}}

{{define "rangefields"}}
<div class="range">
 <label>{{.T.ar_from}}<input class="in" type="datetime-local" name="from" value="{{.Archive.From}}"{{with .Archive.RecFrom}} min="{{.}}"{{end}}{{with .Archive.RecTo}} max="{{.}}"{{end}} required></label>
 <label>{{.T.ar_to}}<input class="in" type="datetime-local" name="to" value="{{.Archive.To}}"{{with .Archive.RecFrom}} min="{{.}}"{{end}}{{with .Archive.RecTo}} max="{{.}}"{{end}} required></label>
 <div class="quick"><button type="button" class="btn sm" data-quick="hour">{{.T.ar_last_hour}}</button><button type="button" class="btn sm" data-quick="today">{{.T.ar_today}}</button><button type="button" class="btn sm" data-quick="yesterday">{{.T.ar_yesterday}}</button></div>
</div>
{{with .Archive.RecText}}<div class="reach"><span class="reachbar" aria-hidden="true"></span>{{.}}</div>{{end}}{{end}}

{{define "destfields"}}
<div class="dest">
 <label>{{.T.ar_storage}}<select class="in" name="storage" required>{{range .Archive.Storages}}<option{{if eq . $.Archive.Storage}} selected{{end}}>{{.}}</option>{{end}}</select></label>
 <label>{{.T.ar_path}}<input class="in" name="path" value="{{.Archive.Path}}" placeholder="{{.T.ar_path_hint}}" pattern="[A-Za-z0-9_][A-Za-z0-9_.\-]*(/[A-Za-z0-9_][A-Za-z0-9_.\-]*)*"></label>
 <label>{{.T.ar_layout}}<select class="in" name="layout"><option value="nvr-camera"{{if eq .Archive.Layout "nvr-camera"}} selected{{end}}>{{.T.ar_layout_dirs}}</option><option value="flat"{{if eq .Archive.Layout "flat"}} selected{{end}}>{{.T.ar_layout_flat}}</option></select></label>
</div>
{{if not .Archive.Storages}}<div class="note warn">{{.T.ar_no_storage}}</div>{{end}}{{end}}

{{define "archive"}}
{{if .Flash}}<div class="flash {{.FlashKind}}" role="status">{{.Flash}}</div>{{end}}
{{with .Archive}}
<div class="tabs" role="tablist">
 {{with .PlayHref}}<a role="tab" href="{{.}}"{{if eq $.Archive.Tab "play"}} aria-selected="true"{{end}}>{{$.T.pb_title}}</a>{{end}}
 <a role="tab" href="/?p=archive&amp;n={{.Nvr.Name}}&amp;tab=search"{{if eq .Tab "search"}} aria-selected="true"{{end}}>{{$.T.ar_search}}</a>
 {{if .Export}}<a role="tab" href="/?p=archive&amp;n={{.Nvr.Name}}&amp;tab=range"{{if eq .Tab "range"}} aria-selected="true"{{end}}>{{$.T.ar_range}}</a>{{end}}
 <a role="tab" id="arqueuetab" href="/?p=archive&amp;n={{.Nvr.Name}}&amp;tab=queue"{{if eq .Tab "queue"}} aria-selected="true"{{end}}{{with .Active}} title="{{printf $.T.ar_queue_active .}}"{{end}}>{{$.T.ar_queue}}{{with .Active}} <span class="count">{{.}}</span>{{end}}</a>
</div>
{{if eq .Tab "play"}}{{template "playback" $}}
{{else if eq .Tab "queue"}}
<section id="arqueue"><div class="sh"><h2>{{$.T.ar_queue}}</h2><span class="note" style="margin-left:8px">{{$.T.ar_queue_note}}</span></div>{{template "jobtable" .Jobs}}</section>
{{else if eq .Tab "range"}}
<form class="card-form" method="post" action="/archive/range" id="rangeform">
 <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="n" value="{{.Nvr.Name}}">
 <section><div class="sh"><h2>{{$.T.ar_range}}</h2></div>
  <div class="fbody">
   {{template "camchecks" $}}
   <div class="fcol">
    {{template "rangefields" $}}
    {{template "destfields" $}}
    <div class="split">
     <label class="chk"><input type="checkbox" name="percamera" value="1" id="percamera" checked> {{$.T.ar_per_camera}}</label>
     <span id="blocks" class="blocks"><label>{{$.T.ar_block}} <select class="in sm" name="block" id="block">{{range .Blocks}}<option{{if eq . $.Archive.Block}} selected{{end}}>{{.}}</option>{{end}}<option value="custom">{{$.T.ar_custom}}</option></select></label>
      <input class="in sm" name="block_custom" id="block_custom" placeholder="1h30m" size="8"></span>
    </div>
    <div class="actions"><span class="note" id="summary"></span><button class="btn primary" type="submit"{{if not .Storages}} disabled{{end}}>{{$.T.ar_queue_range}}</button></div>
   </div>
  </div>
 </section>
</form>
{{else}}
<form class="card-form" method="get" action="/">
 <input type="hidden" name="p" value="archive"><input type="hidden" name="n" value="{{.Nvr.Name}}"><input type="hidden" name="tab" value="search"><input type="hidden" name="search" value="1">
 <section><div class="sh"><h2>{{$.T.ar_search}}</h2></div>
  <div class="fbody">
   {{template "camchecks" $}}
   <div class="fcol">{{template "rangefields" $}}
    <div class="actions"><span></span><button class="btn primary" type="submit">{{$.T.ar_find}}</button></div>
   </div>
  </div>
 </section>
</form>
{{if .Searched}}
<form method="post" action="/archive/files">
 <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="n" value="{{.Nvr.Name}}">
 <input type="hidden" name="from" value="{{.From}}"><input type="hidden" name="to" value="{{.To}}">
 {{range .Cameras}}{{if .Checked}}<input type="hidden" name="c" value="{{.Name}}">{{end}}{{end}}
 <section><div class="sh"><h2>{{$.T.ar_recorded}}</h2><span class="note" style="margin-left:8px">{{len .Files}} {{$.T.ar_found}}</span>
   {{if .ViewList}}<span class="b tabs sm"><a href="{{.ViewList}}"{{if not .Tiles}} aria-selected="true"{{end}}>{{$.T.ar_list}}</a><a href="{{.ViewTiles}}"{{if .Tiles}} aria-selected="true"{{end}}>{{$.T.ar_tiles}}</a></span>{{end}}</div>
  {{if .SearchErr}}<div class="info-note" style="color:#a3222b">{{.SearchErr}}</div>{{end}}
  {{if and .Files .Export}}<div class="bar2">{{template "destfields" $}}<button class="btn primary" type="submit"{{if not .Storages}} disabled{{end}}>{{$.T.ar_archive_sel}}</button></div>{{end}}
  {{if .Tiles}}{{if .Export}}<div class="thsel"><label class="chk"><input type="checkbox" data-all="f"> {{$.T.ar_select_all}}</label></div>{{end}}
  <div class="thumbs">{{range .Files}}{{template "filetile" (tile $.ArchiveTiles .)}}{{end}}</div>{{if not .Files}}<div class="empty">{{$.T.ar_none}}</div>{{end}}
  {{else}}<div class="scroll"><table style="min-width:820px">
   <thead><tr>{{if .Export}}<th style="width:28px"><input type="checkbox" data-all="f" aria-label="{{$.T.ar_select_all}}"></th>{{end}}<th>{{$.T.ar_file}}</th><th>{{$.T.camera}}</th><th>{{$.T.ar_start}}</th><th>{{$.T.ar_end}}</th><th>{{$.T.ar_duration}}</th><th>{{$.T.th_size}}</th>{{if and .Nvr.Local (or .Download .Play)}}<th style="text-align:right">{{$.T.th_action}}</th>{{end}}</tr></thead>
   <tbody>{{range .Files}}<tr>
    {{if $.Archive.Export}}<td><input type="checkbox" name="f" value="{{.Value}}" aria-label="{{.Name}}"></td>{{end}}
    <td class="mono" style="font-size:12px">{{.Name}}</td><td>{{.CameraLabel}}</td><td>{{.Start}}</td><td>{{.End}}</td><td>{{.Duration}}</td><td>{{.Size}}</td>
    {{if and .Local (or $.Archive.Download $.Archive.Play)}}<td style="text-align:right;white-space:nowrap">{{if $.Archive.Play}}<button type="button" class="btn sm primary" data-play="/archive/play?n={{$.Archive.Nvr.Name}}&amp;c={{.Camera}}&amp;f={{.Name}}" data-motion="/archive/motion?n={{$.Archive.Nvr.Name}}&amp;c={{.Camera}}&amp;f={{.Name}}" data-cam="{{.Camera}}" data-label="{{.CameraLabel}}" data-start="{{.Start}}" data-b="{{.Begin}}" title="{{$.T.ar_play}}" aria-label="{{$.T.ar_play}}: {{.CameraLabel}} {{.Start}}">▶</button> {{end}}{{if $.Archive.Download}}<a class="btn sm" href="/archive/file?n={{$.Archive.Nvr.Name}}&amp;c={{.Camera}}&amp;f={{.Name}}&amp;fmt=mkv">MKV</a> <a class="btn sm" href="/archive/file?n={{$.Archive.Nvr.Name}}&amp;c={{.Camera}}&amp;f={{.Name}}&amp;fmt=mp4">MP4</a>{{end}}</td>{{end}}
   </tr>{{end}}</tbody>
  </table></div>{{if not .Files}}<div class="empty">{{$.T.ar_none}}</div>{{end}}{{end}}
 </section>
</form>
{{if and .Play .Nvr.Local}}{{template "playerdlg" $}}{{end}}
{{end}}
{{end}}
{{else}}<div class="placeholder"><p>{{.T.archive}}</p><p>{{.T.no_recorders}}</p></div>{{end}}
{{template "arscript" .}}
{{end}}

{{/* a recorded file as a tile: its picture, motion bar and actions; dot: tile */}}
{{define "filetile"}}{{$o := .O}}{{with .F}}<label class="thumb">
    <span class="timg loading"><img src="/archive/thumb?n={{$o.Nvr}}&amp;c={{.Camera}}&amp;f={{.Name}}" alt="" loading="lazy" data-thumb="1"></span>
    <span class="mbar" title="{{$o.T.ar_motion}}" data-src="/archive/motion?n={{$o.Nvr}}&amp;c={{.Camera}}&amp;f={{.Name}}"{{if not .Motion}} data-pending="1"{{end}}>{{range bar .Motion}}<i class="{{.}}"></i>{{end}}</span>
    <span class="tcap">{{if $o.Export}}<input type="checkbox" name="f" value="{{.Value}}" aria-label="{{.Name}}">{{end}}<b>{{.CameraLabel}}</b><span class="note">{{.Start}} · {{.Duration}}</span></span>
    {{if or $o.Download $o.Play .Archive}}<span class="tdl">{{if $o.Play}}<button type="button" class="btn sm primary" data-play="/archive/play?n={{$o.Nvr}}&amp;c={{.Camera}}&amp;f={{.Name}}" data-motion="/archive/motion?n={{$o.Nvr}}&amp;c={{.Camera}}&amp;f={{.Name}}" data-cam="{{.Camera}}" data-label="{{.CameraLabel}}" data-start="{{.Start}}" data-b="{{.Begin}}" title="{{$o.T.ar_play}}" aria-label="{{$o.T.ar_play}}: {{.CameraLabel}} {{.Start}}">▶</button>{{end}}{{if $o.Download}}<a class="btn sm" href="/archive/file?n={{$o.Nvr}}&amp;c={{.Camera}}&amp;f={{.Name}}&amp;fmt=mkv">MKV</a><a class="btn sm" href="/archive/file?n={{$o.Nvr}}&amp;c={{.Camera}}&amp;f={{.Name}}&amp;fmt=mp4">MP4</a>{{end}}{{with .Archive}}<a class="btn sm" href="{{.}}" style="margin-left:auto">{{$o.T.archive}}</a>{{end}}</span>{{end}}
  </label>{{end}}{{end}}

{{/* the player of recorded files (the ▶ of the tiles and lists); dot: the page */}}
{{define "playerdlg"}}<dialog id="player" class="player" aria-labelledby="pl-title" data-cannot="{{$.T.pl_cannot}}" data-loading="{{$.T.pl_loading}}">
 <div class="plhead"><h2 id="pl-title"></h2><span class="plclock mono" id="pl-clock"></span>
  <button type="button" class="btn sm" id="pl-close" aria-label="{{$.T.pl_close}}">✕</button></div>
 <div class="plvid"><video id="pl-video" controls playsinline preload="auto"></video><div class="plmsg" id="pl-msg" hidden></div></div>
 <div class="plmotion" id="pl-motion" role="group" aria-label="{{$.T.ar_motion}}" data-mv="{{$.T.pl_mv}}" data-still="{{$.T.pl_still}}" hidden></div>
 <div class="plbar">
  <button type="button" class="btn sm" id="pl-prev" title="{{$.T.pl_prev}}">⏮ {{$.T.pl_prev}}</button>
  <button type="button" class="btn sm" id="pl-next" title="{{$.T.pl_next}}">{{$.T.pl_next}} ⏭</button>
  <span class="b tabs sm" id="pl-speed" role="group" aria-label="{{$.T.pl_speed}}"><a href="#" data-rate="0.5">0.5×</a><a href="#" data-rate="1" aria-selected="true">1×</a><a href="#" data-rate="2">2×</a><a href="#" data-rate="4">4×</a><a href="#" data-rate="8">8×</a><a href="#" data-rate="16">16×</a></span>
  <label class="chk"><input type="checkbox" id="pl-auto" checked> {{$.T.pl_auto}}</label>
  <span class="note" id="pl-pos"></span>
 </div>
</dialog>{{end}}

{{/* the scripts of the Archive's search: selecting, thumbnails and motion bars made in the background, the player; dot: the page */}}
{{define "arscript"}}<script nonce="{{.Nonce}}">
(function(){
 document.querySelectorAll('input[data-all]').forEach(function(a){
  var form=a.closest('form'), name=a.dataset.all;
  function boxes(){return form.querySelectorAll('input[type=checkbox][name="'+name+'"]')}
  function sync(){var b=boxes(),n=0;b.forEach(function(x){if(x.checked)n++});a.checked=b.length>0&&n===b.length;a.indeterminate=n>0&&n<b.length;summary()}
  a.addEventListener('change',function(){boxes().forEach(function(x){x.checked=a.checked});sync()});
  boxes().forEach(function(x){x.addEventListener('change',sync)});
  sync();
 });
 function pad(n){return (n<10?'0':'')+n}
 function fmt(d){return d.getFullYear()+'-'+pad(d.getMonth()+1)+'-'+pad(d.getDate())+'T'+pad(d.getHours())+':'+pad(d.getMinutes())}
 document.querySelectorAll('[data-quick]').forEach(function(b){
  b.addEventListener('click',function(){
   var f=b.closest('form'),now=new Date(),from,to;
   now.setSeconds(0,0);
   if(b.dataset.quick==='hour'){to=now;from=new Date(now.getTime()-3600e3)}
   else{var d=new Date(now);d.setHours(0,0,0,0);if(b.dataset.quick==='today'){from=d;to=now}else{to=d;from=new Date(d.getTime()-86400e3)}}
   f.querySelector('[name=from]').value=fmt(from);f.querySelector('[name=to]').value=fmt(to);summary();
  });
 });
 var per=document.getElementById('percamera'),blocks=document.getElementById('blocks'),block=document.getElementById('block'),custom=document.getElementById('block_custom');
 function showBlocks(){if(!per)return;blocks.style.display=per.checked?'none':'';custom.style.display=block.value==='custom'?'':'none';summary()}
 if(per){per.addEventListener('change',showBlocks);block.addEventListener('change',showBlocks);showBlocks()}
 function summary(){
  var s=document.getElementById('summary');if(!s)return;
  var f=s.closest('form'),n=f.querySelectorAll('input[name=c]:checked').length;
  var a=new Date(f.querySelector('[name=from]').value),b=new Date(f.querySelector('[name=to]').value),m=Math.round((b-a)/60000);
  if(!(m>0)){s.textContent='';return}
  var d=(m>=60?Math.floor(m/60)+' h ':'')+(m%60?m%60+' min':'');
  s.textContent=n+' × '+d.trim();
 }
 document.querySelectorAll('[name=from],[name=to]').forEach(function(x){x.addEventListener('change',summary)});
 // thumbnails made in the background: asked again until they are there
 // a spinner turns until the picture is there (the worker makes it); it gives up after ~2 minutes
 function done(i){return i.complete&&i.naturalWidth>1}
 function settle(i){var w=i.parentNode;if(done(i))w.classList.remove('loading')}
 document.querySelectorAll('img[data-thumb]').forEach(function(i){i.addEventListener('load',function(){settle(i)});
  i.addEventListener('error',function(){i.dataset.tries=99;i.parentNode.classList.remove('loading')});settle(i)});
 function thumbs(){var left=0;document.querySelectorAll('img[data-thumb]').forEach(function(i){
  if(!i.complete||done(i)||+(i.dataset.tries||0)>=48)return;
  if(i.getBoundingClientRect().top<innerHeight*2){i.dataset.tries=+(i.dataset.tries||0)+1;
   if(+i.dataset.tries>=48){i.parentNode.classList.remove('loading');return}
   i.src=i.src.replace(/&r=\d+$/,'')+'&r='+Date.now()}
  left++});
  if(left)setTimeout(thumbs,2500)}
 if(document.querySelector('img[data-thumb]'))setTimeout(thumbs,1500);
 // motion bars made in the background: asked for until they are there
 function bars(){var left=[].slice.call(document.querySelectorAll('.mbar[data-pending]'));if(!left.length)return;
  left.slice(0,12).forEach(function(b){fetch(b.dataset.src,{credentials:'same-origin'}).then(function(r){return r.json()}).then(function(m){
   if(!m.ready)return;var is=b.querySelectorAll('i');for(var i=0;i<is.length;i++)is[i].className=m.parts.charAt(i)==='1'?'mv':'still';b.removeAttribute('data-pending')})});
  setTimeout(bars,3000)}
 if(document.querySelector('.mbar[data-pending]'))setTimeout(bars,2000);
 // the player: a file of the results, the camera's next ones after it
 var dlg=document.getElementById('player');
 if(dlg){
  var vid=document.getElementById('pl-video'),msg=document.getElementById('pl-msg'),clock=document.getElementById('pl-clock'),title=document.getElementById('pl-title'),
   pos=document.getElementById('pl-pos'),auto=document.getElementById('pl-auto'),prev=document.getElementById('pl-prev'),next=document.getElementById('pl-next'),
   btns=[].slice.call(document.querySelectorAll('[data-play]')).sort(function(a,b){return a.dataset.b-b.dataset.b}),cur=null,rate=1; // in time order, whatever the order shown
  // H.265 is sent as it is where the browser has its decoder (as in the player of the
  // archive, playback.js); elsewhere the server makes H.264 of it. A browser that says it
  // plays H.265 but fails on it gets the H.264 one (hevcOff: from then on)
  var hevc=(function(){var t='video/mp4; codecs="hvc1.1.6.L150.90"';
   try{return document.createElement('video').canPlayType(t)==='probably'||!!(window.MediaSource&&MediaSource.isTypeSupported(t))}catch(e){return false}})(),hevcOff=false;
  function src(b){return b.dataset.play+(hevc&&!hevcOff?'&hevc=1':'')}
  function same(){return btns.filter(function(b){return b.dataset.cam===cur.dataset.cam})} // the camera's files
  function step(d){var l=same();return l[l.indexOf(cur)+d]||null}
  function show(t){msg.textContent=t||'';msg.hidden=!t}
  function tick(){if(!cur)return;var d=new Date((+cur.dataset.b+vid.currentTime)*1000);
   clock.textContent=d.getFullYear()+'-'+pad(d.getMonth()+1)+'-'+pad(d.getDate())+' '+pad(d.getHours())+':'+pad(d.getMinutes())+':'+pad(d.getSeconds())}
  // the motion bar of the file (when the worker has made it): a part is a jump to its start
  var mbar=document.getElementById('pl-motion'),mot=null,motTimer=0;
  function hms(sec){var d=new Date((+cur.dataset.b+sec)*1000);return pad(d.getHours())+':'+pad(d.getMinutes())+':'+pad(d.getSeconds())}
  function motionLen(){return vid.duration>0&&isFinite(vid.duration)?vid.duration:mot.duration}
  function drawMotion(){mbar.innerHTML='';if(!mot){mbar.hidden=true;return}
   var n=mot.parts.length,len=motionLen();
   for(var i=0;i<n;i++){(function(i){var a=i*len/n,z=(i+1)*len/n,bt=document.createElement('button'),mv=mot.parts.charAt(i)==='1';
    bt.type='button';bt.className=mv?'mv':'';bt.title=hms(a)+(hms(z)!==hms(a)?' – '+hms(z):'')+' · '+(mv?mbar.dataset.mv:mbar.dataset.still);bt.setAttribute('aria-label',bt.title);
    bt.onclick=function(){vid.currentTime=a;var p=vid.play();if(p&&p.catch)p.catch(function(){})};mbar.appendChild(bt)})(i)}
   mbar.hidden=false;nowPart()}
  function nowPart(){if(!mot)return;var bs=mbar.children,len=motionLen(),k=Math.min(bs.length-1,Math.floor(vid.currentTime/len*bs.length));
   for(var i=0;i<bs.length;i++)bs[i].classList.toggle('now',i===k)}
  function loadMotion(b,tries){clearTimeout(motTimer);if(b!==cur)return;
   fetch(b.dataset.motion,{credentials:'same-origin'}).then(function(r){return r.json()}).then(function(m){if(b!==cur)return;
    if(m.ready&&m.parts){mot=m;drawMotion()}else if(tries<20)motTimer=setTimeout(function(){loadMotion(b,tries+1)},3000)}).catch(function(){})}
  vid.addEventListener('loadedmetadata',function(){if(mot)drawMotion()});
  vid.addEventListener('timeupdate',nowPart);
  function play(b){cur=b;mot=null;drawMotion();loadMotion(b,0);title.textContent=b.dataset.label+' · '+b.dataset.start;show(dlg.dataset.loading);
   vid.defaultPlaybackRate=rate;vid.src=src(b);vid.playbackRate=rate;tick();
   var l=same();pos.textContent=(l.indexOf(b)+1)+' / '+l.length;prev.disabled=!step(-1);next.disabled=!step(1);
   if(!dlg.open)dlg.showModal();var p=vid.play();if(p&&p.catch)p.catch(function(){})}
  btns.forEach(function(b){b.addEventListener('click',function(e){e.preventDefault();e.stopPropagation();play(b)})});
  ['playing','canplay','seeked'].forEach(function(ev){vid.addEventListener(ev,function(){show('')})});
  vid.addEventListener('error',function(){if(!vid.getAttribute('src'))return;
   if(hevc&&!hevcOff&&cur&&vid.error&&(vid.error.code===3||vid.error.code===4)){hevcOff=true;var t=vid.currentTime;vid.src=src(cur);vid.playbackRate=rate;
    vid.addEventListener('loadedmetadata',function(){if(t)vid.currentTime=t},{once:true});var p=vid.play();if(p&&p.catch)p.catch(function(){});return}
   show(dlg.dataset.cannot)});
  vid.addEventListener('timeupdate',tick);
  vid.addEventListener('ended',function(){var n=step(1);if(auto.checked&&n)play(n)});
  prev.onclick=function(){var n=step(-1);if(n)play(n)};next.onclick=function(){var n=step(1);if(n)play(n)};
  document.querySelectorAll('#pl-speed a').forEach(function(a){a.addEventListener('click',function(e){e.preventDefault();rate=+a.dataset.rate;
   vid.defaultPlaybackRate=vid.playbackRate=rate;document.querySelectorAll('#pl-speed a').forEach(function(x){x.setAttribute('aria-selected',x===a)})})});
  document.getElementById('pl-close').onclick=function(){dlg.close()};
  dlg.addEventListener('click',function(e){if(e.target===dlg)dlg.close()}); // the backdrop
  dlg.addEventListener('keydown',function(e){if(e.key==='PageDown')next.onclick();else if(e.key==='PageUp')prev.onclick();else return;e.preventDefault()});
  dlg.addEventListener('close',function(){vid.pause();vid.removeAttribute('src');vid.load();cur=null;mot=null;clearTimeout(motTimer);drawMotion();clock.textContent='';show('')});
 }
 summary();
})();
</script>{{end}}

{{define "pbnvr"}}{{$p := .A}}{{with .B}}<details open class="lnvr">
     <summary><span style="flex:1">{{.Name}}</span>{{if .Streams}}<span class="note" title="{{$p.T.pb_remote_note}}">{{$p.T.pb_streams}}</span>{{end}}<span class="badge {{.Kind}}">{{.Status}}</span>{{icon "chev" 14 "chev"}}</summary>
     {{range .Cams}}<button type="button" class="lcam" draggable="true" data-n="{{.Nvr}}" data-c="{{.Cam}}" data-label="{{.Label}}" title="{{.State}}">
      <span class="dot {{.Kind}}" aria-hidden="true"></span><span class="ln">{{.Label}}</span><span class="note">{{.Ch}}</span><span class="shown" aria-hidden="true">{{icon "eye" 14 ""}}</span></button>{{end}}
     {{if not .Cams}}<div class="note" style="padding:4px 16px 8px">{{$p.T.no_cameras}}</div>{{end}}
    </details>{{end}}{{end}}

{{define "playback"}}{{with .Archive}}
<div class="ltool">
 <span class="note" id="lhint"></span>
 <span class="lgrp" role="group" aria-label="{{$.T.lv_layout}}">{{range list 1 4 9 16}}<button type="button" class="btn sm" data-layout="{{.}}">{{.}}</button>{{end}}</span>
 <button type="button" class="btn sm" id="pbclear">{{$.T.pb_clear}}</button>
 <button type="button" class="btn sm primary" id="pbfull" title="{{$.T.lv_full}}">⛶</button>
</div>
<div class="pbstage">
 <div class="pbmain" id="pbmain">
  <div class="lgrid" id="pgrid"></div>
  <div class="pbbar">
   <div class="pbctl">
    <span class="pbgrp" role="group" aria-label="{{$.T.pb_transport}}">
     <button type="button" class="pbbtn" id="pbprev" title="{{$.T.pb_prev_rec}}" aria-label="{{$.T.pb_prev_rec}}">⏮</button>
     <button type="button" class="pbbtn" data-step="-10" title="{{$.T.pb_back}}" aria-label="{{$.T.pb_back}}">−10 s</button>
     <button type="button" class="pbbtn main" id="pbplay" title="{{$.T.pb_play}}" aria-label="{{$.T.pb_play}}">▶</button>
     <button type="button" class="pbbtn" data-step="10" title="{{$.T.pb_fwd}}" aria-label="{{$.T.pb_fwd}}">+10 s</button>
     <button type="button" class="pbbtn" id="pbnext" title="{{$.T.pb_next_rec}}" aria-label="{{$.T.pb_next_rec}}">⏭</button>
    </span>
    <span class="pbclock mono" id="pbclock" aria-live="off"></span>
    <span class="tabs sm" id="pbspeed" role="group" aria-label="{{$.T.pl_speed}}"><a href="#" data-rate="0.5">½×</a><a href="#" data-rate="1" aria-selected="true">1×</a><a href="#" data-rate="2">2×</a><a href="#" data-rate="4">4×</a><a href="#" data-rate="8">8×</a><a href="#" data-rate="16">16×</a></span>
    <span class="pbgrp pbzoom" role="group" aria-label="{{$.T.pb_zoom}}">
     <button type="button" class="pbbtn" data-pan="-1" title="{{$.T.pb_earlier}}" aria-label="{{$.T.pb_earlier}}">‹</button>
     <button type="button" class="pbbtn" data-zoom="1" title="{{$.T.pb_zoom_out}}" aria-label="{{$.T.pb_zoom_out}}">−</button>
     <span class="pbspan mono" id="pbspan"></span>
     <button type="button" class="pbbtn" data-zoom="-1" title="{{$.T.pb_zoom_in}}" aria-label="{{$.T.pb_zoom_in}}">+</button>
     <button type="button" class="pbbtn" data-pan="1" title="{{$.T.pb_later}}" aria-label="{{$.T.pb_later}}">›</button>
    </span>
   </div>
   <div class="pbtl"><div class="pbnames" id="pbnames"></div><div class="pbtlc"><canvas id="pbcanvas" aria-label="{{$.T.pb_timeline}}"></canvas><div class="pbtip" id="pbtip" hidden></div></div></div>
   <div class="pbleg"><span><i class="lrec"></i>{{$.T.pb_recorded}}</span><span><i class="lmv"></i>{{$.T.pb_motion}}</span>{{if .Export}}<span><i class="lsel"></i>{{$.T.pb_range}}</span>{{end}}<span><i class="lcur"></i>{{$.T.pb_cursor}}</span><span class="pbkeys">{{$.T.pb_keys}}</span></div>
  </div>
 </div>
 <aside class="pbcal" aria-label="{{$.T.pb_date}}">
  <div class="pzh"><h2>{{$.T.pb_date}}</h2></div>
  <div class="calnav"><button type="button" class="pzx" data-month="-1" aria-label="{{$.T.pb_month_prev}}">‹</button><b id="calm"></b><button type="button" class="pzx" data-month="1" aria-label="{{$.T.pb_month_next}}">›</button></div>
  <div class="cal" id="calgrid"></div>
  <div class="calfoot">
   <button type="button" class="btn sm" id="caltoday">{{$.T.ar_today}}</button>
   <label class="fl">{{$.T.pb_goto}}<input class="in sm" type="time" step="1" id="pbgoto"></label>
   <p class="note" id="calnote">{{$.T.pb_cal_note}}</p>
  </div>
 </aside>
</div>
{{if .Export}}<form class="card-form" id="pbexport" novalidate>
 <section><div class="sh"><h2>{{$.T.pb_export}}</h2><span class="note" style="margin-left:8px">{{$.T.pb_export_note}}</span></div>
  <div class="fbody">
   <fieldset class="cams" id="pbxcams"><legend>{{$.T.cameras_g}}</legend>
    <label class="chk all"><input type="checkbox" id="pbxall"> {{$.T.ar_select_all}}</label><div id="pbxlist"></div></fieldset>
   <div class="fcol">
    <div class="range">
     <label>{{$.T.ar_from}}<input class="in" type="datetime-local" step="1" id="pbxfrom"></label>
     <label>{{$.T.ar_to}}<input class="in" type="datetime-local" step="1" id="pbxto"></label>
     <div class="quick"><button type="button" class="btn sm" data-mark="a" title="[">⇤ {{$.T.pb_mark_in}}</button><button type="button" class="btn sm" data-mark="b" title="]">{{$.T.pb_mark_out}} ⇥</button><button type="button" class="btn sm" id="pbxshow">{{$.T.pb_mark_show}}</button></div>
    </div>
    {{template "destfields" $}}
    <div class="split">
     <label class="chk"><input type="checkbox" name="percamera" value="1" id="percamera" checked> {{$.T.ar_per_camera}}</label>
     <span id="blocks" class="blocks"><label>{{$.T.ar_block}} <select class="in sm" name="block" id="block">{{range .Blocks}}<option{{if eq . $.Archive.Block}} selected{{end}}>{{.}}</option>{{end}}<option value="custom">{{$.T.ar_custom}}</option></select></label>
      <input class="in sm" name="block_custom" id="block_custom" placeholder="1h30m" size="8"></span>
    </div>
    <div class="flash" id="pbxmsg" role="status" hidden></div>
    <div class="actions"><span class="note" id="pbxsum"></span><button class="btn primary" type="submit" id="pbxgo"{{if not .Storages}} disabled{{end}}>{{$.T.ar_queue_range}}</button></div>
   </div>
  </div>
 </section>
</form>{{end}}
<script type="application/json" id="pbdata">{{.PlayData}}</script>
<script src="/static/playback.js" nonce="{{$.Nonce}}"></script>
{{end}}{{end}}

{{define "tools"}}
{{if .Flash}}<div class="flash {{.FlashKind}}" role="status">{{.Flash}}</div>{{end}}
{{if .BackupRun}}<section><div class="sh"><h2>{{.T.bk_results}}</h2><span class="note" style="margin-left:8px">{{.BackupNote}}</span></div>
 <div class="scroll"><table style="min-width:640px"><thead><tr><th>{{.T.th_nvr}}</th><th>{{.T.th_status}}</th><th>{{.T.ar_file}}</th><th>{{.T.th_size}}</th></tr></thead>
 <tbody>{{range .BackupItems}}<tr><td style="font-weight:500">{{.Nvr}}</td><td><span class="badge {{.Kind}}">{{.State}}</span></td>
  <td class="mono" style="font-size:12px">{{.Path}}{{if .Err}}<div class="note err" style="font-family:inherit">{{.Err}}</div>{{end}}</td><td>{{.Size}}</td></tr>{{end}}</tbody></table></div>
 {{if not .BackupRun.Done}}<span id="bkrunning" hidden></span>{{end}}</section>{{end}}
<section><div class="info-note">{{if eq .Page "reboot"}}{{.T.reboot_note}}{{else}}{{.T.backup_note}}{{end}}</div><div class="scroll"><table style="min-width:720px">
 <thead><tr>{{if eq .Page "backup"}}<th style="width:28px"><input type="checkbox" id="bkall" aria-label="{{.T.ar_select_all}}"></th>{{end}}<th>{{.T.th_nvr}}</th><th>{{.T.th_type}}</th><th>{{.T.address}}</th><th>{{.T.th_model}}</th><th>{{.T.th_status}}</th><th style="text-align:right">{{.T.th_action}}</th></tr></thead>
 <tbody>{{range .Sys.Nvrs}}<tr>
  {{if eq $.Page "backup"}}<td>{{if not .Local}}<input type="checkbox" name="nvr" value="{{.Name}}" form="bkform" class="bksel" aria-label="{{.Name}}">{{end}}</td>{{end}}
  <td style="font-weight:500"><a href="/?p=nvr&amp;n={{.Name}}">{{.Name}}</a></td><td>{{.Type}}</td><td class="mono" style="font-size:12px">{{.Address}}</td>
  <td>{{or .Model "—"}}</td><td><span class="badge {{.Kind}}">{{.Status}}</span></td>
  <td style="text-align:right">{{if .Local}}<span class="note">{{$.T.only_remote}}</span>{{else if eq $.Page "reboot"}}
   <form method="post" action="/tools/reboot" class="inline" data-confirm="{{printf $.T.reboot_q .Name}}">
    <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="nvr" value="{{.Name}}">
    <button type="submit" class="btn sm act danger">{{$.T.reboot_btn}}</button></form>{{else}}
   <form method="post" action="/tools/backup" class="inline">
    <input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="nvr" value="{{.Name}}">
    <button type="submit" class="btn sm act">{{$.T.backup_btn}}</button></form>{{end}}</td>
 </tr>{{end}}</tbody>
</table></div>
{{if eq .Page "backup"}}<form method="post" action="/tools/backup-to" id="bkform" class="bar2" style="border-top:1px solid #e8ebef;border-bottom:0">
 <input type="hidden" name="csrf" value="{{.CSRF}}">
 <div class="dest">
  <label>{{.T.bk_storage}}<select class="in" name="storage" required>{{range .BackupStorages}}<option>{{.}}</option>{{end}}</select></label>
  <label>{{.T.ar_path}}<input class="in" name="dir" placeholder="backup" pattern="[A-Za-z0-9_][A-Za-z0-9_.\-]*(/[A-Za-z0-9_][A-Za-z0-9_.\-]*)*"></label>
 </div>
 {{if not .BackupStorages}}<span class="note warn">{{.T.bk_no_storage}}</span>{{end}}
 <button class="btn primary" type="submit" id="bkgo" disabled>{{.T.bk_selected}}</button>
</form>{{end}}
</section>
<dialog id="confirm" aria-labelledby="confirm-q">
 <form method="dialog">
  <h2 id="confirm-q"></h2>
  <p>{{.T.reboot_warn}}</p>
  <div class="dbtn"><button class="btn" value="cancel" formnovalidate autofocus>{{.T.cancel}}</button><button class="btn danger" value="yes">{{.T.reboot_btn}}</button></div>
 </form>
</dialog>
<script nonce="{{.Nonce}}">
(function(){var all=document.getElementById('bkall'),go=document.getElementById('bkgo');if(!all)return;
 var boxes=[].slice.call(document.querySelectorAll('.bksel')),stor=document.querySelector('#bkform select');
 function sync(){var n=boxes.filter(function(b){return b.checked}).length;all.checked=n&&n===boxes.length;all.indeterminate=n>0&&n<boxes.length;go.disabled=!n||!stor.options.length}
 all.onchange=function(){boxes.forEach(function(b){b.checked=all.checked});sync()};boxes.forEach(function(b){b.onchange=sync});sync()})();
if(document.getElementById('bkrunning'))setTimeout(function(){location.reload()},2000);
document.querySelectorAll('form[data-confirm]').forEach(function(f){
 f.addEventListener('submit',function(e){
  if(f.dataset.ok){return}
  e.preventDefault();
  var d=document.getElementById('confirm');
  d.querySelector('h2').textContent=f.dataset.confirm;
  d.returnValue='';
  d.onclose=function(){if(d.returnValue==='yes'){f.dataset.ok='1';f.submit()}};
  d.showModal();
 });
});
</script>
{{end}}

{{define "firmware"}}
<section><div class="info-note">{{.T.fw_note}}</div><div class="scroll"><table style="min-width:820px">
 <thead><tr><th>{{.T.th_nvr}}</th><th>{{.T.th_type}}</th><th>{{.T.th_model}}</th><th>{{.T.th_firmware}}</th><th>{{.T.th_build}}</th><th>{{.T.th_serial}}</th><th>{{.T.th_checked}}</th>{{if index .Can "system.nvr.firmware"}}<th style="text-align:right">{{.T.th_action}}</th>{{end}}</tr></thead>
 <tbody>{{range .Sys.Nvrs}}<tr>
  <td style="font-weight:500"><a href="/?p=nvr&amp;n={{.Name}}">{{.Name}}</a></td><td>{{.Type}}</td>
  <td>{{or .Model "—"}}</td><td class="mono" style="font-size:12px">{{or .Firmware "—"}}</td><td>{{or .Build "—"}}</td>
  <td class="mono" style="font-size:12px">{{or .Serial "—"}}</td><td style="color:#44505f">{{or .Checked "—"}}</td>
  {{if index $.Can "system.nvr.firmware"}}<td style="text-align:right;white-space:nowrap">{{if .Upgradable}}{{with index $.FwRuns .Name}}{{if .Active}}<span class="badge warn">{{$.T.fwup_running}} · {{.Percent}}%</span> {{end}}{{end}}<a class="btn sm act" href="/?p=fwup&amp;n={{.Name}}">{{$.T.fwup_btn}}</a>{{else}}<span class="note">{{$.T.fwup_na}}</span>{{end}}</td>{{end}}
 </tr>{{end}}</tbody>
</table></div></section>
<section><div class="sh"><h2>{{.T.this_server}}</h2></div><dl>{{range .Server}}<dt>{{.K}}</dt><dd>{{.V}}</dd>{{end}}</dl></section>
{{end}}

{{define "disks"}}
<section><div class="scroll"><table style="min-width:720px">
 <thead><tr><th>{{.T.th_nvr}}</th><th>{{.T.th_disk}}</th><th>{{.T.th_disk_model}}</th><th>{{.T.th_disk_serial}}</th><th>{{.T.th_capacity}}</th><th style="min-width:160px">{{.T.th_usage}}</th><th>{{.T.th_state}}</th></tr></thead>
 <tbody>{{range .Sys.Disks}}<tr>
  <td style="font-weight:500"><a href="/?p=nvr&amp;n={{.Nvr}}">{{.Nvr}}</a></td><td>{{.Name}}</td><td>{{or .Model "—"}}</td><td class="mono" style="font-size:12px">{{or .Serial "—"}}</td><td>{{.Cap}}</td>
  <td><div class="bar"><i class="{{barKind .Pct .Kind}}" style="width:{{pct .Pct}}%"></i></div><span class="note">{{.Used}}</span></td>
  <td><span class="badge {{.Kind}}">{{.State}}</span></td>
 </tr>{{end}}</tbody>
</table></div>{{if not .Sys.Disks}}<div class="empty">{{.T.no_data}}</div>{{end}}</section>
{{end}}

{{define "retention"}}
<section><div class="info-note">{{.T.retention_note}}</div><div class="scroll"><table style="min-width:640px">
 <thead><tr><th>{{.T.th_nvr}}</th><th>{{.T.th_target}}</th><th>{{.T.th_actual}}</th><th>{{.T.th_storage}}</th></tr></thead>
 <tbody>{{range .Sys.Nvrs}}<tr>
  <td style="font-weight:500"><a href="/?p=nvr&amp;n={{.Name}}">{{.Name}}</a></td>
  <td>{{if .Local}}{{or .RetentionTarget $.T.no_limit}}{{else}}{{$.T.on_recorder}}{{end}}</td>
  <td>{{.Retention}}</td><td>{{template "storagecell" .}}</td>
 </tr>{{end}}</tbody>
</table></div></section>
{{end}}

{{define "cams"}}<section>{{template "camtable" .CamTable}}</section>{{end}}

{{define "events"}}{{with .Events}}
<section>
<form method="get" action="/" class="bar2" style="flex-wrap:wrap;gap:8px">
 <input type="hidden" name="p" value="event_log">
 <label>{{$.T.ev_origin}} <select name="origin"><option value="">{{$.T.ev_all}}</option><option value="recorders"{{if eq .Origin "recorders"}} selected{{end}}>{{$.T.ev_recorders}}</option><option value="system"{{if eq .Origin "system"}} selected{{end}}>{{$.T.ev_system}}</option></select></label>
 <label>{{$.T.th_nvr}} <select name="nvr"><option value="">{{$.T.ev_all}}</option>{{range .Nvrs}}<option{{if eq . $.Events.Nvr}} selected{{end}}>{{.}}</option>{{end}}<option value="?"{{if eq .Nvr "?"}} selected{{end}}>{{$.T.ev_unknown}}</option>{{if .CanSystem}}<option value="-"{{if eq .Nvr "-"}} selected{{end}}>{{$.T.ev_no_nvr}}</option>{{end}}</select></label>
 {{if .Camera}}<input type="hidden" name="camera" value="{{.Camera}}"><span class="badge">{{$.T.camera}}: {{.Camera}}</span>{{end}}
 <label>{{$.T.ev_kind}} <select name="kind"><option value="">{{$.T.ev_all}}</option>{{range .Kinds}}<option value="{{.}}"{{if eq . $.Events.Kind}} selected{{end}}>{{kindLabel $.T .}}</option>{{end}}</select></label>
 {{if .CanSystem}}<label>{{$.T.ev_user}} <select name="user"><option value="">{{$.T.ev_all}}</option>{{range .Users}}<option{{if eq . $.Events.User}} selected{{end}}>{{.}}</option>{{end}}</select></label>{{end}}
 <label>{{$.T.ev_day}} <input type="date" name="day" value="{{.Day}}"></label>
 <label>{{$.T.ev_search}} <input type="search" name="text" value="{{.Text}}" size="14"></label>
 <label><input type="checkbox" name="unacked" value="1"{{if .Unacked}} checked{{end}}> {{$.T.ev_unacked_only}}</label>
 <button class="btn sm" type="submit">{{$.T.ev_filter}}</button>
 {{if .Query}}<a class="btn sm" href="/?p=event_log">{{$.T.ev_clear}}</a>{{end}}
</form>
<div class="info-note">{{.State}}{{if not .Missing}} · {{printf $.T.ev_total .Total .Unread}}{{end}}</div>
{{if .Missing}}<div class="empty">{{$.T.ev_missing}}</div>{{else}}
<form method="post" action="/events/ack">
<input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="query" value="{{.Query}}">
{{template "eventtable" (evTable $.T . true)}}
<div class="bar2" style="border-bottom:0;gap:8px">
 {{if and .CanAck .Rows}}<button class="btn sm" type="submit">{{$.T.ev_ack_sel}}</button><button class="btn sm" type="submit" name="all" value="1">{{$.T.ev_ack_all}}</button>{{end}}
 {{if .Next}}<a class="btn sm" href="{{.Next}}">{{$.T.ev_next}}</a>{{end}}
</div>
</form>{{end}}
</section>
{{end}}{{end}}

{{define "eventtable"}}{{$T := .T}}
<div class="scroll"><table style="min-width:{{if .Full}}1040{{else}}760{{end}}px">
 <thead><tr>{{if .Ack}}<th style="width:28px"></th>{{end}}{{if .Full}}<th>#</th>{{end}}<th>{{$T.ev_start}}</th><th>{{$T.ev_length}}</th>{{if .Full}}<th>{{$T.th_nvr}}</th>{{end}}<th>{{$T.camera}}</th><th>{{$T.ev_kind}}</th><th>{{$T.ev_text}}</th>{{if .Full}}<th></th><th>{{$T.ev_acked}}</th>{{end}}{{if .Play}}<th></th>{{end}}</tr></thead>
 <tbody>{{range .Rows}}<tr{{if .System}} class="sysrow"{{end}}>
  {{if $.Ack}}<td>{{if and (not .Acked) (not .System)}}<input type="checkbox" name="id" value="{{.ID}}" aria-label="#{{.ID}}">{{end}}</td>{{end}}
  {{if $.Full}}<td style="color:#5b6878">{{.ID}}</td>{{end}}
  <td style="white-space:nowrap">{{.When}}</td>
  <td style="white-space:nowrap">{{if .Open}}<span class="badge warn">{{.Length}}</span>{{else}}{{.Length}}{{end}}</td>
  {{if $.Full}}<td>{{if .Unknown}}<span class="badge err">?</span>{{else if .Nvr}}<a href="/?p=nvr&amp;n={{.Nvr}}">{{.Nvr}}</a>{{else}}<span class="note">—</span>{{end}}</td>{{end}}
  <td style="font-weight:500">{{.Camera}}</td>
  <td>{{if .System}}<span class="badge mute">{{.Label}}</span>{{else}}<span class="mono" style="font-size:12px">{{.Label}}</span>{{end}} <span class="note">{{.Source}}</span></td>
  <td style="max-width:440px">{{if gt .Count 1}}<span class="badge">×{{.Count}}</span> {{end}}{{if .Who}}<b style="font-weight:500">{{.Who}}</b> {{end}}<span class="note" style="font-family:inherit">{{.Text}}</span></td>
  {{if $.Full}}<td>{{if .Picture}}<a href="/events/picture?id={{.ID}}" target="_blank" rel="noopener">{{$T.ev_picture}}</a>{{end}}</td>
  <td class="note" style="white-space:nowrap">{{.Acked}}</td>{{end}}
  {{if $.Play}}<td style="text-align:right">{{with .Archive}}<a class="btn sm" href="{{.}}" title="{{$T.archive}}">▶ {{$T.ar_play}}</a>{{end}}</td>{{end}}
 </tr>{{end}}</tbody>
</table></div>
{{if not .Rows}}<div class="empty">{{$T.ev_none}}</div>{{end}}
{{end}}

{{define "alerts"}}<section>{{template "alertlist" .Sys.Alerts}}{{if not .Sys.Alerts}}<div class="empty">{{.T.no_alerts}}</div>{{end}}</section>{{end}}

{{define "cam"}}
<div style="display:flex;gap:8px;flex-wrap:wrap"><a class="btn" href="{{.CamBack}}">← {{.T.back}}</a>{{if .CamFrom}}<a class="btn" href="/?p=nvr&amp;n={{.CamNvr}}">{{.CamNvr}}</a>{{end}}</div>
<section><div class="sh"><h2>{{.T.information}}</h2></div><dl>{{range .CamInfo}}<dt>{{.K}}</dt><dd>{{.V}}</dd>{{end}}</dl></section>
{{with .CamRec}}<section id="camrec"><div class="sh"><h2>{{$.T.cam_rec_latest}}</h2><span class="b" style="display:flex;gap:8px;flex-wrap:wrap">{{with .Player}}<a class="btn sm" href="{{.}}">{{$.T.cam_open_player}}</a>{{end}}<a class="btn sm" href="{{.Search}}">{{$.T.cam_rec_more}}</a></span></div>
 {{if .Err}}<div class="info-note" style="color:#a3222b">{{.Err}}</div>{{end}}
 {{if .Files}}<div class="thumbs">{{range .Files}}{{template "filetile" (tile $.CamRec.Tiles .)}}{{end}}</div>{{else}}<div class="empty">{{$.T.cam_rec_none}}</div>{{end}}
</section>{{end}}
{{template "latestevents" .}}
{{if index .Can "system.logs"}}<section><div class="sh"><h2>{{.T.logs}}</h2></div>{{if .Log}}<div class="logbox" tabindex="0" aria-label="{{.T.logs}}"><pre class="log">{{.Log}}</pre></div>{{else}}<div class="empty">{{.T.logs_unavailable}}</div>{{end}}</section>{{end}}
{{with .CamRec}}{{if and .Files .Tiles.Play}}{{template "playerdlg" $}}{{end}}{{template "arscript" $}}{{end}}
{{end}}
`))
