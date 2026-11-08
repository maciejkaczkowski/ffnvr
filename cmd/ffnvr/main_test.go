package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
)

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffnvr")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(stdin string, args ...string) (string, int) {
		c := exec.Command(bin, args...)
		c.Stdin = strings.NewReader(stdin)
		out, err := c.CombinedOutput()
		code := 0
		if e, ok := err.(*exec.ExitError); ok {
			code = e.ExitCode()
		}
		return string(out), code
	}
	empty := filepath.Join(dir, "empty.rsc")
	os.WriteFile(empty, []byte("# nothing yet\n"), 0o644)
	bad := filepath.Join(dir, "bad.rsc")
	os.WriteFile(bad, []byte("/nonsense\nset a=b\n"), 0o644)

	for _, args := range [][]string{{}, {"aply"}, {"export", "-c", empty, "extra"}} {
		if out, code := run("", args...); code != 2 {
			t.Errorf("%v: code %d\n%s", args, code, out)
		}
	}
	if out, code := run("", "help"); code != 0 || !strings.Contains(out, "usage: ffnvr <command>") {
		t.Errorf("help: %d\n%s", code, out)
	}
	if out, code := run("", "export", "-c", empty); code != 0 || out != "" {
		t.Errorf("export of an empty configuration: %d %q", code, out)
	}
	if out, code := run("", "-c", bad, "export"); code != 1 || !strings.Contains(out, "unknown menu /nonsense") {
		t.Errorf("unknown menu: %d\n%s", code, out)
	}
	// the example configuration; /nvr may stand before the /storage it refers to
	cfg := filepath.Join(dir, "nvr.rsc")
	os.WriteFile(cfg, []byte("/nvr\nadd type=\"local\" name=\"nvr01\" storage=\"disk1\"\n\n/storage\nadd name=\"disk1\" path=\"/srv/disk1\"\n"), 0o644)
	want := "/storage\nadd name=disk1 path=/srv/disk1\n\n/nvr\nadd type=local name=nvr01 storage=disk1\n"
	if out, code := run("", "export", "-c", cfg); code != 0 || out != want {
		t.Errorf("export: %d\n%s", code, out)
	}
	// diff and apply on a separate root: files are written, systemctl is skipped
	root := filepath.Join(dir, "root")
	os.WriteFile(cfg, []byte(want+"\n/camera\nadd name=cam1 nvr=nvr01 stream=rtsp://10.0.0.1/s\n"), 0o644)
	unit := filepath.Join(root, "etc/systemd/system/ffnvr-nvr01-cam1.service")
	if out, code := run("", "diff", "-c", cfg, "-root", root); code != 0 || !strings.Contains(out, "+++ /etc/systemd/system/ffnvr-nvr01-cam1.service") {
		t.Errorf("diff: %d\n%s", code, out)
	}
	if _, err := os.Stat(unit); err == nil {
		t.Error("diff must not write")
	}
	if out, code := run("", "apply", "-c", cfg, "-root", root); code != 0 || !strings.Contains(out, "skipped    systemctl enable --now ffnvr-nvr01-cam1.service") {
		t.Errorf("apply: %d\n%s", code, out)
	}
	if b, err := os.ReadFile(unit); err != nil || !strings.Contains(string(b), "-i rtsp://10.0.0.1/s ") {
		t.Errorf("unit: %v\n%s", err, b)
	}
	if out, code := run("help\nquit\n", "shell", "-c", empty, "-history", ""); code != 0 || !strings.Contains(out, "save [file]") {
		t.Errorf("shell: %d\n%s", code, out)
	}
}

func TestCLIDahua(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffnvr")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	root, cfg := filepath.Join(dir, "root"), filepath.Join(dir, "config.rsc")
	os.WriteFile(cfg, nil, 0o600)
	shell := exec.Command(bin, "shell", "-c", cfg, "-root", root, "-history", "")
	shell.Stdin = strings.NewReader(`/nvr add type="dahua" name="nvr02" host="` + fake.Addr() + `" username="admin" password="secret"` + "\n" +
		"/camera print\nsave\n")
	out, err := shell.CombinedOutput()
	if err != nil || !strings.Contains(out2s(out), "ch01: added, alias Brama_wjazdowa") || !strings.Contains(out2s(out), "alias=Brama_wjazdowa") {
		t.Fatalf("%v\n%s", err, out)
	}
	// the worker reads the configuration and refreshes the state
	os.RemoveAll(filepath.Join(root, "var/lib/ffnvr/status"))
	w := exec.Command(bin, "worker", "-c", cfg, "-root", root, "-status-every", "1h")
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(root, "var/lib/ffnvr/status/nvr02.json")
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(status); err == nil && strings.Contains(string(b), "DHI-NVR5216") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.Process.Signal(os.Interrupt)
	w.Wait()
	if b, err := os.ReadFile(status); err != nil || !strings.Contains(string(b), `"connected": 3`) {
		t.Errorf("status: %v\n%s", err, b)
	}
	// a configuration file cannot scan
	bad := filepath.Join(dir, "bad.rsc")
	os.WriteFile(bad, []byte("/nvr scan nvr02\n"), 0o644)
	if out, err := exec.Command(bin, "export", "-c", bad).CombinedOutput(); err == nil || !strings.Contains(string(out), "/nvr scan is not configuration") {
		t.Errorf("%v %s", err, out)
	}
}

func out2s(b []byte) string { return string(b) }

// the configuration of the request, as a file: the plain password becomes a hash on
// loading, and the export loads back to the same users
func TestCLIUsers(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffnvr")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cfg := filepath.Join(dir, "config.rsc")
	os.WriteFile(cfg, []byte(`/user/group
add group="firma1"
add group="firma2"

/user
add user="maciej" groups="firma1,firma2" password-hash="\$6\$qznl79kPr.SktcO6\$M8Kz7/WsqZstuCcZ5w7/hOo/2vXFuM3bbWpbPd86izZRSuh9aL1lc6qiAYkGw1m8aGws4r01bpIzkqOwGMzbQ/"
add user="jan" groups="firma2" password="secret" #zamieniane w locie w hash

/storage
add name=disk1 path=/srv/disk1

/nvr
add type=local name=nvr01 storage=disk1 valid-users="maciej,@firma2"
`), 0o600)
	out, err := exec.Command(bin, "export", "-c", cfg).CombinedOutput()
	if err != nil || strings.Contains(string(out), "secret") || !strings.Contains(string(out), `add user=jan groups=firma2 password-hash="\$6\$`) ||
		!strings.Contains(string(out), `add user=maciej groups=firma1,firma2 password-hash="\$6\$qznl79kPr.SktcO6\$M8Kz7/`) ||
		!strings.Contains(string(out), "valid-users=maciej,@firma2") {
		t.Fatalf("%v\n%s", err, out)
	}
	again := filepath.Join(dir, "again.rsc")
	os.WriteFile(again, out, 0o600)
	out2, err := exec.Command(bin, "export", "-c", again).CombinedOutput()
	if err != nil || string(out2) != string(out) {
		t.Errorf("the export loads back unchanged: %v\n%s\n%s", err, out, out2)
	}
	// the shell: the line with the password stays out of the history
	hist := filepath.Join(dir, "history")
	sh := exec.Command(bin, "shell", "-c", cfg, "-history", hist)
	sh.Stdin = strings.NewReader("/user set jan password=Tajne1\n/user print\n")
	if out, err := sh.CombinedOutput(); err != nil || strings.Contains(string(out), "Tajne1") {
		t.Errorf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(hist); strings.Contains(string(b), "Tajne1") {
		t.Errorf("history: %q", b)
	}
}

// /ip/service exported as RouterOS does, and loaded back
func TestCLIService(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffnvr")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cfg := filepath.Join(dir, "config.rsc")
	os.WriteFile(cfg, []byte("/ip/service/set [find name=www] enabled=true port=80\n/ip service\nset [ find name=www ] port=8080\n"), 0o600)
	out, err := exec.Command(bin, "export", "-c", cfg).CombinedOutput()
	if err != nil || string(out) != "/ip/service\nset [ find name=www ] port=8080 enabled=yes\n" {
		t.Fatalf("%v\n%s", err, out)
	}
	root := filepath.Join(dir, "root")
	out, err = exec.Command(bin, "diff", "-c", cfg, "-root", root).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "+ExecStart=/usr/local/bin/ffnvr web -listen :8080\n") {
		t.Errorf("%v\n%s", err, out)
	}
}
