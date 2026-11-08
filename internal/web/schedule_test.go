package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/modules"
)

func TestSchedulePage(t *testing.T) {
	srv, st := setup(t)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=schedule")
	csrf := csrfRe.FindStringSubmatch(page)[1]
	post := func(v url.Values) (string, string) {
		t.Helper()
		v.Set("csrf", csrf)
		nf := *c
		nf.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := nf.PostForm(srv.URL+"/record/save", v)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		u, _ := url.Parse(loc)
		return loc, u.Query().Get("err")
	}
	// a day template
	if _, e := post(url.Values{"action": {"day-save"}, "name": {"biuro"}, "default": {"none"}, "spans": {"08:00-16:00=regular,16:00-20:00=alarm"}}); e != "" {
		t.Fatal(e)
	}
	if _, e := post(url.Values{"action": {"day-save"}, "name": {"zly"}, "spans": {"10:00-09:00=regular"}}); !strings.Contains(e, "end must come after") {
		t.Errorf("an invalid template: %q", e)
	}
	_, page = get(t, c, srv.URL+"/?p=schedule&day=biuro")
	contains(t, "day editor", page, `id="rsgrid"`, `value="08:00-16:00=regular,16:00-20:00=alarm"`, "drag across the hours", "Delete")
	// renamed, its spans unset
	if _, e := post(url.Values{"action": {"day-save"}, "orig": {"biuro"}, "name": {"office"}, "default": {"regular"}, "spans": {""}}); e != "" {
		t.Fatal(e)
	}
	if it := findItem(st.e.Store, "/record/day", "name", "office"); it == nil || it.Val("spans") != "" || it.Val("default") != "regular" {
		t.Errorf("%v", it)
	}
	post(url.Values{"action": {"day-save"}, "orig": {"office"}, "name": {"office"}, "default": {"none"}, "spans": {"08:00-16:00=regular,16:00-20:00=alarm"}})
	// a week schedule
	st.exec(t, `/event/filter add name=alarmy action=accept kinds="*/ALARM/*"`)
	_, page = get(t, c, srv.URL+"/?p=schedule&week=%2B")
	contains(t, "week editor", page, `name="mon"`, `name="sun"`, `value="alarmy"`, "Copy Monday to Tue–Fri", `placeholder="10s"`)
	v := url.Values{"action": {"week-save"}, "name": {"tydzien"}, "pre-alarm": {"15s"}, "alarm-filters": {"alarmy"}}
	for _, d := range []string{"mon", "tue", "wed", "thu", "fri"} {
		v.Set(d, "office")
	}
	if _, e := post(v); e != "" {
		t.Fatal(e)
	}
	w := findItem(st.e.Store, "/record/schedule", "name", "tydzien")
	if w == nil || w.Val("fri") != "office" || w.Val("sat") != "" || w.Val("pre-alarm") != "15s" || w.Val("alarm-filters") != "alarmy" {
		t.Fatalf("%v", w)
	}
	// assignment: the recorder and one camera's own
	if _, e := post(url.Values{"action": {"assign"}, "nvr": {"nvr01"}, "schedule": {"tydzien"}, "cam_cam1": {""}, "cam_cam3": {"tydzien"}}); e != "" {
		t.Fatal(e)
	}
	if findItem(st.e.Store, "/nvr", "name", "nvr01").Val("record-schedule") != "tydzien" || findCam(st.e.Store, "nvr01", "cam3").Val("record-schedule") != "tydzien" {
		t.Error("not assigned")
	}
	if _, e := post(url.Values{"action": {"assign"}, "nvr": {"nvr09"}, "schedule": {"tydzien"}}); !strings.Contains(e, "no access") {
		t.Errorf("a recorder of somebody else: %q", e)
	}
	if _, e := post(url.Values{"action": {"day-delete"}, "name": {"office"}}); !strings.Contains(e, "used") {
		t.Errorf("a template in use: %q", e)
	}
	// the worker fell back for cam1: the alert and the state
	recDir := filepath.Join(findItem(st.e.Store, "/storage", "name", "disk1").Val("path"), "nvr01")
	os.MkdirAll(modules.TechDir(recDir, "cam1"), 0o755)
	os.WriteFile(filepath.Join(modules.TechDir(recDir, "cam1"), "schedule.json"),
		[]byte(`{"schedule":"tydzien","fallback":true,"reason":"the alarm server is not running","fallback_since":"`+time.Now().Format(time.RFC3339)+`"}`), 0o644)
	_, page = get(t, c, srv.URL+"/?p=schedule")
	contains(t, "overview", page, "1. Day templates", "2. Week schedules", "3. Assignment", `class="m-alarm"`, "From the recorder (tydzien)",
		"Alarm recorded as Regular", `<h3><a href="/?p=nvr&amp;n=nvr01">`)
	if strings.Contains(page, `n=nvr09">`) {
		t.Error("a recorder of somebody else")
	}
	_, page = get(t, c, srv.URL+"/?p=alerts")
	contains(t, "alerts", page, "brama: alarms of the recording schedule not arriving", "the alarm server is not running")
	_, page = get(t, c, srv.URL+"/?p=nvr&n=nvr01")
	contains(t, "recording column", page, "Schedule tydzien")

	// without system.record: seen, not changed
	st.exec(t, `/user/policy add name=viewer allow="system.*" deny="system.record"`, `/user set [ find user=ola ] policy=viewer`)
	c2 := client()
	signIn(t, c2, srv.URL, "ola", "ola")
	_, page = get(t, c2, srv.URL+"/?p=schedule")
	contains(t, "view only", page, "Changing it needs the right system.record.")
	if strings.Contains(page, "New day template") {
		t.Error("no editing without the right")
	}
	m := csrfRe.FindStringSubmatch(page)
	resp, _ := c2.PostForm(srv.URL+"/record/save", url.Values{"csrf": {m[1]}, "action": {"day-delete"}, "name": {"office"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("%d", resp.StatusCode)
	}
}
