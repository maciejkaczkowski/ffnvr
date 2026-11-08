package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/eventdb"
)

func seedEvents(t *testing.T, root string) map[string]int64 {
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := eventdb.NewWriter(db, eventdb.PicturesPath(root), t.Logf)
	now := time.Now()
	w.Add(eventdb.Notice{Nvr: "nvr02", Channel: 1, Camera: "ch01", Kind: "VideoMotion", Source: "smtp", Time: now, State: eventdb.Pulse,
		Text: "Motion CH1", Picture: []byte{0xFF, 0xD8, 9, 9}})
	w.Add(eventdb.Notice{Nvr: "nvr01", Channel: 1, Camera: "cam1", Kind: "VideoLoss", Source: "dahua", Time: now, State: eventdb.Start, Picture: []byte{0xFF, 0xD8, 1}})
	w.Add(eventdb.Notice{Device: "serial X at 10.1.1.1", Kind: "MotionDetect", Source: "dvrip", Time: now, State: eventdb.Start})
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	evs, _ := db.Query(context.Background(), eventdb.Filter{})
	for _, e := range evs {
		ids[e.Kind] = e.ID
	}
	return ids
}

func TestEventLog(t *testing.T) {
	srv, st := setup(t)
	root := st.e.Opts.Root
	c := client()
	signIn(t, c, srv.URL, "ola", "ola")
	// the sign in itself is the first entry
	if code, page := get(t, c, srv.URL+"/?p=event_log&origin=system"); code != 200 || !strings.Contains(page, "<b style=\"font-weight:500\">ola @ 127.0.0.1</b>") {
		t.Fatalf("the sign in in the system log: %d\n%s", code, page)
	}
	ids := seedEvents(t, root)
	code, page := get(t, c, srv.URL+"/?p=event_log")
	if code != 200 || !strings.Contains(page, "Motion CH1") || strings.Contains(page, ">VideoLoss<") || strings.Contains(page, "serial X") {
		t.Fatalf("ola sees the events of nvr02 only:\n%s", page)
	}
	pic := func(kind string) int {
		resp, err := c.Get(srv.URL + "/events/picture?id=" + strconv.FormatInt(ids[kind], 10))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if pic("VideoMotion") != 200 || pic("VideoLoss") != 404 {
		t.Errorf("pictures: own %d, other's %d", pic("VideoMotion"), pic("VideoLoss"))
	}
	// acknowledging everything acknowledges only what is hers
	csrf := csrfRe.FindStringSubmatch(page)[1]
	resp, err := c.PostForm(srv.URL+"/events/ack", url.Values{"csrf": {csrf}, "all": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// also a forged id of another recorder
	c.PostForm(srv.URL+"/events/ack", url.Values{"csrf": {csrf}, "id": {strconv.FormatInt(ids["VideoLoss"], 10)}})
	db, _ := eventdb.Open(eventdb.Path(root))
	defer db.Close()
	for kind, want := range map[string]bool{"VideoMotion": true, "VideoLoss": false, "MotionDetect": false} {
		e, _ := db.Get(context.Background(), ids[kind])
		if acked := !e.AckedAt.IsZero(); acked != want {
			t.Errorf("%s acknowledged: %v, want %v", kind, acked, want)
		}
	}
	// a user without system.events
	st.exec(t, `/user/policy add name=viewer allow=live`, `/user add user=ewa password=ewa policy=viewer`)
	c2 := client()
	signIn(t, c2, srv.URL, "ewa", "ewa")
	resp, _ = c2.Get(srv.URL + "/events/picture?id=" + strconv.FormatInt(ids["VideoMotion"], 10))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("picture without the right: %d", resp.StatusCode)
	}
	if _, page := get(t, c2, srv.URL+"/?p=event_log"); strings.Contains(page, "Motion CH1") {
		t.Error("the event log without the right")
	}
}

func TestSystemLogAndNvrPage(t *testing.T) {
	srv, st := setup(t)
	root := st.e.Opts.Root
	// a user who sees the events of nvr02 but not the system log
	st.exec(t, `/user/policy add name=ev allow="system,system.events"`, `/user add user=ewa password=ewa policy=ev`,
		`/nvr set [ find name=nvr02 ] valid-users="ola,@firma2,ewa"`)
	c := client()
	signIn(t, c, srv.URL, "ola", "nie") // failed
	signIn(t, c, srv.URL, "ola", "ola") // ok
	seedEvents(t, root)
	db, _ := eventdb.Open(eventdb.Path(root))
	db.Log(context.Background(), eventdb.Entry{Nvr: "nvr02", Kind: "NvrOffline", Text: "timeout"})
	n, _ := db.Count(context.Background(), eventdb.Filter{Origin: "system", User: "ola"})
	db.Close()
	if n != 2 {
		t.Errorf("sign in entries of ola: %d, want 2 (failed, ok)", n)
	}
	// filters: a user, a kind, a text
	_, page := get(t, c, srv.URL+"/?p=event_log&origin=system&kind=LoginFailed")
	if !strings.Contains(page, `badge mute">Failed sign in<`) || strings.Contains(page, `badge mute">Sign in<`) {
		t.Errorf("kind filter:\n%s", page)
	}
	_, page = get(t, c, srv.URL+"/?p=event_log&text=timeout")
	if !strings.Contains(page, "Recorder unreachable") || strings.Contains(page, "Motion CH1") {
		t.Error("text filter")
	}
	// the recorder's page: its latest events, recorders' and system's, and the way on
	_, page = get(t, c, srv.URL+"/?p=nvr&n=nvr02")
	for _, want := range []string{"Latest events", "Motion CH1", "Recorder unreachable", `href="/?p=event_log&amp;nvr=nvr02"`} {
		if !strings.Contains(page, want) {
			t.Errorf("nvr page without %q", want)
		}
	}
	if strings.Contains(page, "signed in</span>") {
		t.Error("sign ins are about no recorder: not on its page")
	}
	// ewa: the events of her recorders, not the sign ins
	ce := client()
	signIn(t, ce, srv.URL, "ewa", "ewa")
	_, page = get(t, ce, srv.URL+"/?p=event_log")
	if !strings.Contains(page, "Motion CH1") || strings.Contains(page, "signed in</span>") || strings.Contains(page, `name="user"`) {
		t.Errorf("ewa sees the system log:\n%s", page)
	}
	_, page = get(t, ce, srv.URL+"/?p=event_log&nvr=-")
	if strings.Contains(page, "signed in</span>") {
		t.Error("ewa: nvr=- shows the sign ins")
	}
}
