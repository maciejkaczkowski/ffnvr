package web

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"

	"ffnvr/internal/dahua/dahuatest"
)

func TestLivePTZ(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`,
		`/nvr set [ find name=nvr02 ] host=`+fake.Addr()+` username=`+dahuatest.User+` password=`+dahuatest.Pass)

	c := client()
	signIn(t, c, srv.URL, "ola", "ola")
	_, page := get(t, c, srv.URL+"/?p=live")
	contains(t, "live page", page, `id="lptz"`, `data-a="leftup"`, `class="pzstop" data-a="stop"`, `id="pzspeed"`,
		`data-need="zoom"`, `data-aux="deicing"`, `"ptz":true`, `"ptzmax":8`, `"pz_title":"PTZ control"`,
		`"c":"ch01","label":"wjazd"`, `"ptz":{"move":true,"zoom":true,"focus":true,"iris":true,"aux":[]}`)
	csrf := regexpFind(t, page, `"csrf":"([^"]+)"`)

	ptz := func(v url.Values) (int, map[string]any) {
		t.Helper()
		resp, err := c.PostForm(srv.URL+"/live/ptz", v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]any
		json.Unmarshal(b, &m)
		return resp.StatusCode, m
	}
	form := func(kv ...string) url.Values {
		v := url.Values{"csrf": {csrf}, "n": {"nvr02"}, "c": {"ch01"}, "s": {"6"}}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	for _, x := range []struct {
		v    url.Values
		code int
		err  string
	}{
		{form("a", "up", "on", "1"), 200, ""},
		{form("a", "up", "on", "0"), 200, ""},
		{form("a", "zoomout", "on", "1"), 200, ""},
		{form("a", "stop"), 200, ""},
		{form("a", "wiper", "on", "1"), 400, `nvr02 cannot do "wiper"`},
		{form("a", "up", "csrf", "zly"), 403, "forbidden"},
		{form("a", "up", "n", "nvr01", "c", "cam1"), 403, "no access"}, // ola does not see nvr01
		{form("a", "up", "n", "nvr09", "c", "x"), 403, "no access"},
	} {
		code, m := ptz(x.v)
		if code != x.code || (x.err == "" && m["ok"] != true) || (x.err != "" && !strings.Contains(m["error"].(string), x.err)) {
			t.Errorf("%v: %d %v", x.v, code, m)
		}
	}
	fake.Mu.Lock()
	got := strings.Join(fake.PTZ, "\n")
	fake.Mu.Unlock()
	if want := "rpc start Up ch1 6\nrpc stop Up ch1 6\nrpc start ZoomWide ch1 6\nrpc stop ZoomWide ch1 6"; got != want {
		t.Errorf("sent to the recorder:\n%s\nwant\n%s", got, want)
	}

	// a local recorder's cameras: no PTZ (jan sees nvr01 too)
	cj := client()
	signIn(t, cj, srv.URL, "jan", "secret")
	_, pj := get(t, cj, srv.URL+"/?p=live")
	if cam1 := regexpFind(t, pj, `("c":"cam1"[^}]*})`); strings.Contains(cam1, "ptz") {
		t.Errorf("local camera with PTZ: %s", cam1)
	}
	contains(t, "jan", pj, `"c":"ch01","label":"wjazd"`)

	// ...until the camera is given its PTZ protocol: steered directly, not through a recorder
	st.exec(t, `/camera set [ find name=cam1 ] type=dahua ptz=yes ptz-host=`+fake.Addr()+` username=`+dahuatest.User+` password=`+dahuatest.Pass)
	_, pj = get(t, cj, srv.URL+"/?p=live")
	if cam1 := regexpFind(t, pj, `("c":"cam1","label"[^}]*})`); !strings.Contains(cam1, `"ptz":{"move":true`) {
		t.Errorf("local camera with type=dahua ptz=yes: %s", cam1)
	}
	fake.Mu.Lock()
	fake.PTZ = nil
	fake.Mu.Unlock()
	jcsrf := regexpFind(t, pj, `"csrf":"([^"]+)"`)
	resp, err := cj.PostForm(srv.URL+"/live/ptz", url.Values{"csrf": {jcsrf}, "n": {"nvr01"}, "c": {"cam1"}, "a": {"right"}, "on": {"1"}, "s": {"4"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	fake.Mu.Lock()
	got = strings.Join(fake.PTZ, "\n")
	fake.Mu.Unlock()
	if resp.StatusCode != 200 || got != "rpc start Right ch1 4" {
		t.Errorf("local camera: %d, sent %q", resp.StatusCode, got)
	}

	// without live.ptz: no panel data, refused
	st.exec(t, `/user/policy add name=bez allow="live,live.views"`, `/user set ola policy=bez`)
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "without live.ptz", page, `"ptz":false`)
	notContains(t, "without live.ptz", page, `"ptz":{`)
	if code, m := ptz(form("a", "up", "on", "1")); code != 403 || !strings.Contains(m["error"].(string), "live.ptz") {
		t.Errorf("without the right: %d %v", code, m)
	}
	// with it alone (and its parent)
	st.exec(t, `/user/policy set [ find name=bez ] allow="live,live.ptz"`)
	if code, m := ptz(form("a", "down", "on", "0")); code != 200 {
		t.Errorf("with live.ptz: %d %v", code, m)
	}
}

func TestLivePTZSwap(t *testing.T) {
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes`)
	c := client()
	signIn(t, c, srv.URL, "ola", "ola")
	_, page := get(t, c, srv.URL+"/?p=live")
	contains(t, "live page", page, `"pzedit":true`, `id="pzswaph"`, `id="pzswapv"`, `id="pzswapsave"`, `id="pzshow"`,
		`"pz_swaph":"Swap left and right"`)
	notContains(t, "live page", page, `"swaph"`, `"swapv"`)
	csrf := regexpFind(t, page, `"csrf":"([^"]+)"`)
	save := func(n, cam, h, v string) (int, string) {
		t.Helper()
		resp, err := c.PostForm(srv.URL+"/live/ptz/swap", url.Values{"csrf": {csrf}, "n": {n}, "c": {cam}, "h": {h}, "v": {v}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	cam := func() string { return st.e.Store.Export("/camera") }

	if code, body := save("nvr02", "ch01", "1", "0"); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(cam(), "name=ch01 nvr=nvr02 channel=1 alias=wjazd enabled=yes ptz-swap-horizontal=yes") || strings.Contains(cam(), "ptz-swap-vertical") {
		t.Errorf("after h:\n%s", cam())
	}
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "saved", page, `"c":"ch01","label":"wjazd"`, `"swaph":true}`)
	save("nvr02", "ch01", "0", "1")
	if !strings.Contains(cam(), "alias=wjazd enabled=yes ptz-swap-vertical=yes") || strings.Contains(cam(), "ptz-swap-horizontal") {
		t.Errorf("after v:\n%s", cam())
	}
	save("nvr02", "ch01", "0", "0") // back to the default: nothing left in the file
	if strings.Contains(cam(), "ptz-swap") {
		t.Errorf("after none:\n%s", cam())
	}
	for _, x := range []struct{ n, c, want string }{{"nvr01", "cam1", "no access to nvr01"}, {"nvr02", "nope", "no such camera"}} {
		if code, body := save(x.n, x.c, "1", "1"); code != 400 || !strings.Contains(body, x.want) {
			t.Errorf("%v: %d %s", x, code, body)
		}
	}
	// live.ptz alone: the panel, the swap only in the browser
	st.exec(t, `/user/policy add name=op allow="live,live.ptz"`, `/user set ola policy=op`)
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "without live.ptz.edit", page, `"pzedit":false`, `"ptz":true`)
	if code, body := save("nvr02", "ch01", "1", "1"); code != 403 || !strings.Contains(body, "live.ptz.edit") {
		t.Errorf("without the right: %d %s", code, body)
	}
	if strings.Contains(cam(), "ptz-swap") {
		t.Errorf("written without the right:\n%s", cam())
	}
}
