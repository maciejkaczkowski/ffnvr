package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffnvr/internal/engine"
)

// commit with the real modules: units written, systemctl called, configuration saved
func TestSessionCommit(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.rsc")
	var calls []string
	e, err := engine.New(engine.Options{Root: root, Run: func(args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	in := strings.Join([]string{
		`/storage add name="disk1" path="/srv/disk1"`,
		`/nvr add type="local" name="nvr01" storage="disk1"`,
		`/camera/add name="cam02" nvr="nvr01" stream="rtsp://192.168.0.59:554/live0.264" enabled="true"`,
		"diff",
		"commit",
		"/camera disable cam02",
		"commit",
	}, "\n") + "\n"
	if err := Run(e, strings.NewReader(in), &out, Options{SaveFile: cfg}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "error") {
		t.Fatalf("%s", out.String())
	}
	for _, want := range []string{"+Description=NVR: nvr01, Group: -, Camera: cam02", "written    /etc/systemd/system/ffnvr-nvr01-cam02.service",
		"saved " + cfg, "removed    /etc/systemd/system/ffnvr-nvr01-cam02.service"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
	wantCalls := "systemctl daemon-reload|systemctl enable --now ffnvr-nvr01-cam02.service ffnvr-worker.service|" +
		"systemctl disable --now ffnvr-nvr01-cam02.service|systemctl daemon-reload|systemctl enable --now ffnvr-worker.service"
	if got := strings.Join(calls, "|"); got != wantCalls {
		t.Errorf("calls:\n%s\nwant\n%s", got, wantCalls)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "enabled=no") {
		t.Errorf("commit saves the configuration:\n%s", b)
	}
}

func TestCompleteDownload(t *testing.T) {
	e, err := engine.New(engine.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c := &Completer{Store: e.Store, Root: e.Opts.Root}
	_, cs := c.Complete("/nvr/download", "")
	if !has(cs, "add", "cancel", "remove", "print") || has(cs, "set", "export", "enable") {
		t.Errorf("verbs: %s", texts(cs))
	}
	if _, cs = c.Complete("/nvr/download", "add "); !has(cs, "camera", "storage", "start", "end", "path") {
		t.Errorf("params: %s", texts(cs))
	}
}

func TestCompleteSharedCameraNames(t *testing.T) {
	e, _ := engine.New(engine.Options{Root: t.TempDir()})
	var out strings.Builder
	Run(e, strings.NewReader(strings.Join([]string{
		"/storage add name=d path=/srv/d",
		"/nvr add type=local name=n1 storage=d",
		"/nvr add type=local name=n2 storage=d",
		"/camera add name=cam01 nvr=n1 stream=rtsp://h/1",
		"/camera add name=cam01 nvr=n2 stream=rtsp://h/2",
		"/camera add name=door nvr=n2 stream=rtsp://h/3",
	}, "\n")+"\n"), &out, Options{})
	c := &Completer{Store: e.Store}
	if _, cs := c.Complete("/camera", "set "); texts(cs) != "0 1 door" {
		t.Errorf("a shared name is offered as numbers: %s\n%s", texts(cs), out.String())
	}
	if _, cs := c.Complete("/nvr/download", "add camera="); texts(cs) != "cam01 door" {
		t.Errorf("values once: %s", texts(cs))
	}
}

func TestCompleteAliases(t *testing.T) {
	e, _ := engine.New(engine.Options{Root: t.TempDir()})
	var out strings.Builder
	Run(e, strings.NewReader(strings.Join([]string{
		"/storage add name=d path=/srv/d",
		"/nvr add type=local name=n1 storage=d",
		"/camera add name=ch01 nvr=n1 stream=rtsp://h/1 alias=brama",
		"/camera add name=ch02 nvr=n1 stream=rtsp://h/2",
	}, "\n")+"\n"), &out, Options{})
	c := &Completer{Store: e.Store}
	if _, cs := c.Complete("/camera", "set "); texts(cs) != "ch01 brama ch02" {
		t.Errorf("selectors: %s\n%s", texts(cs), out.String())
	}
	if _, cs := c.Complete("/nvr/download", "add camera="); texts(cs) != "brama ch01 ch02" && texts(cs) != "ch01 ch02 brama" {
		t.Errorf("values: %s", texts(cs))
	}
}

func TestCompleteNvrScan(t *testing.T) {
	e, _ := engine.New(engine.Options{Root: t.TempDir()})
	var out strings.Builder
	Run(e, strings.NewReader("/storage add name=d path=/srv/d\n/nvr add type=local name=n1 storage=d\n"), &out, Options{})
	c := &Completer{Store: e.Store}
	_, cs := c.Complete("/nvr", "")
	if !has(cs, "scan", "add", "print") {
		t.Errorf("verbs: %s", texts(cs))
	}
	if _, cs = c.Complete("/nvr", "scan "); texts(cs) != "n1" {
		t.Errorf("items: %s", texts(cs))
	}
	if _, cs = c.Complete("/nvr", "add type="); texts(cs) != "local dahua uniview tiandy dvrip" {
		t.Errorf("types: %s", texts(cs))
	}
}

func TestCompleteValidUsers(t *testing.T) {
	e, _ := engine.New(engine.Options{Root: t.TempDir()})
	var out strings.Builder
	Run(e, strings.NewReader(strings.Join([]string{
		"/user/group add group=firma1",
		"/user/group add group=firma2",
		"/user add user=maciej password=x",
	}, "\n")+"\n"), &out, Options{})
	c := &Completer{Store: e.Store}
	if _, cs := c.Complete("/nvr", "add valid-users="); texts(cs) != "maciej @firma1 @firma2" {
		t.Errorf("%s\n%s", texts(cs), out.String())
	}
	if _, cs := c.Complete("/nvr", "add valid-users=maciej,@"); texts(cs) != "@firma1 @firma2" {
		t.Errorf("%s", texts(cs))
	}
}

func TestCompleteTools(t *testing.T) {
	e, _ := engine.New(engine.Options{Root: t.TempDir()})
	var out strings.Builder
	Run(e, strings.NewReader("/storage add name=d path=/srv/d\n/nvr add type=dahua name=n2 host=h username=a password=b\n"), &out, Options{})
	c := &Completer{Store: e.Store}
	if _, cs := c.Complete("/nvr/tools", ""); !has(cs, "reboot", "backup") || has(cs, "add") {
		t.Errorf("verbs: %s", texts(cs))
	}
	if _, cs := c.Complete("/nvr/tools", "backup "); !has(cs, "nvr", "storage", "directory") {
		t.Errorf("params: %s", texts(cs))
	}
	if _, cs := c.Complete("/nvr/tools", "reboot nvr="); texts(cs) != "n2" {
		t.Errorf("values: %s", texts(cs))
	}
}

func TestCompleteProfiles(t *testing.T) {
	e, _ := engine.New(engine.Options{Root: t.TempDir()})
	var out strings.Builder
	Run(e, strings.NewReader(strings.Join([]string{
		"/storage add name=d path=/srv/d",
		"/nvr add type=local name=n1 storage=d",
		`/camera/profile add name=tiandy username=admin password=x stream="rtsp://<host>/<ch>/1"`,
		`/camera/profile add name=uniview username=admin stream="rtsp://<host>/unicast/c<ch>/s0/live"`,
	}, "\n")+"\n"), &out, Options{})
	c := &Completer{Store: e.Store}
	if _, cs := c.Complete("/camera", "print "); texts(cs) != "detail" {
		t.Errorf("print: %s\n%s", texts(cs), out.String())
	}
	if _, cs := c.Complete("/camera/profile", "print "); texts(cs) != "" {
		t.Errorf("profile print: %s", texts(cs))
	}
	if _, cs := c.Complete("/camera", "add profile="); texts(cs) != "tiandy uniview" {
		t.Errorf("profile names: %s", texts(cs))
	}
	if _, cs := c.Complete("/camera", "add "); !has(cs, "profile", "host", "username", "password", "channel") {
		t.Errorf("camera params: %s", texts(cs))
	}
	if _, cs := c.Complete("/camera/", ""); !has(cs, "profile") {
		t.Errorf("submenus of /camera: %s", texts(cs))
	}
}
