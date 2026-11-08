package web

import "testing"

// The frame of the pages: narrow screens get a menu button and a drawer with the brand,
// the user and signing out; the side menu of the Archive keeps the tab when the recorder
// changes.
func TestLayoutDrawerAndArchiveTab(t *testing.T) {
	srv, _ := setup(t)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/")
	contains(t, "layout", page, `id="menubtn"`, `aria-controls="drawer"`, `<dialog class="drawer" id="drawer"`,
		`action="/logout"`, `href="/?p=alerts"`, `id="drawerx"`, `id="dsec"`, `window.ffnvrNav=`)
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=search")
	contains(t, "archive search", page, `href="/?p=archive&amp;n=nvr02&amp;tab=search"`)
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=queue")
	contains(t, "archive queue", page, `href="/?p=archive&amp;n=nvr02&amp;tab=queue"`)
}
