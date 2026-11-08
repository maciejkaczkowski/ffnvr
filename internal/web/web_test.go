package web

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/engine"
	"ffnvr/internal/modules"
	"ffnvr/internal/script"
	"ffnvr/internal/sysd"
)

// store: users, groups, recorders and cameras; changes through exec are seen by the
// panel at the next request (like changes saved by the shell).
type store struct {
	mu sync.Mutex
	e  *engine.Engine
}

func newStore(t *testing.T, lines ...string) *store {
	e, err := engine.New(engine.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	st := &store{e: e}
	st.exec(t, lines...)
	return st
}

func (st *store) exec(t *testing.T, lines ...string) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, l := range lines {
		c, _, err := script.ParseLine("/", l)
		if err == nil {
			_, err = st.e.Store.Exec(c)
		}
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
	}
}

func (st *store) load() (*config.Store, error) { return st.e.Store, nil }

// fakeSystemd: the camera services of nvr01 and their logs.
type fakeSystemd struct{}

func (fakeSystemd) Show(_ context.Context, names ...string) (map[string]sysd.Unit, error) {
	all := map[string]sysd.Unit{
		"ffnvr-nvr01-cam1.service": {Name: "ffnvr-nvr01-cam1.service", Loaded: true, ActiveState: "active", SubState: "running",
			Result: "success", Restarts: 2, Memory: 50 << 20, Since: time.Now().Add(-26*time.Hour - time.Minute), PID: 4242},
		"ffnvr-nvr01-cam3.service": {Name: "ffnvr-nvr01-cam3.service", Loaded: true, ActiveState: "activating", SubState: "auto-restart",
			Result: "exit-code", Restarts: 17, Memory: -1},
		"ffnvr-nvr01-cam4.service": {Name: "ffnvr-nvr01-cam4.service", Loaded: false, ActiveState: "inactive", SubState: "dead", Memory: -1},
	}
	out := map[string]sysd.Unit{}
	for _, n := range names {
		if u, ok := all[n]; ok {
			out[n] = u
		}
	}
	return out, nil
}

func (fakeSystemd) Logs(_ context.Context, name string, n int) (string, error) {
	return "2026-10-26T10:00:00+0100 nvr ffmpeg[4242]: <" + name + "> connection refused\n", nil
}

func setup(t *testing.T, allow ...netip.Prefix) (*httptest.Server, *store) {
	return setupWith(t, func(o *Options) { o.Allow = allow })
}

// setupWith: setup with the options of the server changed.
func setupWith(t *testing.T, change func(*Options)) (*httptest.Server, *store) {
	disk := t.TempDir()
	st := newStore(t,
		`/storage add name=disk1 path=`+disk+` types=archive,backup`,
		`/storage add name=other path=/srv/other types=archive`,
		`/storage add name=rec2 path=/srv/rec2`,
		`/user/group add group=firma2`,
		`/user/policy add name=admin allow="live.*,archive.*,system.*"`,
		`/user add user=jan groups=firma2 password="secret" policy=admin`,
		`/user add user=ola password=ola policy=admin`,
		`/nvr add type=local name=nvr01 storage=disk1 retention=30d valid-users=@firma2`,
		`/nvr add type=local name=nvr09 storage=other`,
		`/nvr add type=dahua name=nvr02 host=10.0.4.20 username=admin password=x valid-users="ola,@firma2"`,
		`/camera add name=cam1 nvr=nvr01 stream=rtsp://192.168.10.21/s alias=brama`,
		`/camera add name=cam2 nvr=nvr01 stream=rtsp://192.168.10.22/s enabled=no`,
		`/camera add name=cam3 nvr=nvr01 stream=rtsp://192.168.10.23/s alias=rampa`,
		`/camera add name=cam4 nvr=nvr01 stream=rtsp://192.168.10.24/s`,
		`/camera add name=ch01 nvr=nvr02 channel=1 alias=wjazd`,
		`/camera add name=ch02 nvr=nvr02 channel=2`,
	)
	// a recording of nvr01, 3 days old
	seg := filepath.Join(disk, "nvr01", "cam1", time.Now().Add(-72*time.Hour).Format("2006-01-02T15-04-05")+".mkv")
	os.MkdirAll(filepath.Dir(seg), 0o755)
	os.WriteFile(seg, make([]byte, 2000), 0o644)
	// what the worker learned about nvr02
	status := modules.NvrStatus{Checked: time.Now(), LastOK: time.Now(),
		Device:   modules.Device{Type: "DHI-NVR2108-S3", Firmware: "4.005.0000000.5.R", Serial: "9H0076CPAZ1D877", Uptime: 50 * time.Hour},
		Channels: 3, Connected: 1,
		Disks: []modules.Disk{{Name: "/dev/sda", State: "Success", TotalBytes: 2e12, UsedBytes: 1.9e12}, {Name: "/dev/sdb", State: "Error", Error: true}},
		Clock: -95,
		ChannelList: []modules.Channel{{Number: 1, Title: "Site20", State: "Connected", Address: "10.0.4.31"},
			{Number: 2, Title: "Plac", State: "Disconnected"}, {Number: 3, Title: "Channel3", State: "Empty"}},
	}
	modules.MaintainLocal(st.e.Store, time.Now(), func(string, ...any) {}) // the worker's index
	b, _ := json.Marshal(status)
	os.MkdirAll(filepath.Join(st.e.Opts.Root, modules.StatusDir), 0o755)
	os.WriteFile(filepath.Join(st.e.Opts.Root, modules.StatusDir, "nvr02.json"), b, 0o644)
	opt := Options{Load: st.load, Root: st.e.Opts.Root, Version: "1.0",
		Log: log.New(io.Discard, "", 0), Systemd: fakeSystemd{},
		Edit: func(fn func(*config.Store) error) error { st.mu.Lock(); defer st.mu.Unlock(); return fn(st.e.Store) }}
	change(&opt)
	srv := httptest.NewServer(New(opt))
	t.Cleanup(srv.Close)
	return srv, st
}

func client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func signIn(t *testing.T, c *http.Client, base, user, pass string) (int, string) {
	t.Helper()
	_, page := get(t, c, base+"/login")
	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no csrf in\n%s", page)
	}
	resp, err := c.PostForm(base+"/login", url.Values{"csrf": {m[1]}, "username": {user}, "password": {pass}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSignIn(t *testing.T) {
	srv, st := setup(t)
	c := client()
	code, page := get(t, c, srv.URL+"/")
	if code != 200 || !strings.Contains(page, `<form class="card" method="post" action="/login"`) {
		t.Fatalf("not signed in: the sign in page\n%s", page)
	}
	for _, want := range []string{"Central management for video recorders", `autocomplete="current-password"`, "border-bottom:3px solid #066fd1",
		`<option value="pl">Polski</option>`, `aria-label="Show or hide password"`} {
		if !strings.Contains(page, want) {
			t.Errorf("sign in page misses %q", want)
		}
	}
	// behind a reverse proxy the panel does not know whether the browser's connection is
	// encrypted; and the version is nothing for a stranger
	notContains(t, "sign in page", page, "ffnvr 1.0", "unencrypted", "encrypted connection")
	if code, page := signIn(t, c, srv.URL, "jan", "zle"); code != 401 || !strings.Contains(page, "Wrong username or password.") || !strings.Contains(page, `value="jan"`) {
		t.Errorf("%d\n%s", code, page)
	}
	code, page = signIn(t, c, srv.URL, "jan", "secret")
	if code != 200 || !strings.Contains(page, "Signed in as <b>jan</b>") || !strings.Contains(page, "<h1>Status dashboard</h1>") {
		t.Fatalf("%d\n%s", code, page)
	}
	// a session ends when the user is disabled
	st.exec(t, `/user disable jan`)
	if _, page := get(t, c, srv.URL+"/"); !strings.Contains(page, `action="/login"`) {
		t.Errorf("a disabled user is signed out")
	}
	st.exec(t, `/user enable jan`)
	signIn(t, c, srv.URL, "jan", "secret")
	_, page = get(t, c, srv.URL+"/")
	resp, _ := c.PostForm(srv.URL+"/logout", url.Values{"csrf": {csrfRe.FindStringSubmatch(page)[1]}})
	resp.Body.Close()
	if _, page := get(t, c, srv.URL+"/"); !strings.Contains(page, `action="/login"`) {
		t.Errorf("signed out")
	}
}

func TestSignInProtection(t *testing.T) {
	srv, _ := setup(t)
	// without the form's token
	resp, _ := http.PostForm(srv.URL+"/login", url.Values{"username": {"jan"}, "password": {"secret"}})
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("no csrf: %d", resp.StatusCode)
	}
	// 5 failures block the address for a minute, even with the right password
	c := client()
	for i := 0; i < 5; i++ {
		signIn(t, c, srv.URL, "jan", "zle")
	}
	if code, page := signIn(t, c, srv.URL, "jan", "secret"); code != 429 || !strings.Contains(page, "Too many failed attempts") {
		t.Errorf("%d", code)
	}
	// security headers
	resp, _ = http.Get(srv.URL + "/login")
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'nonce-") || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("%v", resp.Header)
	}
}

func TestLanguageAndAllow(t *testing.T) {
	srv, _ := setup(t)
	req, _ := http.NewRequest("GET", srv.URL+"/login", nil)
	req.Header.Set("Accept-Language", "pl-PL,pl;q=0.9,en;q=0.8")
	resp, _ := http.DefaultClient.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Nazwa użytkownika") || !strings.Contains(string(b), `<option value="pl" selected>`) {
		t.Errorf("browser language:\n%s", b)
	}
	c := client()
	get(t, c, srv.URL+"/lang?lang=de")
	if _, page := get(t, c, srv.URL+"/login"); !strings.Contains(page, "Anmelden") {
		t.Errorf("the selector's language")
	}
	if _, page := get(t, c, srv.URL+"/login?forgot=1"); !strings.Contains(page, "Passwörter ändert der Administrator.") || strings.Contains(page, "/user set") {
		t.Errorf("forgot")
	}

	other, _ := setup(t, netip.MustParsePrefix("10.0.0.0/8"))
	if code, _ := get(t, client(), other.URL+"/login"); code != 403 {
		t.Errorf("127.0.0.1 is not in 10.0.0.0/8: %d", code)
	}
	ok, _ := setup(t, netip.MustParsePrefix("127.0.0.1/32"))
	if code, _ := get(t, client(), ok.URL+"/login"); code != 200 {
		t.Errorf("%d", code)
	}
}

func regexpFind(t *testing.T, s, re string) string {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("%s not in the page", re)
	}
	return m[1]
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(content), 0o644)
}

func findItemVal(st *store, path, name, key string) string {
	for _, it := range st.e.Store.Items(path) {
		if it.Val("name") == name {
			return it.Val(key)
		}
	}
	return ""
}

func TestMD5HashRefused(t *testing.T) {
	_, st := setup(t)
	// from htpasswd -m (openssl passwd -apr1 -salt 43Q55M8k test): not accepted
	c, _, _ := script.ParseLine("/", `/user add user=klient policy=admin password-hash="\$apr1\$43Q55M8k\$65Fd66gB.xBa46j6kFl7v."`)
	if _, err := st.e.Store.Exec(c); err == nil || !strings.Contains(err.Error(), "SHA-512") {
		t.Errorf("an MD5 hash: %v", err)
	}
}
