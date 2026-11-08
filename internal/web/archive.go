package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/jobs"
	"ffnvr/internal/modules"
	"ffnvr/internal/script"
)

// The Archive module: securing recordings of a recorder into an archive storage, by
// range (one file per camera, or in blocks) or by picking recorded files. Everything
// becomes /nvr/download jobs; the queue shows them.

const inputLayout = "2006-01-02T15:04" // <input type="datetime-local">

type camOption struct {
	Name, Label, Ch string
	Checked         bool
}

type fileRow struct {
	Camera, CameraLabel, Name  string
	Start, End, Duration, Size string
	Value                      string // camera|start|end (unix)
	Begin                      int64  // the start (unix), for the player's clock
	Local                      bool
	Motion                     string // the motion bar (tiles view), "" while it is made
	Archive                    string // a camera's page: the file in the Archive (its player or search)
}

// tileOpts: what the tiles of files show besides the file (the Archive's search, a
// camera's page): the recorder, the T of the page and the actions the rights allow.
type tileOpts struct {
	T                      map[string]string
	Nvr                    string
	Export, Download, Play bool
}

// ArchiveTiles: the tiles of the Archive's search.
func (p systemPage) ArchiveTiles() tileOpts {
	a := p.Archive
	return tileOpts{T: p.T, Nvr: a.Nvr.Name, Export: a.Export, Download: a.Download, Play: a.Play}
}

// camRecView: the latest recordings of a camera on its page.
type camRecView struct {
	Files          []fileRow // newest first
	Tiles          tileOpts
	Player, Search string // the Archive: its player on the camera, its search (tiles)
	Err            string
}

// camRecentFiles: how many recordings a camera's page shows
const camRecentFiles = 8

// playerLink: the Archive's player on a camera at a moment.
func playerLink(nvr, camera string, at time.Time) string {
	return "/?" + url.Values{"p": {"archive"}, "n": {nvr}, "tab": {"play"}, "c": {camera}, "t": {strconv.FormatInt(at.Unix(), 10)}}.Encode()
}

// searchLink: the Archive's search of a camera's files in [from, to), as tiles.
func searchLink(nvr, camera string, from, to time.Time) string {
	return "/?" + url.Values{"p": {"archive"}, "n": {nvr}, "tab": {"search"}, "search": {"1"}, "c": {camera},
		"from": {from.Format(inputLayout)}, "to": {to.Format(inputLayout)}, "view": {"tiles"}}.Encode()
}

// camRecent: the latest recordings of a camera of a local recorder for its page, as the
// Archive's tiles with links into the Archive (nil: no right archive).
func (s *Server) camRecent(ctx context.Context, p *systemPage, store *config.Store, nvr string, cam *camView) *camRecView {
	if !p.Can[modules.PermArchive] {
		return nil
	}
	v := &camRecView{Tiles: tileOpts{T: p.T, Nvr: nvr, Download: p.Can[modules.PermArchiveDown], Play: p.Can[modules.PermArchivePlay]}}
	now := time.Now()
	files, err := modules.FindFiles(ctx, store, nvr, cam.Name, time.Time{}, now.Add(time.Hour))
	if err != nil {
		v.Err = err.Error()
	}
	if len(files) > camRecentFiles {
		files = files[len(files)-camRecentFiles:]
	}
	// the search: the last day, or back to the oldest file shown
	to := now.Truncate(time.Minute).Add(time.Minute)
	from := to.Add(-24 * time.Hour)
	if len(files) > 0 && files[0].Start.Before(from) {
		from = files[0].Start.Truncate(time.Minute)
	}
	v.Search = searchLink(nvr, cam.Name, from, to)
	if v.Tiles.Play {
		v.Player = "/?" + url.Values{"p": {"archive"}, "n": {nvr}, "tab": {"play"}, "c": {cam.Name}}.Encode()
	}
	label := or(cam.Alias, cam.Name)
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		r := fileRow{Camera: cam.Name, CameraLabel: label, Name: f.Name, Start: f.Start.Format("2006-01-02 15:04:05"),
			End: f.End.Format("15:04:05"), Duration: humanDur(f.End.Sub(f.Start)), Size: "—", Local: true,
			Value: fmt.Sprintf("%s|%d|%d", cam.Name, f.Start.Unix(), f.End.Unix()), Begin: f.Start.Unix()}
		if f.Size > 0 {
			r.Size = human(uint64(f.Size))
		}
		if v.Tiles.Play {
			r.Archive = playerLink(nvr, cam.Name, f.Start)
		} else {
			r.Archive = searchLink(nvr, cam.Name, f.Start.Add(-30*time.Minute).Truncate(time.Minute), f.End.Add(30*time.Minute).Truncate(time.Minute))
		}
		v.Files = append(v.Files, r)
	}
	s.fillMotion(ctx, store, nvr, map[string][]modules.RecFile{cam.Name: files}, v.Files)
	return v
}

// fillMotion puts the motion bars on the files (rows) of a local recorder from the event
// database; what the detector has yet to look at is asked of the worker.
func (s *Server) fillMotion(ctx context.Context, store *config.Store, nvr string, found map[string][]modules.RecFile, rows []fileRow) {
	bars := map[string]string{}
	for cam, files := range found {
		known, pending, err := modules.MotionBars(ctx, store, s.opt.Root, nvr, cam, files)
		if err != nil {
			s.opt.Log.Printf("web: motion of %s/%s: %v", nvr, cam, err)
		}
		for name, m := range known {
			bars[cam+"/"+name] = m.Parts
		}
		for _, name := range pending {
			modules.RequestMotion(s.opt.Root, nvr, cam, name)
		}
	}
	for i := range rows {
		rows[i].Motion = bars[rows[i].Camera+"/"+rows[i].Name]
	}
}

type archiveView struct {
	Nvr           *nvrView
	Tab           string
	Cameras       []camOption
	Selected      int
	From, To      string
	Storages      []string
	Storage, Path string
	Layout        string // layout= of the jobs: modules.LayoutNvrCamera (default) or LayoutFlat
	Block         string
	Files         []fileRow
	Searched      bool
	SearchErr     string
	Jobs          jobTable
	Blocks        []string
	// a local recorder: how far its recordings reach (for the date pickers)
	RecFrom, RecTo, RecText string
	Tiles                   bool   // the files as pictures (local recorders)
	Export, Download, Play  bool   // rights: archive.export, archive.download, archive.play
	ViewList, ViewTiles     string // links switching between list and tiles
	// the player (tab=play): the local recorders and their cameras on the left, its data
	PlayNvrs   []liveNvr
	PlayRemote []liveNvr // the remote recorders that play (Dahua, Tiandy, Uniview, DVRIP)
	PlayExt    []nvrView // the other external recorders: links to their search
	PlayData   template.JS
	PlayHref   string // the player's tab: this recorder's, or the first local one's for an external recorder
	Active     int    // jobs waiting or running (the count on the queue's tab)
}

var blockChoices = []string{"15m", "30m", "1h", "2h", "3h", "6h", "12h", "1d"}

func parseInput(v string) (time.Time, bool) {
	t, err := time.ParseInLocation(inputLayout, strings.TrimSpace(v), time.Local)
	if err != nil {
		t, err = time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(v), time.Local)
	}
	return t, err == nil
}

func humanDur(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute && int(d.Seconds())%60 == 0:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d >= time.Minute:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}

// archivePage fills the Archive module of the System layout.
func (s *Server) archivePage(ctx context.Context, p *systemPage, store *config.Store, q url.Values, allJobs []jobView) {
	t := p.T
	p.Module, p.Crumb = "archive", t["archive"]
	p.Title = t["archive"]
	var nvr *nvrView
	for i := range p.Sys.Nvrs {
		if p.Sys.Nvrs[i].Name == q.Get("n") || (q.Get("n") == "" && nvr == nil) {
			nvr = &p.Sys.Nvrs[i]
		}
	}
	if nvr == nil {
		return // no recorder: the page says so
	}
	a := &archiveView{Nvr: nvr, Tab: q.Get("tab"), Blocks: blockChoices, Path: q.Get("path"), Layout: or(q.Get("layout"), modules.LayoutNvrCamera), Block: or(q.Get("block"), "1h"),
		Export: p.Can[modules.PermArchiveExp], Download: p.Can[modules.PermArchiveDown], Play: p.Can[modules.PermArchivePlay]}
	if a.Play {
		for _, n := range p.Sys.Nvrs {
			if playableView(n) && (a.PlayHref == "" || n.Name == nvr.Name) {
				a.PlayHref = "/?p=archive&n=" + url.QueryEscape(n.Name) + "&tab=play"
			}
		}
	}
	switch canPlay := playableView(*nvr) && a.Play; {
	case a.Tab == "play" && canPlay, a.Tab == "range" && a.Export, a.Tab == "search", a.Tab == "queue":
	case canPlay: // a local recorder opens on its player
		a.Tab = "play"
	default:
		a.Tab = "search"
	}
	p.Title, p.Crumb = nvr.Name, t["archive"]+" › "+t["ar_external"]
	if nvr.Local {
		p.Crumb = t["archive"] + " › " + t["ar_local"]
	}
	from, okF := parseInput(q.Get("from"))
	to, okT := parseInput(q.Get("to"))
	if !okF || !okT || !to.After(from) {
		to = time.Now().Truncate(time.Minute)
		from = to.Add(-time.Hour)
	}
	a.From, a.To = from.Format(inputLayout), to.Format(inputLayout)
	if nvr.Local {
		if rec := modules.Recordings(store, nvr.Name); rec.Files > 0 {
			end := rec.Newest.Add(15 * time.Minute)
			a.RecFrom, a.RecTo = rec.Oldest.Format(inputLayout), end.Format(inputLayout)
			a.RecText = fmt.Sprintf(t["ar_reach"], rec.Oldest.Format("2006-01-02 15:04"), rec.Newest.Format("2006-01-02 15:04"))
		}
	}
	chosen := map[string]bool{}
	for _, c := range q["c"] {
		chosen[c] = true
	}
	for _, c := range store.Items("/camera") {
		if c.Val("nvr") != nvr.Name {
			continue
		}
		o := camOption{Name: c.Val("name"), Label: label(c), Ch: c.Val("channel"), Checked: chosen[c.Val("name")]}
		if o.Ch != "" {
			o.Ch = "CH" + o.Ch
		}
		if o.Checked {
			a.Selected++
		}
		a.Cameras = append(a.Cameras, o)
	}
	a.Storages = modules.ArchiveStorages(store, p.User)
	a.Storage = q.Get("storage")
	if a.Tab == "play" {
		s.playbackPage(p, store, a, q)
	}
	if a.Tab == "search" && q.Get("search") != "" {
		a.Searched = true
		var errs []string
		found := map[string][]modules.RecFile{} // camera: its files (the motion bars of tiles)
		for _, o := range a.Cameras {
			if !o.Checked {
				continue
			}
			files, err := modules.FindFiles(ctx, store, nvr.Name, o.Name, from, to)
			if err != nil {
				errs = append(errs, o.Label+": "+err.Error())
			}
			found[o.Name] = files
			for _, f := range files {
				r := fileRow{Camera: o.Name, CameraLabel: o.Label, Name: f.Name, Start: f.Start.Format("2006-01-02 15:04:05"),
					End: f.End.Format("15:04:05"), Duration: humanDur(f.End.Sub(f.Start)), Size: "—", Local: nvr.Local,
					Value: fmt.Sprintf("%s|%d|%d", o.Name, f.Start.Unix(), f.End.Unix()), Begin: f.Start.Unix()}
				if f.Size > 0 {
					r.Size = human(uint64(f.Size))
				}
				if !nvr.Local {
					r.Name = filepath.Base(strings.ReplaceAll(f.Name, "\\\\", "/"))
				}
				a.Files = append(a.Files, r)
			}
		}
		a.SearchErr = strings.Join(errs, "; ")
		if nvr.Local {
			a.Tiles = q.Get("view") == "tiles"
			if a.Tiles { // the bars from the event database; what the detector has to look at is asked of the worker
				s.fillMotion(ctx, store, nvr.Name, found, a.Files)
			}
			v := url.Values{}
			for k, vs := range q {
				v[k] = vs
			}
			v.Set("view", "tiles")
			a.ViewTiles = "/?" + v.Encode()
			v.Del("view")
			a.ViewList = "/?" + v.Encode()
		}
	}
	// the queue of the recorder: its running and waiting jobs and the last others
	var shown []jobView
	done := 0
	for _, j := range allJobs {
		if j.Nvr != nvr.Name {
			continue
		}
		if j.Active {
			a.Active++
			shown = append(shown, j)
		} else if done < queueHistory {
			done++
			shown = append(shown, j)
		}
	}
	if a.Tab == "queue" {
		a.Jobs = jobTable{Jobs: shown, T: t, Manage: p.Can[modules.PermArchiveJobs], Download: p.Can[modules.PermArchiveDown], CSRF: p.CSRF,
			Back: "/?p=archive&n=" + url.QueryEscape(nvr.Name) + "&tab=queue"}
		a.Jobs.Actions = a.Jobs.Manage || a.Jobs.Download
	}
	if v := q.Get("ok"); v != "" {
		p.Flash, p.FlashKind = v, "ok"
	}
	if v := q.Get("err"); v != "" {
		p.Flash, p.FlashKind = v, "err"
	}
	p.Archive = a
}

// queue makes /nvr/download jobs through the same checks as the shell.
func queue(store *config.Store, params []script.Param) (string, error) {
	return store.Exec(script.Command{Path: "/nvr/download", Verb: "add", Params: params})
}

// archiveRequest: a signed in user with the right perm and access to the recorder n.
func (s *Server) archiveRequest(w http.ResponseWriter, r *http.Request, perm string) (*config.Store, *session, string, bool) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, nil, "", false
	}
	if r.Method == http.MethodPost && r.PostFormValue("csrf") != se.csrf {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, "", false
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return nil, nil, "", false
	}
	if !can(store, se, perm) {
		s.denied(w, se, perm)
		return nil, nil, "", false
	}
	name := r.FormValue("n")
	if !modules.CanAccess(store, se.user, name) {
		http.Redirect(w, r, "/?p=archive", http.StatusSeeOther)
		return nil, nil, "", false
	}
	return store, se, name, true
}

func back(r *http.Request, name, tab string, extra url.Values) string {
	v := url.Values{"p": {"archive"}, "n": {name}, "tab": {tab}}
	for k, vs := range extra {
		v[k] = vs
	}
	return "/?" + v.Encode()
}

func (s *Server) checkStorage(store *config.Store, user, storage string) bool {
	for _, st := range modules.ArchiveStorages(store, user) {
		if st == storage {
			return true
		}
	}
	return false
}

// queueHistory: the finished jobs the queue shows besides the running ones
const queueHistory = 50

// rangeParams: the /nvr/download job of a range of one camera (block "": one file).
func rangeParams(nvr, cam, storage string, from, to time.Time, user, path, layout, block string) []script.Param {
	params := []script.Param{{Key: "nvr", Value: nvr}, {Key: "camera", Value: cam}, {Key: "storage", Value: storage},
		{Key: "start", Value: from.Format(jobs.TimeFormat)}, {Key: "end", Value: to.Format(jobs.TimeFormat)},
		{Key: "comment", Value: "web: " + user}}
	if p := strings.Trim(path, "/ "); p != "" {
		params = append(params, script.Param{Key: "path", Value: p})
	}
	if layout == modules.LayoutFlat || layout == modules.LayoutNvrCamera {
		params = append(params, script.Param{Key: "layout", Value: layout})
	}
	if block != "" {
		params = append(params, script.Param{Key: "block", Value: block})
	}
	return params
}

// POST /archive/range: a range of the chosen cameras, one file per camera or in blocks.
func (s *Server) archiveRange(w http.ResponseWriter, r *http.Request) {
	store, se, name, ok := s.archiveRequest(w, r, modules.PermArchiveExp)
	if !ok {
		return
	}
	t := texts[lang(r)]
	keep := url.Values{"c": r.PostForm["c"], "from": {r.PostFormValue("from")}, "to": {r.PostFormValue("to")},
		"storage": {r.PostFormValue("storage")}, "path": {r.PostFormValue("path")}, "block": {r.PostFormValue("block")}, "layout": {r.PostFormValue("layout")}}
	fail := func(msg string) {
		keep.Set("err", msg)
		http.Redirect(w, r, back(r, name, "range", keep), http.StatusSeeOther)
	}
	from, ok1 := parseInput(r.PostFormValue("from"))
	to, ok2 := parseInput(r.PostFormValue("to"))
	if !ok1 || !ok2 {
		fail(t["ar_bad_range"])
		return
	}
	storage := r.PostFormValue("storage")
	if !s.checkStorage(store, se.user, storage) {
		fail(t["ar_no_storage"])
		return
	}
	if len(r.PostForm["c"]) == 0 {
		fail(t["ar_no_cameras"])
		return
	}
	block := ""
	if r.PostFormValue("percamera") == "" {
		block = r.PostFormValue("block")
		if block == "custom" {
			block = strings.TrimSpace(r.PostFormValue("block_custom"))
		}
	}
	n, errs := 0, []string{}
	for _, cam := range r.PostForm["c"] {
		params := rangeParams(name, cam, storage, from, to, se.user, r.PostFormValue("path"), r.PostFormValue("layout"), block)
		if _, err := queue(store, params); err != nil {
			errs = append(errs, cam+": "+strings.TrimPrefix(err.Error(), "/nvr/download: "))
			continue
		}
		n++
	}
	s.opt.Log.Printf("web: %s queued %d range jobs on %s", se.user, n, name)
	if len(errs) > 0 {
		keep.Set("ok", fmt.Sprintf(t["ar_queued"], n))
		fail(strings.Join(errs, "; "))
		return
	}
	keep.Set("ok", fmt.Sprintf(t["ar_queued"], n))
	http.Redirect(w, r, back(r, name, "range", keep), http.StatusSeeOther)
}

// POST /archive/files: the chosen recorded files, each a job of its exact time.
func (s *Server) archiveFiles(w http.ResponseWriter, r *http.Request) {
	store, se, name, ok := s.archiveRequest(w, r, modules.PermArchiveExp)
	if !ok {
		return
	}
	t := texts[lang(r)]
	keep := url.Values{"c": r.PostForm["c"], "from": {r.PostFormValue("from")}, "to": {r.PostFormValue("to")}, "search": {"1"},
		"storage": {r.PostFormValue("storage")}, "path": {r.PostFormValue("path")}, "layout": {r.PostFormValue("layout")}}
	storage := r.PostFormValue("storage")
	if !s.checkStorage(store, se.user, storage) {
		keep.Set("err", t["ar_no_storage"])
		http.Redirect(w, r, back(r, name, "search", keep), http.StatusSeeOther)
		return
	}
	n, errs := 0, []string{}
	for _, v := range r.PostForm["f"] {
		parts := strings.Split(v, "|")
		if len(parts) != 3 {
			continue
		}
		b, e1 := strconv.ParseInt(parts[1], 10, 64)
		e, e2 := strconv.ParseInt(parts[2], 10, 64)
		if e1 != nil || e2 != nil || e <= b {
			continue
		}
		params := []script.Param{{Key: "nvr", Value: name}, {Key: "camera", Value: parts[0]}, {Key: "storage", Value: storage},
			{Key: "start", Value: time.Unix(b, 0).Format(jobs.TimeFormat)}, {Key: "end", Value: time.Unix(e, 0).Format(jobs.TimeFormat)},
			{Key: "comment", Value: "web: " + se.user}}
		if p := strings.Trim(r.PostFormValue("path"), "/ "); p != "" {
			params = append(params, script.Param{Key: "path", Value: p})
		}
		if l := r.PostFormValue("layout"); l == modules.LayoutFlat || l == modules.LayoutNvrCamera {
			params = append(params, script.Param{Key: "layout", Value: l})
		}
		if _, err := queue(store, params); err != nil {
			errs = append(errs, strings.TrimPrefix(err.Error(), "/nvr/download: "))
			continue
		}
		n++
	}
	if n == 0 && len(errs) == 0 {
		errs = append(errs, t["ar_no_files"])
	}
	s.opt.Log.Printf("web: %s queued %d files of %s", se.user, n, name)
	keep.Set("ok", fmt.Sprintf(t["ar_queued"], n))
	if len(errs) > 0 {
		keep.Set("err", strings.Join(errs, "; "))
	}
	http.Redirect(w, r, back(r, name, "search", keep), http.StatusSeeOther)
}

// GET /archive/file: one segment of a local camera, as it is (mkv) or as mp4.
func (s *Server) archiveFile(w http.ResponseWriter, r *http.Request) {
	store, se, name, ok := s.archiveRequest(w, r, modules.PermArchiveDown)
	if !ok {
		return
	}
	path, err := modules.LocalSegment(store, name, r.FormValue("c"), r.FormValue("f"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	w = longAnswer(w)
	base := name + "_" + r.FormValue("c") + "_" + strings.TrimSuffix(filepath.Base(path), ".mkv")
	s.opt.Log.Printf("web: %s downloads %s (%s)", se.user, path, r.FormValue("fmt"))
	if r.FormValue("fmt") != "mp4" {
		st, _ := f.Stat()
		w.Header().Set("Content-Disposition", `attachment; filename="`+base+`.mkv"`)
		http.ServeContent(w, r, base+".mkv", st.ModTime(), f)
		return
	}
	// mp4, made on the fly (fragmented: it can be written while it is sent)
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="`+base+`.mp4"`)
	cmd := exec.CommandContext(r.Context(), "ffmpeg", "-hide_banner", "-loglevel", "error", "-nostdin", "-i", path,
		"-map", "0", "-c", "copy", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-f", "mp4", "pipe:1")
	cmd.Stdout = w
	if err := cmd.Run(); err != nil && r.Context().Err() == nil {
		s.opt.Log.Printf("web: mp4 of %s: %v", path, err)
	}
}

// jobFor returns a job the user may act on.
// jobFor: a job of a recorder the user may use, and the right perm.
func (s *Server) jobFor(w http.ResponseWriter, r *http.Request, perm string) (*jobs.Job, *config.Store, *session, bool) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil, nil, nil, false
	}
	if r.Method == http.MethodPost && r.PostFormValue("csrf") != se.csrf {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, nil, false
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return nil, nil, nil, false
	}
	if !can(store, se, perm) {
		s.denied(w, se, perm)
		return nil, nil, nil, false
	}
	id, _ := strconv.Atoi(r.FormValue("id"))
	j, err := s.spool().Get(id)
	if err != nil || !modules.CanAccess(store, se.user, j.Nvr) {
		http.Error(w, "no such job", http.StatusNotFound)
		return nil, nil, nil, false
	}
	return j, store, se, true
}

func safeBack(v string) string {
	if strings.HasPrefix(v, "/?") && !strings.Contains(v, "//") {
		return v
	}
	return "/?p=jobs"
}

// POST /archive/cancel
func (s *Server) archiveCancel(w http.ResponseWriter, r *http.Request) {
	j, _, se, ok := s.jobFor(w, r, modules.PermArchiveJobs)
	if !ok {
		return
	}
	s.spool().Cancel(j.ID)
	s.opt.Log.Printf("web: %s cancels job %d", se.user, j.ID)
	http.Redirect(w, r, safeBack(r.PostFormValue("back")), http.StatusSeeOther)
}

// POST /archive/retry: the job once more, as a new job.
func (s *Server) archiveRetry(w http.ResponseWriter, r *http.Request) {
	j, _, se, ok := s.jobFor(w, r, modules.PermArchiveJobs)
	if !ok {
		return
	}
	if j.State == jobs.Failed || j.State == jobs.Cancelled {
		n := *j
		n.ID, n.Progress, n.Speed, n.Error, n.Size = 0, 0, "", "", 0
		n.Started, n.Finished = time.Time{}, time.Time{}
		for i := range n.Parts {
			n.Parts[i].State, n.Parts[i].Size = "", 0
		}
		if nj, err := s.spool().Submit(n); err == nil {
			s.opt.Log.Printf("web: %s retries job %d as %d", se.user, j.ID, nj.ID)
		}
	}
	http.Redirect(w, r, safeBack(r.PostFormValue("back")), http.StatusSeeOther)
}

// GET /archive/result: the file of a finished single-file job.
func (s *Server) archiveResult(w http.ResponseWriter, r *http.Request) {
	j, _, se, ok := s.jobFor(w, r, modules.PermArchiveDown)
	if !ok {
		return
	}
	if j.State != jobs.Done || len(j.Parts) != 1 {
		http.Error(w, "no file", http.StatusNotFound)
		return
	}
	f, err := os.Open(j.Parts[0].Target)
	if err != nil {
		http.Error(w, "the file is gone", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	s.opt.Log.Printf("web: %s downloads %s", se.user, j.Parts[0].Target)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(j.Parts[0].Target)+`"`)
	http.ServeContent(longAnswer(w), r, filepath.Base(j.Parts[0].Target), st.ModTime(), f)
}

// a transparent pixel: the thumbnail is being made
var pixelGIF = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")

// GET /archive/thumb: the first picture of a local segment; asked of the worker when
// it is not there yet (the page asks again).
func (s *Server) archiveThumb(w http.ResponseWriter, r *http.Request) {
	store, _, name, ok := s.archiveRequest(w, r, modules.PermArchive)
	if !ok {
		return
	}
	c, f := r.FormValue("c"), r.FormValue("f")
	thumb := modules.ThumbPath(store, name, c, f)
	if thumb == "" {
		http.Error(w, "not a segment", http.StatusBadRequest)
		return
	}
	if b, err := os.ReadFile(thumb); err == nil {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Write(b)
		return
	}
	if err := modules.RequestThumb(s.opt.Root, name, c, f); err != nil {
		s.opt.Log.Printf("web: thumbnail: %v", err)
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("X-Ffnvr-Pending", "1")
	w.WriteHeader(http.StatusAccepted)
	w.Write(pixelGIF)
}

// GET /archive/motion: the motion bar of a local segment, {"ready":bool,"parts":"0101…"},
// from the event database; asked of the worker (the detector) when it is not there yet.
func (s *Server) archiveMotion(w http.ResponseWriter, r *http.Request) {
	store, _, name, ok := s.archiveRequest(w, r, modules.PermArchive)
	if !ok {
		return
	}
	c, f := r.FormValue("c"), r.FormValue("f")
	if _, err := modules.LocalSegment(store, name, c, f); err != nil {
		http.Error(w, "not a segment", http.StatusBadRequest)
		return
	}
	m, ready := modules.ReadMotion(r.Context(), store, s.opt.Root, name, c, f)
	if !ready {
		modules.RequestMotion(s.opt.Root, name, c, f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": ready, "parts": m.Parts, "duration": m.Duration})
}
