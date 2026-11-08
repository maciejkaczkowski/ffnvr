// ffnvr playback: the cameras of the local recorders on tiles (as in the live view),
// played in sync on one clock along a timeline of a day; a range of the timeline is
// archived as jobs. No libraries.
//
// The clock: while playing, it follows the video of one tile (the selected one when it
// plays, else the first that plays), so that it never runs ahead of what is shown; the
// other tiles are pulled to it when they drift. With no video at the clock's time it
// runs on its own, and when no tile has a recording there it jumps to the next one.
(function () {
'use strict';
var D = JSON.parse(document.getElementById('pbdata').textContent), T = D.t;
D.cams = D.cams || []; D.init = D.init || [];
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
var $ = function (s, r) { return (r || document).querySelector(s); };
var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };
var LANG = document.documentElement.lang || 'en', enc = encodeURIComponent;
var cams = {}; D.cams.forEach(function (c) { cams[c.n + '/' + c.c] = c; });
function cam(t) { return t && cams[t.n + '/' + t.c]; }

// ---------- time ----------
function pad(n) { return (n < 10 ? '0' : '') + n; }
function fmtDate(t) { var d = new Date(t * 1000); return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()); }
function fmtTime(t, sec) { var d = new Date(t * 1000); return pad(d.getHours()) + ':' + pad(d.getMinutes()) + (sec === false ? '' : ':' + pad(d.getSeconds())); }
function fmtFull(t) { return fmtDate(t) + ' ' + fmtTime(t); }
function fmtInput(t) { return fmtDate(t) + 'T' + fmtTime(t); } // <input type="datetime-local" step="1">
function dayStart(t) { var d = new Date(t * 1000); d.setHours(0, 0, 0, 0); return d.getTime() / 1000; }
function dayAdd(day, n) { var d = new Date(day * 1000); d.setDate(d.getDate() + n); d.setHours(0, 0, 0, 0); return d.getTime() / 1000; }
function dayEnd(day) { return dayAdd(day, 1); }
function nowS() { return Date.now() / 1000; }
function human(s) {
  s = Math.round(s); var h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, x = s % 60;
  return h ? h + ' h ' + m + ' min' : m ? m + ' min' + (x ? ' ' + x + ' s' : '') : x + ' s';
}
function spanLabel(s) { return s >= 3600 ? s / 3600 + ' h' : s / 60 + ' min'; }

// ---------- state ----------
var KEY = 'ffnvr.playback', RATES = [0.5, 1, 2, 4, 8, 16], LAYOUTS = [1, 4, 9, 16];
var st = { layout: 4, tiles: [], rate: 1 }, sel = 0, big = -1;
try { var s0 = JSON.parse(localStorage.getItem(KEY) || 'null'); if (s0 && LAYOUTS.indexOf(s0.layout) >= 0) st = s0; } catch (e) {}
st.tiles = (st.tiles || []).map(function (t) { return t && cam(t) ? { n: t.n, c: t.c } : null; });
if (D.init.length) { // cameras asked for by the address (a link from a live tile)
  st.tiles = D.init.slice(0, 16).map(function (t) { return { n: t.n, c: t.c }; });
  st.layout = LAYOUTS.filter(function (n) { return n >= st.tiles.length; })[0] || 16;
}
function remember() { try { localStorage.setItem(KEY, JSON.stringify(st)); } catch (e) {} }
var clock = { t: D.t0, playing: false, rate: RATES.indexOf(+st.rate) >= 0 ? +st.rate : 1 };
var day = dayStart(clock.t);
var ZOOMS = [86400, 43200, 21600, 10800, 7200, 3600, 1800, 900, 600, 300];
var view = { start: day, span: 86400 };
var mark = { a: null, b: null }; // the range to archive (unix)
var jumpPending = true; // when the segments are there: from a gap to the nearest recording
var endNote = false;

// ---------- the segments of a camera, by day ----------
// segs[camera key][day] = {state: loading|ok|err, list: [{b, e, f, m, dur}], at}
var segs = {}, days = {};
function entry(k, d) { return segs[k] && segs[k][d]; }
function list(x) { var e = entry(x.key, day); return e && e.list && e.state !== 'err' && (e.state === 'ok' || e.list.length) ? e.list : null; }
function fetchDay(k, d, again) {
  segs[k] = segs[k] || {};
  var old = segs[k][d];
  if (old && !again) return;
  var e = old || { state: 'loading', list: null }; e.at = Date.now(); e.busy = true; segs[k][d] = e;
  var c = cams[k];
  fetch('/archive/segments?n=' + enc(c.n) + '&c=' + enc(c.c) + '&from=' + (d - 900) + '&to=' + (dayEnd(d) + 900), { credentials: 'same-origin' })
    .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw new Error(j.error || r.status); return j; }); })
    .then(function (j) {
      var known = {}; (e.list || []).forEach(function (s) { known[s.f] = s; });
      e.list = (j.segs || []).map(function (x) { var o = known[x.f]; return { b: x.b, e: x.e, f: x.f, m: x.m || '', r: !!j.remote, dur: o && o.dur, warm: o && o.warm, ready: o && o.ready }; });
      e.state = 'ok'; e.busy = false; e.at = Date.now(); e.remote = !!j.remote;
      days[k] = {}; (j.days || []).forEach(function (x) { days[k][x] = true; });
      renderCal(); draw();
    })
    .catch(function (err) { e.busy = false; if (!e.list) { e.state = 'err'; e.err = err.message; } draw(); });
}
// a local file ends where its video ends; a piece of a remote recorder is recorded to its
// end (the recorder's list says so) even when its video is a little shorter
function segEnd(s) { return s.dur && !s.r ? Math.min(s.e, s.b + s.dur) : s.e; }
// a piece of a remote recorder begins at the key frame before its start (or after it):
// its first frame is off seconds after its start (X-Start of the server), so the time t
// is at t - b - off in its video
function toMedia(s, t) { return t - s.b - (s.off || 0); }
function fromMedia(s, m) { return s.b + (s.off || 0) + m; }
function takeOff(s, r) { var v = parseFloat(r.headers.get('X-Start')); s.off = isFinite(v) ? v : 0; s.offKnown = true; }
function segAt(l, t) { for (var i = 0; i < l.length; i++) if (l[i].b <= t && t < segEnd(l[i])) return l[i]; return null; }
// H.265 (most Dahua recorders) plays where the browser has a decoder for it; elsewhere
// the server makes H.264 of it
var HEVC = (function () {
  var t = 'video/mp4; codecs="hvc1.1.6.L150.90"';
  try { return document.createElement('video').canPlayType(t) === 'probably' || !!(window.MediaSource && MediaSource.isTypeSupported(t)); } catch (e) { return false; }
})();
function playURL(x, s, more) { return '/archive/play?n=' + enc(x.n) + '&c=' + enc(x.c) + '&f=' + enc(s.f) + (HEVC ? '&hevc=1' : '') + (more || ''); }
function allSegs() { var out = []; tiles.forEach(function (x) { var l = x.key && list(x); if (l) out = out.concat(l); }); return out; }
function nextStart(t) { var best = null; allSegs().forEach(function (s) { if (s.b > t + 0.5 && (best === null || s.b < best)) best = s.b; }); return best; }
function prevStart(t) { var best = null; allSegs().forEach(function (s) { if (s.b < t - 2 && (best === null || s.b > best)) best = s.b; }); return best; }
function lastEnd(t) { var best = null; allSegs().forEach(function (s) { var e = segEnd(s); if (e <= t && (best === null || e > best)) best = e; }); return best; }
// the first day after the clock's day on which a camera of the tiles recorded (the calendar's days)
function nextRecDay() {
  var cur = fmtDate(day), best = null;
  tiles.forEach(function (x) { if (x.key && days[x.key]) Object.keys(days[x.key]).forEach(function (d) { if (d > cur && (best === null || d < best)) best = d; }); });
  if (best === null) return null;
  var p = best.split('-'); return new Date(+p[0], +p[1] - 1, +p[2]).getTime() / 1000;
}
function pending() { return tiles.some(function (x) { var e = x.key && entry(x.key, day); return x.key && (!e || e.state === 'loading'); }); }

// ---------- tiles ----------
var grid = $('#pgrid'), tiles = []; // tiles[i]: {key, n, c, el, video, seg, loading, err, state} ({key: ''} when empty)
function makeEmpty() {
  var el = document.createElement('div'); el.className = 'tile empty'; el.tabIndex = 0; el.draggable = true;
  el.innerHTML = '<div class="nsg"></div>'; $('.nsg', el).textContent = narrow() ? T.pb_empty_m : T.pb_empty;
  return { key: '', el: el };
}
function makeTile(t) {
  var c = cam(t), el = document.createElement('div'), x = { key: t.n + '/' + t.c, n: t.n, c: t.c, el: el, seg: null, loading: false, err: '', state: null, seekAt: 0 };
  el.className = 'tile'; el.tabIndex = 0; el.draggable = true;
  el.innerHTML = '<div class="th"><span class="pbtag"></span><span class="tn"></span><span class="tr"></span></div><video muted playsinline preload="auto"></video><video class="back" muted playsinline preload="auto"></video><div class="nsg"></div>' +
    '<div class="tf"><span class="ts"></span><span class="tb"><button type="button" class="snd">🔇</button><button type="button" class="snap">📷</button><a class="dl" hidden>⬇</a><button type="button" class="x">✕</button></span></div>';
  $('.tn', el).textContent = c.label; $('.tr', el).textContent = c.nvr;
  // two videos: the one shown (x.video) and the one the next file is made ready in
  // (x.back), shown in its place when the clock gets there - no wait between files
  var vs = $$('video', el); x.video = vs[0]; x.back = vs[1]; x.next = null;
  vs.forEach(function (v) {
    v.addEventListener('loadedmetadata', function () {
      if (v === x.back) { // the next file: at its start, waiting
        var n = x.next; if (!n) return;
        if (isFinite(v.duration) && v.duration > 0) n.s.dur = v.duration;
        try { v.currentTime = Math.max(0, toMedia(n.s, n.s.b)); } catch (e) {}
        return;
      }
      var s = x.seg; if (!s) return;
      if (isFinite(v.duration) && v.duration > 0) s.dur = v.duration;
      try { v.currentTime = Math.max(0, Math.min(toMedia(s, clock.t), (s.dur || 1e9) - 0.1)); } catch (e) {}
      x.seekAt = performance.now();
    });
    v.addEventListener('canplay', function () {
      if (v === x.back) { if (x.next) x.next.ok = true; return; }
      if (x.loading) { x.loading = false; x.tries = 0; sync(x); }
    });
    v.addEventListener('error', function () {
      if (!v.getAttribute('src')) return;
      if (v === x.back) { x.next = null; return; } // played the usual way when its time comes
      failed(x);
    });
    v.addEventListener('ended', function () { if (v === x.video && x.seg && !x.seg.dur) x.seg.dur = v.currentTime; });
  });
  var v = x.video;
  var snd = $('.snd', el); snd.title = T.pb_sound; snd.setAttribute('aria-label', T.pb_sound);
  snd.onclick = function (e) { e.stopPropagation(); var on = x.video.muted;
    tiles.forEach(function (y) { if (y.video) { y.video.muted = y.back.muted = true; $('.snd', y.el).classList.remove('on'); $('.snd', y.el).textContent = '🔇'; } });
    x.video.muted = x.back.muted = !on; snd.classList.toggle('on', on); snd.textContent = on ? '🔊' : '🔇'; };
  var sn = $('.snap', el); sn.title = T.pb_snapshot; sn.setAttribute('aria-label', T.pb_snapshot);
  sn.onclick = function (e) { e.stopPropagation(); snapshot(x); };
  var dl = $('.dl', el); dl.title = T.pb_download; dl.setAttribute('aria-label', T.pb_download); dl.onclick = function (e) { e.stopPropagation(); };
  var cl = $('.x', el); cl.title = T.pb_close; cl.setAttribute('aria-label', T.pb_close);
  cl.onclick = function (e) { e.stopPropagation(); var i = tileAt(el); st.tiles[i] = null; if (i === big) big = -1; render(); };
  return x;
}
function unload(v) { v.pause(); v.removeAttribute('src'); v.load(); }
function stopTile(x) { if (!x.video) return; unload(x.video); unload(x.back); x.next = null; }
function show(x, state) { // '' playing, wait, norec, err
  if (x.state === state) return; x.state = state;
  x.el.classList.toggle('wait', state === 'wait'); x.el.classList.toggle('norec', state === 'norec'); x.el.classList.toggle('nosig', state === 'err');
  $('.nsg', x.el).textContent = state === 'wait' ? T.pb_loading : state === 'norec' ? T.pb_norec : state === 'err' ? (x.err || T.pl_cannot) : '';
}
function snapshot(x) {
  var v = x.video; if (!v || !v.videoWidth) return;
  var c = document.createElement('canvas'); c.width = v.videoWidth; c.height = v.videoHeight; c.getContext('2d').drawImage(v, 0, 0);
  c.toBlob(function (b) { if (!b) return; var a = document.createElement('a'); a.href = URL.createObjectURL(b);
    a.download = x.n + '_' + x.c + '_' + fmtFull(fromMedia(x.seg, v.currentTime)).replace(/[: ]/g, '-') + '.jpg';
    document.body.appendChild(a); a.click(); setTimeout(function () { URL.revokeObjectURL(a.href); a.remove(); }, 2000); }, 'image/jpeg', 0.92);
}
function tileAt(el) { return +el.dataset.i; }
function selectTile(i) { sel = i; tiles.forEach(function (x, j) { x.el.classList.toggle('sel', j === sel); }); hint(); names(); draw(); }
function wireTile(el) { // the handlers read the tile's place when they run: it changes
  el.addEventListener('click', function () { selectTile(tileAt(el)); if (!st.tiles[sel] && narrow()) window.ffnvrNav.open(); });
  el.addEventListener('dblclick', function (e) { if (e.target.closest('.tb,.znav')) return; e.preventDefault(); setBig(big >= 0 ? -1 : tileAt(el)); });
  el.addEventListener('dragstart', function (e) { e.dataTransfer.setData('text/x-tile', String(tileAt(el))); e.dataTransfer.effectAllowed = 'move'; });
  el.addEventListener('dragover', function (e) { e.preventDefault(); el.classList.add('over'); });
  el.addEventListener('dragleave', function () { el.classList.remove('over'); });
  el.addEventListener('drop', function (e) {
    e.preventDefault(); el.classList.remove('over');
    var i = tileAt(el), from = e.dataTransfer.getData('text/x-tile'), c = e.dataTransfer.getData('text/x-cam');
    if (from !== '') { from = +from; if (from === i) return; var a = st.tiles[from] || null; st.tiles[from] = st.tiles[i] || null; st.tiles[i] = a; }
    else if (c) { var k = c.split('\n'); st.tiles[i] = { n: k[0], c: k[1] }; }
    else return;
    sel = i; render();
  });
}
// render reconciles the grid with the state: a tile whose camera stays keeps its element
// and its video; tiles are placed by the CSS order, never moved in the document
function render() {
  var n = st.layout;
  if (big >= n || big >= 0 && !st.tiles[big]) big = -1;
  grid.style.setProperty('--cols', big >= 0 ? 1 : Math.sqrt(n)); grid.classList.toggle('big', big >= 0);
  if (sel >= n) sel = 0;
  var old = tiles, next = [], nav = big >= 0 && zoomables().length > 1;
  for (var i = 0; i < n; i++) {
    var t = st.tiles[i] && cam(st.tiles[i]) ? st.tiles[i] : null, key = t ? t.n + '/' + t.c : '', found = null;
    for (var j = 0; j < old.length; j++) if (old[j] && old[j].key === key) { found = old[j]; old[j] = null; break; }
    if (!found) { found = key ? makeTile(t) : makeEmpty(); wireTile(found.el); grid.appendChild(found.el); }
    if (key) fetchDay(key, day);
    found.el.dataset.i = i; found.el.style.order = i;
    found.el.classList.toggle('sel', i === sel); found.el.classList.toggle('zoomed', i === big);
    zoomNav(found.el, nav && i === big, stepBig, T);
    next.push(found);
  }
  old.forEach(function (x) { if (x) { stopTile(x); x.el.remove(); } });
  tiles = next;
  $$('[data-layout]').forEach(function (b) { b.classList.toggle('primary', +b.dataset.layout === n); });
  $$('.lcam').forEach(function (b) { b.classList.toggle('on', st.tiles.slice(0, n).some(function (t) { return t && t.n === b.dataset.n && t.c === b.dataset.c; })); });
  remember(); hint(); names(); exportCams(); renderCal(); sizeCanvas(); tiles.forEach(function (x) { if (x.key) sync(x); }); draw();
}
function hint() {
  var x = tiles[sel], c = x && x.key ? cams[x.key] : null;
  $('#lhint').textContent = (big >= 0 ? (narrow() ? T.pb_zoomed_m : T.pb_zoomed) : narrow() ? T.pb_tile_m : T.pb_tile).replace('%d', sel + 1).replace('%s', c ? c.label : '—');
}
function setBig(i) { if (i >= 0 && (!st.tiles[i] || st.layout === 1)) return; if (i >= 0) sel = i; big = i; render(); }
// ‹ › and a sideways swipe on the zoomed tile: the previous or the next camera of the grid
function zoomables() { var r = []; for (var i = 0; i < st.layout; i++) if (st.tiles[i] && cam(st.tiles[i])) r.push(i); return r; }
function stepBig(d) { if (big < 0) return; var z = zoomables(), k = z.indexOf(big); if (z.length > 1) setBig(z[(k + d + z.length) % z.length]); }
swipeTiles(grid, function () { return big >= 0; }, stepBig);

// ---------- keeping a tile on the clock ----------
function sync(x) {
  var l = list(x), now = performance.now();
  if (!l) { var e = entry(x.key, day); if (e && e.state === 'err') { x.err = T.pb_fetch_err.replace('%s', cams[x.key].label).replace('%s', e.err); show(x, 'err'); } else show(x, 'wait'); return; }
  var s = segAt(l, clock.t), v = x.video;
  if (!s) { if (!v.paused) v.pause(); show(x, 'norec'); $('.ts', x.el).textContent = fmtFull(clock.t); return; }
  if (!x.seg || x.seg.f !== s.f) { // another file
    var dl = $('.dl', x.el), rem = (entry(x.key, day) || {}).remote; dl.hidden = !D.download || rem; if (D.download && !rem) dl.href = '/archive/file?n=' + enc(x.n) + '&c=' + enc(x.c) + '&f=' + enc(s.f) + '&fmt=mp4';
    if (x.next && x.next.s.f === s.f && x.next.ok && !x.err) { swapIn(x, s); v = x.video; }
    else {
      if (x.next) { x.next = null; unload(x.back); }
      x.seg = s; x.err = ''; x.loading = true; x.loadAt = now; x.tries = 0;
      loadFront(x, s, '');
      show(x, s.ready && x.state === '' ? '' : 'wait'); return; // a piece made ahead: no spinner for the moment it takes
    }
  }
  x.seg = s; // the same file, perhaps from the list of another day
  if (x.err) { show(x, 'err'); return; }
  if (x.loading) { show(x, 'wait'); return; }
  var want = toMedia(s, clock.t);
  show(x, ''); $('.ts', x.el).textContent = fmtFull(clock.t);
  if (s.r && s.dur && want >= s.dur - 0.05) { // a remote piece a little shorter than its time: its last frame waits for the next
    if (!v.paused) v.pause();
    if (clock.playing) { prefetch(x, l, s, want); prepare(x, l, s); }
    return;
  }
  if (want < -0.05) { // the first frame comes after the piece's start: it waits there
    if (!v.paused) v.pause();
    if (v.currentTime > 0.05 && !v.seeking) { v.currentTime = 0; x.seekAt = now; }
    if (clock.playing) { prefetch(x, l, s, 0); prepare(x, l, s); }
    return;
  }
  if (clock.playing) {
    if (v.playbackRate !== clock.rate) { v.defaultPlaybackRate = clock.rate; v.playbackRate = clock.rate; }
    if (v.paused && !v.ended) { var p = v.play(); if (p && p.catch) p.catch(function () {}); }
    if (Math.abs(v.currentTime - want) > Math.max(0.3, clock.rate * 0.25) && now - x.seekAt > 1500) { v.currentTime = want; x.seekAt = now; }
    prefetch(x, l, s, want);
    prepare(x, l, s);
  } else {
    if (!v.paused) v.pause();
    if (Math.abs(v.currentTime - want) > 0.1 && !v.seeking) { v.currentTime = want; x.seekAt = now; }
  }
}
// loadFront plays a file in the shown video, after the usual wait. A remote piece is
// asked for first to learn where its first frame is (X-Start; the asking also makes it)
function loadFront(x, s, more) {
  var v = x.video;
  function go() { if (x.seg !== s) return; v.defaultPlaybackRate = clock.rate; v.src = playURL(x, s, more); v.playbackRate = clock.rate; }
  if (!s.r || s.offKnown) { go(); return; }
  fetch(playURL(x, s, more), { credentials: 'same-origin', headers: { Range: 'bytes=0-1' } })
    .then(function (r) { if (r.body && r.body.cancel) r.body.cancel(); if (r.ok) { takeOff(s, r); s.ready = true; } go(); }, go);
}
// prepare: the next file, when it follows without a gap and plays within 10 s, is made
// ready in the hidden video (a remote piece once the server has made it: warm)
function prepare(x, l, s) {
  var n = l[l.indexOf(s) + 1];
  if (!n || n.b - segEnd(s) > 1 || (segEnd(s) - clock.t) / clock.rate > 10 || !n.ready || (n.r && !n.offKnown)) return;
  if (x.next && x.next.s.f === n.f) return;
  x.next = { s: n, ok: false };
  x.back.defaultPlaybackRate = clock.rate; x.back.src = playURL(x, n); x.back.playbackRate = clock.rate;
}
// swapIn: the next file, ready in the hidden video, takes the place of the shown one
function swapIn(x, s) {
  var b = x.back, f = x.video;
  x.video = b; x.back = f; x.next = null;
  b.classList.remove('back'); f.classList.add('back');
  b.muted = f.muted; b.defaultPlaybackRate = clock.rate; b.playbackRate = clock.rate;
  x.seg = s; x.err = ''; x.loading = false; x.tries = 0; x.seekAt = performance.now();
  var want = toMedia(s, clock.t);
  if (Math.abs(b.currentTime - want) > 0.3) try { b.currentTime = Math.max(0, want); } catch (e) {}
  if (clock.playing) { var p = b.play(); if (p && p.catch) p.catch(function () {}); }
  unload(f);
  show(x, '');
}
// the next files of a tile are made (mp4) before they are needed: asked for with a tiny
// range. A local file: the next one, 40 s before the end. A remote recorder's pieces of
// 30 s, each read from the recorder: as many as play in the next minute (at least 2, at
// most 6), so that a slow recorder does not stop the playing (ahead=1: the server
// leaves a stream of the recorder to the pieces being played)
function prefetch(x, l, s, want) {
  var i = l.indexOf(s), last = s;
  if (!s.r) {
    if (!s.dur || (s.dur - want) / clock.rate > 40) return;
    warm(x, l[i + 1], s, '');
    return;
  }
  var reach = clock.t + Math.max(60, 60 * clock.rate), k = 0;
  for (var j = i + 1; j < l.length && k < 6; j++, k++) {
    var n = l[j];
    if (n.b - segEnd(last) > 60 || (k >= 2 && n.b > reach)) break;
    warm(x, n, last, '&ahead=1');
    last = n;
  }
}
function warm(x, n, prev, more) {
  if (!n || n.warm || n.b - segEnd(prev) > 60) return;
  n.warm = true;
  fetch(playURL(x, n, more), { credentials: 'same-origin', headers: { Range: 'bytes=0-1' } })
    .then(function (r) { if (r.body && r.body.cancel) r.body.cancel(); n.ready = r.ok; if (r.ok) takeOff(n, r); else setTimeout(function () { n.warm = false; }, 5000); },
      function () { n.warm = false; });
}
// failed: a file the browser could not play. A remote recorder busy with other streams
// fails now and then: asked again (up to 3 times); then the reason is told: no recording,
// the recorder's error or a codec the browser does not play
function failed(x) {
  var s = x.seg; if (!s) return;
  x.loading = false; x.tries = (x.tries || 0) + 1;
  if (x.tries <= 3) {
    show(x, 'wait');
    setTimeout(function () { if (x.seg === s && x.video) { x.loading = true; x.loadAt = performance.now(); loadFront(x, s, '&try=' + x.tries); } }, 800 * x.tries);
    return;
  }
  fetch(playURL(x, s), { credentials: 'same-origin', headers: { Range: 'bytes=0-1' } }).then(function (r) {
    if (r.body && r.body.cancel) r.body.cancel();
    if (x.seg !== s) return;
    x.err = r.status === 404 ? T.pb_norec : r.ok ? T.pl_cannot : T.pb_rec_err; show(x, 'err');
  }, function () { if (x.seg === s) { x.err = T.pb_rec_err; show(x, 'err'); } });
}
function setDay(d) {
  if (d === day) return;
  var off = view.start - day; day = d; view.start = day + off; clampView();
  calMonth = new Date(day * 1000); calMonth.setDate(1);
  tiles.forEach(function (x) { if (x.key) fetchDay(x.key, day); });
  renderCal();
}
function seek(t) {
  clock.t = t; endNote = false;
  if (t < day || t >= dayEnd(day)) setDay(dayStart(t));
  var now = performance.now();
  tiles.forEach(function (x) {
    if (!x.key || !x.seg || x.loading) return; var l = list(x), s = l && segAt(l, t);
    if (s && s.f === x.seg.f) { try { x.video.currentTime = Math.max(0, toMedia(s, t)); } catch (e) {} x.seekAt = now; }
  });
  ensureVisible(t, true); tiles.forEach(function (x) { if (x.key) sync(x); }); status(); draw();
}
function setPlaying(on) {
  clock.playing = on; endNote = false;
  if (on && !pending() && !tiles.some(function (x) { var l = x.key && list(x); return l && segAt(l, clock.t); })) {
    var nx = nextStart(clock.t); if (nx !== null) seek(nx);
  }
  var b = $('#pbplay'); b.textContent = on ? '❚❚' : '▶';
  tiles.forEach(function (x) { if (x.key) sync(x); }); status();
}

// ---------- the clock ----------
var lastTick = performance.now();
function tick() {
  var now = performance.now(), dt = Math.min(1, (now - lastTick) / 1000); lastTick = now;
  var act = tiles.filter(function (x) { return x.key; });
  if (!act.length && clock.playing) setPlaying(false);
  if (clock.t >= dayEnd(day) || clock.t < day) setDay(dayStart(clock.t));
  var wait = pending();
  var here = act.filter(function (x) { var l = list(x); return l && segAt(l, clock.t); });
  if (clock.playing && !(here.some(function (x) { return x.loading && now - x.loadAt < 10000; }))) {
    var adv = clock.t + dt * clock.rate, cand = [tiles[sel]].concat(here).filter(function (x) {
      return x && here.indexOf(x) >= 0 && x.seg && !x.loading && !x.err && !x.video.paused && !x.video.seeking && x.video.readyState >= 3; })[0];
    if (cand) { var lt = fromMedia(cand.seg, cand.video.currentTime); clock.t = Math.abs(lt - adv) < Math.max(1, clock.rate) ? lt : adv; }
    else clock.t = adv;
    if (!here.length && !wait) { // a gap of all the tiles: to the next recording
      var nx = nextStart(clock.t);
      if (nx !== null) { if (nx - clock.t > 1) seek(nx); }
      else {
        var nd = nextRecDay();
        if (nd !== null) { seek(nd); jumpPending = true; } // the next day with recordings
        else { var le = lastEnd(clock.t); setPlaying(false); if (le !== null) seek(le - 0.2); endNote = true; } // the last frame
      }
    }
    if (clock.t < view.start || clock.t > view.start + view.span) ensureVisible(clock.t, false);
  }
  if (jumpPending && act.length && !wait) {
    jumpPending = false;
    if (!here.length) { var n2 = nextStart(clock.t), p2 = prevStart(clock.t); if (n2 !== null || p2 !== null) seek(n2 !== null ? n2 : p2); }
  }
  // the segments of today grow: asked again every minute
  if (day === dayStart(nowS())) act.forEach(function (x) { var e = entry(x.key, day); if (e && !e.busy && Date.now() - e.at > 60000) fetchDay(x.key, day, true); });
  act.forEach(sync);
  status(); draw();
}
var lastStatus = '';
function status() {
  var c = fmtFull(clock.t), tag = clock.playing ? clock.rate + '×' : '❚❚', s = c + tag + endNote;
  if (s === lastStatus) return; lastStatus = s;
  $('#pbclock').textContent = c;
  tiles.forEach(function (x) { if (x.key) $('.pbtag', x.el).textContent = tag; });
  var g = $('#pbgoto'); if (document.activeElement !== g) g.value = fmtTime(clock.t);
  if (endNote) $('#lhint').textContent = T.pb_end; else hint();
}
setInterval(tick, 200);

// ---------- the timeline ----------
var cv = $('#pbcanvas'), ctx = cv.getContext('2d'), RULER = 24, ROW = 20, GAP = 4, hover = null, drag = null;
var STEPS = [[10, 2], [30, 10], [60, 15], [120, 30], [300, 60], [600, 120], [900, 300], [1800, 300], [3600, 900], [7200, 1800], [10800, 3600], [21600, 3600]];
function rowsList() { return tiles.filter(function (x) { return x.key; }); }
function W() { return cv.clientWidth; }
function xOf(t) { return (t - view.start) / view.span * W(); }
function tAt(px) { return view.start + px / W() * view.span; }
function clampView() {
  var end = dayEnd(day);
  if (view.span > end - day) view.span = Math.min(ZOOMS[0], end - day);
  view.start = Math.max(day, Math.min(view.start, end - view.span));
  $('#pbspan').textContent = spanLabel(view.span);
}
function ensureVisible(t, center) {
  if (t >= view.start && t <= view.start + view.span) return;
  view.start = t - (center ? view.span / 2 : view.span * 0.1); clampView();
}
function zoom(dir, anchor) {
  var i = ZOOMS.indexOf(view.span); if (i < 0) i = 0;
  i = Math.max(0, Math.min(ZOOMS.length - 1, i + dir)); if (ZOOMS[i] === view.span) return;
  if (anchor === undefined) anchor = clock.t >= view.start && clock.t <= view.start + view.span ? clock.t : view.start + view.span / 2;
  var f = (anchor - view.start) / view.span; view.span = ZOOMS[i]; view.start = anchor - f * view.span; clampView(); draw();
}
function sizeCanvas() {
  var n = Math.max(1, rowsList().length), h = RULER + n * (ROW + GAP), dpr = window.devicePixelRatio || 1;
  cv.style.height = h + 'px'; cv.width = Math.max(1, Math.round(cv.clientWidth * dpr)); cv.height = Math.round(h * dpr);
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
}
function names() {
  var box = $('#pbnames'), rl = rowsList(); box.innerHTML = '';
  if (!rl.length) { var d = document.createElement('div'); d.className = 'empty'; d.textContent = '—'; box.appendChild(d); return; }
  rl.forEach(function (x) { var d = document.createElement('div'), c = cams[x.key], i = tiles.indexOf(x);
    d.textContent = (i + 1) + '. ' + c.label; d.title = c.label + ' (' + c.nvr + ')'; d.className = i === sel ? 'sel' : '';
    d.onclick = function () { selectTile(i); }; box.appendChild(d); });
}
function draw() {
  var w = W(), rl = rowsList(), h = RULER + Math.max(1, rl.length) * (ROW + GAP); if (!w) return;
  if (Math.round(w * (window.devicePixelRatio || 1)) !== cv.width) sizeCanvas();
  ctx.clearRect(0, 0, w, h);
  // the hour scale
  var step = STEPS[STEPS.length - 1];
  for (var k = 0; k < STEPS.length; k++) if (STEPS[k][0] / view.span * w >= 64) { step = STEPS[k]; break; }
  ctx.font = '11px ui-monospace, SFMono-Regular, Menlo, monospace'; ctx.textBaseline = 'top'; ctx.textAlign = 'center';
  var t0 = day + Math.floor((view.start - day) / step[1]) * step[1];
  for (var t = t0; t <= view.start + view.span; t += step[1]) {
    var x = Math.round(xOf(t)) + 0.5, major = (t - day) % step[0] === 0;
    ctx.fillStyle = major ? '#5b6878' : '#33455e'; ctx.fillRect(x - 0.5, major ? RULER - 9 : RULER - 5, 1, major ? 9 : 5);
    if (major && x > 14 && x < w - 14) { ctx.fillStyle = '#b8c4d4'; ctx.fillText(t >= dayEnd(day) - 1 ? '24:00' : fmtTime(t, step[0] < 60), x, 3); }
  }
  // the rows: what each camera recorded, and its motion where the worker analysed it (or
  // the recorder told its alarms or events: Tiandy, Uniview)
  if (!rl.length) { ctx.fillStyle = '#1a2433'; ctx.fillRect(0, RULER, w, ROW); ctx.fillStyle = '#8a96a5'; ctx.textAlign = 'left'; ctx.fillText(T.pb_no_tiles, 8, RULER + 4); }
  rl.forEach(function (x, r) {
    var y = RULER + r * (ROW + GAP), e = entry(x.key, day);
    ctx.fillStyle = tiles.indexOf(x) === sel ? '#22324a' : '#1a2433'; ctx.fillRect(0, y, w, ROW);
    if (!e || !e.list) { ctx.fillStyle = e && e.state === 'err' ? '#f1a5a9' : '#5b6878'; ctx.textAlign = 'left'; ctx.fillText(e && e.state === 'err' ? e.err : '…', 8, y + 4); return; }
    // the recording as runs: adjacent pieces (30 s of a remote recorder) drawn as one
    var run = null; ctx.fillStyle = '#2fb344';
    function flush() { if (run) { var a = xOf(run[0]), b = xOf(run[1]); ctx.fillRect(Math.floor(a), y + 4, Math.max(1, Math.ceil(b) - Math.floor(a)), ROW - 8); } }
    e.list.forEach(function (s) {
      var end = segEnd(s); if (end < view.start || s.b > view.start + view.span) return;
      if (run && s.b - run[1] < 1) run[1] = Math.max(run[1], end); else { flush(); run = [s.b, end]; }
    });
    flush();
    e.list.forEach(function (s) {
      var end = segEnd(s); if (!s.m || end < view.start || s.b > view.start + view.span) return;
      if (s.m) { var n = s.m.length, len = end - s.b; ctx.fillStyle = '#d63939';
        for (var i = 0; i < n; i++) if (s.m.charAt(i) === '1') { var a = xOf(s.b + i * len / n), b = xOf(s.b + (i + 1) * len / n); ctx.fillRect(a, y + 2, Math.max(1, b - a), ROW - 4); } }
    });
  });
  // the range to archive
  if (mark.a !== null || mark.b !== null) {
    var a = mark.a !== null ? mark.a : mark.b, b = mark.b !== null ? mark.b : mark.a, xa = xOf(Math.min(a, b)), xb = xOf(Math.max(a, b));
    ctx.fillStyle = 'rgba(78,161,240,.28)'; ctx.fillRect(xa, 0, xb - xa, h);
    ctx.fillStyle = '#4ea1f0'; ctx.fillRect(xa - 1, 0, 2, h); ctx.fillRect(xb - 1, 0, 2, h);
    ctx.fillRect(xa - 1, 0, 6, 4); ctx.fillRect(xb - 5, 0, 6, 4);
  }
  // the pointer and the clock
  if (hover !== null) { ctx.fillStyle = 'rgba(255,255,255,.35)'; ctx.fillRect(Math.round(xOf(hover)), 0, 1, h); }
  var ct = drag && drag.mode === 'seek' && drag.moved ? drag.t : clock.t, cx = Math.round(xOf(ct));
  ctx.fillStyle = '#ffd166'; ctx.fillRect(cx - 1, 0, 2, h);
  ctx.beginPath(); ctx.moveTo(cx - 5, 0); ctx.lineTo(cx + 5, 0); ctx.lineTo(cx, 6); ctx.closePath(); ctx.fill();
}
function pos(e) { var r = cv.getBoundingClientRect(); return { x: Math.max(0, Math.min(r.width, e.clientX - r.left)), y: e.clientY - r.top }; }
function tip(t, px) { var tp = $('#pbtip'); if (t === null) { tp.hidden = true; return; } tp.hidden = false; tp.textContent = fmtTime(t); tp.style.left = Math.max(28, Math.min(W() - 28, px)) + 'px'; }
cv.addEventListener('pointerdown', function (e) {
  if (e.button !== 0) return;
  var p = pos(e), t = tAt(p.x), edge = null;
  if (D.export && mark.a !== null && mark.b !== null) { if (Math.abs(p.x - xOf(mark.a)) < 6) edge = 'a'; else if (Math.abs(p.x - xOf(mark.b)) < 6) edge = 'b'; }
  if (edge) drag = { mode: 'edge', edge: edge };
  else if (e.shiftKey && D.export) { mark.a = mark.b = t; drag = { mode: 'sel' }; }
  else if (p.y < RULER) { drag = { mode: 'pan', x: p.x, start: view.start }; cv.classList.add('pan'); }
  else drag = { mode: 'seek', x: p.x, t: t, moved: false };
  if (p.y >= RULER) { var rl = rowsList(), row = rl[Math.floor((p.y - RULER) / (ROW + GAP))]; if (row) selectTile(tiles.indexOf(row)); }
  try { cv.setPointerCapture(e.pointerId); } catch (x) {}
  e.preventDefault(); draw();
});
cv.addEventListener('pointermove', function (e) {
  var p = pos(e), t = tAt(p.x);
  hover = t; tip(t, p.x);
  cv.style.cursor = D.export && mark.a !== null && mark.b !== null && (Math.abs(p.x - xOf(mark.a)) < 6 || Math.abs(p.x - xOf(mark.b)) < 6) ? 'ew-resize' : p.y < RULER ? 'grab' : '';
  if (drag) {
    if (drag.mode === 'pan') { view.start = drag.start - (p.x - drag.x) / W() * view.span; clampView(); }
    else if (drag.mode === 'edge') { mark[drag.edge] = Math.round(t); exportFields(); }
    else if (drag.mode === 'sel') { mark.b = Math.round(t); exportFields(); }
    else if (drag.mode === 'seek') { if (Math.abs(p.x - drag.x) > 3) drag.moved = true; drag.t = t; }
  }
  draw();
});
function endDrag(e) {
  if (!drag) return; var d = drag; drag = null; cv.classList.remove('pan');
  if (d.mode === 'seek') seek(Math.max(day, Math.min(dayEnd(day) - 1, d.moved ? d.t : tAt(pos(e).x))));
  if (d.mode === 'sel' || d.mode === 'edge') {
    if (mark.a > mark.b) { var x = mark.a; mark.a = mark.b; mark.b = x; }
    if (mark.b - mark.a < 1) mark.a = mark.b = null;
    exportFields();
  }
  draw();
}
cv.addEventListener('pointerup', endDrag);
cv.addEventListener('pointercancel', function () { drag = null; cv.classList.remove('pan'); draw(); });
cv.addEventListener('pointerleave', function () { hover = null; tip(null); draw(); });
cv.addEventListener('wheel', function (e) {
  e.preventDefault();
  if (e.shiftKey || Math.abs(e.deltaX) > Math.abs(e.deltaY)) { view.start += (e.deltaX || e.deltaY) / W() * view.span; clampView(); draw(); return; }
  zoom(e.deltaY < 0 ? 1 : -1, tAt(pos(e).x));
}, { passive: false });
$$('[data-zoom]').forEach(function (b) { b.onclick = function () { zoom(+b.dataset.zoom === 1 ? -1 : 1); }; });
$$('[data-pan]').forEach(function (b) { b.onclick = function () { view.start += +b.dataset.pan * view.span / 2; clampView(); draw(); }; });
if (window.ResizeObserver) new ResizeObserver(function () { sizeCanvas(); draw(); }).observe($('.pbtlc'));
else window.addEventListener('resize', function () { sizeCanvas(); draw(); });

// ---------- transport ----------
$('#pbplay').onclick = function () { setPlaying(!clock.playing); };
$$('[data-step]').forEach(function (b) { b.onclick = function () { seek(clock.t + +b.dataset.step); }; });
$('#pbprev').onclick = function () { var t = prevStart(clock.t); if (t !== null) seek(t); };
$('#pbnext').onclick = function () { var t = nextStart(clock.t); if (t !== null) seek(t); };
function setRate(r) {
  clock.rate = r; st.rate = r; remember(); lastStatus = '';
  $$('#pbspeed a').forEach(function (a) { a.setAttribute('aria-selected', +a.dataset.rate === r); });
  tiles.forEach(function (x) { if (x.video) [x.video, x.back].forEach(function (v) { v.defaultPlaybackRate = r; v.playbackRate = r; }); });
}
$$('#pbspeed a').forEach(function (a) { a.addEventListener('click', function (e) { e.preventDefault(); setRate(+a.dataset.rate); }); });
document.addEventListener('keydown', function (e) {
  var tg = e.target, typing = tg.closest && tg.closest('input,select,textarea');
  if (e.key === 'Escape' && big >= 0 && !document.fullscreenElement) { setBig(-1); return; }
  if (typing || e.ctrlKey || e.metaKey || e.altKey) return;
  if (e.key === ' ' && !tg.closest('button,a')) { e.preventDefault(); setPlaying(!clock.playing); }
  else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') { e.preventDefault(); seek(clock.t + (e.key === 'ArrowLeft' ? -1 : 1) * (e.shiftKey ? 60 : 10)); }
  else if ((e.key === '[' || e.key === ']') && D.export) { setMark(e.key === '[' ? 'a' : 'b'); }
});
$('#pbgoto').addEventListener('change', function () {
  var p = this.value.split(':'); if (p.length < 2) return;
  var d = new Date(day * 1000); d.setHours(+p[0], +p[1], +(p[2] || 0), 0); seek(d.getTime() / 1000);
});
$('#pbfull').onclick = function () { var el = $('#pbmain'); if (el.requestFullscreen) el.requestFullscreen(); };
document.addEventListener('fullscreenchange', function () { sizeCanvas(); draw(); });

// ---------- the calendar ----------
var calMonth = new Date(day * 1000); calMonth.setDate(1);
function pickDay(d) {
  var t = d + (clock.t - day), now = nowS();
  if (t > now - 30) t = Math.max(d, now - 30);
  seek(Math.min(t, dayEnd(d) - 1)); jumpPending = true;
}
function renderCal() {
  var box = $('#calgrid'); box.innerHTML = '';
  $('#calm').textContent = calMonth.toLocaleDateString(LANG, { month: 'long', year: 'numeric' });
  for (var i = 0; i < 7; i++) { var sp = document.createElement('span'); sp.textContent = new Date(2024, 0, 1 + i).toLocaleDateString(LANG, { weekday: 'short' }); box.appendChild(sp); }
  var have = {}; tiles.forEach(function (x) { if (x.key && days[x.key]) Object.keys(days[x.key]).forEach(function (k) { have[k] = true; }); });
  var off = (calMonth.getDay() + 6) % 7, len = new Date(calMonth.getFullYear(), calMonth.getMonth() + 1, 0).getDate();
  var cells = Math.ceil((off + len) / 7) * 7, today = fmtDate(nowS()), cur = fmtDate(day);
  for (var j = 0; j < cells; j++) {
    var d = new Date(calMonth.getFullYear(), calMonth.getMonth(), 1 - off + j), ds = fmtDate(d.getTime() / 1000), b = document.createElement('button');
    b.type = 'button'; b.textContent = d.getDate();
    b.className = (d.getMonth() !== calMonth.getMonth() ? 'out ' : '') + (have[ds] ? 'rec ' : '') + (ds === today ? 'today ' : '') + (ds === cur ? 'cur' : '');
    b.setAttribute('aria-label', d.toLocaleDateString(LANG, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' }));
    if (ds === cur) b.setAttribute('aria-current', 'date');
    (function (t) { b.onclick = function () { pickDay(t); }; })(d.getTime() / 1000);
    box.appendChild(b);
  }
}
$$('[data-month]').forEach(function (b) { b.onclick = function () { calMonth.setMonth(calMonth.getMonth() + +b.dataset.month); renderCal(); }; });
$('#caltoday').onclick = function () { pickDay(dayStart(nowS())); };

// ---------- the cameras on the left ----------
$$('.lcam').forEach(function (b) {
  b.onclick = function () { st.tiles[sel] = { n: b.dataset.n, c: b.dataset.c }; if (sel < st.layout - 1) sel++; render(); };
  b.addEventListener('dragstart', function (e) { e.dataTransfer.setData('text/x-cam', b.dataset.n + '\n' + b.dataset.c); });
});
$('#lsearch').oninput = function () { var v = this.value.toLowerCase();
  $$('.lcam').forEach(function (b) { b.style.display = (b.dataset.label + ' ' + b.dataset.c + ' ' + b.dataset.n).toLowerCase().indexOf(v) >= 0 ? '' : 'none'; }); };
$$('[data-layout]').forEach(function (b) { b.onclick = function () { st.layout = +b.dataset.layout; big = -1; render(); }; });
$('#pbclear').onclick = function () { st.tiles = []; sel = 0; big = -1; render(); };

// ---------- archiving the range ----------
var X = $('#pbexport'), unchecked = {};
function setMark(which) {
  mark[which] = Math.floor(clock.t);
  if (mark.a !== null && mark.b !== null && mark.a > mark.b) { var o = which === 'a' ? 'b' : 'a'; mark[o] = mark[which]; }
  exportFields(); draw();
}
function exportCams() {
  if (!X) return;
  var box = $('#pbxlist'), seen = {}; box.innerHTML = '';
  tiles.forEach(function (x) {
    if (!x.key || seen[x.key]) return; seen[x.key] = true;
    var c = cams[x.key], l = document.createElement('label'); l.className = 'chk';
    l.innerHTML = '<input type="checkbox"> <span style="flex:1"></span><span class="note"></span>';
    var cb = $('input', l); cb.value = c.n + '|' + c.c; cb.checked = !unchecked[x.key];
    cb.onchange = function () { if (cb.checked) delete unchecked[x.key]; else unchecked[x.key] = true; exportSum(); };
    $('span', l).textContent = c.label; $('.note', l).textContent = c.nvr; box.appendChild(l);
  });
  if (!box.children.length) { var n = document.createElement('span'); n.className = 'note'; n.textContent = T.pb_no_tiles; box.appendChild(n); }
  exportSum();
}
function chosen() { return $$('#pbxlist input:checked').map(function (i) { return i.value; }); }
function exportFields() {
  if (!X) return;
  $('#pbxfrom').value = mark.a !== null ? fmtInput(mark.a) : ''; $('#pbxto').value = mark.b !== null ? fmtInput(mark.b) : '';
  exportSum();
}
function exportSum() {
  if (!X) return;
  var all = $$('#pbxlist input'), n = chosen().length, a = $('#pbxall');
  a.checked = all.length > 0 && n === all.length; a.indeterminate = n > 0 && n < all.length;
  $('#pbxsum').textContent = mark.a !== null && mark.b !== null && mark.b > mark.a ? T.pb_sum.replace('%d', n).replace('%s', human(mark.b - mark.a)) : T.pb_sel_none;
}
function flash(kind, text, link) {
  var m = $('#pbxmsg'); m.className = 'flash ' + kind; m.textContent = text; m.hidden = !text;
  var tab = $('#arqueuetab');
  if (link && tab) { var a = document.createElement('a'); a.href = tab.href; a.textContent = tab.firstChild.textContent.trim() + ' →'; a.style.marginLeft = '8px'; m.appendChild(a); }
}
function refreshQueue() { // the count of running jobs on the queue's tab, as the server tells it
  fetch(location.href, { credentials: 'same-origin' }).then(function (r) { return r.text(); }).then(function (h) {
    var q = new DOMParser().parseFromString(h, 'text/html').getElementById('arqueuetab'), cur = $('#arqueuetab');
    if (q && cur) { cur.innerHTML = q.innerHTML; cur.title = q.title; }
  }).catch(function () {});
}
if (X) {
  $('#pbxall').onchange = function () { var on = this.checked; $$('#pbxlist input').forEach(function (i) { i.checked = on; i.onchange(); }); };
  ['pbxfrom', 'pbxto'].forEach(function (id, k) {
    $('#' + id).addEventListener('change', function () { var v = this.value ? new Date(this.value).getTime() / 1000 : null;
      mark[k ? 'b' : 'a'] = v !== null && isFinite(v) ? v : null; exportSum(); if (mark[k ? 'b' : 'a'] !== null) ensureVisible(mark[k ? 'b' : 'a'], true); draw(); });
  });
  $$('[data-mark]', X).forEach(function (b) { b.onclick = function () { setMark(b.dataset.mark); }; });
  $('#pbxshow').onclick = function () {
    if (mark.a === null || mark.b === null) return;
    if (dayStart(mark.a) !== day) seek(mark.a);
    var len = mark.b - mark.a, fit = ZOOMS.filter(function (z) { return z >= len * 1.2; }).pop() || ZOOMS[0];
    view.span = fit; view.start = mark.a - (fit - len) / 2; clampView(); draw();
  };
  X.addEventListener('submit', function (e) {
    e.preventDefault();
    var cs = chosen(), f = new FormData(X);
    if (mark.a === null || mark.b === null || mark.b <= mark.a) { flash('err', T.ar_bad_range); return; }
    if (!cs.length) { flash('err', T.ar_no_cameras); return; }
    var body = new URLSearchParams();
    body.set('csrf', D.csrf); body.set('from', Math.floor(mark.a)); body.set('to', Math.ceil(mark.b));
    ['storage', 'path', 'layout', 'percamera', 'block', 'block_custom'].forEach(function (k) { if (f.get(k) !== null) body.set(k, f.get(k)); });
    cs.forEach(function (c) { body.append('cam', c); });
    var go = $('#pbxgo'); go.disabled = true;
    fetch('/archive/export', { method: 'POST', body: body, credentials: 'same-origin' }).then(function (r) { return r.json(); }).then(function (r) {
      go.disabled = false;
      var q = T.ar_queued.replace('%d', r.queued || 0);
      if (r.ok) flash('ok', q, true); else flash('err', (r.queued ? q + ' ' : '') + T.pb_error.replace('%s', r.error || '?'), r.queued > 0);
      if (r.queued) refreshQueue();
    }, function (err) { go.disabled = false; flash('err', T.pb_error.replace('%s', err.message)); });
  });
}

// ---------- start ----------
view.span = 7200; view.start = clock.t - 3600; clampView(); // two hours around the start
setRate(clock.rate);
render(); exportFields(); status();
})();
