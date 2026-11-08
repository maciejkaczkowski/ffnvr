package web

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
)

// The event log (System › Events & alerts › Event log): what the alarm server received,
// newest first, with filters, paging and acknowledging. A user sees the events of the
// recorders valid-users= gives them; events of unknown senders only who sees every
// recorder.

type eventRow struct {
	ID                        int64
	When, Length, Nvr, Camera string
	Kind, Label, Source, Text string
	Who, Acked                string // Who: system log, user@address
	Count                     int
	Open, Unknown, Picture    bool
	System                    bool
	Archive                   string // a camera's page: the archive's player at the event
}

type eventsView struct {
	More, MoreUnacked  string // a recorder\'s page: the links to the event log
	Rows               []eventRow
	Nvrs, Kinds, Users []string
	Nvr, Kind, Day     string
	Camera             string // the events of one camera (its page, or camera= of the log)
	Origin, User, Text string
	Unacked, CanSystem bool
	Total, Unread      int
	Next, Query        string // the link of the next page; the filters as a query
	CanAck             bool
	State              string // the alarm server's state, one line
	Missing            bool   // no database yet
	Archive            bool   // the rows link to the archive's player
}

// eventFilter: the filters of the page as a database filter, limited to the user's
// recorders.
// sys: the user may see the system log about no recorder (system.events.system).
func eventFilter(store *config.Store, user string, sys bool, q url.Values) (eventdb.Filter, []string) {
	var mine []string
	all := true
	for _, n := range store.Items("/nvr") {
		if modules.CanAccess(store, user, n.Val("name")) {
			mine = append(mine, n.Val("name"))
		} else {
			all = false
		}
	}
	f := eventdb.Filter{Nvrs: mine, Unknown: all, System: sys, Kind: q.Get("kind"), Unacked: q.Get("unacked") == "1", Camera: q.Get("camera"),
		User: q.Get("user"), Text: strings.TrimSpace(q.Get("text"))}
	if o := q.Get("origin"); o == "recorders" || o == "system" {
		f.Origin = o
	}
	if f.Nvrs == nil {
		f.Nvrs = []string{}
	}
	if n := q.Get("nvr"); n != "" {
		f.Nvr = n
		if n == "?" { // the unknown senders
			f.Nvr, f.Nvrs, f.System, f.Origin = "", []string{}, false, "recorders"
			if !all {
				f.Unknown = false
			}
		}
		if n == "-" { // the system log about no recorder
			f.Nvr, f.Nvrs, f.Unknown, f.Origin = "", []string{}, false, "system"
		}
	}
	if d, err := time.ParseInLocation("2006-01-02", q.Get("day"), time.Local); err == nil {
		f.From, f.To = d, d.AddDate(0, 0, 1)
	}
	if b, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil {
		f.Before = b
	}
	f.Limit = 100
	return f, mine
}

func (s *Server) eventsPage(ctx context.Context, p *systemPage, store *config.Store, q url.Values) {
	v := &eventsView{Nvr: q.Get("nvr"), Camera: q.Get("camera"), Kind: q.Get("kind"), Day: q.Get("day"), Unacked: q.Get("unacked") == "1",
		Origin: q.Get("origin"), User: q.Get("user"), Text: q.Get("text"),
		CanAck: p.Can[modules.PermSystemEventsAck], CanSystem: p.Can[modules.PermSystemEventsSystem]}
	p.Events = v
	v.State = modules.AlarmStateText(s.opt.Root)
	db, ok, err := eventdb.OpenIfExists(eventdb.Path(s.opt.Root))
	if err != nil {
		p.Flash, p.FlashKind = err.Error(), "err"
		return
	}
	if !ok {
		v.Missing = true
		return
	}
	defer db.Close()
	f, mine := eventFilter(store, p.User, v.CanSystem, q)
	v.Nvrs = mine
	v.Kinds, _ = db.Kinds(ctx, f)
	if v.CanSystem {
		v.Users, _ = db.Users(ctx, f)
	}
	evs, err := db.Query(ctx, f)
	if err != nil {
		p.Flash, p.FlashKind = err.Error(), "err"
		return
	}
	v.Total, _ = db.Count(ctx, f)
	uf := f
	uf.Unacked = true
	v.Unread, _ = db.Count(ctx, uf)
	v.Rows = eventRows(evs, p.T)
	qs := url.Values{}
	for _, k := range []string{"nvr", "camera", "kind", "day", "unacked", "origin", "user", "text"} {
		if q.Get(k) != "" {
			qs.Set(k, q.Get(k))
		}
	}
	v.Query = qs.Encode()
	if len(evs) == f.Limit {
		qs.Set("before", strconv.FormatInt(evs[len(evs)-1].ID, 10))
		v.Next = "/?p=event_log&" + qs.Encode()
	}
}

// eventRows: the events as the tables show them.
func eventRows(evs []eventdb.Event, t map[string]string) []eventRow {
	var rows []eventRow
	for _, e := range evs {
		r := eventRow{ID: e.ID, When: e.Started.Format("2006-01-02 15:04:05"), Nvr: e.Nvr, Camera: e.Camera, Kind: e.Kind,
			Label: e.Kind, Source: e.Source, Text: e.Text, Count: e.Count, Open: e.Open(), Picture: e.Picture != "",
			Unknown: e.Nvr == "" && !e.System(), System: e.System()}
		if l := t["sk_"+e.Kind]; l != "" && e.System() {
			r.Label = l
		}
		switch {
		case e.Open():
			r.Length = t["ev_going"]
		case e.Ended.Equal(e.Started):
			r.Length = "—"
		default:
			r.Length = e.Ended.Sub(e.Started).Round(time.Second).String()
		}
		if r.Camera == "" && e.Channel > 0 {
			r.Camera = "CH" + strconv.Itoa(e.Channel)
		}
		if r.Unknown {
			r.Nvr = "?"
			r.Text = strings.TrimSpace(e.Device + " " + r.Text)
		}
		if r.System {
			r.Source = t["ev_system"]
			r.Who = e.User
			if e.Device != "" {
				r.Who = strings.TrimPrefix(r.Who+" @ "+e.Device, " ")
			}
		}
		if !e.AckedAt.IsZero() {
			r.Acked = e.AckedBy + " · " + e.AckedAt.Format("2006-01-02 15:04")
		}
		rows = append(rows, r)
	}
	return rows
}

// nvrEvents: the latest events of a recorder for its page, or of one of its cameras
// (camera != "") for the camera's page (nil: no right).
func (s *Server) nvrEvents(ctx context.Context, p *systemPage, nvr, camera string) *eventsView {
	if !p.Can[modules.PermSystemEvents] {
		return nil
	}
	qv := url.Values{"nvr": {nvr}}
	if camera != "" {
		qv.Set("camera", camera)
	}
	v := &eventsView{Nvr: nvr, Camera: camera, Query: qv.Encode(), Archive: camera != "" && p.Can[modules.PermArchivePlay]}
	v.More = "/?p=event_log&" + v.Query
	v.MoreUnacked = v.More + "&unacked=1"
	db, ok, err := eventdb.OpenIfExists(eventdb.Path(s.opt.Root))
	if err != nil || !ok {
		v.Missing = true
		return v
	}
	defer db.Close()
	f := eventdb.Filter{Nvr: nvr, Camera: camera, Limit: 10}
	evs, err := db.Query(ctx, f)
	if err != nil {
		return v
	}
	v.Rows = eventRows(evs, p.T)
	if v.Archive { // the player a few seconds before the event
		for i, e := range evs {
			v.Rows[i].Archive = playerLink(nvr, camera, e.Started.Add(-10*time.Second))
		}
	}
	v.Total, _ = db.Count(ctx, f)
	f.Unacked = true
	v.Unread, _ = db.Count(ctx, f)
	return v
}

// POST /events/ack: the events ticked, or (all=1) every one the filters show.
func (s *Server) eventsAck(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(se.csrf), []byte(r.PostFormValue("csrf"))) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, modules.PermSystemEventsAck) {
		s.denied(w, se, modules.PermSystemEventsAck)
		return
	}
	q, _ := url.ParseQuery(r.PostFormValue("query"))
	back := "/?p=event_log"
	if len(q) > 0 {
		back += "&" + q.Encode()
	}
	db, ok, err := eventdb.OpenIfExists(eventdb.Path(s.opt.Root))
	if err != nil || !ok {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	defer db.Close()
	f, _ := eventFilter(store, se.user, can(store, se, modules.PermSystemEventsSystem), q)
	f.Before = 0
	var ids []int64
	if r.PostFormValue("all") != "1" {
		ids = []int64{}
		for _, v := range r.PostForm["id"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
	}
	// the filter limits ids too: nobody acknowledges events of recorders not theirs
	n, err := db.Ack(r.Context(), ids, f, se.user, time.Now())
	if err != nil {
		s.opt.Log.Printf("web: events ack: %v", err)
	} else if n > 0 {
		s.opt.Log.Printf("web: %s acknowledges %d events", se.user, n)
		s.syslog(eventdb.Entry{Kind: modules.SysEventsAcked, User: se.user, Address: s.clientIP(r).String(), Text: fmt.Sprintf("%d events acknowledged", n)})
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// GET /events/picture?id=: the picture that came with an event.
func (s *Server) eventPicture(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	store, err := s.opt.Load()
	if err != nil || !can(store, se, modules.PermSystemEvents) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	db, ok, err := eventdb.OpenIfExists(eventdb.Path(s.opt.Root))
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	defer db.Close()
	e, err := db.Get(r.Context(), id)
	f, _ := eventFilter(store, se.user, false, nil)
	visible := e.Nvr == "" && f.Unknown || e.Nvr != "" && modules.CanAccess(store, se.user, e.Nvr)
	file := eventdb.PictureFile(eventdb.PicturesPath(s.opt.Root), e)
	if err != nil || !visible || file == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=event-%d.jpg", e.ID))
	http.ServeFile(w, r, file)
}
