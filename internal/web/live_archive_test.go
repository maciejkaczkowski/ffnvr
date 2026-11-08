package web

import (
	"testing"
)

// The live view (grid, map, thumbnails) opens the recordings of its cameras in the
// archive only as far as the rights go: archive for the search, archive.play too for
// the player.
func TestLiveOpenInArchive(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`,
		`/user/policy add name=player allow="live.*,archive.*"`,
		`/user/policy add name=searcher allow="live.*,archive"`,
		`/user/policy add name=watcher allow="live.*"`)
	for _, tc := range []struct {
		policy string
		data   string // in the page's data
		button bool   // "open in archive" of the strip
	}{
		{"player", `"archive":true,"archplay":true`, true},
		{"searcher", `"archive":true,"archplay":false`, true},
		{"watcher", `"archive":false,"archplay":false`, false},
	} {
		st.exec(t, `/user set jan policy=`+tc.policy)
		c := client()
		code, page := signIn(t, c, srv.URL, "jan", "secret")
		if code != 200 {
			t.Fatalf("%s: %d", tc.policy, code)
		}
		_, page = get(t, c, srv.URL+"/?p=live")
		contains(t, tc.policy, page, `id="lgrid"`, tc.data, `"lv_open_archive":"Open in archive"`)
		if tc.button {
			contains(t, tc.policy, page, `<a class="btn sm" id="march" href="#">Open in archive</a>`)
		} else {
			notContains(t, tc.policy, page, `id="march"`)
		}
	}
	// the link as the live view makes it (encoded "|"): the player with both cameras
	st.exec(t, `/user set jan policy=player`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=play&t=1793855880&c=nvr01%7Ccam1&c=nvr01%7Ccam3")
	contains(t, "player", page, `"init":[{"n":"nvr01","c":"cam1"},{"n":"nvr01","c":"cam3"}]`, `"t0":1793855880`)
	// without archive.play the same link opens the search
	st.exec(t, `/user set jan policy=searcher`)
	c = client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page = get(t, c, srv.URL+"/?p=archive&n=nvr01&tab=play&c=nvr01%7Ccam1")
	notContains(t, "searcher", page, `id="pbdata"`)
}
