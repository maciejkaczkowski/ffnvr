package web

import (
	"io"
	"net/url"
	"strings"
	"testing"

	"ffnvr/internal/dahua/dahuatest"
)

func TestClockPages(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	fake.Time = "2026-10-24 10:00:00"
	srv, st := setup(t)
	st.exec(t, `/nvr set nvr02 host="`+fake.Addr()+`" password=secret`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// the list: the offset as last read, a sync button; the menu
	_, list := get(t, c, srv.URL+"/?p=clock")
	contains(t, "clock list", list, "<h1>Recorder time</h1>", `<span class="badge warn">-95 s</span>`, `name="action" value="sync"`,
		`href="/?p=clock&amp;n=nvr02">Settings</a>`, "local (not available)", `<a href="/?p=clock" aria-current="page">`)
	// reached from the recorder's page
	_, nvr := get(t, c, srv.URL+"/?p=nvr&n=nvr02")
	contains(t, "recorder page", nvr, `href="/?p=clock&amp;n=nvr02">Time and NTP</a>`, `href="/?p=fwup&amp;n=nvr02">Update firmware</a>`)

	// the page: read live
	_, page := get(t, c, srv.URL+"/?p=clock&n=nvr02")
	contains(t, "clock page", page, "<h1>Time: nvr02</h1>", "2026-10-24 10:00:00", "Warsaw", `name="ntp" checked`,
		`name="ntp_server" value="192.168.0.254"`, `name="ntp_port" min="1" max="65535" value="123"`, `value="60"`,
		`type="datetime-local" name="time" step="1"`, `<span class="badge ok">On</span>`)
	csrf := csrfRe.FindStringSubmatch(page)[1]
	post := func(v url.Values) (int, string) {
		t.Helper()
		v.Set("csrf", csrf)
		resp, err := c.PostForm(srv.URL+"/tools/clock", v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	_, page = post(url.Values{"nvr": {"nvr02"}, "action": {"set"}, "time": {"2026-11-05T10:03:17"}})
	contains(t, "set", page, "nvr02: the clock is set to 2026-11-05 10:03:17.", "2026-11-05 10:03:17")
	_, page = post(url.Values{"nvr": {"nvr02"}, "action": {"ntp"}, "ntp_server": {"pool.ntp.org"}, "ntp_port": {"1123"}, "ntp_interval": {"15"}})
	contains(t, "ntp", page, "nvr02: the NTP settings are saved.", `name="ntp_server" value="pool.ntp.org"`, `value="1123"`, `value="15"`,
		`<span class="badge mute">Off</span>`)
	if strings.Contains(page, `name="ntp" checked`) {
		t.Error("NTP should be off")
	}
	_, list = post(url.Values{"nvr": {"nvr02"}, "action": {"sync"}, "back": {"list"}})
	contains(t, "sync from the list", list, "<h1>Recorder time</h1>", "nvr02: the clock is set to this server&#39;s time.")
	if strings.Contains(list, "-95 s") {
		t.Error("the offset is noted after setting the clock")
	}
	fake.Mu.Lock()
	sets := strings.Join(fake.TimeSets, "|")
	fake.Mu.Unlock()
	if !strings.HasPrefix(sets, "rpc 2026-11-05 10:03:17 tolerance=5|rpc ") {
		t.Errorf("times set: %s", sets)
	}

	// refusals
	_, page = post(url.Values{"nvr": {"nvr02"}, "action": {"set"}, "time": {"jutro"}})
	contains(t, "bad time", page, "Give the time as YYYY-MM-DD HH:MM:SS.")
	_, page = post(url.Values{"nvr": {"nvr02"}, "action": {"ntp"}, "ntp": {"on"}})
	contains(t, "ntp without server", page, "NTP needs a server address")
	_, page = post(url.Values{"nvr": {"nvr02"}, "action": {"ntp"}, "ntp_server": {"x"}, "ntp_port": {"-1"}})
	contains(t, "bad port", page, "must be positive numbers")
	_, page = post(url.Values{"nvr": {"nvr01"}, "action": {"sync"}})
	contains(t, "local", page, "not supported")
	if code, _ := post(url.Values{"nvr": {"nvr02"}, "action": {"x"}}); code != 400 {
		t.Errorf("unknown action: %d", code)
	}
	resp, _ := c.PostForm(srv.URL+"/tools/clock", url.Values{"nvr": {"nvr02"}, "action": {"sync"}, "csrf": {"forged"}})
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("forged: %d", resp.StatusCode)
	}
	// a recorder that does not answer: the page says so
	st.exec(t, `/nvr set nvr02 host=127.0.0.1:1`)
	_, page = get(t, c, srv.URL+"/?p=clock&n=nvr02")
	contains(t, "unreachable", page, "The recorder&#39;s clock could not be read")
	if _, page := get(t, c, srv.URL+"/?p=clock&n=nope"); !strings.Contains(page, "<h1>Recorder time</h1>") {
		t.Error("an unknown recorder leads to the list")
	}

	// the system log
	_, log := get(t, c, srv.URL+"/?p=event_log&nvr=nvr02")
	contains(t, "system log", log, "Clock set", "clock set to 2026-11-05 10:03:17", "NTP off, server &#34;pool.ntp.org&#34;, port 1123, every 15 min",
		"clock set to this server&#39;s time")
}
