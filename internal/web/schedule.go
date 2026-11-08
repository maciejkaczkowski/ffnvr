package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
	"ffnvr/internal/script"
)

// The recording schedule (/?p=schedule, section Storage): the three steps of
// /record/day, /record/schedule and record-schedule= of /nvr and /camera, as in the
// shell. Everybody with system sees it; changing it needs system.record. Day templates
// and week schedules are shared by all recorders; a user assigns them only to the
// recorders of valid-users.
//
//	/?p=schedule              the overview: templates, schedules, assignment and the
//	                          state of every camera (do its alarms arrive)
//	/?p=schedule&day=<n>      the editor of a day template (day=+ a new one)
//	/?p=schedule&week=<n>     the editor of a week schedule (week=+ a new one)
//	POST /record/save         every change, back to the page with ok= or err=

// stripSeg is one coloured stretch of a 24-hour strip (percentages of the day).
type stripSeg struct {
	Left, Width string
	Mode, Title string
}

func strip(d *modules.DayTemplate) []stripSeg {
	full := []modules.DaySpan{{From: 0, To: 24 * 60, Mode: modules.RecRegular}}
	if d != nil {
		full = d.Full()
	}
	out := make([]stripSeg, 0, len(full))
	for _, s := range full {
		out = append(out, stripSeg{Left: fmt.Sprintf("%.3f", float64(s.From)/14.4), Width: fmt.Sprintf("%.3f", float64(s.To-s.From)/14.4),
			Mode: s.Mode, Title: modules.FormatDayClock(s.From) + "–" + modules.FormatDayClock(s.To)})
	}
	return out
}

type dayView struct {
	Name, Default, Spans, Comment string
	Strip                         []stripSeg
	Used                          int // week schedules naming it
}

type weekDayView struct {
	Key, Label, Template string
	Strip                []stripSeg
}

type weekView struct {
	Name, Comment             string
	Days                      []weekDayView
	Pre, Post, Silence, Alarm string
	Used                      []string
}

type schedCam struct {
	Name, Label             string
	Own                     string // record-schedule= of the camera
	Effective, From         string
	Type                    string
	Kind, Status, Note      string // the state of its alarms
	LastEvent, Today, Error string
	Details                 string // link to the camera's page
}

type schedNvr struct {
	Name, Schedule string
	Cams           []schedCam
}

type filterOpt struct {
	Name string
	On   bool
}

type dayEdit struct {
	New                                 bool
	Orig, Name, Default, Spans, Comment string
}

type weekEdit struct {
	New                         bool
	Orig, Name, Comment         string
	Days                        []weekDayView
	Pre, Post, Silence          string
	Filters                     []filterOpt
	Strips                      template.JS // template name -> strip, for the preview
	DayNames                    []string
	DefPre, DefPost, DefSilence string
}

type scheduleView struct {
	CanEdit  bool // system.record and a configuration the panel may write
	Days     []dayView
	Weeks    []weekView
	Nvrs     []schedNvr
	Names    []string // the week schedules, for the selects
	Day      *dayEdit
	Week     *weekEdit
	Modes    []string
	Weekdays []string
}

func weekdayLabels(t map[string]string) []string {
	out := make([]string, 7)
	for i, k := range modules.WeekdayKeys {
		out[i] = t["rs_d_"+k]
	}
	return out
}

func (s *Server) schedulePage(ctx context.Context, p *systemPage, store *config.Store, q url.Values) bool {
	t := p.T
	p.Title, p.Crumb = t["schedule"], t["system"]+" › "+t["storage_g"]
	if v := q.Get("ok"); v != "" {
		p.Flash, p.FlashKind = v, "ok"
	}
	if v := q.Get("err"); v != "" {
		p.Flash, p.FlashKind = v, "err"
	}
	v := &scheduleView{CanEdit: p.Can[modules.PermSystemRecord] && s.opt.Edit != nil, Modes: modules.RecModes, Weekdays: weekdayLabels(t)}
	p.Schedule = v
	templates := map[string]*modules.DayTemplate{}
	for _, it := range store.Items("/record/day") {
		d := readDay(store, it.Val("name"))
		templates[d.Name] = d
		used := 0
		for _, w := range store.Items("/record/schedule") {
			for _, k := range modules.WeekdayKeys {
				if w.Val(k) == d.Name {
					used++
					break
				}
			}
		}
		v.Days = append(v.Days, dayView{Name: d.Name, Default: d.Default, Spans: it.Val("spans"), Comment: it.Val("comment"), Strip: strip(d), Used: used})
	}
	for _, w := range store.Items("/record/schedule") {
		sc := modules.ReadSchedule(store, w.Val("name"))
		wv := weekView{Name: sc.Name, Comment: w.Val("comment"), Pre: fmtSec(sc.Pre), Post: fmtSec(sc.Post)}
		if sc.Silence == 0 {
			wv.Silence = t["rs_off"]
		} else {
			wv.Silence = fmtLen(sc.Silence)
		}
		wv.Alarm = strings.Join(append([]string{t["rs_motion"]}, sc.FilterName...), ", ")
		for i, k := range modules.WeekdayKeys {
			wv.Days = append(wv.Days, weekDayView{Key: k, Label: v.Weekdays[i], Template: w.Val(k), Strip: strip(templates[w.Val(k)])})
		}
		for _, n := range store.Items("/nvr") {
			if n.Val("record-schedule") == sc.Name {
				wv.Used = append(wv.Used, n.Val("name"))
			}
		}
		for _, c := range store.Items("/camera") {
			if c.Val("record-schedule") == sc.Name {
				wv.Used = append(wv.Used, c.Val("nvr")+"/"+c.Val("name"))
			}
		}
		v.Weeks = append(v.Weeks, wv)
		v.Names = append(v.Names, sc.Name)
	}

	switch day := q.Get("day"); {
	case day == "+":
		v.Day = &dayEdit{New: true, Default: modules.RecNone}
	case day != "":
		it := findItem(store, "/record/day", "name", day)
		if it == nil {
			return false
		}
		v.Day = &dayEdit{Orig: day, Name: day, Default: or(it.Val("default"), modules.RecNone), Spans: it.Val("spans"), Comment: it.Val("comment")}
	}
	if v.Day != nil {
		p.Title = t["rs_day_edit"]
		if v.Day.New {
			p.Title = t["rs_day_new"]
		}
		return true
	}
	switch week := q.Get("week"); {
	case week == "+":
		v.Week = &weekEdit{New: true}
	case week != "":
		it := findItem(store, "/record/schedule", "name", week)
		if it == nil {
			return false
		}
		v.Week = &weekEdit{Orig: week, Name: week, Comment: it.Val("comment"), Pre: it.Val("pre-alarm"), Post: it.Val("post-alarm"), Silence: it.Val("alarm-silence")}
	}
	if w := v.Week; w != nil {
		p.Title = t["rs_week_edit"]
		if w.New {
			p.Title = t["rs_week_new"]
		}
		it := findItem(store, "/record/schedule", "name", w.Orig)
		strips := map[string][]stripSeg{"": strip(nil)}
		for name, d := range templates {
			strips[name] = strip(d)
			w.DayNames = append(w.DayNames, name)
		}
		sortStrings(w.DayNames)
		b, _ := json.Marshal(strips)
		w.Strips = template.JS(b)
		for i, k := range modules.WeekdayKeys {
			tmpl := ""
			if it != nil {
				tmpl = it.Val(k)
			}
			w.Days = append(w.Days, weekDayView{Key: k, Label: v.Weekdays[i], Template: tmpl, Strip: strip(templates[tmpl])})
		}
		on := map[string]bool{}
		if it != nil {
			for _, f := range strings.Split(it.Val("alarm-filters"), ",") {
				on[f] = true
			}
		}
		for _, f := range store.Items("/event/filter") {
			if f.Val("action") == "accept" {
				w.Filters = append(w.Filters, filterOpt{f.Val("name"), on[f.Val("name")]})
			}
		}
		// as the fields take them
		w.DefPre, w.DefPost, w.DefSilence = "10s", "1m", "1d"
		return true
	}

	// the assignment and the state of the cameras: the local recorders of the user
	now := time.Now()
	var local []string
	for _, n := range p.Sys.Nvrs {
		if n.Local {
			local = append(local, n.Name)
		}
	}
	infos := map[string]modules.CameraScheduleInfo{}
	if len(local) > 0 {
		for _, in := range modules.ScheduleInfos(ctx, store, s.opt.Root, local, "", now) {
			infos[in.Nvr+"/"+in.Camera] = in
		}
	}
	for _, name := range local {
		nvr := findItem(store, "/nvr", "name", name)
		sn := schedNvr{Name: name, Schedule: nvr.Val("record-schedule")}
		for _, c := range store.Items("/camera") {
			if c.Val("nvr") != name {
				continue
			}
			sc := schedCam{Name: c.Val("name"), Label: or(c.Val("alias"), c.Val("name")), Own: c.Val("record-schedule"),
				Type:    modules.ResolveCamera(store, c).Type,
				Details: "/?p=cam&n=" + url.QueryEscape(name) + "&c=" + url.QueryEscape(c.Val("name")) + "&from=schedule"}
			in, ok := infos[name+"/"+c.Val("name")]
			switch {
			case !ok:
				sc.Kind, sc.Status = "mute", t["rs_st_always"]
			case !in.HasAlarm:
				sc.Kind, sc.Status = "ok", t["rs_st_no_alarm"]
			case !in.Capable:
				sc.Kind, sc.Status, sc.Note = "mute", t["rs_st_as_regular"], fmt.Sprintf(t["rs_st_type"], in.Type)
				if in.Type == "" {
					sc.Note = t["rs_st_notype"]
				}
			case in.State != nil && in.State.Fallback && !in.State.Waiting:
				sc.Kind, sc.Status, sc.Note = "err", t["rs_st_fallback"], in.State.Reason
			case !in.Trusted && !in.Waiting:
				sc.Kind, sc.Status, sc.Note = "warn", t["rs_st_no_data"], in.Reason
			case !in.Trusted || (in.State != nil && in.State.Fallback):
				// the alarm server or the subscription has just begun: nothing is wrong
				sc.Kind, sc.Status, sc.Note = "mute", t["rs_st_waiting"], t["rs_st_waiting_note"]
			default:
				sc.Kind, sc.Status = "ok", t["rs_st_ok"]
			}
			if ok {
				sc.Effective, sc.From = in.Schedule, in.From
				if in.Capable && in.HasAlarm {
					if in.LastEvent.IsZero() {
						sc.LastEvent = t["rs_never"]
					} else {
						sc.LastEvent = fmt.Sprintf(t["rs_last_event"], in.LastEvent.Format("2006-01-02 15:04"), in.Events24h)
					}
				}
				if st := in.State; st != nil && st.Day == now.Format("2006-01-02") {
					sc.Today = fmt.Sprintf(t["rs_today"], st.Kept, st.Cut, st.Deleted, human(uint64(st.Freed)))
					sc.Error = st.Error
				}
			}
			sn.Cams = append(sn.Cams, sc)
		}
		v.Nvrs = append(v.Nvrs, sn)
	}
	return true
}

func readDay(store *config.Store, name string) *modules.DayTemplate {
	it := findItem(store, "/record/day", "name", name)
	spans, _ := modules.ParseDaySpans(it.Val("spans"))
	return &modules.DayTemplate{Name: name, Default: or(it.Val("default"), modules.RecNone), Spans: spans}
}

func fmtSec(d time.Duration) string {
	if d > 0 && d%time.Minute == 0 {
		return fmt.Sprintf("%d min", d/time.Minute)
	}
	return fmt.Sprintf("%d s", d/time.Second)
}

func fmtLen(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%d d", d/(24*time.Hour))
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%d h", d/time.Hour)
	}
	return fmt.Sprintf("%d min", d/time.Minute)
}

func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// POST /record/save: action day-save, day-delete, week-save, week-delete, assign.
func (s *Server) recordSave(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.PostFormValue("csrf") != se.csrf {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, modules.PermSystemRecord) {
		s.denied(w, se, modules.PermSystemRecord)
		return
	}
	t := texts[lang(r)]
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	back := "/?p=schedule"
	answer := func(kind, msg, to string) {
		http.Redirect(w, r, to+"&"+kind+"="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if s.opt.Edit == nil {
		answer("err", t["rs_readonly"], back)
		return
	}
	// one item of a menu: add (orig "") or set, the empty values unset
	save := func(st *config.Store, path, orig string, vals [][2]string) error {
		var set []script.Param
		var unset []string
		for _, kv := range vals {
			if kv[1] != "" {
				set = append(set, p(kv[0], kv[1]))
			} else if orig != "" {
				unset = append(unset, kv[0])
			}
		}
		if orig == "" {
			return run(st, path, "add", nil, set)
		}
		find := []script.Param{p("name", orig)}
		if findItem(st, path, "name", orig) == nil {
			return fmt.Errorf("%s: no %s", path, orig)
		}
		if len(unset) > 0 {
			if _, err := st.Exec(script.Command{Path: path, Verb: "unset", Find: find, HasFind: true, Args: unset}); err != nil {
				return err
			}
		}
		return run(st, path, "set", find, set)
	}
	var logText, nvr, done string
	action := f("action")
	switch action {
	case "day-save":
		orig, name := f("orig"), f("name")
		back = "/?p=schedule&day=" + url.QueryEscape(or(orig, "+"))
		err = s.opt.Edit(func(st *config.Store) error {
			return save(st, "/record/day", orig, [][2]string{{"name", name}, {"default", f("default")}, {"spans", f("spans")}, {"comment", f("comment")}})
		})
		logText = fmt.Sprintf("/record/day %s: default=%s spans=%q", name, f("default"), f("spans"))
		done = fmt.Sprintf(t["rs_saved"], name)
		if err == nil {
			back = "/?p=schedule&day=" + url.QueryEscape(name)
		}
	case "week-save":
		orig, name := f("orig"), f("name")
		back = "/?p=schedule&week=" + url.QueryEscape(or(orig, "+"))
		vals := [][2]string{{"name", name}}
		for _, k := range modules.WeekdayKeys {
			vals = append(vals, [2]string{k, f(k)})
		}
		filters := strings.Join(r.PostForm["alarm-filters"], ",")
		vals = append(vals, [2]string{"pre-alarm", f("pre-alarm")}, [2]string{"post-alarm", f("post-alarm")},
			[2]string{"alarm-silence", f("alarm-silence")}, [2]string{"alarm-filters", filters}, [2]string{"comment", f("comment")})
		err = s.opt.Edit(func(st *config.Store) error { return save(st, "/record/schedule", orig, vals) })
		logText = fmt.Sprintf("/record/schedule %s saved", name)
		done = fmt.Sprintf(t["rs_saved"], name)
		if err == nil {
			back = "/?p=schedule&week=" + url.QueryEscape(name)
		}
	case "day-delete", "week-delete":
		path := map[string]string{"day-delete": "/record/day", "week-delete": "/record/schedule"}[action]
		name := f("name")
		err = s.opt.Edit(func(st *config.Store) error {
			if findItem(st, path, "name", name) == nil {
				return fmt.Errorf("%s: no %s", path, name)
			}
			return run(st, path, "remove", []script.Param{p("name", name)}, nil)
		})
		logText = path + " " + name + " removed"
		done = fmt.Sprintf(t["rs_deleted"], name)
	case "assign":
		nvr = f("nvr")
		err = s.opt.Edit(func(st *config.Store) error {
			n := findItem(st, "/nvr", "name", nvr)
			if n == nil || !modules.CanAccess(st, se.user, nvr) || n.Val("type") != "local" {
				return fmt.Errorf("no access to %s", nvr)
			}
			assign := func(path string, find []script.Param, val string) error {
				if val == "" {
					_, err := st.Exec(script.Command{Path: path, Verb: "unset", Find: find, HasFind: true, Args: []string{"record-schedule"}})
					return err
				}
				return run(st, path, "set", find, []script.Param{p("record-schedule", val)})
			}
			if err := assign("/nvr", []script.Param{p("name", nvr)}, f("schedule")); err != nil {
				return err
			}
			for _, c := range st.Items("/camera") {
				if c.Val("nvr") != nvr {
					continue
				}
				val, given := r.PostForm["cam_"+c.Val("name")]
				if !given || (len(val) > 0 && strings.TrimSpace(val[0]) == c.Val("record-schedule")) {
					continue
				}
				if err := assign("/camera", []script.Param{p("nvr", nvr), p("name", c.Val("name"))}, strings.TrimSpace(val[0])); err != nil {
					return err
				}
			}
			return nil
		})
		logText = fmt.Sprintf("/nvr %s: record-schedule=%s and of its cameras", nvr, or(f("schedule"), "(none)"))
		done = fmt.Sprintf(t["rs_assigned"], nvr)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.opt.Log.Printf("web: %s: recording schedule: %s: %v", se.user, action, err)
		answer("err", err.Error(), back)
		return
	}
	s.opt.Log.Printf("web: %s: %s", se.user, logText)
	s.syslog(eventdb.Entry{Kind: modules.SysConfigChanged, Nvr: nvr, User: se.user, Address: s.clientIP(r).String(), Text: logText})
	if strings.HasSuffix(action, "-delete") || action == "assign" {
		back = "/?p=schedule"
	}
	answer("ok", done, back)
}
