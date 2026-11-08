package web

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffnvr/internal/modules"
	"ffnvr/internal/profile"
)

func TestViewsAndPositions(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`)
	prof := profile.New(st.e.Opts.Root)
	views := func(user string) profile.Dashboards {
		t.Helper()
		var ds profile.Dashboards
		if _, err := prof.Read(user, profile.DashboardsFile, &ds); err != nil {
			t.Fatal(err)
		}
		return ds
	}
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=live")
	csrf := csrfRe.FindStringSubmatch(page)
	if csrf == nil {
		csrf = []string{"", regexpFind(t, page, `"csrf":"([^"]+)"`)}
	}
	post := func(path string, v url.Values) jsonResult {
		v.Set("csrf", csrf[1])
		resp, err := c.PostForm(srv.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r jsonResult
		b, _ := io.ReadAll(resp.Body)
		json.Unmarshal(b, &r)
		return r
	}
	// a new view
	r := post("/live/view/save", url.Values{"name": {"mon01"}, "size": {"9"},
		"tiles": {`[{"pos":3,"n":"nvr01","c":"cam1","q":"lq"},{"pos":5,"n":"nvr01","c":"cam3","q":"hq"},{"pos":6,"n":"nvr09","c":"x","q":"lq"}]`}})
	if !r.OK || r.ID == "" {
		t.Fatalf("%+v", r)
	}
	ds := views("jan")
	if len(ds.Dashboards) != 1 || ds.Version != 1 {
		t.Fatalf("%+v", ds)
	}
	if d := ds.Dashboards[0]; d.ID != r.ID || d.Name != "mon01" || d.Size != 9 || len(d.Tiles) != 2 || d.Tiles[0] != (profile.DashTile{Position: 3, Nvr: "nvr01", Camera: "cam1", Quality: "lq"}) || d.Tiles[1].Quality != "hq" {
		t.Errorf("%+v (a camera without access is left out)", d)
	}
	b, _ := os.ReadFile(filepath.Join(st.e.Opts.Root, profile.Dir, "jan", "dashboards.json"))
	if !strings.Contains(string(b), `"version": 1`) || !strings.Contains(string(b), `"camera": "cam1"`) {
		t.Errorf("the file:\n%s", b)
	}
	if strings.Contains(st.e.Store.Export("/"), "dashboard") {
		t.Error("nothing in the configuration")
	}
	for _, bad := range []url.Values{
		{"name": {"x"}, "size": {"7"}, "tiles": {"[]"}},
		{"name": {""}, "size": {"4"}, "tiles": {"[]"}},
		{"name": {"x"}, "size": {"4"}, "tiles": {`[{"pos":5,"n":"nvr01","c":"cam1"}]`}},
		{"name": {"x"}, "size": {"4"}, "tiles": {`[{"pos":1,"n":"nvr01","c":"cam1"},{"pos":1,"n":"nvr01","c":"cam3"}]`}},
		{"name": {"x"}, "size": {"4"}, "tiles": {`[{"pos":1,"n":"nvr01","c":"../x"}]`}},
	} {
		if r := post("/live/view/save", bad); r.OK {
			t.Errorf("accepted: %v", bad)
		}
	}
	// overwritten
	r2 := post("/live/view/save", url.Values{"id": {r.ID}, "name": {"mon01b"}, "size": {"4"}, "tiles": {`[{"pos":1,"n":"nvr01","c":"cam4","q":"lq"}]`}})
	if ds := views("jan").Dashboards; !r2.OK || r2.ID != r.ID || len(ds) != 1 || ds[0].Name != "mon01b" || len(ds[0].Tiles) != 1 {
		t.Errorf("%+v %+v", r2, ds)
	}
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "views in the page", page, `"views":[{"id":"`+r.ID+`","name":"mon01b","size":4`)
	// someone else cannot change or delete it
	c2 := client()
	signIn(t, c2, srv.URL, "ola", "ola")
	_, page2 := get(t, c2, srv.URL+"/?p=live")
	csrf2 := regexpFind(t, page2, `"csrf":"([^"]+)"`)
	resp, _ := c2.PostForm(srv.URL+"/live/view/delete", url.Values{"id": {r.ID}, "csrf": {csrf2}})
	resp.Body.Close()
	if len(views("jan").Dashboards) != 1 {
		t.Error("ola deleted jan's view")
	}
	if strings.Contains(page2, r.ID) {
		t.Error("ola sees jan's view")
	}
	// deleted by its owner, with its cameras
	if r := post("/live/view/delete", url.Values{"id": {r.ID}}); !r.OK || len(views("jan").Dashboards) != 0 {
		t.Errorf("%+v", r)
	}
	// positions on the map
	r = post("/live/positions", url.Values{"positions": {`[{"n":"nvr01","c":"cam1","lat":52.40001,"long":16.92,"dir":-30,"angle":104,"range":50}]`}})
	ps := modules.Positions(st.e.Store)
	if !r.OK || len(ps) != 1 || ps[0].Direction != 330 || ps[0].Angle != 104 || ps[0].Lat != 52.40001 {
		t.Errorf("%+v %+v", r, ps)
	}
	post("/live/positions", url.Values{"positions": {`[{"n":"nvr01","c":"cam1","lat":52.5,"long":16.9,"dir":90,"angle":60,"range":20}]`}})
	if ps := modules.Positions(st.e.Store); len(ps) != 1 || ps[0].Lat != 52.5 {
		t.Errorf("moved: %+v", ps)
	}
	if r := post("/live/positions", url.Values{"positions": {`[{"n":"nvr09","c":"x","lat":1,"long":1}]`}}); r.OK {
		t.Error("a recorder without access")
	}
	post("/live/positions", url.Values{"positions": {`[{"n":"nvr01","c":"cam1","remove":true}]`}})
	if len(modules.Positions(st.e.Store)) != 0 {
		t.Error("removed")
	}
	// without the token
	resp, _ = c.PostForm(srv.URL+"/live/view/save", url.Values{"name": {"x"}, "size": {"4"}, "tiles": {"[]"}})
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("%d", resp.StatusCode)
	}
}
