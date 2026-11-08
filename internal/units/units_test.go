package units

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, content string) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	if err := os.WriteFile(filepath.Join(root, Dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanAndApply(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ffnvr-n-same.service", "same\n")
	write(t, root, "ffnvr-n-upd.service", "old\n")
	write(t, root, "ffnvr-n-gone.service", "gone\n")
	write(t, root, "other.service", "not ours\n")
	p, err := NewPlan(root, []Unit{{"ffnvr-n-same.service", "same\n"}, {"ffnvr-n-upd.service", "new\n"}, {"ffnvr-n-add.service", "add\n"}})
	if err != nil {
		t.Fatal(err)
	}
	var diff strings.Builder
	p.WriteDiff(&diff)
	for _, want := range []string{"+++ /etc/systemd/system/ffnvr-n-add.service\n@@ -0,0 +1,1 @@\n+add", "-old\n+new",
		"--- /etc/systemd/system/ffnvr-n-gone.service\n+++ /dev/null", "ffnvr-n-same.service: unchanged",
		"# would run: systemctl disable --now ffnvr-n-gone.service\n# would run: systemctl daemon-reload\n" +
			"# would run: systemctl enable --now ffnvr-n-add.service ffnvr-n-same.service ffnvr-n-upd.service\n" +
			"# would run: systemctl restart ffnvr-n-upd.service\n"} {
		if !strings.Contains(diff.String(), want) {
			t.Errorf("diff misses %q:\n%s", want, diff.String())
		}
	}
	if strings.Contains(diff.String(), "other.service") {
		t.Error("foreign units are not ours")
	}
	var calls []string
	var log strings.Builder
	err = p.Apply(&log, func(args ...string) error {
		// the unit being disabled must still exist
		if args[1] == "disable" {
			if _, err := os.Stat(filepath.Join(root, Dir, "ffnvr-n-gone.service")); err != nil {
				t.Error("disable must run before the file is removed")
			}
		}
		calls = append(calls, strings.Join(args, " "))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || calls[1] != "systemctl daemon-reload" {
		t.Errorf("calls: %q", calls)
	}
	for name, want := range map[string]string{"ffnvr-n-add.service": "add\n", "ffnvr-n-upd.service": "new\n", "other.service": "not ours\n"} {
		if b, _ := os.ReadFile(filepath.Join(root, Dir, name)); string(b) != want {
			t.Errorf("%s: %q", name, b)
		}
	}
	if _, err := os.Stat(filepath.Join(root, Dir, "ffnvr-n-gone.service")); err == nil {
		t.Error("gone must be removed")
	}
	// now in sync: no file changes, no reload, units only (re)enabled
	p, _ = NewPlan(root, []Unit{{"ffnvr-n-same.service", "same\n"}, {"ffnvr-n-upd.service", "new\n"}, {"ffnvr-n-add.service", "add\n"}})
	if p.Changed() || len(p.commands()) != 1 {
		t.Errorf("in sync: %v", p.commands())
	}
}

func TestPlanRejects(t *testing.T) {
	if _, err := NewPlan(t.TempDir(), []Unit{{"x.service", ""}}); err == nil {
		t.Error("units must be named ffnvr-*.service")
	}
	if _, err := NewPlan(t.TempDir(), []Unit{{"ffnvr-a.service", ""}, {"ffnvr-a.service", ""}}); err == nil {
		t.Error("duplicate unit")
	}
	p, _ := NewPlan(t.TempDir(), nil)
	var log strings.Builder
	if err := p.Apply(&log, nil); err != nil || log.String() != "" {
		t.Errorf("empty plan: %v %q", err, log.String())
	}
}

func TestManagedFile(t *testing.T) {
	root := t.TempDir()
	want := []Unit{{"ffnvr-go2rtc.service", "unit\n"}, {"/etc/ffnvr/go2rtc.yaml", "secret: yes\n"}}
	p, err := NewPlan(root, want)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	var log strings.Builder
	p.Apply(&log, func(a ...string) error { calls = append(calls, strings.Join(a, " ")); return nil })
	st, err := os.Stat(filepath.Join(root, "etc/ffnvr/go2rtc.yaml"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("%v %v", st, err)
	}
	if strings.Join(calls, "|") != "systemctl daemon-reload|systemctl enable --now ffnvr-go2rtc.service" {
		t.Errorf("no systemctl for the file: %v", calls)
	}
	if !strings.Contains(log.String(), "written    /etc/ffnvr/go2rtc.yaml") {
		t.Error(log.String())
	}
	// gone from the configuration: removed
	p, _ = NewPlan(root, nil)
	p.Apply(&log, nil)
	if _, err := os.Stat(filepath.Join(root, "etc/ffnvr/go2rtc.yaml")); err == nil {
		t.Error("removed")
	}
	if _, err := NewPlan(root, []Unit{{"/etc/passwd", "x"}}); err == nil {
		t.Error("only managed files")
	}
}
