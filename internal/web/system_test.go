package web

import (
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
	"ffnvr/internal/jobs"
)

func contains(t *testing.T, what, page string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(page, w) {
			t.Errorf("%s misses %q", what, w)
		}
	}
}

func TestSystemScreens(t *testing.T) {
	srv, _ := setup(t)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	_, dash := get(t, c, srv.URL+"/")
	contains(t, "dashboard", dash,
		`<span class="l">Recorders online</span><span class="v">2<span> of 2</span>`,
		`<span class="l">Cameras online</span><span class="v">2<span> of 5</span>`, // cam1 + ch01 of cam1,3,4 + ch01,ch02
		`<span class="l">Healthy disks</span><span class="v">2<span> of 3</span>`,
		`<span class="l">Shortest retention</span><span class="v">3<span> days</span>`, "nvr01, target: 30d",
		`<b>nvr01</b>`, `<b>nvr02</b><span class="mono">10.0.4.20</span>`, "30d (oldest 3 d 0 h)",
		"rampa: recording stopped", "cam4: Not applied", "nvr02: /dev/sdb failure", "nvr02: clock is off",
		`<span class="count">`, `<span class="dot warn" aria-hidden="true"></span><span>nvr01</span>`)
	if strings.Contains(dash, "nvr09") {
		t.Error("a recorder without access is not shown")
	}
	if !strings.Contains(dash, "ch02: disconnected") {
		t.Errorf("a configured remote camera that is offline is an alert")
	}

	_, local := get(t, c, srv.URL+"/?p=nvr&n=nvr01")
	contains(t, "local recorder", local,
		"<dt>Retention (configured)</dt><dd>30d</dd>", "<dt>Recordings</dt><dd>1 files, 2 kB</dd>", "<dt>Server uptime</dt>",
		"disk1 · ", "free ", // storage from the filesystem
		`<td class="mono" style="font-size:12px">192.168.10.21</td>`, "1 d 2 h", ">2</td><td>52 MB</td>", `<span class="badge ok">Recording</span>`,
		`<span class="badge err">Restarting</span>`, `<span class="badge warn">Not applied</span>`, `<span class="badge mute">Disabled</span>`,
		`href="/?p=cam&amp;n=nvr01&amp;c=cam1">Details</a>`)

	_, remote := get(t, c, srv.URL+"/?p=nvr&n=nvr02")
	contains(t, "remote recorder", remote,
		"<dt>Model</dt><dd>DHI-NVR2108-S3</dd>", "<dt>Firmware version</dt><dd>4.005.0000000.5.R</dd>", "<dt>Serial number</dt><dd>9H0076CPAZ1D877</dd>",
		"<dt>Uptime</dt><dd>2 d 2 h</dd>", "<dt>Clock offset</dt><dd>-95 s</dd>", "<dt>Cameras online</dt><dd>1 / 2</dd>",
		"10.0.4.31", "Site20", `<span class="badge warn">Disconnected</span>`, `<span class="badge mute">No camera</span>`,
		`<b>/dev/sdb</b><span class="badge err">Error</span>`, "used 95%")
	if strings.Contains(remote, "Details</a>") {
		t.Error("remote cameras have no page of their own")
	}

	_, cam := get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1")
	contains(t, "camera log", cam, "<h1>brama</h1>", "<dt>Service</dt><dd>ffnvr-nvr01-cam1.service</dd>", "<dt>Process</dt><dd>4242</dd>",
		"<dt>Recorder</dt><dd>nvr01</dd>", "<dt>IP address</dt><dd>192.168.10.21</dd>",
		"active / running (success)", `<div class="logbox" tabindex="0" aria-label="Logs"><pre class="log">`,
		"&lt;ffnvr-nvr01-cam1.service&gt; connection refused", `<a class="btn" href="/?p=nvr&amp;n=nvr01">← Back</a>`)

	_, disks := get(t, c, srv.URL+"/?p=disks")
	contains(t, "disks", disks, ">nvr01</a>", "/dev/sda", "/dev/sdb")
	if strings.Contains(disks, "other") || strings.Contains(disks, "/srv/other") {
		t.Error("a storage without access is not shown")
	}
	_, ret := get(t, c, srv.URL+"/?p=retention")
	contains(t, "retention", ret, "<td>30d</td>", "on the recorder")
	_, cams := get(t, c, srv.URL+"/?p=cams")
	contains(t, "camera list", cams, ">nvr01</a></td>", ">nvr02</a></td>", "brama", "wjazd",
		`href="/?p=cam&amp;n=nvr01&amp;c=cam1&amp;from=cams">Details</a>`)
	_, ph := get(t, c, srv.URL+"/?p=schedule")
	contains(t, "schedule", ph, "<h1>Recording schedule</h1>", "1. Day templates", "No day templates yet.",
		`href="/?p=cam&amp;n=nvr01&amp;c=cam1&amp;from=schedule">Details</a>`)
	// Back leads where the page was opened, the menu marks that page
	_, cam = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1&from=cams")
	contains(t, "camera from the list", cam, `<a class="btn" href="/?p=cams">← Back</a>`, `<a class="btn" href="/?p=nvr&amp;n=nvr01">nvr01</a>`,
		`<a href="/?p=cams" aria-current="page">`)
	_, cam = get(t, c, srv.URL+"/?p=cam&n=nvr01&c=cam1&from=schedule")
	contains(t, "camera from the schedule", cam, `<a class="btn" href="/?p=schedule">← Back</a>`, `<a href="/?p=schedule" aria-current="page">`)
	if strings.Contains(ph, ">Users<") || strings.Contains(ph, ">Network<") || strings.Contains(ph, "Email notifications") {
		t.Error("hidden sections")
	}
	_, live := get(t, c, srv.URL+"/?p=live")
	contains(t, "live", live, `<a href="/?p=live" aria-current="page">`)
	if strings.Contains(live, "Status dashboard") || !strings.Contains(live, "Live view is not enabled on this server.") {
		t.Error("the live view has its own side menu; off by default")
	}
	if strings.Contains(dash+local+remote+cam, "/user set") {
		t.Error("no commands in the panel")
	}
}

func TestSystemAccess(t *testing.T) {
	srv, _ := setup(t)
	c := client()
	signIn(t, c, srv.URL, "ola", "ola")
	_, dash := get(t, c, srv.URL+"/")
	if !strings.Contains(dash, "<b>nvr02</b>") || strings.Contains(dash, "nvr01") {
		t.Errorf("ola sees nvr02 only")
	}
	for _, u := range []string{"/?p=nvr&n=nvr01", "/?p=cam&n=nvr01&c=cam1", "/?p=nvr&n=nvr09", "/?p=nonsense"} {
		_, page := get(t, c, srv.URL+u)
		if !strings.Contains(page, "<h1>Status dashboard</h1>") || strings.Contains(page, "brama") {
			t.Errorf("%s must lead back to the dashboard", u)
		}
	}
}

func TestJobsFirmwareBranding(t *testing.T) {
	srv, st := setup(t)
	sp := jobs.Spool{Dir: filepath.Join(st.e.Opts.Root, jobs.Dir)}
	from := time.Date(2026, 10, 27, 8, 0, 0, 0, time.Local)
	j1, _ := sp.Submit(jobs.Job{Nvr: "nvr01", Camera: "cam1", Alias: "brama", Start: from, End: from.Add(time.Hour), Target: "/srv/archive/a.mp4"})
	j1.State, j1.Progress, j1.Speed, j1.Started = jobs.Running, 0.42, "8.1x", time.Now()
	sp.Save(j1)
	j2, _ := sp.Submit(jobs.Job{Nvr: "nvr02", Camera: "ch01", Start: from, End: from.Add(2 * time.Hour), Target: "/srv/archive/b/",
		Block: "1h", Parts: []jobs.Part{{Start: from, End: from.Add(time.Hour), State: "done"}, {Start: from.Add(time.Hour), End: from.Add(2 * time.Hour), State: "empty"}}})
	j2.State, j2.Size, j2.Started, j2.Finished = jobs.Done, 5e8, time.Now().Add(-time.Hour), time.Now()
	sp.Save(j2)
	j3, _ := sp.Submit(jobs.Job{Nvr: "nvr09", Camera: "x", Start: from, End: from.Add(time.Hour), Target: "/srv/secret.mp4"})
	_ = j3
	j4, _ := sp.Submit(jobs.Job{Nvr: "nvr01", Camera: "cam3", Start: from, End: from.Add(time.Hour), Target: "/srv/archive/c.mp4"})
	j4.State, j4.Error, j4.Finished = jobs.Failed, "no recordings between ...", time.Now()
	sp.Save(j4)

	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, queue := get(t, c, srv.URL+"/?p=jobs")
	contains(t, "queue", queue, "<h1>Queue</h1>", "brama (cam1)", `<span class="badge ok">Running</span>`, "42% · 8.1x", `style="width:42%"`,
		`<span>Queue</span>`, `<span class="gbadge">1</span>`)
	if strings.Contains(queue, "secret") || strings.Contains(queue, "b/") {
		t.Error("the queue: active jobs of accessible recorders only")
	}
	_, hist := get(t, c, srv.URL+"/?p=history")
	contains(t, "history", hist, `<span class="badge ok">Done</span>`, "1 / 2 (1 empty)", "/srv/archive/b/ (1h)", "500 MB · 1 h 0 min",
		`<span class="badge err">Failed</span>`, "no recordings between ...")
	if strings.Contains(hist, "secret") || strings.Contains(hist, "Running") {
		t.Error("the history: finished jobs of accessible recorders only")
	}
	_, nvr := get(t, c, srv.URL+"/?p=nvr&n=nvr01")
	contains(t, "recorder jobs", nvr, "<h2>Jobs</h2>", "brama (cam1)", "no recordings between ...")
	if strings.Contains(nvr, "/srv/archive/b/") {
		t.Error("the recorder's own jobs only")
	}

	_, fw := get(t, c, srv.URL+"/?p=firmware")
	contains(t, "firmware", fw, "<h1>Firmware</h1>", "Dahua recorders can be updated from the panel", "DHI-NVR2108-S3", "4.005.0000000.5.R",
		"9H0076CPAZ1D877", "<dt>ffnvr</dt><dd>1.0</dd>", `<span>Firmware</span>`)
	if strings.Contains(fw, "Firmware updates") {
		t.Error("renamed")
	}
	if !strings.Contains(fw, "tbody tr:hover td{background:") {
		t.Error("rows light up")
	}

	// branding: the name everywhere, the sign in page without name and logo
	st.exec(t, `/system/www set name="Monitoring ACME" login-name=no login-logo=no`)
	_, dash := get(t, c, srv.URL+"/")
	contains(t, "branded header", dash, "<b>Monitoring ACME</b>", "— Monitoring ACME</title>")
	_, login := get(t, client(), srv.URL+"/login")
	contains(t, "branded sign in", login, "— Monitoring ACME</title>")
	if strings.Contains(login, `<div class="logo">`) || strings.Contains(login, "Central management") {
		t.Error("no name and no logo on the sign in page")
	}
	st.exec(t, `/system/www set login-name=yes tagline="Centrum monitoringu"`)
	_, login = get(t, client(), srv.URL+"/login")
	contains(t, "tagline", login, "<b>Monitoring ACME</b><span>Centrum monitoringu</span>")
	// the tab goes from the user name to the password, the link comes after it
	if i, j := strings.Index(login, `id="pwd"`), strings.Index(login, `class="forgot"`); i < 0 || j < i {
		t.Error("the password field must come before the forgot link")
	}
}

func TestToolsPages(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	srv, st := setup(t)
	st.exec(t, `/nvr set nvr02 host="`+fake.Addr()+`" password=secret`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=reboot")
	contains(t, "reboot page", page, "<h1>Reboot devices</h1>", `action="/tools/reboot"`, `data-confirm="Reboot nvr02?"`, "local (not available)",
		`<dialog id="confirm"`, "showModal()", `<script nonce="`)
	csrf := csrfRe.FindStringSubmatch(page)[1]
	post := func(path string, v url.Values) *http.Response {
		v.Set("csrf", csrf)
		resp, err := c.PostForm(srv.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post("/tools/reboot", url.Values{"nvr": {"nvr02"}})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !fake.Rebooted || !strings.Contains(string(b), "nvr02: reboot requested.") {
		t.Errorf("reboot: %v\n%s", fake.Rebooted, b)
	}
	resp = post("/tools/backup", url.Values{"nvr": {"nvr02"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != string(fake.Backup) || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), `attachment; filename="nvr02_`) ||
		!strings.HasSuffix(resp.Header.Get("Content-Disposition"), `.backup"`) {
		t.Errorf("backup: %q %v", b, resp.Header)
	}
	// a local recorder, one without access, a wrong token
	resp = post("/tools/backup", url.Values{"nvr": {"nvr01"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "nvr01: nvr01 is type=local") {
		t.Errorf("local: %s", b)
	}
	fake.Rebooted = false
	resp = post("/tools/reboot", url.Values{"nvr": {"nvr09"}})
	resp.Body.Close()
	resp, _ = c.PostForm(srv.URL+"/tools/reboot", url.Values{"nvr": {"nvr02"}, "csrf": {"forged"}})
	resp.Body.Close()
	if resp.StatusCode != 403 || fake.Rebooted {
		t.Errorf("forged: %d %v", resp.StatusCode, fake.Rebooted)
	}
	_, page = get(t, c, srv.URL+"/?p=backup")
	contains(t, "backup page", page, "<h1>Configuration backup</h1>", `action="/tools/backup"`, ">Download</button>")
	_, page = get(t, c, srv.URL+"/?p=disks")
	contains(t, "disk models", page, "<th>Model</th>", "<th>Serial number</th>")
}

func TestMaskSecrets(t *testing.T) {
	for in, want := range map[string]string{
		"omv2 ffmpeg[1906544]: Error opening input file rtsp://admin:secret@192.168.0.72:554/1/1.": "omv2 ffmpeg[1906544]: Error opening input file rtsp://admin:*****@192.168.0.72:554/1/1.",
		"Input #0, rtsp, from 'rtsp://u:p@ss:w@h/s':":                                                  "Input #0, rtsp, from 'rtsp://u:*****@h/s':",
		"rtsp://192.168.1.2/live no password":                                                          "rtsp://192.168.1.2/live no password",
		"dvrip://admin:@h:34567?channel=0":                                                             "dvrip://admin:*****@h:34567?channel=0",
		"/user=admin&password=secret&channel=1":                                                        "/user=admin&password=*****&channel=1",
	} {
		if got := maskSecrets(in); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestBackupSelected(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	srv, st := setup(t)
	st.exec(t, `/nvr set nvr02 host="`+fake.Addr()+`" password=secret`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=backup")
	contains(t, "backup page", page, `id="bkall"`, `name="nvr" value="nvr02" form="bkform"`, `action="/tools/backup-to"`, "<option>disk1</option>")
	if strings.Contains(page, "<option>other</option>") {
		t.Error("only storages of types=backup")
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]
	resp, _ := c.PostForm(srv.URL+"/tools/backup-to", url.Values{"csrf": {csrf}, "nvr": {"nvr02", "nvr09"}, "storage": {"disk1"}, "dir": {"kopie/2026"}})
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	contains(t, "run", string(b), "<h2>Backups</h2>", "into disk1 / kopie/2026")
	runURL := resp.Request.URL.String()
	for i := 0; i < 50 && !strings.Contains(string(b), `<span class="badge ok">OK</span>`); i++ {
		time.Sleep(50 * time.Millisecond)
		_, s := get(t, c, runURL)
		b = []byte(s)
	}
	disk := findItemVal(st, "/storage", "disk1", "path")
	contains(t, "done", string(b), `<span class="badge ok">OK</span>`, disk+"/kopie/2026/nvr02_")
	if strings.Contains(string(b), "<td style=\"font-weight:500\">nvr09</td><td><span class=\"badge") {
		t.Error("a recorder without access is not backed up")
	}
	// another user does not see the run
	c2 := client()
	signIn(t, c2, srv.URL, "ola", "ola")
	if _, p2 := get(t, c2, runURL); strings.Contains(p2, "<h2>Backups</h2>") {
		t.Error("someone else's run")
	}
	resp, _ = c.PostForm(srv.URL+"/tools/backup-to", url.Values{"csrf": {csrf}, "nvr": {"nvr02"}, "storage": {"other"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	contains(t, "not a backup storage", string(b), "No storage has types=backup.")
}
