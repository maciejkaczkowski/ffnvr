package web

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"ffnvr/internal/modules"
)

func TestAccountPassword(t *testing.T) {
	srv, st := setup(t)
	c, other := client(), client()
	signIn(t, c, srv.URL, "jan", "secret")
	signIn(t, other, srv.URL, "jan", "secret") // a second session of jan

	_, page := get(t, c, srv.URL+"/")
	contains(t, "header", page, `<a class="me" href="/?p=account"`, `<a class="duser" href="/?p=account"`)
	_, page = get(t, c, srv.URL+"/?p=account")
	contains(t, "account page", page, "<h1>My account</h1>", "<dt>User</dt><dd>jan</dd>", "<dt>Groups</dt><dd>firma2</dd>",
		"<dt>Policy</dt><dd>admin</dd>", "<dt>Recorders</dt><dd>nvr01, nvr02</dd>", "<dt>Active sessions</dt><dd>2</dd>",
		`action="/account/password"`, `autocomplete="current-password"`, `minlength="8"`)
	csrf := csrfRe.FindStringSubmatch(page)[1]
	post := func(cl *http.Client, cur, nw, rep string) string {
		t.Helper()
		resp, err := cl.PostForm(srv.URL+"/account/password", url.Values{"csrf": {csrf}, "current": {cur}, "new": {nw}, "repeat": {rep}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return b.String()
	}
	for _, x := range []struct{ cur, nw, rep, want string }{
		{"wrong", "Nowe-haslo1", "Nowe-haslo1", "The current password is wrong."},
		{"secret", "short", "short", "The new password is too short: at least 8 characters."},
		{"secret", "Nowe-haslo1", "Nowe-haslo2", "The new passwords differ."},
	} {
		if page := post(c, x.cur, x.nw, x.rep); !strings.Contains(page, x.want) {
			t.Errorf("%+v: no %q", x, x.want)
		}
	}
	if _, ok := modules.Authenticate(st.e.Store, "jan", "secret"); !ok {
		t.Fatal("refused changes keep the password")
	}
	// a foreign CSRF token is refused
	if resp, _ := c.PostForm(srv.URL+"/account/password", url.Values{"csrf": {"x"}, "current": {"secret"}, "new": {"Nowe-haslo1"}, "repeat": {"Nowe-haslo1"}}); resp.StatusCode != 403 {
		t.Errorf("csrf: %d", resp.StatusCode)
	}

	page = post(c, "secret", "Nowe \"hasło\" 1", "Nowe \"hasło\" 1")
	contains(t, "changed", page, "The password has been changed.", "<dt>Active sessions</dt><dd>1</dd>")
	if _, ok := modules.Authenticate(st.e.Store, "jan", "secret"); ok {
		t.Error("the old password still works")
	}
	if _, ok := modules.Authenticate(st.e.Store, "jan", `Nowe "hasło" 1`); !ok {
		t.Error("the new password does not work")
	}
	if strings.Contains(st.e.Store.Export("/user"), "hasło") {
		t.Error("the password is kept as a hash only")
	}
	// the other session is signed out, this one goes on
	if _, page := get(t, other, srv.URL+"/"); !strings.Contains(page, `action="/login"`) {
		t.Error("the other session must end")
	}
	if _, page := get(t, c, srv.URL+"/?p=account"); !strings.Contains(page, "<h1>My account</h1>") {
		t.Error("this session goes on")
	}
	if code, _ := signIn(t, other, srv.URL, "jan", `Nowe "hasło" 1`); code != 200 {
		t.Errorf("sign in with the new password: %d", code)
	}
	// in the system log
	_, page = get(t, c, srv.URL+"/?p=event_log&origin=system")
	contains(t, "system log", page, "Password changed", "password changed, 1 other session(s) ended")
}

// A user without the right system has the page too; read only: no form.
func TestAccountOtherUsers(t *testing.T) {
	srv, st := setupWith(t, func(o *Options) { o.Edit = nil })
	st.exec(t, `/user/policy add name=viewer allow="live"`, `/user set jan policy=viewer`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=account")
	contains(t, "viewer's account", page, "<h1>My account</h1>", "<dt>Rights</dt><dd>live</dd>",
		"The configuration is read only here")
	notContains(t, "read only", page, `action="/account/password"`)
}

// The start page: the modules the user may use; / and signing in open it.
func TestAccountStartPage(t *testing.T) {
	srv, st := setup(t)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=account")
	contains(t, "start pages", page, "<h2>Start page</h2>", `action="/account/start"`,
		`<input type="radio" name="start" value="live"> Live view`, `<input type="radio" name="start" value="archive"> Archive`,
		`<input type="radio" name="start" value="system" checked> System`)
	contains(t, "the system module", page, `<a href="/?p=dash">`)
	csrf := csrfRe.FindStringSubmatch(page)[1]
	set := func(cl *http.Client, v string) *http.Response {
		t.Helper()
		resp, err := cl.PostForm(srv.URL+"/account/start", url.Values{"csrf": {csrf}, "start": {v}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := set(c, "archive"); resp.Request.URL.RawQuery != "p=account&ok=start" {
		t.Fatalf("saved: %s", resp.Request.URL)
	}
	_, page = get(t, c, srv.URL+"/?p=account&ok=start")
	contains(t, "saved", page, "The start page has been saved.", `value="archive" checked`)
	// / opens the archive, the dashboard stays at ?p=dash
	if resp, _ := c.Get(srv.URL + "/"); resp.Request.URL.Query().Get("p") != "archive" {
		t.Errorf("/ goes to %s", resp.Request.URL)
	}
	if _, page := get(t, c, srv.URL+"/?p=dash"); !strings.Contains(page, "<h1>Status dashboard</h1>") {
		t.Error("the dashboard")
	}
	// signing in again opens it too
	c2 := client()
	if code, page := signIn(t, c2, srv.URL, "jan", "secret"); code != 200 || !strings.Contains(page, `<a href="/?p=archive" aria-current="page">`) {
		t.Errorf("sign in: %d", code)
	}
	// nothing else than the modules the user may use
	if resp := set(c, "nonsense"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("nonsense: %d", resp.StatusCode)
	}
	if resp, _ := c.PostForm(srv.URL+"/account/start", url.Values{"csrf": {"x"}, "start": {"live"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("csrf: %d", resp.StatusCode)
	}
	st.exec(t, `/user/policy add name=lv allow="live,system"`, `/user set jan policy=lv`)
	_, page = get(t, c, srv.URL+"/?p=account")
	notContains(t, "no archive", page, `value="archive"`)
	contains(t, "no archive", page, `value="live"`, `value="system" checked`) // the choice it cannot use: the panel's
	if resp := set(c, "archive"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("archive without the right: %d", resp.StatusCode)
	}
	if _, page := get(t, c, srv.URL+"/"); !strings.Contains(page, "<h1>Status dashboard</h1>") {
		t.Error("/ without the chosen module: the panel's choice")
	}
	// one module only: nothing to choose
	st.exec(t, `/user/policy set lv allow="live"`)
	_, page = get(t, c, srv.URL+"/?p=account")
	contains(t, "one module", page, "The panel opens with Live view: the only module you may use.")
	notContains(t, "one module", page, `action="/account/start"`)
}

// The live view's own settings: a zoomed tile switches to HQ unless the user turns it off.
func TestAccountLive(t *testing.T) {
	g := newFakeGo2rtc()
	defer g.srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(g.srv.URL, "http://"))
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes port=`+port)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=account")
	contains(t, "live settings", page, `action="/account/live"`, `<input type="checkbox" name="zoom_hq" value="1" checked>`)
	csrf := csrfRe.FindStringSubmatch(page)[1]
	set := func(v url.Values) *http.Response {
		t.Helper()
		v.Set("csrf", csrf)
		resp, err := c.PostForm(srv.URL+"/account/live", v)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	// unchecked: the box is not sent
	if resp := set(url.Values{}); resp.Request.URL.RawQuery != "p=account&ok=live" {
		t.Fatalf("saved: %s", resp.Request.URL)
	}
	_, page = get(t, c, srv.URL+"/?p=account&ok=live")
	contains(t, "off", page, "The settings of the live view have been saved.", `<input type="checkbox" name="zoom_hq" value="1">`)
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "live page: off", page, `"zoomhq":false`)
	set(url.Values{"zoom_hq": {"1"}})
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "live page: on", page, `"zoomhq":true`)
	if resp, _ := c.PostForm(srv.URL+"/account/live", url.Values{"csrf": {"x"}, "zoom_hq": {"1"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("csrf: %d", resp.StatusCode)
	}
	// without the right live: no section, no saving
	st.exec(t, `/user/policy add name=sys allow="system.*"`, `/user set jan policy=sys`)
	_, page = get(t, c, srv.URL+"/?p=account")
	notContains(t, "no live", page, `action="/account/live"`)
	if resp := set(url.Values{"zoom_hq": {"1"}}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("without live: %d", resp.StatusCode)
	}
}
