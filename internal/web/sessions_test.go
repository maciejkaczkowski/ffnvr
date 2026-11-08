package web

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/profile"
)

// The sessions are kept in the users' profiles: a restart of the panel signs nobody
// out, a sign out removes the session there too.
func TestSessionsSurviveRestart(t *testing.T) {
	var opt Options
	srv, st := setupWith(t, func(o *Options) { opt = *o })
	c := client()
	if code, page := signIn(t, c, srv.URL, "jan", "secret"); code != 200 || !strings.Contains(page, "Signed in as <b>jan</b>") {
		t.Fatalf("%d\n%s", code, page)
	}
	file := filepath.Join(st.e.Opts.Root, profile.Dir, "jan", profile.SessionsFile+".json")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, ck := range c.Jar.Cookies(mustURL(t, srv.URL)) {
		if ck.Name == cookieName && strings.Contains(string(b), ck.Value) {
			t.Error("the cookie itself is in the profile")
		}
	}

	// the panel restarts: a new server on the same profiles
	srv.Config.Handler = New(opt)
	_, page := get(t, c, srv.URL+"/")
	if !strings.Contains(page, "Signed in as <b>jan</b>") {
		t.Fatalf("signed out by the restart\n%s", page)
	}
	// the same CSRF token as before the restart: the sign out works
	resp, _ := c.PostForm(srv.URL+"/logout", url.Values{"csrf": {csrfRe.FindStringSubmatch(page)[1]}})
	resp.Body.Close()
	var ss profile.Sessions
	profile.New(st.e.Opts.Root).Read("jan", profile.SessionsFile, &ss)
	if len(ss.Sessions) != 0 {
		t.Errorf("signed out, still in the profile: %+v", ss.Sessions)
	}
	srv.Config.Handler = New(opt)
	if _, page := get(t, c, srv.URL+"/"); !strings.Contains(page, `action="/login"`) {
		t.Error("signed out, signed in again by the restart")
	}
}

// Sessions expired meanwhile are not taken from the profiles.
func TestSessionsExpiredNotLoaded(t *testing.T) {
	var opt Options
	srv, st := setupWith(t, func(o *Options) { opt = *o })
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	prof := profile.New(st.e.Opts.Root)
	var ss profile.Sessions
	prof.Read("jan", profile.SessionsFile, &ss)
	if len(ss.Sessions) != 1 {
		t.Fatalf("%+v", ss)
	}
	old := ss.Sessions[0]
	old.Active = time.Now().Add(-idleTimeout - time.Minute)
	prof.SaveSession("jan", old)
	srv.Config.Handler = New(opt)
	if _, page := get(t, c, srv.URL+"/"); !strings.Contains(page, `action="/login"`) {
		t.Error("an idle session came back")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
