// ffnvr live view: the grid of live tiles (MSE over WebSocket, HLS as fallback), the
// saved views, and the map with the cameras' positions. No libraries.
(function () {
'use strict';
var D = JSON.parse(document.getElementById('ldata').textContent), T = D.t;
D.cams = D.cams || []; D.views = D.views || []; D.positions = D.positions || []; D.snaps = D.snaps || [];
var $ = function (s, r) { return (r || document).querySelector(s); };
var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
var KEY = 'ffnvr.live', cams = {};
D.cams.forEach(function (c) { cams[c.n + '/' + c.c] = c; });
function cam(t) { return t && cams[t.n + '/' + t.c]; }
function live(c) { return c && (c.hq || c.lq); }
// a narrow screen: the cameras are in the drawer of the page, opened from an empty tile
function narrow() { return !!(window.ffnvrNav && window.ffnvrNav.narrow()); }
// zoomNav puts ‹ › on the zoomed tile (on: shown), to step(-1) and step(1); swipeTiles
// steps on a sideways swipe over the grid while active() (the same in playback.js).
function zoomNav(el, on, step, T) {
  var b = $$('.znav', el);
  if (!on) { b.forEach(function (x) { x.remove(); }); return; }
  if (b.length) return;
  [[-1, 'prev', '\u2039', T.lv_prev_cam], [1, 'next', '\u203a', T.lv_next_cam]].forEach(function (a) {
    var x = document.createElement('button'); x.type = 'button'; x.className = 'znav ' + a[1]; x.textContent = a[2];
    x.title = a[3]; x.setAttribute('aria-label', a[3]); x.draggable = false;
    x.onclick = function (e) { e.stopPropagation(); step(a[0]); };
    x.addEventListener('dblclick', function (e) { e.stopPropagation(); e.preventDefault(); });
    el.appendChild(x);
  });
}
function swipeTiles(grid, active, step) {
  var sx = 0, sy = 0, t0 = 0, on = false;
  grid.addEventListener('touchstart', function (e) {
    on = active() && e.touches.length === 1 && !e.target.closest('.tb,.znav,.th button') && !(window.visualViewport && visualViewport.scale > 1.05);
    if (on) { sx = e.touches[0].clientX; sy = e.touches[0].clientY; t0 = Date.now(); }
  }, { passive: true });
  grid.addEventListener('touchmove', function (e) { if (e.touches.length > 1) on = false; }, { passive: true });
  grid.addEventListener('touchcancel', function () { on = false; }, { passive: true });
  grid.addEventListener('touchend', function (e) {
    if (!on) return; on = false;
    var t = e.changedTouches[0], dx = t.clientX - sx, dy = t.clientY - sy;
    if (Date.now() - t0 < 800 && Math.abs(dx) > 50 && Math.abs(dx) > 1.5 * Math.abs(dy)) step(dx < 0 ? 1 : -1); // to the left: the next one
  }, { passive: true });
}

// ---------- state ----------
var state = { layout: 4, tiles: [], view: '' }, sel = 0;
try { var s = JSON.parse(localStorage.getItem(KEY) || 'null'); if (s && s.layout) state = s; } catch (e) {}
function remember() { try { localStorage.setItem(KEY, JSON.stringify(state)); } catch (e) {} }

// ---------- player ----------
var CODECS = ['avc1.640029', 'avc1.64002A', 'avc1.640033', 'hvc1.1.6.L153.B0', 'mp4a.40.2', 'mp4a.40.5', 'flac', 'opus'];
function codecs() { return CODECS.filter(function (c) { return window.MediaSource && MediaSource.isTypeSupported('video/mp4; codecs="' + c + '"'); }).join(','); }
function qs(t) { return 'n=' + encodeURIComponent(t.n) + '&c=' + encodeURIComponent(t.c) + '&q=' + t.q; }
function quality(t, many) { var c = cam(t); if (!c) return 'lq'; if (t.q === 'hq' && c.hq || t.q === 'lq' && c.lq) return t.q; return c.lq && many ? 'lq' : (c.hq ? 'hq' : 'lq'); }
// H.265 cameras: a browser without H.265 gets "codecs not matched" from go2rtc for such a
// stream, HQ or LQ. A user allowed to (D.tchq, D.tclq) then gets it transcoded to H.264
// by the server (/live/ws?...&tc=1), after asking /live/transcode whether the limit of
// that quality lets one more in; the tile says so. hevcCams remembers those streams
// (camera and quality), so the next tile skips the try.
var hevcCams = {};
function noHevc() { return !/hvc1/.test(codecs()); }
// the codecs asked of go2rtc: the video ones only. The tiles are muted, and go2rtc lets a
// consumer in when any track matches: offered audio, a camera with H.265 video and G.711
// or AAC audio (DVRIP recorders mostly) would come as sound alone - a black tile and no
// "codecs not matched" to start the transcoding.
function videoCodecs() { return codecs().split(',').filter(function (c) { return /^(avc1|hvc1)/.test(c); }).join(','); }
// hasVideo: go2rtc's answer ({type: 'mse', value: 'video/mp4; codecs="..."'}) has a video track
function hasVideo(mime) { return /avc1|hvc1/.test(mime || ''); }
function Player(el, t) {
  var p = this, video = $('video', el); p.el = el; p.video = video; p.tc = false;
  var badge = $('.tcb', el);
  function fail(msg, wait, again) { el.classList.add('nosig'); $('.nsg', el).textContent = msg || T.lv_nosignal;
    clearTimeout(p.retry); if (!p.dead) p.retry = setTimeout(function () { if (p.dead) return; reset(); if (again) again(); else if (p.tc) transcode(); else p.start(); }, wait || 5000); }
  function reset() { clearTimeout(p.retry); if (p.ws) { p.ws.onclose = null; p.ws.close(); p.ws = null; } video.removeAttribute('src'); video.load(); }
  function showBadge() { if (!badge) return; badge.hidden = !p.tc;
    badge.title = T.lv_tc_info.replace('%s', t.q.toUpperCase()) + (p.tch ? ' (' + p.tch + 'p)' : ''); }
  function hevcKey() { return t.n + '/' + t.c + '/' + t.q; }
  // transcode: ask the server for a slot, then play the H.264 stream
  function transcode() {
    p.tc = false; showBadge();
    var no = t.q === 'lq' ? T.lv_hevc_lq : T.lv_hevc;
    if (!(t.q === 'lq' ? D.tclq : D.tchq)) { p.dead = true; fail(no); return; }
    fetch('/live/transcode?' + qs(t), { credentials: 'same-origin' }).then(function (r) { return r.json(); }).then(function (j) {
      if (p.dead) return;
      if (j.ok) { p.tc = true; p.tch = j.height; showBadge(); p.start(); return; }
      if (j.busy) { fail(T.lv_tc_busy.replace('%s', t.q.toUpperCase()).replace('%d', j.used).replace('%d', j.max), 15000, transcode); return; }
      p.dead = true; fail(j.error || no);
    }).catch(function () { if (!p.dead) fail(null, 5000, transcode); });
  }
  p.start = function () {
    el.classList.remove('nosig'); showBadge();
    if (!p.tc && hevcCams[hevcKey()] && window.MediaSource && noHevc()) { transcode(); return; }
    if (!window.MediaSource || !/avc1|hvc1/.test(codecs())) {
      if (video.canPlayType('application/vnd.apple.mpegurl')) { video.src = '/live/api/stream.m3u8?' + qs(t); video.onerror = function () { fail(); }; video.play().catch(function () {}); return; }
      p.dead = true; fail(T.lv_codec); return;
    }
    var ms = new MediaSource(); video.src = URL.createObjectURL(ms);
    ms.addEventListener('sourceopen', function () {
      var ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/live/ws?' + qs(t) + (p.tc ? '&tc=1' : ''));
      ws.binaryType = 'arraybuffer'; p.ws = ws;
      var sb = null, queue = [];
      function pump() { if (sb && !sb.updating && queue.length) { try { sb.appendBuffer(queue.shift()); } catch (e) { fail(); } } }
      function trim() { if (!sb || sb.updating || !sb.buffered.length) return; var end = sb.buffered.end(sb.buffered.length - 1), st = sb.buffered.start(0);
        if (end - video.currentTime > 3) video.currentTime = end - 0.5; if (end - st > 30) { try { sb.remove(st, end - 20); } catch (e) {} } }
      ws.onopen = function () { ws.send(JSON.stringify({ type: 'mse', value: videoCodecs() })); };
      ws.onmessage = function (e) {
        if (typeof e.data === 'string') { var m = JSON.parse(e.data);
          if (m.type === 'mse' && !hasVideo(m.value)) { // no video of a codec this browser plays
            if (!p.tc && noHevc()) { hevcCams[hevcKey()] = true; reset(); transcode(); } else fail();
            return; }
          if (m.type === 'mse') { try { sb = ms.addSourceBuffer(m.value); sb.mode = 'segments'; sb.addEventListener('updateend', function () { pump(); trim(); }); } catch (x) { fail(); } }
          else if (m.type === 'error') {
            if (!p.tc && /codecs not matched/i.test(m.value || '') && noHevc()) { hevcCams[hevcKey()] = true; reset(); transcode(); }
            else fail(m.value);
          }
          return; }
        queue.push(e.data); if (queue.length > 100) queue.splice(0, queue.length - 100); pump();
      };
      ws.onclose = function () { if (!p.dead) fail(); };
    });
    video.play().catch(function () {});
  };
  p.stop = function () { p.dead = true; reset(); };
  p.start();
}
function tileHTML() {
  return '<div class="th"><span class="live">LIVE</span><button type="button" class="tcb" hidden></button><span class="tn"></span><span class="tr"></span></div><video muted autoplay playsinline></video>' +
    '<div class="nsg"></div><div class="tf"><span class="ts"></span><span class="tb"><button type="button" class="q"></button><button type="button" class="snap">📷</button><a class="arch">⟲</a><button type="button" class="x">✕</button></span></div>';
}
// archiveHref: where the recordings of cameras open, as far as the rights go (null: no
// right archive). The player (archive.play) plays those of them it plays, from two
// minutes ago, all on its tiles (c=recorder|camera); a camera it does not play (an
// external recorder) opens on its recorder's search.
function archiveHref(cs) {
  if (!D.archive || !cs.length) return null;
  var pl = D.archplay ? cs.filter(function (c) { return c.play; }) : [];
  if (!pl.length) return '/?p=archive&n=' + encodeURIComponent(cs[0].n) + '&tab=search&search=1&c=' + encodeURIComponent(cs[0].c);
  return '/?p=archive&n=' + encodeURIComponent(pl[0].n) + '&tab=play&t=' + Math.floor(Date.now() / 1000 - 120) +
    pl.map(function (c) { return '&c=' + encodeURIComponent(c.n + '|' + c.c); }).join('');
}
function fillTile(el, t, onClose, onQuality) {
  var c = cam(t); el.innerHTML = tileHTML();
  $('.tn', el).textContent = c.label; $('.tr', el).textContent = c.nvr;
  var qb = $('.q', el); qb.textContent = t.q.toUpperCase(); qb.disabled = !(c.hq && c.lq); qb.title = 'HQ / LQ';
  qb.onclick = function (e) { e.stopPropagation(); onQuality(); };
  var sn = $('.snap', el); sn.title = T.lv_snapshot; sn.onclick = function (e) { e.stopPropagation(); window.open('/live/frame.jpeg?' + qs(t), '_blank'); };
  var ar = $('.arch', el), ah = archiveHref([c]); ar.title = T.lv_archive; ar.hidden = !ah; if (ah) ar.href = ah;
  var x = $('.x', el); x.title = T.lv_close; x.onclick = function (e) { e.stopPropagation(); onClose(); };
  var tb = $('.tcb', el); tb.textContent = T.lv_tc_badge; // the explanation also where no hover shows the title
  tb.onclick = function (e) { e.stopPropagation(); $('#lhint').textContent = tb.title; };
  return new Player(el, t);
}

// ---------- the grid ----------
// render reconciles the grid with the state instead of building it anew: a tile whose
// camera and quality stay keeps its element and its player (and its connection), so
// closing, moving or adding a tile does not interrupt the others. Tiles are placed by
// the CSS order of the grid, never moved in the document: a moved <video> may stop.
var grid = $('#lgrid'), players = [], tileEls = []; // tileEls: {el, key, player} on the page
function stopAll() { tileEls.forEach(function (x) { if (x.player) x.player.stop(); }); tileEls = []; players = []; grid.innerHTML = ''; }
function tileKey(t) { return t.n + '\n' + t.c + '\n' + t.q; }
function tileAt(el) { return +el.dataset.i; }
function wireTile(el) { // the handlers read the tile's place when they run: it changes
  el.onclick = function () { sel = tileAt(el); $$('.tile', grid).forEach(function (x) { x.classList.toggle('sel', tileAt(x) === sel); }); hint();
    if (!state.tiles[sel] && narrow()) window.ffnvrNav.open(); };
  el.addEventListener('dblclick', function (e) { if (e.target.closest('.tb,.znav')) return; e.preventDefault(); setBig(big >= 0 ? -1 : tileAt(el)); });
  el.addEventListener('dragstart', function (e) { e.dataTransfer.setData('text/x-tile', String(tileAt(el))); e.dataTransfer.effectAllowed = 'move'; });
  el.addEventListener('dragover', function (e) { e.preventDefault(); el.classList.add('over'); });
  el.addEventListener('dragleave', function () { el.classList.remove('over'); });
  el.addEventListener('drop', function (e) {
    e.preventDefault(); el.classList.remove('over');
    var i = tileAt(el), from = e.dataTransfer.getData('text/x-tile'), c = e.dataTransfer.getData('text/x-cam');
    if (from !== '') { from = +from; if (from === i) return; var a = state.tiles[from] || null; state.tiles[from] = state.tiles[i] || null; state.tiles[i] = a; sel = i; }
    else if (c) { var k = c.split('\n'); state.tiles[i] = { n: k[0], c: k[1], q: state.layout > 1 ? 'lq' : 'hq' }; sel = i; }
    else return;
    remember(); render();
  });
}
function render() {
  var n = state.layout;
  if (big >= n || big >= 0 && !(state.tiles[big] && live(cam(state.tiles[big])))) big = -1;
  grid.style.setProperty('--cols', big >= 0 ? 1 : Math.sqrt(n)); grid.classList.toggle('big', big >= 0);
  if (sel >= n) sel = 0;
  var old = tileEls, next = [], nav = big >= 0 && zoomables().length > 1;
  players = [];
  for (var i = 0; i < n; i++) {
    var t = state.tiles[i], key = '', found = null;
    var shown = t; // what the tile plays: the zoomed one its own quality (HQ when there is one)
    if (t && live(cam(t))) { t.q = quality(t, n > 1); if (i === big) shown = { n: t.n, c: t.c, q: bigQ }; key = tileKey(shown); }
    if (key) for (var j = 0; j < old.length; j++) if (old[j] && old[j].key === key) { found = old[j]; old[j] = null; break; }
    if (!found) {
      var el = document.createElement('div'); el.tabIndex = 0; el.draggable = true; wireTile(el);
      found = { el: el, key: key, player: null };
      if (key) {
        (function (x, t) {
          x.player = fillTile(x.el, t, function () { var i = tileAt(x.el); state.tiles[i] = null; if (i === big) big = -1; render(); },
            function () { if (tileAt(x.el) === big) bigQ = bigQ === 'hq' ? 'lq' : 'hq'; else state.tiles[tileAt(x.el)].q = t.q === 'hq' ? 'lq' : 'hq'; render(); });
        })(found, shown);
      } else { el.innerHTML = '<div class="nsg">' + (narrow() ? T.lv_empty_m : T.lv_empty) + '</div>'; }
      grid.appendChild(el);
    }
    found.el.dataset.i = i; found.el.style.order = i;
    found.el.className = 'tile' + (key ? '' : ' empty') + (found.player && found.el.classList.contains('nosig') ? ' nosig' : '') + (i === sel ? ' sel' : '') + (i === big ? ' zoomed' : '');
    zoomNav(found.el, nav && i === big, stepBig, T);
    next.push(found); players[i] = found.player;
  }
  old.forEach(function (x) { if (x) { if (x.player) x.player.stop(); x.el.remove(); } }); // no longer shown
  tileEls = next;
  $$('[data-layout]').forEach(function (b) { b.classList.toggle('primary', +b.dataset.layout === n); });
  $$('.lcam').forEach(function (b) { b.classList.toggle('on', state.tiles.slice(0, n).some(function (t) { return t && t.n === b.dataset.n && t.c === b.dataset.c; })); });
  hint(); remember(); renderViews();
}
function hint() { var c = cam(state.tiles[sel]); $('#lhint').textContent = (big >= 0 ? (narrow() ? T.lv_zoomed_m : T.lv_zoomed) : narrow() ? T.lv_tile_m : T.lv_tile).replace('%d', sel + 1).replace('%s', c ? c.label : '—'); ptzUpdate(); }
// a big tile: a double click shows one tile over the whole grid (in HQ when the camera has
// it and the user's profile says so, D.zoomhq; else in the quality the tile had), another
// double click or Esc goes back. The other tiles keep playing, hidden, so going back is
// instant; it is not remembered. ‹ ›, ←/→ and a sideways swipe show the previous or the
// next camera of the grid in its place.
var big = -1, bigQ = 'hq';
function setBig(i) {
  if (i >= 0) { var t = state.tiles[i], c = t && cam(t); if (!live(c) || state.layout === 1) return; bigQ = D.zoomhq && c.hq ? 'hq' : t.q; sel = i; }
  big = i; render();
}
// zoomables: the places of the grid that can be enlarged (a camera with a live stream)
function zoomables() { var r = []; for (var i = 0; i < state.layout; i++) if (state.tiles[i] && live(cam(state.tiles[i]))) r.push(i); return r; }
function stepBig(d) { if (big < 0) return; var z = zoomables(), k = z.indexOf(big); if (z.length > 1) setBig(z[(k + d + z.length) % z.length]); }
swipeTiles(grid, function () { return big >= 0; }, stepBig);
document.addEventListener('keydown', function (e) {
  if (big < 0) return;
  if (e.key === 'Escape' && !document.fullscreenElement) { setBig(-1); return; }
  if ((e.key === 'ArrowLeft' || e.key === 'ArrowRight') && !e.altKey && !e.ctrlKey && !e.metaKey && !(e.target.closest && e.target.closest('input,textarea,select,[contenteditable]'))) {
    e.preventDefault(); stepBig(e.key === 'ArrowLeft' ? -1 : 1);
  }
});
$$('.lcam').forEach(function (b) {
  b.onclick = function () { state.tiles[sel] = { n: b.dataset.n, c: b.dataset.c, q: state.layout > 1 ? 'lq' : 'hq' }; if (sel < state.layout - 1) sel++; render(); };
  b.addEventListener('dragstart', function (e) { e.dataTransfer.setData('text/x-cam', b.dataset.n + '\n' + b.dataset.c); });
});
$$('[data-layout]').forEach(function (b) { b.onclick = function () { state.layout = +b.dataset.layout; big = -1; render(); }; });
$('#lsearch').oninput = function () { var v = this.value.toLowerCase();
  $$('.lcam').forEach(function (b) { b.style.display = (b.dataset.label + ' ' + b.dataset.c).toLowerCase().indexOf(v) >= 0 ? '' : 'none'; }); };
setInterval(function () { var d = new Date(), s = d.getFullYear() + '-' + ('0' + (d.getMonth() + 1)).slice(-2) + '-' + ('0' + d.getDate()).slice(-2) + ' ' + d.toTimeString().slice(0, 8);
  $$('.tile .ts').forEach(function (x) { x.textContent = s; }); }, 1000);
$('#lfull').onclick = function () { var el = mapMode ? $('#lmap') : thumbMode ? $('#lthumbs') : grid; if (el.requestFullscreen) el.requestFullscreen(); };

// ---------- PTZ (right live.ptz) ----------
// The panel controls the camera of the selected tile. A held button starts its action
// and its release stops it; the commands go out one after another (a stop never
// overtakes its start) and a held button repeats its start every few seconds, so the
// server's watchdog stops the camera only when the browser is gone.
var PZ = { el: $('#lptz'), key: 'ffnvr.ptz', hold: null, queue: Promise.resolve(), aux: {}, pref: { hidden: false, speed: 5 } };
try { var pz = JSON.parse(localStorage.getItem(PZ.key) || 'null'); if (pz) PZ.pref = pz; } catch (e) {}
function pzSave() { try { localStorage.setItem(PZ.key, JSON.stringify(PZ.pref)); } catch (e) {} }
function pzCam() { var t = state.tiles[sel]; return t && live(cam(t)) ? { t: t, c: cam(t) } : null; } // the camera the tile shows
// direction swap of a camera: the configuration (ptz-swap-*), or this browser's own
// choice until it is saved
PZ.swaps = {}; try { PZ.swaps = JSON.parse(localStorage.getItem('ffnvr.ptzswap') || '{}') || {}; } catch (e) {}
function pzKey(x) { return x.t.n + '\n' + x.t.c; }
function pzConf(x) { return { h: !!x.c.swaph, v: !!x.c.swapv }; }
function pzSwap(x) { return PZ.swaps[pzKey(x)] || pzConf(x); }
function pzSetSwap(x, sw) {
  var c = pzConf(x);
  if (sw.h === c.h && sw.v === c.v) delete PZ.swaps[pzKey(x)]; else PZ.swaps[pzKey(x)] = sw;
  try { localStorage.setItem('ffnvr.ptzswap', JSON.stringify(PZ.swaps)); } catch (e) {}
}
var PZMOVES = /^(left|right|up|down|leftup|rightup|leftdown|rightdown)$/;
function pzMap(a, sw) {
  if (!PZMOVES.test(a)) return a;
  if (sw.h) a = a.replace(/left|right/, function (m) { return m === 'left' ? 'right' : 'left'; });
  if (sw.v) a = a.replace(/up|down/, function (m) { return m === 'up' ? 'down' : 'up'; });
  return a;
}
function pzMsg(text, err) { var m = $('#pzmsg'); if (!m) return; m.textContent = text || ''; m.classList.toggle('err', !!err); }
function pzShown() { return D.ptz && !mapMode && !thumbMode; }
function pzSend(target, a, on) {
  var body = { n: target.n, c: target.c, a: a, on: on ? 1 : 0, s: PZ.pref.speed };
  var run = PZ.queue.then(function () { return post('/live/ptz', body); })
    .then(function (r) { if (!r.ok) throw new Error(r.error || 'error'); pzMsg(T.pz_hold); return true; })
    .catch(function (e) { pzMsg(T.pz_error.replace('%s', e.message), true); return false; });
  PZ.queue = run;
  return run;
}
function pzRelease() {
  var h = PZ.hold; if (!h) return;
  PZ.hold = null; clearInterval(h.beat); h.btn.classList.remove('held');
  pzSend(h.target, h.a, false);
}
function pzPress(btn) {
  var x = pzCam(); if (!x || !x.c.ptz) return;
  pzRelease();
  var target = { n: x.t.n, c: x.t.c }, a = btn.dataset.a ? pzMap(btn.dataset.a, pzSwap(x)) : btn.dataset.aux; // swapped when pressed: the release stops the same
  PZ.hold = { btn: btn, a: a, target: target, beat: setInterval(function () { pzSend(target, a, true); }, 4000) };
  btn.classList.add('held');
  pzSend(target, a, true);
}
function pzWireHold(btn) {
  btn.addEventListener('pointerdown', function (e) { if (e.button !== 0) return; e.preventDefault(); try { btn.setPointerCapture(e.pointerId); } catch (x) {} pzPress(btn); });
  ['pointerup', 'pointercancel', 'lostpointercapture'].forEach(function (ev) { btn.addEventListener(ev, function () { if (PZ.hold && PZ.hold.btn === btn) pzRelease(); }); });
  btn.addEventListener('keydown', function (e) { if ((e.key === ' ' || e.key === 'Enter') && !e.repeat) { e.preventDefault(); pzPress(btn); } });
  btn.addEventListener('keyup', function (e) { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); if (PZ.hold && PZ.hold.btn === btn) pzRelease(); } });
  btn.addEventListener('blur', function () { if (PZ.hold && PZ.hold.btn === btn) pzRelease(); });
  btn.addEventListener('contextmenu', function (e) { e.preventDefault(); });
}
function pzAuxKey(x, a) { return x.t.n + '\n' + x.t.c + '\n' + a; }
function ptzUpdate() {
  if (!PZ.el) return;
  var show = pzShown();
  PZ.el.hidden = !show || PZ.pref.hidden; $('#pzshow').hidden = !show || !PZ.pref.hidden;
  if (!show) { pzRelease(); return; }
  var x = pzCam(), caps = x && x.c.ptz;
  if (PZ.hold && (!x || PZ.hold.target.n !== x.t.n || PZ.hold.target.c !== x.t.c)) pzRelease(); // another tile
  $('#pzcam').innerHTML = '';
  if (x) { var b = document.createElement('b'); b.textContent = x.c.label; $('#pzcam').appendChild(b); $('#pzcam').appendChild(document.createTextNode(' · ' + x.c.nvr)); }
  else $('#pzcam').textContent = T.pz_none;
  $('#pzbody').classList.toggle('off', !caps);
  $$('[data-need]', PZ.el).forEach(function (el) { el.hidden = caps ? !caps[el.dataset.need] : false; });
  var auxs = (caps && caps.aux) || [], names = auxs.map(function (a) { return a.name; });
  $$('.pzaux', PZ.el).forEach(function (btn) {
    var i = names.indexOf(btn.dataset.aux); btn.hidden = i < 0; if (i < 0) return;
    btn.dataset.hold = auxs[i].hold ? '1' : '';
    if (auxs[i].hold) btn.removeAttribute('aria-pressed'); else btn.setAttribute('aria-pressed', PZ.aux[pzAuxKey(x, btn.dataset.aux)] ? 'true' : 'false');
  });
  $('#pzauxs').classList.toggle('none', !names.length);
  if (x && caps) {
    var sw = pzSwap(x), cf = pzConf(x), local = sw.h !== cf.h || sw.v !== cf.v;
    $('#pzswaph').checked = sw.h; $('#pzswapv').checked = sw.v;
    $('#pzflag').textContent = (sw.h ? '⇄' : '') + (sw.v ? '⇅' : '');
    $('#pzswapsave').hidden = !(local && D.pzedit && D.edit);
    $('#pzswapnote').textContent = local ? T.pz_swap_local : (PZ.saved === pzKey(x) ? T.pz_swap_saved : '');
  }
  if (!x) pzMsg(''); else if (!caps) pzMsg(T.pz_unsupported); else if (!$('#pzmsg').classList.contains('err')) pzMsg(T.pz_hold);
}
if (PZ.el) {
  $$('[data-hold]', PZ.el).forEach(pzWireHold);
  $$('.pzaux', PZ.el).forEach(function (btn) {
    var held = function () { return btn.dataset.hold === '1'; };
    btn.addEventListener('pointerdown', function (e) { if (held() && e.button === 0) { e.preventDefault(); try { btn.setPointerCapture(e.pointerId); } catch (x) {} pzPress(btn); } });
    ['pointerup', 'pointercancel', 'lostpointercapture'].forEach(function (ev) { btn.addEventListener(ev, function () { if (PZ.hold && PZ.hold.btn === btn) pzRelease(); }); });
    btn.addEventListener('click', function () {
      if (held()) return;
      var x = pzCam(); if (!x || !x.c.ptz) return;
      var k = pzAuxKey(x, btn.dataset.aux), on = !PZ.aux[k];
      pzSend({ n: x.t.n, c: x.t.c }, btn.dataset.aux, on).then(function (ok) { if (ok) { PZ.aux[k] = on; ptzUpdate(); } });
    });
  });
  $('.pzstop', PZ.el).onclick = function () { var x = pzCam(); if (!x || !x.c.ptz) return; if (PZ.hold) { PZ.hold.btn.classList.remove('held'); clearInterval(PZ.hold.beat); PZ.hold = null; } pzSend({ n: x.t.n, c: x.t.c }, 'stop', false); };
  var sp = $('#pzspeed'); sp.max = D.ptzmax || 8; sp.value = Math.min(+PZ.pref.speed || 5, +sp.max); $('#pzspeedv').textContent = sp.value;
  sp.oninput = function () { PZ.pref.speed = +sp.value; $('#pzspeedv').textContent = sp.value; pzSave(); };
  $('#pzhide').onclick = function () { pzRelease(); PZ.pref.hidden = true; pzSave(); ptzUpdate(); $('#pzshow').focus(); };
  $('#pzshow').onclick = function () { PZ.pref.hidden = false; pzSave(); ptzUpdate(); };
  $$('#pzswaph,#pzswapv').forEach(function (cb) {
    cb.onchange = function () { var x = pzCam(); if (!x) return; pzRelease(); PZ.saved = '';
      pzSetSwap(x, { h: $('#pzswaph').checked, v: $('#pzswapv').checked }); ptzUpdate(); };
  });
  $('#pzswapsave').onclick = function () {
    var x = pzCam(); if (!x) return; var sw = pzSwap(x), btn = this; btn.disabled = true;
    post('/live/ptz/swap', { n: x.t.n, c: x.t.c, h: sw.h ? 1 : 0, v: sw.v ? 1 : 0 }).then(function (r) {
      btn.disabled = false;
      if (!r.ok) { pzMsg(T.pz_error.replace('%s', r.error || 'error'), true); return; }
      x.c.swaph = sw.h; x.c.swapv = sw.v; pzSetSwap(x, sw); PZ.saved = pzKey(x); ptzUpdate();
    }, function () { btn.disabled = false; });
  };
  window.addEventListener('blur', pzRelease);
  document.addEventListener('visibilitychange', function () { if (document.hidden) pzRelease(); });
  window.addEventListener('pagehide', function () {
    var h = PZ.hold; if (!h) return; PZ.hold = null;
    var body = new URLSearchParams({ csrf: D.csrf, n: h.target.n, c: h.target.c, a: h.a, on: '0', s: PZ.pref.speed });
    if (navigator.sendBeacon) navigator.sendBeacon('/live/ptz', body);
  });
}

// ---------- saved views (the user's profile, dashboards.json) ----------
function post(url, data) {
  var body = new URLSearchParams(); body.set('csrf', D.csrf);
  Object.keys(data).forEach(function (k) { body.set(k, data[k]); });
  return fetch(url, { method: 'POST', body: body, credentials: 'same-origin' }).then(function (r) { return r.json(); });
}
function renderViews() {
  var box = $('#lviews'); box.innerHTML = '';
  if (!D.views.length) { box.innerHTML = '<div class="note" style="padding:6px 16px 10px">' + T.lv_no_views + '</div>'; return; }
  D.views.forEach(function (v) {
    var row = document.createElement('div'); row.className = 'lview' + (v.id === state.view ? ' cur' : '');
    var a = document.createElement('button'); a.type = 'button'; a.className = 'lvn'; a.textContent = v.name; a.title = v.size + ' · ' + v.name;
    a.onclick = function () { state.layout = v.size; state.tiles = []; state.view = v.id;
      (v.tiles || []).forEach(function (t) { state.tiles[t.pos - 1] = { n: t.n, c: t.c, q: t.q }; }); sel = 0; render(); };
    var note = document.createElement('span'); note.className = 'note'; note.textContent = v.size;
    var del = document.createElement('button'); del.type = 'button'; del.className = 'lvdel'; del.textContent = '✕'; del.title = T.lv_delete;
    del.hidden = !D.vedit; // views are in the user's profile: no need of the configuration
    del.onclick = function () { var d = $('#ddel'); $('h2', d).textContent = T.lv_del_q.replace('%s', v.name); d.returnValue = '';
      d.onclose = function () { if (d.returnValue !== 'ok') return;
        post('/live/view/delete', { id: v.id }).then(function (r) { if (!r.ok) { alert(r.error); return; }
          D.views = D.views.filter(function (x) { return x.id !== v.id; }); if (state.view === v.id) state.view = ''; renderViews(); }); };
      d.showModal(); };
    row.appendChild(a); row.appendChild(note); row.appendChild(del); box.appendChild(row);
  });
}
$('#lsave').hidden = !D.vedit; // the rights of the user (policy)
$('#medit').hidden = !D.medit;
$('#tabmap').hidden = !D.map;
$('#tabthumbs').hidden = !D.thumbs;
$('#lsave').onclick = function () {
  var d = $('#dsave'), cur = D.views.filter(function (v) { return v.id === state.view; })[0];
  $('#dsname').value = cur ? cur.name : ''; $('#dsnew').checked = !cur; $('#dsnewl').hidden = !cur; d.returnValue = '';
  d.onclose = function () {
    if (d.returnValue !== 'ok' || !$('#dsname').value.trim()) return;
    var tiles = []; for (var i = 0; i < state.layout; i++) { var t = state.tiles[i]; if (t && cam(t)) tiles.push({ pos: i + 1, n: t.n, c: t.c, q: t.q }); }
    var id = cur && !$('#dsnew').checked ? cur.id : '', name = $('#dsname').value.trim();
    post('/live/view/save', { id: id, name: name, size: state.layout, tiles: JSON.stringify(tiles) }).then(function (r) {
      if (!r.ok) { alert(r.error); return; }
      D.views = D.views.filter(function (v) { return v.id !== r.id; }); D.views.push({ id: r.id, name: name, size: state.layout, tiles: tiles });
      state.view = r.id; remember(); renderViews(); $('#lsave').textContent = T.lv_saved; setTimeout(function () { $('#lsave').textContent = T.lv_save; }, 2000);
    });
  };
  d.showModal();
};

// ---------- the map ----------
var mapMode = false, thumbMode = false, map = $('#lmap'), zoom = 6, center = { lat: 52.0, lng: 19.0 };
var pos = {}; (D.positions || []).forEach(function (p) { pos[p.n + '/' + p.c] = p; });
var editing = false, changed = {}, selected = null, strip = [], stripPlayers = [];
var TILE = 256;
function project(lat, lng, z) { var s = TILE * Math.pow(2, z), x = (lng + 180) / 360 * s, r = lat * Math.PI / 180;
  return { x: x, y: (1 - Math.log(Math.tan(r) + 1 / Math.cos(r)) / Math.PI) / 2 * s }; }
function unproject(x, y, z) { var s = TILE * Math.pow(2, z), lng = x / s * 360 - 180, n = Math.PI - 2 * Math.PI * y / s;
  return { lat: 180 / Math.PI * Math.atan(0.5 * (Math.exp(n) - Math.exp(-n))), lng: lng }; }
function mpp(lat) { return 156543.03392 * Math.cos(lat * Math.PI / 180) / Math.pow(2, zoom); } // meters per pixel
function size() { return { w: map.clientWidth, h: map.clientHeight }; }
function toScreen(lat, lng) { var c = project(center.lat, center.lng, zoom), p = project(lat, lng, zoom), s = size(); return { x: p.x - c.x + s.w / 2, y: p.y - c.y + s.h / 2 }; }
function toLatLng(x, y) { var c = project(center.lat, center.lng, zoom), s = size(); return unproject(c.x + x - s.w / 2, c.y + y - s.h / 2, zoom); }
function drawTiles() {
  var box = $('.mtiles', map); box.innerHTML = '';
  if (!$('#lystreet').checked) return;
  var c = project(center.lat, center.lng, zoom), s = size(), n = Math.pow(2, zoom);
  var x0 = Math.floor((c.x - s.w / 2) / TILE), x1 = Math.floor((c.x + s.w / 2) / TILE), y0 = Math.floor((c.y - s.h / 2) / TILE), y1 = Math.floor((c.y + s.h / 2) / TILE);
  for (var x = x0; x <= x1; x++) for (var y = y0; y <= y1; y++) {
    if (y < 0 || y >= n) continue;
    var img = document.createElement('img'), wx = ((x % n) + n) % n;
    img.src = D.tiles.replace('{z}', zoom).replace('{x}', wx).replace('{y}', y).replace('{s}', 'abc'[Math.abs(x + y) % 3]);
    img.style.left = (x * TILE - c.x + s.w / 2) + 'px'; img.style.top = (y * TILE - c.y + s.h / 2) + 'px'; img.alt = ''; img.draggable = false; img.onerror = function () { this.style.visibility = 'hidden'; };
    box.appendChild(img);
  }
}
function sector(p, cx, cy) { // the field of view: a circle sector from the camera
  var r = Math.max(6, p.range / mpp(p.lat)), a0 = (p.dir - p.angle / 2 - 90) * Math.PI / 180, a1 = (p.dir + p.angle / 2 - 90) * Math.PI / 180;
  if (p.angle >= 359) return 'M' + (cx - r) + ',' + cy + 'a' + r + ',' + r + ' 0 1,0 ' + 2 * r + ',0a' + r + ',' + r + ' 0 1,0 ' + (-2 * r) + ',0';
  return 'M' + cx + ',' + cy + 'L' + (cx + r * Math.cos(a0)) + ',' + (cy + r * Math.sin(a0)) + 'A' + r + ',' + r + ' 0 ' + (p.angle > 180 ? 1 : 0) + ',1 ' + (cx + r * Math.cos(a1)) + ',' + (cy + r * Math.sin(a1)) + 'Z';
}
var markEls = {}, handleEl = null;
function drawMap() {
  if (!mapMode) return;
  drawTiles();
  var svg = $('.mfov', map), marks = $('.mmarks', map), s = size(); svg.setAttribute('width', s.w); svg.setAttribute('height', s.h);
  svg.innerHTML = '';
  var fov = $('#lyfov').checked, names = $('#lynames').checked, ns = 'http://www.w3.org/2000/svg', seen = {};
  // the markers are kept (not rebuilt): a drag must not lose its element
  Object.keys(pos).forEach(function (k) {
    var p = pos[k], c = cams[k]; if (!c) return; seen[k] = true;
    var q = toScreen(p.lat, lngOf(p));
    if (fov) { var path = document.createElementNS(ns, 'path'); path.setAttribute('d', sector(p, q.x, q.y));
      path.setAttribute('class', 'fov' + (selected === k ? ' sel' : '') + (c.kind === 'err' || c.kind === 'warn' ? ' bad' : '')); svg.appendChild(path); }
    var m = markEls[k];
    if (!m) { m = document.createElement('div'); markEls[k] = m;
      m.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4"><path d="M15 10l5-3v10l-5-3zM3 7h12v10H3z"/></svg><span></span>';
      $('span', m).textContent = c.label; m.title = c.label + ' · ' + c.nvr; markerEvents(m, k); marks.appendChild(m); }
    m.className = 'mk ' + (c.kind || 'mute') + (selected === k ? ' sel' : '') + (editing ? ' edit' : '') + (names ? '' : ' nonames');
    m.style.left = q.x + 'px'; m.style.top = q.y + 'px';
  });
  Object.keys(markEls).forEach(function (k) { if (!seen[k]) { markEls[k].remove(); delete markEls[k]; } });
  if (!handleEl) { handleEl = document.createElement('div'); handleEl.className = 'mh'; handleEl.title = T.mp_handle; handleEvents(handleEl); marks.appendChild(handleEl); }
  if (editing && selected && pos[selected]) { // the handle: where it looks and how far
    var p = pos[selected], q = toScreen(p.lat, lngOf(p)), r = Math.max(20, p.range / mpp(p.lat)), a = (p.dir - 90) * Math.PI / 180;
    handleEl.style.left = (q.x + r * Math.cos(a)) + 'px'; handleEl.style.top = (q.y + r * Math.sin(a)) + 'px'; handleEl.hidden = false;
  } else handleEl.hidden = true;
  var at = $('.mattr', map); at.textContent = $('#lystreet').checked && D.osm ? '© OpenStreetMap contributors' : '';
  renderMapSide(); renderSel();
}
function lngOf(p) { return p.lng !== undefined ? p.lng : p.long; }
function drag(el, move, end) {
  el.addEventListener('pointerdown', function (e) {
    if (e.button !== 0) return; e.preventDefault(); e.stopPropagation(); el.setPointerCapture(e.pointerId);
    var sx = e.clientX, sy = e.clientY, moved = false;
    function mv(ev) { if (Math.abs(ev.clientX - sx) + Math.abs(ev.clientY - sy) > 3) moved = true; if (moved) move(ev, ev.clientX - sx, ev.clientY - sy); }
    function up(ev) { el.removeEventListener('pointermove', mv); el.removeEventListener('pointerup', up); end(ev, moved); }
    el.addEventListener('pointermove', mv); el.addEventListener('pointerup', up);
  });
}
// previews: the last picture of each camera is kept (as a blob) and shown at once, never
// the picture of another camera; a fresh one replaces it when it arrives
var shots = {}, hovered = null;
function preview(k) {
  var c = cams[k], pv = $('.mprev', map), img = $('img', pv), q = toScreen(pos[k].lat, lngOf(pos[k]));
  hovered = k; $('b', pv).textContent = c.label;
  if (shots[k]) { img.src = shots[k]; pv.classList.remove('loading'); } else { img.removeAttribute('src'); pv.classList.add('loading'); }
  pv.style.left = Math.min(q.x + 14, size().w - 250) + 'px'; pv.style.top = Math.max(q.y - 160, 4) + 'px'; pv.hidden = false;
  fetch('/live/frame.jpeg?' + qs({ n: c.n, c: c.c, q: c.lq ? 'lq' : 'hq' }), { credentials: 'same-origin' })
    .then(function (r) { if (!r.ok) throw new Error(r.status); return r.blob(); })
    .then(function (b) { if (shots[k]) URL.revokeObjectURL(shots[k]); shots[k] = URL.createObjectURL(b);
      if (hovered === k) { img.src = shots[k]; pv.classList.remove('loading'); } })
    .catch(function () { if (hovered === k) pv.classList.add('loading'); });
}
function markerEvents(m, k) {
  var c = cams[k];
  m.onmouseenter = function () { if (editing || !live(c)) return; preview(k); };
  m.onmouseleave = function () { hovered = null; $('.mprev', map).hidden = true; };
  drag(m, function (ev) { if (!editing) return; var r = map.getBoundingClientRect(), ll = toLatLng(ev.clientX - r.left, ev.clientY - r.top);
      pos[k].lat = ll.lat; pos[k].lng = ll.lng; changed[k] = true; selected = k; drawMap(); },
    function (ev, moved) { if (moved) return; if (editing) { selected = k; drawMap(); } else if (live(c)) addStrip(k); });
}
function handleEvents(h) {
  drag(h, function (ev) { var k = selected; if (!k || !pos[k]) return; var r = map.getBoundingClientRect(), p = pos[k], q = toScreen(p.lat, lngOf(p)), dx = ev.clientX - r.left - q.x, dy = ev.clientY - r.top - q.y;
    p.dir = Math.round((Math.atan2(dy, dx) * 180 / Math.PI + 90 + 360) % 360); p.range = Math.max(1, Math.round(Math.sqrt(dx * dx + dy * dy) * mpp(p.lat))); changed[k] = true; drawMap(); },
    function () {});
}
var selShown = null;
function renderSel() { // the panel of the selected camera while editing
  var box = $('.msel', map); if (!editing || !selected || !pos[selected]) { box.hidden = true; selShown = null; return; }
  var p = pos[selected], c = cams[selected]; box.hidden = false;
  if (selShown === selected) { $$('input', box).forEach(function (i) { if (document.activeElement !== i) i.value = Math.round(p[i.dataset.k]); }); return; }
  selShown = selected;
  box.innerHTML = '<b></b><label>' + T.mp_dir + ' <input type="number" min="0" max="359" data-k="dir"></label><label>' + T.mp_angle + ' <input type="number" min="1" max="360" data-k="angle"></label>' +
    '<label>' + T.mp_range + ' <input type="number" min="1" max="5000" data-k="range"></label><button type="button" class="btn sm danger">' + T.mp_remove + '</button>';
  $('b', box).textContent = c.label;
  $$('input', box).forEach(function (i) { i.value = Math.round(p[i.dataset.k]); i.onchange = function () { p[i.dataset.k] = +i.value; changed[selected] = true; drawMap(); }; });
  $('button', box).onclick = function () { changed[selected] = 'remove'; delete pos[selected]; selected = null; selShown = null; drawMap(); };
}
function renderMapSide() {
  var box = $('#mcams'), keys = Object.keys(pos).filter(function (k) { return cams[k]; }); $('#moncount').textContent = keys.length; box.innerHTML = '';
  var compass = ['N', 'NE', 'E', 'SE', 'S', 'SW', 'W', 'NW'];
  keys.sort(function (a, b) { return cams[a].label.localeCompare(cams[b].label); }).forEach(function (k) {
    var c = cams[k], p = pos[k], b = document.createElement('button'); b.type = 'button'; b.className = 'mrow' + (selected === k ? ' cur' : '');
    b.innerHTML = '<span class="dot ' + (c.kind || 'mute') + '"></span><span class="ln"></span><span class="note">' + Math.round(p.dir) + '° ' + compass[Math.round(p.dir / 45) % 8] + '</span>';
    $('.ln', b).textContent = c.label;
    b.onclick = function () { center = { lat: p.lat, lng: lngOf(p) }; if (zoom < 17) zoom = 18; selected = k; drawMap(); };
    box.appendChild(b);
  });
}
function fit() { // the view of all positioned cameras
  var ks = Object.keys(pos).filter(function (k) { return cams[k]; });
  if (!ks.length) return;
  var la = ks.map(function (k) { return pos[k].lat; }), lo = ks.map(function (k) { return lngOf(pos[k]); });
  center = { lat: (Math.min.apply(null, la) + Math.max.apply(null, la)) / 2, lng: (Math.min.apply(null, lo) + Math.max.apply(null, lo)) / 2 };
  var s = size(); for (zoom = 19; zoom > 3; zoom--) { var a = project(Math.max.apply(null, la), Math.min.apply(null, lo), zoom), b = project(Math.min.apply(null, la), Math.max.apply(null, lo), zoom);
    if (b.x - a.x < s.w - 120 && b.y - a.y < s.h - 120) break; }
}
// pan and zoom
(function () {
  var start = null;
  map.addEventListener('pointerdown', function (e) { if (e.target.closest('.mk,.mh,.mzoom,.msel,button,input')) return; start = { x: e.clientX, y: e.clientY, c: project(center.lat, center.lng, zoom) }; map.setPointerCapture(e.pointerId); map.classList.add('pan'); });
  map.addEventListener('pointermove', function (e) { if (!start) return; var p = unproject(start.c.x - (e.clientX - start.x), start.c.y - (e.clientY - start.y), zoom); center = { lat: p.lat, lng: p.lng }; drawMap(); });
  map.addEventListener('pointerup', function () { start = null; map.classList.remove('pan'); });
  map.addEventListener('wheel', function (e) { e.preventDefault(); var r = map.getBoundingClientRect(), x = e.clientX - r.left, y = e.clientY - r.top, before = toLatLng(x, y);
    zoom = Math.max(3, Math.min(19, zoom + (e.deltaY < 0 ? 1 : -1)));
    var after = toLatLng(x, y); center = { lat: center.lat + before.lat - after.lat, lng: center.lng + before.lng - after.lng }; drawMap(); }, { passive: false });
  $$('.mzoom button', map).forEach(function (b) { b.onclick = function () { zoom = Math.max(3, Math.min(19, zoom + +b.dataset.z)); drawMap(); }; });
})();
['lyfov', 'lynames', 'lystreet'].forEach(function (id) { $('#' + id).onchange = drawMap; });
window.addEventListener('resize', drawMap);
if (window.ResizeObserver) new ResizeObserver(function () { drawMap(); }).observe(map); // the strip takes room: the map follows

// edit mode: move, turn, add, remove; saved into the configuration
var backup = null;
$('#medit').onclick = function () { if (!D.medit) return; if (!D.edit) { alert(T.lv_no_edit); return; } editing = true; backup = JSON.stringify(pos); changed = {}; $('#meditbar').hidden = false; this.hidden = true; $('#lhint').textContent = T.mp_edit_hint; drawMap(); };
function endEdit() { editing = false; selected = null; $('#meditbar').hidden = true; $('#medit').hidden = false; $('#lhint').textContent = T.mp_hint; drawMap(); }
$('#mcancel').onclick = function () { pos = JSON.parse(backup); selShown = null; endEdit(); };
$('#msave').onclick = function () {
  var list = Object.keys(changed).map(function (k) { var c = cams[k];
    if (changed[k] === 'remove') return { n: c.n, c: c.c, remove: true };
    var p = pos[k]; return { n: c.n, c: c.c, lat: p.lat, long: lngOf(p), dir: p.dir, angle: p.angle, range: p.range }; });
  if (!list.length) { endEdit(); return; }
  post('/live/positions', { positions: JSON.stringify(list) }).then(function (r) { if (!r.ok) { alert(r.error); return; } endEdit(); });
};
$('#madd').onclick = function () {
  var d = $('#dadd'), list = $('#daddlist'); list.innerHTML = '';
  D.cams.filter(function (c) { return !pos[c.n + '/' + c.c]; }).forEach(function (c) {
    var l = document.createElement('label'); l.className = 'chk'; l.innerHTML = '<input type="checkbox"> <span style="flex:1"></span><span class="note"></span>';
    $('input', l).value = c.n + '/' + c.c; $('span', l).textContent = c.label; $('.note', l).textContent = c.nvr + (c.ch ? ' · ' + c.ch : ''); list.appendChild(l); });
  if (!list.children.length) list.innerHTML = '<div class="note">' + T.mp_all_placed + '</div>';
  $('#daddq').value = ''; $('#daddq').oninput = function () { var v = this.value.toLowerCase(); $$('label', list).forEach(function (l) { l.style.display = l.textContent.toLowerCase().indexOf(v) >= 0 ? '' : 'none'; }); };
  d.returnValue = '';
  d.onclose = function () { if (d.returnValue !== 'ok') return;
    var ks = $$('input:checked', list).map(function (i) { return i.value; }), s = size();
    ks.forEach(function (k, i) { // placed in a row across the middle of the map, to be moved one by one
      var ll = toLatLng(s.w / 2 + (i - (ks.length - 1) / 2) * 40, s.h / 2);
      pos[k] = { n: cams[k].n, c: cams[k].c, lat: ll.lat, lng: ll.lng, dir: 0, angle: 90, range: 30 }; changed[k] = true; selected = k; });
    drawMap(); };
  d.showModal();
};

// the preview strip
function addStrip(k) { if (strip.indexOf(k) < 0) { strip.push(k); if (strip.length > 8) strip.shift(); } renderStrip(); }
// the strip is reconciled like the grid: a camera that stays keeps its tile and its player
// (adding, removing, switching map and thumbnails, searching do not interrupt the others);
// tiles are placed by the CSS order, never moved in the document
var stripEls = {}; // camera key → {el, player}
function stopStrip() { Object.keys(stripEls).forEach(function (k) { stripEls[k].player.stop(); }); stripEls = {}; stripPlayers = []; $('#mstriptiles').innerHTML = ''; }
function renderStrip() {
  var box = $('#mstriptiles'), shown = mapMode || thumbMode;
  $('#mstrip').hidden = !shown || !strip.length; $('#mstripn').textContent = strip.length + ' / 8';
  $$('#lthumbs .tile').forEach(function (el) { el.classList.toggle('instrip', strip.indexOf(el.dataset.k) >= 0); });
  if (!shown) { stopStrip(); return; }
  Object.keys(stripEls).forEach(function (k) { if (strip.indexOf(k) < 0) { stripEls[k].player.stop(); stripEls[k].el.remove(); delete stripEls[k]; } });
  strip.forEach(function (k, i) {
    var x = stripEls[k];
    if (!x) {
      var c = cams[k], el = document.createElement('div'); el.className = 'tile';
      x = stripEls[k] = { el: el, player: fillTile(el, { n: c.n, c: c.c, q: c.lq ? 'lq' : 'hq' }, function () { strip.splice(strip.indexOf(k), 1); renderStrip(); }, function () {}) };
      el.onmouseenter = function () { selected = k; drawMap(); }; box.appendChild(el);
    }
    x.el.style.order = i;
  });
  stripPlayers = strip.map(function (k) { return stripEls[k].player; });
  stripArchive();
}
// "open in archive": the cameras of the strip; the link is made when it is used (its
// time is now), told how many it leaves out
function stripArchive() {
  var a = $('#march'); if (!a) return;
  var cs = strip.map(function (k) { return cams[k]; }), skip = D.archplay ? cs.filter(function (c) { return !c.play; }).length : 0;
  a.hidden = !archiveHref(cs);
  a.title = cs.length === 1 ? T.lv_open_archive_1 : T.lv_open_archive_t + (skip && skip < cs.length ? ' — ' + T.lv_archive_skip.replace('%d', skip) : '');
}
if ($('#march')) $('#march').onclick = function (e) { var h = archiveHref(strip.map(function (k) { return cams[k]; })); if (!h) { e.preventDefault(); return; } this.href = h; };
$('#mclear').onclick = function () { strip = []; renderStrip(); };
$('#mgrid').onclick = function () { state.tiles = strip.map(function (k) { return { n: cams[k].n, c: cams[k].c, q: 'lq' }; });
  state.layout = [1, 4, 9, 16, 25].filter(function (n) { return n >= strip.length; })[0] || 25; state.view = ''; strip = []; setMode('grid'); };

// ---------- thumbnails of all cameras (/system/snapshot), by name ----------
var thumbsBuilt = false;
function snapRows() { // the cameras with their thumbnails, in the order of their symbols (sorted by the server)
  return D.snaps.map(function (s) { var k = s.n + '/' + s.c; return { k: k, s: s, c: cams[k] }; });
}
function renderThumbs() {
  var box = $('#lthumbs'), rows = snapRows(), bad = 0;
  if (!thumbsBuilt) {
    thumbsBuilt = true; box.innerHTML = '';
    if (!rows.length) box.innerHTML = '<div class="empty-note">' + T.tb_empty + '</div>';
    rows.forEach(function (r) {
      var s = r.s, c = r.c, el = document.createElement('div'), sub = s.sym.indexOf(s.c) < 0 ? s.c : ''; // the camera's name when the symbol is its alias
      el.className = 'tile' + (live(c) ? '' : ' nolive'); el.dataset.k = r.k; el.dataset.q = (s.sym + ' ' + s.c + ' ' + s.n).toLowerCase();
      el.innerHTML = '<div class="th"><span class="tn"></span><span class="tr"></span></div>' +
        (s.has ? '<img alt="" loading="lazy" draggable="false">' : '<div class="none"></div>') +
        '<div class="tf"><span class="tt"></span><span class="badge"></span></div>';
      $('.tn', el).textContent = s.sym; if (sub) { var sm = document.createElement('small'); sm.textContent = sub; $('.tn', el).appendChild(sm); }
      $('.tr', el).textContent = s.n;
      if (s.has) $('img', el).src = '/live/thumb?n=' + encodeURIComponent(s.n) + '&c=' + encodeURIComponent(s.c) + '&v=' + s.v;
      else $('.none', el).textContent = T.tb_none;
      $('.tt', el).textContent = s.taken ? T.tb_taken.replace('%s', s.taken) : '';
      var b = $('.badge', el);
      if (!live(c)) { b.className = 'badge mute'; b.textContent = T.tb_off_cam; }
      else if (s.tried) { b.className = 'badge ' + (s.ok ? 'ok' : 'err'); b.textContent = s.ok ? T.tb_ok : T.tb_bad; }
      else b.hidden = true;
      el.title = live(c) ? (s.tried && !s.ok ? T.tb_tried.replace('%s', s.tried).replace('%s', s.err) : '') : T.tb_no_live;
      el.onclick = function () { if (live(c)) addStrip(r.k); };
      box.appendChild(el);
    });
  }
  // unreachable: a camera with a live stream whose last try failed (one without is just off)
  var isBad = {}; rows.forEach(function (r) { if (live(r.c) && r.s.tried && !r.s.ok) { isBad[r.k] = true; bad++; } });
  var q = $('#tsearch').value.toLowerCase(), onlyBad = $('#tbad').checked;
  $$('.tile', box).forEach(function (el) { el.hidden = el.dataset.q.indexOf(q) < 0 || (onlyBad && !isBad[el.dataset.k]); });
  var info = $('#tinfo'); info.innerHTML = '';
  function line(cls, a, b) { var d = document.createElement('div'); d.className = cls;
    [a, b].forEach(function (x) { if (x === undefined) return; var sp = document.createElement('span'); sp.textContent = x; d.appendChild(sp); }); info.appendChild(d); }
  line('', D.snap.sched); line('row', T.tb_last, D.snap.last || T.tb_never); if (D.snap.next) line('row', T.tb_next, D.snap.next);
  line('note', T.tb_count.replace('%d', rows.length).replace('%d', bad)); if (D.snap.err) line('err', D.snap.err);
  renderStrip();
}
$('#tsearch').oninput = renderThumbs;
$('#tbad').onchange = renderThumbs;

// grid / map
var fitted = false;
// the modes: grid (the tiles), map, thumbs; map and thumbs share the strip at the bottom
function setMode(m) {
  if ((m === 'map' && !D.map) || (m === 'thumbs' && !D.thumbs)) m = 'grid';
  var g = m === 'grid';
  if (editing && m !== 'map') $('#mcancel').onclick();
  mapMode = m === 'map'; thumbMode = m === 'thumbs';
  ['grid', 'map', 'thumbs'].forEach(function (x) { $('#tab' + x).setAttribute('aria-selected', m === x); });
  grid.hidden = !g; map.hidden = !mapMode; $('#lthumbs').hidden = !thumbMode;
  $('.lgridside').hidden = !g; $('.lmapside').hidden = !mapMode; $('.lthumbside').hidden = !thumbMode;
  $('.gridtools').hidden = !g; $('.maptools').hidden = !mapMode;
  document.querySelector('main').classList.toggle('mapmode', !g);
  if (mapMode) { stopAll(); if (!fitted) { fit(); fitted = true; } $('#lhint').textContent = T.mp_hint; drawMap(); renderStrip(); }
  else if (thumbMode) { stopAll(); $('#lhint').textContent = T.tb_hint; renderThumbs(); }
  else { stopStrip(); $('#mstrip').hidden = true; render(); }
  ptzUpdate();
  try { sessionStorage.setItem('ffnvr.mode', m); } catch (e) {}
}
$('#tabgrid').onclick = function (e) { e.preventDefault(); setMode('grid'); };
$('#tabmap').onclick = function (e) { e.preventDefault(); setMode('map'); };
$('#tabthumbs').onclick = function (e) { e.preventDefault(); setMode('thumbs'); };
var startMode = 'grid'; try { startMode = sessionStorage.getItem('ffnvr.mode') || 'grid'; } catch (e) {}
if (startMode !== 'grid') setMode(startMode); else render();
})();
