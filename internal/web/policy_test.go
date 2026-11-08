package web

import (
	"ffnvr/internal/profile"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// notContains: none of the strings is in the page.
func notContains(t *testing.T, what, page string, nots ...string) {
	t.Helper()
	for _, n := range nots {
		if strings.Contains(page, n) {
			t.Errorf("%s: %q should not be there", what, n)
		}
	}
}

func TestPolicyViewer(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`,
		`/user/policy add name=viewer allow="live,archive"`,
		`/user set jan policy=viewer`)
	c := client()
	code, page := signIn(t, c, srv.URL, "jan", "secret")
	// no System: the first module it may use
	if code != 200 || !strings.Contains(page, `id="lgrid"`) {
		t.Fatalf("%d: not the live page\n%s", code, page)
	}
	contains(t, "modules", page, `<a href="/?p=live" aria-current="page">`, `<a href="/?p=archive">`)
	notContains(t, "modules", page, `<a href="/" `, `<a href="/">`)
	contains(t, "live rights", page, `"vedit":false,"map":false,"medit":false`)
	notContains(t, "live rights", page, `"positions":[{`)
	csrf := regexpFind(t, page, `"csrf":"([^"]+)"`)

	for _, u := range []string{"/", "/?p=dash", "/?p=reboot", "/?p=nvr&n=nvr02", "/?p=fwup&n=nvr02", "/?p=clock", "/?p=clock&n=nvr02"} {
		if _, page := get(t, c, srv.URL+u); !strings.Contains(page, `id="lgrid"`) {
			t.Errorf("%s: should lead to the live page", u)
		}
	}
	try := func(method, path string, v url.Values) (int, string) {
		t.Helper()
		v.Set("csrf", csrf)
		var resp *http.Response
		var err error
		if method == "GET" {
			resp, err = c.Get(srv.URL + path + "?" + v.Encode())
		} else {
			resp, err = c.PostForm(srv.URL+path, v)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, x := range []struct {
		method, path, perm string
		v                  url.Values
	}{
		{"POST", "/tools/reboot", "system.nvr.reboot", url.Values{"nvr": {"nvr02"}}},
		{"POST", "/tools/backup", "system.nvr.backup", url.Values{"nvr": {"nvr02"}}},
		{"POST", "/tools/backup-to", "system.nvr.backup", url.Values{"nvr": {"nvr02"}, "storage": {"disk1"}}},
		{"POST", "/tools/firmware", "system.nvr.firmware", url.Values{"nvr": {"nvr02"}}},
		{"POST", "/tools/clock", "system.nvr.clock", url.Values{"nvr": {"nvr02"}, "action": {"sync"}}},
		{"POST", "/archive/range", "archive.export", url.Values{"n": {"nvr01"}, "c": {"cam1"}, "storage": {"disk1"}}},
		{"POST", "/archive/files", "archive.export", url.Values{"n": {"nvr01"}, "storage": {"disk1"}}},
		{"GET", "/archive/file", "archive.download", url.Values{"n": {"nvr01"}, "c": {"cam1"}, "f": {"x.mkv"}}},
		{"GET", "/archive/result", "archive.download", url.Values{"id": {"1"}}},
		{"POST", "/archive/cancel", "archive.jobs", url.Values{"id": {"1"}}},
		{"POST", "/archive/retry", "archive.jobs", url.Values{"id": {"1"}}},
		{"POST", "/live/view/save", "live.views", url.Values{"name": {"x"}, "size": {"4"}, "tiles": {"[]"}}},
		{"POST", "/live/view/delete", "live.views", url.Values{"id": {"x"}}},
		{"POST", "/live/positions", "live.map.edit", url.Values{"positions": {"[]"}}},
		{"GET", "/live/tiles/1/0/0.png", "live.map", url.Values{}},
	} {
		if code, body := try(x.method, x.path, x.v); code != 403 || !strings.Contains(body, x.perm) {
			t.Errorf("%s %s: %d %s", x.method, x.path, code, body)
		}
	}
	if _, err := os.Stat(filepath.Join(st.e.Opts.Root, profile.Dir, "jan", profile.DashboardsFile+".json")); err == nil {
		t.Error("a view was saved")
	}
	// what it may: watching (the proxy answers, go2rtc is not there), the archive without its actions
	if code, _ := try("GET", "/live/frame.jpeg", url.Values{"n": {"nvr01"}, "c": {"cam1"}}); code == 403 {
		t.Error("live is allowed")
	}
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=search&search=1&c=cam1&from=2000-01-01T00:00&to=2100-01-01T00:00")
	contains(t, "archive", page, `<h2>Recorded files</h2>`, "<td>brama</td>")
	notContains(t, "archive", page, `tab=range`, `action="/archive/files"><`, `name="f"`, `/archive/file?`, `data-all="f"`)
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=range")
	notContains(t, "range tab", page, `action="/archive/range"`)

	// the policy taken away: the session ends
	st.exec(t, `/user/policy add name=none comment="no rights"`, `/user set jan policy=none`)
	if _, page := get(t, c, srv.URL+"/?p=live"); !strings.Contains(page, `action="/login"`) {
		t.Error("without rights the session ends")
	}
	code, page = signIn(t, c, srv.URL, "jan", "secret")
	if code != 403 || !strings.Contains(page, "This account has no rights in the panel") {
		t.Errorf("sign in without rights: %d\n%s", code, page)
	}
	// a wrong password still says only that
	if code, page := signIn(t, c, srv.URL, "jan", "zle"); code != 401 || strings.Contains(page, "no rights") {
		t.Errorf("%d %s", code, page)
	}
}

func TestPolicyOperator(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`,
		`/user/policy add name=operator allow="*" deny="system.nvr.reboot,system.logs,live.map.edit"`,
		`/user set jan policy=operator`)
	c := client()
	code, page := signIn(t, c, srv.URL, "jan", "secret")
	if code != 200 || !strings.Contains(page, "<h1>Status dashboard</h1>") {
		t.Fatalf("%d\n%s", code, page)
	}
	contains(t, "menu", page, `href="/?p=backup"`, `href="/?p=firmware"`)
	notContains(t, "menu", page, `href="/?p=reboot"`)
	st.exec(t, `/user/policy set operator deny="system.nvr.reboot,system.nvr.firmware,system.nvr.clock,system.logs,live.map.edit"`)
	_, page = get(t, c, srv.URL+"/?p=firmware")
	notContains(t, "firmware without the right", page, `?p=fwup`, "<th style=\"text-align:right\">Action</th>")
	_, page = get(t, c, srv.URL+"/?p=nvr&n=nvr02")
	notContains(t, "recorder without the right", page, `?p=fwup`, `?p=clock`)
	if _, page := get(t, c, srv.URL+"/?p=clock"); !strings.Contains(page, "<h1>Status dashboard</h1>") {
		t.Error("the clock page leads to the dashboard")
	}
	if _, page := get(t, c, srv.URL+"/?p=fwup&n=nvr02"); !strings.Contains(page, "<h1>Status dashboard</h1>") {
		t.Error("the update page leads to the dashboard")
	}
	if _, page := get(t, c, srv.URL+"/?p=reboot"); !strings.Contains(page, "<h1>Status dashboard</h1>") {
		t.Error("the reboot page leads to the dashboard")
	}
	_, page = get(t, c, srv.URL+"/?p=backup")
	contains(t, "backup page", page, `action="/tools/backup"`)
	_, page = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	notContains(t, "camera without logs", page, "connection refused", `class="logbox"`)
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "live rights", page, `"vedit":true,"map":true,"medit":false`)
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=range")
	contains(t, "range tab", page, `action="/archive/range"`)
	// the same search as the viewer's: here with its actions
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=search&search=1&c=cam1&from=2000-01-01T00:00&to=2100-01-01T00:00")
	contains(t, "archive", page, `tab=range`, `name="f"`, `/archive/file?`, `data-all="f"`)

	// admin (live.*,archive.*,system.*): everything
	st.exec(t, `/user set jan policy=admin`)
	_, page = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	contains(t, "camera with logs", page, "connection refused")
	_, page = get(t, c, srv.URL+"/")
	contains(t, "menu", page, `href="/?p=reboot"`)

	// without policy: nothing, the session ends and signing in is refused
	st.exec(t, `/user unset jan policy`)
	if _, page := get(t, c, srv.URL+"/"); !strings.Contains(page, `action="/login"`) {
		t.Error("without policy the session ends")
	}
	if code, page := signIn(t, c, srv.URL, "jan", "secret"); code != 403 || !strings.Contains(page, "This account has no rights in the panel") {
		t.Errorf("sign in without policy: %d\n%s", code, page)
	}
}
