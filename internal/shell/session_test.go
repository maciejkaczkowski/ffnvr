package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runShell(t *testing.T, root, input string) string {
	t.Helper()
	e := newEngine(t, root)
	var out strings.Builder
	if err := Run(e, strings.NewReader(input), &out, Options{}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestSessionPlainCommands(t *testing.T) {
	out := runShell(t, t.TempDir(), strings.Join([]string{
		"/system/clock",
		"set time-zone-name=UTC",
		"print",
		"/camera",
		`add name=front url="rtsp://10.0.0.20/s1" transport=tcp`,
		"print",
		"/nonsense",
		"bogus-command",
		"quit",
		"print", // never reached
	}, "\n")+"\n")
	for _, want := range []string{"time-zone-name: UTC", "0  name=front url=rtsp://10.0.0.20/s1 transport=tcp", "error: no such menu: /nonsense"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Count(out, "name=front") != 1 {
		t.Errorf("commands after quit must not run:\n%s", out)
	}
}

func TestSessionValidationAndFind(t *testing.T) {
	out := runShell(t, t.TempDir(), strings.Join([]string{
		"/camera add name=a fps=x",
		"/camera add name=a transport=ftp",
		"/camera add name=a bogus=1",
		"/camera add name=a events=motion,line",
		"/camera set [find name=nobody] fps=5",
		"/camera disable a",
		"/camera print",
		"/system/clock add x=1",
	}, "\n")+"\n")
	for _, want := range []string{`fps: "x" is not an integer`, `transport: "ftp" is not one of`, `unknown parameter "bogus"`,
		"note: /camera: set [find name=nobody] matched no item", "0  name=a events=motion,line disabled=yes", "'add' is not available here"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestSessionQuestionMarkHelp(t *testing.T) {
	out := runShell(t, t.TempDir(), "/camera\nadd ?\nadd transport=?\nadd comment=\"why?\"\n?\nquit\n")
	for _, want := range []string{"name", "transport", "tcp", "udp", "add", "print"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "error") {
		t.Errorf("a ? inside quotes is text, not help:\n%s", out)
	}
	out = runShell(t, t.TempDir(), "/camera\nadd name=x comment=\"why?\"\nprint\n")
	if !strings.Contains(out, `comment="why?"`) && !strings.Contains(out, "comment=why?") {
		t.Errorf("comment lost:\n%s", out)
	}
}

func TestSessionHistoryHidesSecrets(t *testing.T) {
	out := runShell(t, t.TempDir(), strings.Join([]string{
		"/camera",
		`add name=front password="StrongPass123!"`,
		"print",
		"/system/clock set time-zone-name=UTC",
		"history",
	}, "\n")+"\n")
	hist := out[strings.Index(out, "   1  "):]
	if strings.Contains(out[:strings.Index(out, "   1  ")], "StrongPass123") || !strings.Contains(out, "password=*****") {
		t.Errorf("print must mask the secret:\n%s", out)
	}
	for _, kept := range []string{"/camera", "time-zone-name=UTC"} {
		if !strings.Contains(hist, kept) {
			t.Errorf("%q is a normal line and stays in the history:\n%s", kept, hist)
		}
	}
	if strings.Contains(hist, "name=front") {
		t.Errorf("a line with a secret must not be in the history:\n%s", hist)
	}
}

func TestSessionDirtyMarkerAndSave(t *testing.T) {
	f := filepath.Join(t.TempDir(), "c.rsc")
	e := newEngine(t, t.TempDir())
	s := &session{e: e, out: &strings.Builder{}, cur: "/", opts: Options{SaveFile: f}, comp: &Completer{Store: e.Store}}
	if strings.Contains(s.prompt(), "*") {
		t.Fatal("a fresh session is clean")
	}
	s.handle("/system/clock set time-zone-name=UTC")
	if !strings.HasPrefix(s.prompt(), "[ffnvr*]") {
		t.Errorf("unsaved changes must be marked: %q", s.prompt())
	}
	s.handle("print")
	s.handle("export")
	if !strings.HasPrefix(s.prompt(), "[ffnvr*]") {
		t.Error("print/export must not clear the marker")
	}
	s.handle("save")
	if strings.Contains(s.prompt(), "*") {
		t.Errorf("save clears the marker: %q (%s)", s.prompt(), s.out.(*strings.Builder).String())
	}
	if b, err := os.ReadFile(f); err != nil || string(b) != "/system/clock\nset time-zone-name=UTC\n" {
		t.Errorf("save must write the file: %q %v", b, err)
	}
	s.handle("/system/clock set bogus=1")
	if strings.Contains(s.prompt(), "*") {
		t.Error("an error is not a change")
	}
	s.handle("/system/clock")
	if s.cur != "/system/clock" || !strings.Contains(s.prompt(), "/system/clock>") {
		t.Errorf("prompt: %q", s.prompt())
	}
}

func TestSessionExportLevels(t *testing.T) {
	out := runShell(t, t.TempDir(), strings.Join([]string{
		"/system/identity set name=nvr1",
		"/system/clock set time-zone-name=UTC",
		"/camera add name=front",
		"/system export",
		"/system print",
		"/recording set enabled=yes",
		"/record add camera=front mode=events",
		"/record export",
	}, "\n")+"\n")
	for _, want := range []string{
		"/system/clock\nset time-zone-name=UTC\n\n/system/identity\nset name=nvr1\n",
		"only 'export' works here",
		"/record\nset enabled=yes\nadd camera=front mode=events\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestSessionSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "saved.rsc")
	runShell(t, t.TempDir(), "/camera add name=front comment=\"a b\"\n/record set enabled=yes\nsave "+f+"\n")
	st, err := os.Stat(f)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("saved file must exist with 0600: %v %v", st, err)
	}
	out := runShell(t, t.TempDir(), "load "+f+"\n/camera print\n/ export\n")
	if !strings.Contains(out, `name=front comment="a b"`) || !strings.Contains(out, "/record\nset enabled=yes") {
		t.Errorf("load: %s", out)
	}
	if out := runShell(t, t.TempDir(), "load\n"); !strings.Contains(out, "usage: load") {
		t.Errorf("usage: %s", out)
	}
	if out := runShell(t, t.TempDir(), "save\n"); !strings.Contains(out, "error: no file given") {
		t.Errorf("save without a file: %s", out)
	}
}

func TestHistoryFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h")
	appendHistory(p, "one")
	appendHistory(p, "two")
	if got := loadHistory(p); strings.Join(got, "|") != "one|two" {
		t.Errorf("got %v", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("history must be private: %v", st.Mode())
	}
	if loadHistory("") != nil || loadHistory(filepath.Join(t.TempDir(), "missing")) != nil {
		t.Error("no file, no history")
	}
	var b strings.Builder
	for i := 0; i < 2500; i++ {
		b.WriteString("line\n")
	}
	os.WriteFile(p, []byte(b.String()), 0o600)
	if got := loadHistory(p); len(got) != 1000 {
		t.Errorf("trimmed to %d", len(got))
	}
}
