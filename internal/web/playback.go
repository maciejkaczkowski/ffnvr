package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/modules"
)

// The player of the Archive module (tab=play): the cameras of the local recorders are
// put on tiles like in the live view and played in sync, on one clock, along a timeline
// of what each camera recorded; a range marked on the timeline is archived as
// /nvr/download jobs. The browser asks for the segments of a day (/archive/segments)
// and plays them as mp4 (/archive/play); of the remote recorders, Dahua, Tiandy,
// Uniview and DVRIP (XMeye) are played in pieces read from them (modules.OpenPlay).

// pbCam: a camera of the player.
type pbCam struct {
	N     string `json:"n"`
	C     string `json:"c"`
	Label string `json:"label"`
	Ch    string `json:"ch"`
	Kind  string `json:"kind"`
	Nvr   string `json:"nvr"`
}

type pbTile struct {
	N string `json:"n"`
	C string `json:"c"`
}

type pbData struct {
	CSRF     string            `json:"csrf"`
	Cams     []pbCam           `json:"cams"`
	Init     []pbTile          `json:"init"`   // cameras asked for by the address (c=)
	T0       int64             `json:"t0"`     // the time to start at (unix)
	Oldest   int64             `json:"oldest"` // the recordings of the local recorders reach from…
	Newest   int64             `json:"newest"` // …to (unix, 0: none)
	Export   bool              `json:"export"`
	Download bool              `json:"download"`
	T        map[string]string `json:"t"`
}

// the longest range the timeline asks for at once
const segmentsSpan = 3 * 24 * time.Hour

// playbackPage fills the player: the tree of local recorders and the page's data.
func (s *Server) playbackPage(p *systemPage, store *config.Store, a *archiveView, q url.Values) {
	t := p.T
	p.Title, p.Crumb = t["pb_title"], t["archive"]
	data := pbData{CSRF: p.CSRF, Cams: []pbCam{}, Init: []pbTile{}, Export: a.Export, Download: a.Download, T: map[string]string{}}
	known := map[string]bool{}
	for _, n := range p.Sys.Nvrs {
		if !playableView(n) {
			a.PlayExt = append(a.PlayExt, n)
			continue
		}
		ln := liveNvr{Name: n.Name, Type: n.Type, Kind: n.Kind, Status: n.Status}
		for _, c := range n.Cameras {
			if c.Name == "" || findCam(store, n.Name, c.Name) == nil {
				continue
			}
			lc := liveCam{Nvr: n.Name, Cam: c.Name, Label: or(c.Alias, c.Name), Ch: c.Ch, Kind: c.Kind, State: c.Status, NvrLabel: n.Name}
			if lc.Ch == "—" {
				lc.Ch = ""
			}
			ln.Cams = append(ln.Cams, lc)
			ln.Total++
			data.Cams = append(data.Cams, pbCam{N: n.Name, C: c.Name, Label: lc.Label, Ch: lc.Ch, Kind: c.Kind, Nvr: n.Name})
			known[n.Name+"\n"+c.Name] = true
		}
		if !n.Local {
			ln.Streams = modules.PlayStreams
			a.PlayRemote = append(a.PlayRemote, ln)
			continue
		}
		a.PlayNvrs = append(a.PlayNvrs, ln)
		if rec := modules.Recordings(store, n.Name); rec.Files > 0 {
			if o := rec.Oldest.Unix(); data.Oldest == 0 || o < data.Oldest {
				data.Oldest = o
			}
			if e := rec.Newest.Add(15 * time.Minute).Unix(); e > data.Newest {
				data.Newest = e
			}
		}
	}
	// c=camera (of the recorder n) or c=recorder|camera: put on the tiles
	for _, c := range q["c"] {
		n, cam, ok := strings.Cut(c, "|")
		if !ok {
			n, cam = a.Nvr.Name, c
		}
		if known[n+"\n"+cam] {
			data.Init = append(data.Init, pbTile{N: n, C: cam})
		}
	}
	// the start: t= (unix), else a few minutes before the newest recording
	now := time.Now()
	data.T0 = now.Add(-10 * time.Minute).Unix()
	if data.Newest > 0 && data.Newest < now.Unix() {
		data.T0 = data.Newest - 25*60 // the end of the newest segment is a guess: 15 minutes
	}
	if v, err := strconv.ParseInt(q.Get("t"), 10, 64); err == nil && v > 0 {
		data.T0 = v
	}
	for k, v := range t {
		if strings.HasPrefix(k, "pb_") || strings.HasPrefix(k, "lv_") {
			data.T[k] = v
		}
	}
	for _, k := range []string{"pl_cannot", "ar_no_storage", "ar_no_cameras", "ar_queued", "ar_bad_range"} {
		data.T[k] = t[k]
	}
	b, _ := json.Marshal(data) // < > & are escaped: safe inside <script>
	a.PlayData = template.JS(b)
}

// playableView: the player plays this recorder (modules.Playable for the views).
func playableView(n nvrView) bool { return n.Local || modules.PlayableType(n.Type) }

func isLocal(store *config.Store, nvr string) bool {
	for _, it := range store.Items("/nvr") {
		if it.Val("name") == nvr {
			return it.Val("type") == "local"
		}
	}
	return false
}

// A remote recorder keeps files of an hour; the player reads them in pieces of
// remotePiece, named "start-end" (unix): short enough to start soon, long enough not
// to change files often (the next one is made while one plays).
const remotePiece = 30 * time.Second

// eventParts: the motion bar of a piece [b, e) of a remote recorder from the alarms the
// recorder told (Tiandy): a part a second, marked where an alarm was recorded ("": none).
func eventParts(events []modules.Span, b, e time.Time) string {
	n := int(e.Sub(b) / time.Second)
	if n < 1 || len(events) == 0 {
		return ""
	}
	parts := []byte(strings.Repeat("0", n))
	any := false
	// the events are in order: from the first that ends after b
	for k := sort.Search(len(events), func(i int) bool { return events[i].End.After(b) }); k < len(events) && events[k].Start.Before(e); k++ {
		for i := 0; i < n; i++ {
			pb := b.Add(time.Duration(i) * time.Second)
			if events[k].Start.Before(pb.Add(time.Second)) && events[k].End.After(pb) {
				parts[i] = '1'
				any = true
			}
		}
	}
	if !any {
		return ""
	}
	return string(parts)
}

func remoteChunk(f string) (from, to time.Time, ok bool) {
	a, b, cut := strings.Cut(f, "-")
	x, e1 := strconv.ParseInt(a, 10, 64)
	y, e2 := strconv.ParseInt(b, 10, 64)
	if !cut || e1 != nil || e2 != nil || x <= 0 || y <= x || y-x > 600 {
		return time.Time{}, time.Time{}, false
	}
	return time.Unix(x, 0), time.Unix(y, 0), true
}

// the files of a remote camera by month: one search of a month serves the days of the
// timeline and the calendar; a month that is going on is asked again after a minute
var remoteMonths = struct {
	sync.Mutex
	m map[string]remoteMonth
}{m: map[string]remoteMonth{}}

type remoteMonth struct {
	at    time.Time
	files []modules.RecFile
}

func monthFiles(ctx context.Context, store *config.Store, nvr, cam string, month time.Time) ([]modules.RecFile, error) {
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 1, 0)
	key := nvr + "|" + cam + "|" + from.Format("2006-01")
	keep := 10 * time.Minute
	if time.Now().Before(to.Add(time.Hour)) {
		keep = time.Minute
	}
	remoteMonths.Lock()
	m, ok := remoteMonths.m[key]
	remoteMonths.Unlock()
	if ok && time.Since(m.at) < keep {
		return m.files, nil
	}
	files, err := modules.PlayFiles(ctx, store, nvr, cam, from, to)
	if err != nil {
		return nil, err
	}
	remoteMonths.Lock()
	if len(remoteMonths.m) > 500 {
		remoteMonths.m = map[string]remoteMonth{}
	}
	remoteMonths.m[key] = remoteMonth{at: time.Now(), files: files}
	remoteMonths.Unlock()
	return files, nil
}

// A recorder searched by day (Tiandy): the files of each day, and the days of the
// calendar from its calendar of the month, kept like the months above.
type remoteDay struct {
	at    time.Time
	files []modules.RecFile
	days  []string
}

var remoteDays = struct {
	sync.Mutex
	m map[string]remoteDay
}{m: map[string]remoteDay{}}

// cachedDay keeps what fill tells of [from, to) for a while: longer when it is over.
func cachedDay(key string, to time.Time, fill func() (remoteDay, error)) (remoteDay, error) {
	keep := 10 * time.Minute
	if time.Now().Before(to.Add(time.Hour)) {
		keep = time.Minute
	}
	remoteDays.Lock()
	d, ok := remoteDays.m[key]
	remoteDays.Unlock()
	if ok && time.Since(d.at) < keep {
		return d, nil
	}
	d, err := fill()
	if err != nil {
		return d, err
	}
	d.at = time.Now()
	remoteDays.Lock()
	if len(remoteDays.m) > 2000 {
		remoteDays.m = map[string]remoteDay{}
	}
	remoteDays.m[key] = d
	remoteDays.Unlock()
	return d, nil
}

// dayFiles: the files of the days of [from, to) and the recorded days of their months.
func dayFiles(ctx context.Context, store *config.Store, nvr, cam string, from, to time.Time) ([]modules.RecFile, []string, error) {
	var files []modules.RecFile
	var days []string
	seen := map[string]bool{}
	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.Local); m.Before(to); m = m.AddDate(0, 1, 0) {
		d, err := cachedDay(nvr+"|"+cam+"|month|"+m.Format("2006-01"), m.AddDate(0, 1, 0), func() (remoteDay, error) {
			ds, err := modules.PlayDays(ctx, store, nvr, cam, m)
			return remoteDay{days: ds}, err
		})
		if err != nil {
			return nil, nil, err
		}
		days = append(days, d.days...)
	}
	recorded := map[string]bool{}
	for _, d := range days {
		recorded[d] = true
	}
	for day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local); day.Before(to); day = day.AddDate(0, 0, 1) {
		if !recorded[day.Format("2006-01-02")] {
			continue // the calendar says nothing was recorded: not searched
		}
		end := day.AddDate(0, 0, 1)
		d, err := cachedDay(nvr+"|"+cam+"|day|"+day.Format("2006-01-02"), end, func() (remoteDay, error) {
			fs, err := modules.PlayFiles(ctx, store, nvr, cam, day, end)
			return remoteDay{files: fs}, err
		})
		if err != nil {
			return nil, nil, err
		}
		for _, f := range d.files {
			if k := f.Start.String() + f.End.String(); !seen[k] {
				seen[k] = true
				files = append(files, f)
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Start.Before(files[j].Start) })
	return files, days, nil
}

// remoteFiles: the files of the months of [from, to), without repeats.
func remoteFiles(ctx context.Context, store *config.Store, nvr, cam string, from, to time.Time) ([]modules.RecFile, error) {
	var out []modules.RecFile
	seen := map[string]bool{}
	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.Local); m.Before(to); m = m.AddDate(0, 1, 0) {
		fs, err := monthFiles(ctx, store, nvr, cam, m)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			if k := f.Start.String() + f.End.String(); !seen[k] {
				seen[k] = true
				out = append(out, f)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// GET /archive/segments?n=&c=&from=&to= (unix): the segments of a local camera
// overlapping the range, with their motion bars when the worker has made them, and
// the days that have recordings (the calendar):
// {"segs":[{"b":start,"e":end,"f":"name","m":"0101…"}],"days":["2026-11-03",…]}
func (s *Server) archiveSegments(w http.ResponseWriter, r *http.Request) {
	store, _, name, ok := s.archiveRequest(w, r, modules.PermArchivePlay)
	if !ok {
		return
	}
	from, e1 := strconv.ParseInt(r.FormValue("from"), 10, 64)
	to, e2 := strconv.ParseInt(r.FormValue("to"), 10, 64)
	if e1 != nil || e2 != nil || to <= from {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: "from and to: unix times, from < to"})
		return
	}
	f, tt := time.Unix(from, 0), time.Unix(to, 0)
	if tt.Sub(f) > segmentsSpan {
		tt = f.Add(segmentsSpan)
	}
	cam := r.FormValue("c")
	local := isLocal(store, name)
	var all []modules.RecFile
	var calendar []string
	var err error
	if local {
		all, err = modules.LocalFiles(store, name, cam, time.Unix(0, 0), time.Now().Add(24*time.Hour))
	} else if modules.Playable(store, name) && modules.SearchedByDay(store, name) {
		// the days of the range, the calendar of their months
		all, calendar, err = dayFiles(r.Context(), store, name, cam, f, tt)
	} else if modules.Playable(store, name) {
		// the month(s) of the range: the days of the calendar come with them
		all, err = remoteFiles(r.Context(), store, name, cam, f.Add(-time.Hour), tt) // a file of an hour begun before
	} else {
		err = fmt.Errorf("the recordings of %s are not played in the panel", name)
	}
	if err != nil {
		code := http.StatusBadRequest
		if !local && modules.Playable(store, name) {
			code = http.StatusBadGateway // the recorder did not answer
		}
		writeJSON(w, code, jsonResult{Error: err.Error()})
		return
	}
	type seg struct {
		B int64  `json:"b"`
		E int64  `json:"e"`
		F string `json:"f"`
		M string `json:"m,omitempty"`
	}
	out := struct {
		Segs   []seg    `json:"segs"`
		Days   []string `json:"days"`
		Remote bool     `json:"remote,omitempty"` // pieces of a remote recorder: no download of files
	}{Segs: []seg{}, Days: []string{}, Remote: !local}
	var motion []modules.Span // local: the motion of the camera, from the event database only
	if local {
		if motion, err = modules.MotionSpans(r.Context(), store, s.opt.Root, name, cam, f, tt); err != nil {
			s.opt.Log.Printf("web: motion of %s/%s: %v", name, cam, err)
		}
	}
	days := map[string]bool{}
	for _, d := range calendar {
		days[d] = true
	}
	for _, x := range all {
		days[x.Start.Format("2006-01-02")] = true
		days[x.End.Add(-time.Second).Format("2006-01-02")] = true
		if !x.End.After(f) || !x.Start.Before(tt) {
			continue
		}
		if !local { // pieces of remotePiece, on the clock's half minutes
			b := x.Start
			if b.Before(f) {
				b = f.Add(-f.Sub(time.Unix(0, 0)) % remotePiece)
				if b.Before(x.Start) {
					b = x.Start
				}
			}
			for b.Before(x.End) && b.Before(tt) {
				e := b.Truncate(remotePiece).Add(remotePiece)
				if e.After(x.End) {
					e = x.End
				}
				if e.Sub(b) >= time.Second {
					out.Segs = append(out.Segs, seg{B: b.Unix(), E: e.Unix(), F: fmt.Sprintf("%d-%d", b.Unix(), e.Unix()), M: eventParts(x.Events, b, e)})
				}
				b = e
			}
			continue
		}
		out.Segs = append(out.Segs, seg{B: x.Start.Unix(), E: x.End.Unix(), F: x.Name, M: eventParts(motion, x.Start, x.End)})
	}
	for d := range days {
		out.Days = append(out.Days, d)
	}
	sort.Strings(out.Days)
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, out)
}

// POST /archive/export: csrf, from and to (unix), storage, path, layout, percamera or
// block (block_custom), cam (recorder|camera, many): the range of the player's cameras
// as /nvr/download jobs, cameras of several recorders at once. Answers
// {"ok":true,"queued":n} or {"error":…,"queued":n}.
func (s *Server) archiveExport(w http.ResponseWriter, r *http.Request) {
	type answer struct {
		OK     bool   `json:"ok"`
		Queued int    `json:"queued"`
		Error  string `json:"error,omitempty"`
	}
	se := s.current(r)
	if se == nil {
		writeJSON(w, http.StatusUnauthorized, answer{Error: "not signed in"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(r.PostFormValue("csrf"))) != 1 {
		writeJSON(w, http.StatusForbidden, answer{Error: "forbidden"})
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, answer{Error: "configuration error"})
		return
	}
	if !can(store, se, modules.PermArchiveExp) {
		s.opt.Log.Printf("web: %s: no right %s", se.user, modules.PermArchiveExp)
		writeJSON(w, http.StatusForbidden, answer{Error: "the right " + modules.PermArchiveExp + " is missing"})
		return
	}
	t := texts[lang(r)]
	from, e1 := strconv.ParseInt(r.PostFormValue("from"), 10, 64)
	to, e2 := strconv.ParseInt(r.PostFormValue("to"), 10, 64)
	if e1 != nil || e2 != nil || to <= from {
		writeJSON(w, http.StatusBadRequest, answer{Error: t["ar_bad_range"]})
		return
	}
	storage := r.PostFormValue("storage")
	if !s.checkStorage(store, se.user, storage) {
		writeJSON(w, http.StatusBadRequest, answer{Error: t["ar_no_storage"]})
		return
	}
	if len(r.PostForm["cam"]) == 0 {
		writeJSON(w, http.StatusBadRequest, answer{Error: t["ar_no_cameras"]})
		return
	}
	block := ""
	if r.PostFormValue("percamera") == "" {
		block = r.PostFormValue("block")
		if block == "custom" {
			block = strings.TrimSpace(r.PostFormValue("block_custom"))
		}
	}
	n, errs, seen := 0, []string{}, map[string]bool{}
	for _, v := range r.PostForm["cam"] {
		nvr, cam, ok := strings.Cut(v, "|")
		if !ok || seen[v] {
			continue
		}
		seen[v] = true
		if !modules.CanAccess(store, se.user, nvr) {
			errs = append(errs, fmt.Sprintf("%s: no access", nvr))
			continue
		}
		params := rangeParams(nvr, cam, storage, time.Unix(from, 0), time.Unix(to, 0), se.user, r.PostFormValue("path"), r.PostFormValue("layout"), block)
		if _, err := queue(store, params); err != nil {
			errs = append(errs, nvr+" "+cam+": "+strings.TrimPrefix(err.Error(), "/nvr/download: "))
			continue
		}
		n++
	}
	s.opt.Log.Printf("web: %s queued %d range jobs from the player", se.user, n)
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, answer{Queued: n, Error: strings.Join(errs, "; ")})
		return
	}
	writeJSON(w, http.StatusOK, answer{OK: true, Queued: n})
}
