package web

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
	"ffnvr/internal/profile"
	"ffnvr/internal/script"
)

// Live view: the panel proxies go2rtc (on 127.0.0.1, see modules/live.go) for signed in
// users with access to the camera: MSE over WebSocket (fragmented MP4) and HLS, plus
// snapshots. The browser never names a go2rtc stream itself: it asks for a camera of a
// recorder and a quality (hq/lq), the panel checks and maps them.

type liveCam struct {
	Nvr      string `json:"n"`
	Cam      string `json:"c"`
	Label    string `json:"label"`
	Ch       string `json:"ch"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	HQ       bool   `json:"hq"`
	LQ       bool   `json:"lq"`
	NvrLabel string `json:"nvr"`
	Play     bool   `json:"play,omitempty"` // its recordings play in the panel's player
	// PTZ: what the control panel offers for the camera (right live.ptz); nil: nothing
	PTZ   *modules.PTZCaps `json:"ptz,omitempty"`
	SwapH bool             `json:"swaph,omitempty"` // ptz-swap-horizontal
	SwapV bool             `json:"swapv,omitempty"` // ptz-swap-vertical
}

type liveNvr struct {
	Name, Type, Kind, Status string
	Online, Total            int
	Cams                     []liveCam
	Streams                  int // the player: the most streams read from it at once (0: no limit)
}

type liveTileJSON struct {
	Pos int    `json:"pos"`
	N   string `json:"n"`
	C   string `json:"c"`
	Q   string `json:"q"`
}

type liveViewJSON struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Size  int            `json:"size"`
	Tiles []liveTileJSON `json:"tiles"`
}

type liveData struct {
	CSRF      string            `json:"csrf"`
	Tiles     string            `json:"tiles"`
	OSM       bool              `json:"osm"`
	Edit      bool              `json:"edit"`     // the panel can write the configuration
	ViewEdit  bool              `json:"vedit"`    // rights: live.views
	Map       bool              `json:"map"`      // live.map
	MapEdit   bool              `json:"medit"`    // live.map.edit
	Thumbs    bool              `json:"thumbs"`   // live.thumbs
	PTZ       bool              `json:"ptz"`      // live.ptz
	PTZEdit   bool              `json:"pzedit"`   // live.ptz.edit
	PTZSpeed  int               `json:"ptzmax"`   // speeds 1..ptzmax
	Archive   bool              `json:"archive"`  // archive: the recordings of a camera can be opened
	ArchPlay  bool              `json:"archplay"` // archive.play too: in the player
	TCHQ      bool              `json:"tchq"`     // HQ may be transcoded to H.264 for this user
	TCLQ      bool              `json:"tclq"`     // LQ too
	ZoomHQ    bool              `json:"zoomhq"`   // the user's setting: a zoomed tile switches to HQ
	Snaps     []snapJSON        `json:"snaps"`
	Snap      snapInfo          `json:"snap"`
	Cams      []liveCam         `json:"cams"`
	Views     []liveViewJSON    `json:"views"`
	Positions []posJSON         `json:"positions"`
	T         map[string]string `json:"t"`
}

// snapJSON: the thumbnail of a camera (see modules.Thumbs).
type snapJSON struct {
	N     string `json:"n"`
	C     string `json:"c"`
	Sym   string `json:"sym"`   // the symbol: alias, else RECORDER camera; the order
	Has   bool   `json:"has"`   // there is a picture
	V     int64  `json:"v"`     // its version (when it was taken), for the cache
	Taken string `json:"taken"` // when the picture was taken
	Tried string `json:"tried"` // the last try
	OK    bool   `json:"ok"`    // the last try got a picture
	Err   string `json:"err"`
}

// snapInfo: the schedule of the thumbnails, as the side panel shows it.
type snapInfo struct {
	Sched string `json:"sched"`
	Last  string `json:"last"`
	Next  string `json:"next"`
	Err   string `json:"err"`
}

const snapLayout = "2006-01-02 15:04"

func snapTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(snapLayout)
}

// snapshots fills the thumbnails of the recorders the user may see.
func (s *Server) snapshots(data *liveData, store *config.Store, access map[string]bool, t map[string]string) {
	data.Snaps = []snapJSON{}
	if !data.Thumbs {
		return
	}
	for _, th := range modules.Thumbs(store, s.opt.Root, access) {
		data.Snaps = append(data.Snaps, snapJSON{N: th.Nvr, C: th.Camera, Sym: th.Symbol, Has: th.Has, V: th.Taken.Unix(), Taken: snapTime(th.Taken),
			Tried: snapTime(th.Tried), OK: th.OK, Err: maskSecrets(th.Error)})
	}
	c, st, now := modules.SnapshotSettings(store), modules.ReadSnapState(s.opt.Root), time.Now()
	at := fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
	switch {
	case !c.Enabled:
		data.Snap.Sched = t["tb_off"]
	case c.Weekly:
		data.Snap.Sched = fmt.Sprintf(t["tb_weekly"], t["tb_d_"+strings.ToLower(c.Day.String())], at)
	default:
		data.Snap.Sched = fmt.Sprintf(t["tb_daily"], at)
	}
	data.Snap.Last, data.Snap.Err = snapTime(st.LastRun), st.Error
	if c.Enabled {
		data.Snap.Next = snapTime(c.Next(now))
		if c.Due(st, now) {
			data.Snap.Next = t["tb_soon"]
		}
	}
}

// GET /live/thumb?n=&c=: the thumbnail of a camera.
func (s *Server) thumb(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, modules.PermLiveThumbs) {
		s.denied(w, se, modules.PermLiveThumbs)
		return
	}
	n, c := r.FormValue("n"), r.FormValue("c")
	if !modules.CanAccess(store, se.user, n) || findCam(store, n, c) == nil {
		http.Error(w, "no access", http.StatusForbidden)
		return
	}
	f, err := os.Open(modules.SnapshotPath(s.opt.Root, n, c))
	if err != nil {
		http.Error(w, "no thumbnail yet", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400") // the address carries its version
	http.ServeContent(w, r, "", st.ModTime(), f)
}

type liveView struct {
	Enabled       bool
	Nvrs          []liveNvr
	Online, Total int
	Data          template.JS
	TileHost      string
}

func (s *Server) livePage(p *systemPage, store *config.Store) {
	t := p.T
	p.Module, p.Title, p.Crumb = "live", t["live"], ""
	set := modules.LiveSettings(store)
	lv := &liveView{Enabled: set.Enabled}
	streams := modules.LiveStreams(store)
	data := liveData{CSRF: p.CSRF, Tiles: "/live/tiles/{z}/{x}/{y}.png", OSM: strings.Contains(set.MapTiles, "openstreetmap"), Edit: s.opt.Edit != nil, T: map[string]string{},
		Cams: []liveCam{}, Views: []liveViewJSON{}, Positions: []posJSON{},
		ViewEdit: p.Can[modules.PermLiveViews], Map: p.Can[modules.PermLiveMap], MapEdit: p.Can[modules.PermLiveMapEdit],
		Thumbs: p.Can[modules.PermLiveThumbs], PTZ: p.Can[modules.PermLivePTZ], PTZEdit: p.Can[modules.PermLivePTZEdit], PTZSpeed: modules.PTZSpeedMax,
		Archive: p.Can[modules.PermArchive], ArchPlay: p.Can[modules.PermArchive] && p.Can[modules.PermArchivePlay]}
	if st, err := s.prof.Settings(p.User); err != nil {
		s.opt.Log.Printf("web: settings of %s: %v", p.User, err)
		data.ZoomHQ = true
	} else {
		data.ZoomHQ = st.ZoomToHQ()
	}
	if modules.CanTranscode(store, p.User) {
		data.TCHQ, data.TCLQ = set.Transcode.HQ.Max > 0, set.Transcode.LQ.Max > 0
	}
	access := map[string]bool{}
	for _, n := range p.Sys.Nvrs {
		access[n.Name] = true
		ln := liveNvr{Name: n.Name, Type: n.Type, Kind: n.Kind, Status: n.Status}
		for _, c := range n.Cameras {
			if c.Name == "" || findCam(store, n.Name, c.Name) == nil {
				continue // a channel without a camera of the configuration
			}
			lc := liveCam{Nvr: n.Name, Cam: c.Name, Label: or(c.Alias, c.Name), Ch: c.Ch, Kind: c.Kind, State: c.Status, NvrLabel: n.Name, Play: playableView(n)}
			if lc.Ch == "—" {
				lc.Ch = ""
			}
			if caps, ok := modules.CameraPTZ(store, n.Name, c.Name); ok && data.PTZ {
				lc.PTZ = &caps
				lc.SwapH, lc.SwapV = modules.PTZSwap(store, n.Name, c.Name)
			}
			_, lc.HQ = streams[modules.LiveName(n.Name, c.Name, "hq")]
			_, lc.LQ = streams[modules.LiveName(n.Name, c.Name, "lq")]
			data.Cams = append(data.Cams, lc) // the map knows them all
			if !lc.HQ && !lc.LQ {
				continue // nothing to watch (disabled, or no address)
			}
			ln.Total++
			if c.Kind == kOK {
				ln.Online++
			}
			ln.Cams = append(ln.Cams, lc)
		}
		lv.Online += ln.Online
		lv.Total += ln.Total
		lv.Nvrs = append(lv.Nvrs, ln)
	}
	var ds profile.Dashboards
	if _, err := s.prof.Read(p.User, profile.DashboardsFile, &ds); err != nil {
		s.opt.Log.Printf("web: %v", err)
	}
	for _, d := range ds.Dashboards {
		v := liveViewJSON{ID: d.ID, Name: d.Name, Size: d.Size}
		for _, c := range d.Tiles {
			if access[c.Nvr] {
				v.Tiles = append(v.Tiles, liveTileJSON{c.Position, c.Nvr, c.Camera, c.Quality})
			}
		}
		data.Views = append(data.Views, v)
	}
	for _, ps := range modules.Positions(store) {
		if access[ps.Nvr] && data.Map {
			data.Positions = append(data.Positions, posJSON{N: ps.Nvr, C: ps.Camera, Lat: ps.Lat, Long: ps.Long, Dir: ps.Direction, Angle: ps.Angle, Range: ps.RangeMeter})
		}
	}
	s.snapshots(&data, store, access, t)
	for k, v := range t {
		if strings.HasPrefix(k, "lv_") || strings.HasPrefix(k, "mp_") || strings.HasPrefix(k, "tb_") || strings.HasPrefix(k, "pz_") {
			data.T[k] = v
		}
	}
	b, _ := json.Marshal(data) // < > & are escaped: safe inside <script>
	lv.Data = template.JS(b)
	p.Live = lv
}

func findCam(s *config.Store, nvr, cam string) *config.Item {
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvr && c.Val("name") == cam {
			return c
		}
	}
	return nil
}

// liveTarget checks a live request: the user's session and access to the camera, and
// returns go2rtc's address and the stream. tc=1 (only /live/ws) asks for the camera's
// stream of that quality transcoded to H.264: then tcMax is the limit of such streams
// (see tcSlots).
func (s *Server) liveTarget(w http.ResponseWriter, r *http.Request) (api, stream string, tcMax int, ok bool) {
	se := s.current(r)
	if se == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, modules.PermLive) {
		s.denied(w, se, modules.PermLive)
		return
	}
	l := modules.LiveSettings(store)
	if !l.Enabled {
		http.Error(w, "live view is off", http.StatusNotFound)
		return
	}
	api = l.API()
	if strings.HasPrefix(r.URL.Path, "/live/api/hls/") {
		return api, "", 0, true // a session of go2rtc, named by its id: the user is signed in
	}
	n, c, q := r.FormValue("n"), r.FormValue("c"), r.FormValue("q")
	if q != "lq" {
		q = "hq"
	}
	if !modules.CanAccess(store, se.user, n) {
		http.Error(w, "no access", http.StatusForbidden)
		return
	}
	stream = modules.LiveName(n, c, q)
	if _, exists := modules.LiveStreams(store)[stream]; !exists {
		http.Error(w, "no such stream", http.StatusNotFound)
		return
	}
	if r.FormValue("tc") == "1" {
		if r.URL.Path != "/live/ws" {
			http.Error(w, "transcoding: only over /live/ws", http.StatusBadRequest)
			return
		}
		if tcMax = l.Transcode.Of(q).Max; tcMax == 0 || !modules.CanTranscode(store, se.user) {
			http.Error(w, "transcoding is not allowed", http.StatusForbidden)
			return
		}
		stream = modules.TranscodeName(n, c, q)
	}
	return api, stream, tcMax, true
}

// tcSlots counts the transcoded streams being watched through the panel: go2rtc runs one
// ffmpeg per stream, shared by all its viewers, so the limit is on streams, not viewers.
// Each quality has its own limit: a stream counts against the streams of its quality
// (the suffix of its name, tc-hq or tc-lq).
type tcSlots struct {
	mu     sync.Mutex
	active map[string]int // stream -> viewers
}

func tcClass(stream string) string { return stream[strings.LastIndexByte(stream, '/')+1:] }

// running: the streams of the stream's quality being watched (t.mu held).
func (t *tcSlots) running(stream string) int {
	n, class := 0, tcClass(stream)
	for x := range t.active {
		if tcClass(x) == class {
			n++
		}
	}
	return n
}

// acquire takes a viewer of a stream: always when the stream already runs, else while
// fewer than max streams of its quality run.
func (t *tcSlots) acquire(stream string, max int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		t.active = map[string]int{}
	}
	if t.active[stream] == 0 && t.running(stream) >= max {
		return false
	}
	t.active[stream]++
	return true
}

func (t *tcSlots) release(stream string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[stream]--; t.active[stream] <= 0 {
		delete(t.active, stream)
	}
}

// free tells whether a viewer of the stream would be let in now, and how many streams of
// its quality run.
func (t *tcSlots) free(stream string, max int) (ok bool, used int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	used = t.running(stream)
	return t.active[stream] > 0 || used < max, used
}

// GET /live/transcode?n=&c=&q=: whether the browser may open the camera's transcoded
// stream of quality q (hq, lq) now (/live/ws?...&tc=1). Answers {"ok":true,"height":720}
// (0: the stream's own size), {"busy":true,"used":2,"max":2} (the limit is reached: try
// later) or an error.
func (s *Server) transcodeCheck(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		writeJSON(w, http.StatusUnauthorized, jsonResult{Error: "not signed in"})
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResult{Error: "configuration error"})
		return
	}
	n, c, q := r.FormValue("n"), r.FormValue("c"), r.FormValue("q")
	if q != "lq" {
		q = "hq"
	}
	if !can(store, se, modules.PermLive) || !modules.CanAccess(store, se.user, n) {
		writeJSON(w, http.StatusForbidden, jsonResult{Error: "no access"})
		return
	}
	set := modules.LiveSettings(store).Transcode.Of(q)
	if set.Max == 0 || !modules.CanTranscode(store, se.user) {
		writeJSON(w, http.StatusForbidden, jsonResult{Error: "transcoding is not allowed"})
		return
	}
	if hq, lq := modules.HasLive(store, n, c); q == "hq" && !hq || q == "lq" && !lq {
		writeJSON(w, http.StatusNotFound, jsonResult{Error: "no such stream"})
		return
	}
	ok, used := s.tc.free(modules.TranscodeName(n, c, q), set.Max)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"busy": true, "used": used, "max": set.Max})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "height": set.Height})
}

// sameOrigin: a WebSocket must come from the panel's own pages (behind a reverse proxy
// of -proxy that changes the Host header: the host it forwards in X-Forwarded-Host).
func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // not a browser
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	if u.Host == r.Host {
		return true
	}
	fh, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Host"), ",")
	return s.viaProxy(r) && fh != "" && strings.EqualFold(u.Host, strings.TrimSpace(fh))
}

// liveProxy serves /live/ws, /live/api/stream.m3u8, /live/api/hls/..., /live/frame.jpeg.
func (s *Server) liveProxy(w http.ResponseWriter, r *http.Request) {
	path := map[string]string{
		"/live/ws":              "/api/ws",
		"/live/api/stream.m3u8": "/api/stream.m3u8",
		"/live/frame.jpeg":      "/api/frame.jpeg",
	}[r.URL.Path]
	if path == "" && strings.HasPrefix(r.URL.Path, "/live/api/hls/") {
		path = strings.TrimPrefix(r.URL.Path, "/live")
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/live/ws" && !s.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	api, stream, tcMax, ok := s.liveTarget(w, r)
	if !ok {
		return
	}
	if tcMax > 0 { // a transcoded stream: within the limit, for as long as it is watched
		if !s.tc.acquire(stream, tcMax) {
			s.opt.Log.Printf("web: %s: transcoding limit reached (%d), %s refused", s.current(r).user, tcMax, stream)
			http.Error(w, "transcoding limit reached", http.StatusTooManyRequests)
			return
		}
		defer s.tc.release(stream)
		s.opt.Log.Printf("web: %s: watching %s (H.265 -> H.264 for the browser)", s.current(r).user, stream)
	}
	q := url.Values{}
	if stream != "" {
		q.Set("src", stream)
	} else {
		q = r.URL.Query() // hls: id=...
	}
	if r.URL.Path == "/live/ws" || strings.HasPrefix(path, "/api/hls/") {
		// a stream lasts: no deadlines of the server
		rc := http.NewResponseController(w)
		rc.SetReadDeadline(time.Time{})
		rc.SetWriteDeadline(time.Time{})
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", api
			pr.Out.URL.Path, pr.Out.URL.RawPath, pr.Out.URL.RawQuery = path, "", q.Encode()
			pr.Out.Host = api
			for _, h := range []string{"Cookie", "Authorization", "Origin", "Referer"} {
				pr.Out.Header.Del(h) // the panel's own, and go2rtc checks the origin
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Access-Control-Allow-Origin")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.opt.Log.Printf("web: live: %v", err)
			http.Error(w, "live view is not available", http.StatusBadGateway)
		},
	}
	if r.URL.Path == "/live/frame.jpeg" {
		w.Header().Set("Content-Disposition", `inline; filename="`+strings.ReplaceAll(stream, "/", "_")+`_`+time.Now().Format("2006-01-02T15-04-05")+`.jpg"`)
	}
	proxy.ServeHTTP(w, r)
}

// POST /live/ptz: csrf, n, c (the camera), a (the action, modules.PTZUp...), on (1: start
// or switch on, else stop or switch off), s (the speed). Answers {"ok":true} or an error.
func (s *Server) ptz(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		writeJSON(w, http.StatusUnauthorized, jsonResult{Error: "not signed in"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(r.PostFormValue("csrf"))) != 1 {
		writeJSON(w, http.StatusForbidden, jsonResult{Error: "forbidden"})
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResult{Error: "configuration error"})
		return
	}
	if !can(store, se, modules.PermLivePTZ) {
		s.opt.Log.Printf("web: %s: no right %s", se.user, modules.PermLivePTZ)
		writeJSON(w, http.StatusForbidden, jsonResult{Error: "the right " + modules.PermLivePTZ + " is missing"})
		return
	}
	n, c, a := r.PostFormValue("n"), r.PostFormValue("c"), r.PostFormValue("a")
	if !modules.CanAccess(store, se.user, n) {
		writeJSON(w, http.StatusForbidden, jsonResult{Error: "no access"})
		return
	}
	on := r.PostFormValue("on") == "1"
	speed, _ := strconv.Atoi(r.PostFormValue("s"))
	if err := s.ptzc.Do(store, n, c, a, on, speed); err != nil {
		code := http.StatusBadGateway // the recorder
		if errors.Is(err, modules.ErrPTZ) {
			code = http.StatusBadRequest
		}
		msg := maskSecrets(err.Error())
		s.opt.Log.Printf("web: %s: ptz %s %s %s on=%v: %s", se.user, n, c, a, on, msg)
		writeJSON(w, code, jsonResult{Error: msg})
		return
	}
	writeJSON(w, http.StatusOK, jsonResult{OK: true})
}

// POST /live/ptz/swap: csrf, n, c, h and v (1: swapped) - the camera's
// ptz-swap-horizontal and ptz-swap-vertical in the configuration (right live.ptz.edit).
func (s *Server) ptzSwap(w http.ResponseWriter, r *http.Request) {
	se, ok := s.editRequest(w, r, modules.PermLivePTZEdit)
	if !ok {
		return
	}
	n, c := r.PostFormValue("n"), r.PostFormValue("c")
	h, v := r.PostFormValue("h") == "1", r.PostFormValue("v") == "1"
	err := s.opt.Edit(func(st *config.Store) error {
		if !modules.CanAccess(st, se.user, n) {
			return fmt.Errorf("no access to %s", n)
		}
		if findCam(st, n, c) == nil {
			return fmt.Errorf("no such camera: %s %s", n, c)
		}
		find := []script.Param{p("nvr", n), p("name", c)}
		var set []script.Param
		var unset []string
		for _, x := range []struct {
			key string
			on  bool
		}{{"ptz-swap-horizontal", h}, {"ptz-swap-vertical", v}} {
			if x.on {
				set = append(set, p(x.key, "yes"))
			} else {
				unset = append(unset, x.key) // the default: nothing left in the file
			}
		}
		if len(set) > 0 {
			if err := run(st, "/camera", "set", find, set); err != nil {
				return err
			}
		}
		if len(unset) > 0 {
			_, err := st.Exec(script.Command{Path: "/camera", Verb: "unset", Find: find, HasFind: true, Args: unset})
			return err
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: err.Error()})
		return
	}
	s.opt.Log.Printf("web: %s set the PTZ swap of %s %s: horizontal=%v vertical=%v", se.user, n, c, h, v)
	s.syslog(eventdb.Entry{Kind: modules.SysConfigChanged, Nvr: n, Camera: c, User: se.user, Address: s.clientIP(r).String(),
		Text: fmt.Sprintf("/camera %s: ptz-swap-horizontal=%s ptz-swap-vertical=%s", c, yesNo(h), yesNo(v))})
	writeJSON(w, http.StatusOK, jsonResult{OK: true})
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
