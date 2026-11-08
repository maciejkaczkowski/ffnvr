package engine

import (
	"ffnvr/internal/script"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/module"
)

type testModule struct{ menus []*config.Menu }

func (m testModule) Name() string          { return "test" }
func (m testModule) Menus() []*config.Menu { return m.menus }

func menus() []*config.Menu {
	return []*config.Menu{
		{Path: "/system/identity", Kind: config.Single, Fields: []config.Field{{Name: "name"}}},
		{Path: "/camera", Kind: config.Multi, NameKey: "name", Fields: []config.Field{{Name: "name"}, {Name: "url"}}},
		{Path: "/record", Kind: config.Multi, Fields: []config.Field{{Name: "camera"}}},
	}
}

func TestLoadOrderAndExport(t *testing.T) {
	e, err := New(Options{Root: t.TempDir(), Modules: []module.Module{testModule{menus()}}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// file names sort "a" before "b", but commands are applied in menu registration order
	// (inside one menu the order of the files is kept)
	os.WriteFile(filepath.Join(dir, "a.rsc"), []byte("/record add camera=front\n/camera add name=front\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.rsc"), []byte("/camera\nset front url=rtsp://x\n/system/identity set name=nvr1\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("garbage"), 0o644)
	if err := e.LoadFiles([]string{dir}); err != nil {
		t.Fatal(err)
	}
	want := "/system/identity\nset name=nvr1\n\n/camera\nadd name=front url=rtsp://x\n\n/record\nadd camera=front\n"
	if got := e.Store.Export("/"); got != want {
		t.Errorf("export:\n%q\nwant\n%q", got, want)
	}
	// the export reads back to the same configuration
	e2, _ := New(Options{Modules: []module.Module{testModule{menus()}}})
	f := filepath.Join(t.TempDir(), "x.rsc")
	os.WriteFile(f, []byte(want), 0o644)
	if err := e2.LoadFiles([]string{f}); err != nil || e2.Store.Export("/") != want {
		t.Errorf("round trip: %v\n%s", err, e2.Store.Export("/"))
	}
}

func TestLoadErrors(t *testing.T) {
	e, _ := New(Options{Modules: []module.Module{testModule{menus()}}})
	f := filepath.Join(t.TempDir(), "x.rsc")
	os.WriteFile(f, []byte("\n/nonsense add a=b\n"), 0o644)
	if err := e.LoadFiles([]string{f}); err == nil || !strings.Contains(err.Error(), "x.rsc:2: unknown menu /nonsense") {
		t.Errorf("unknown menu: %v", err)
	}
	os.WriteFile(f, []byte("/camera add bogus=1\n"), 0o644)
	if err := e.LoadFiles([]string{f}); err == nil || !strings.Contains(err.Error(), "x.rsc:1:") {
		t.Errorf("bad parameter: %v", err)
	}
	if _, err := New(Options{Modules: []module.Module{testModule{menus()}, testModule{menus()[:1]}}}); err == nil {
		t.Error("a menu registered twice must be an error")
	}
}

func TestDefaultModulesLoadEmptyConfig(t *testing.T) {
	e, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "x.rsc")
	os.WriteFile(f, []byte("# empty\n"), 0o644)
	if err := e.LoadFiles([]string{f}); err != nil || e.Store.Export("/") != "" {
		t.Errorf("%v %q", err, e.Store.Export("/"))
	}
}

func TestApplyLinksTheBinary(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "ffnvr")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	e, _ := New(Options{Root: root, Exe: func() (string, error) { return bin, nil }, Run: func(...string) error { return nil }})
	apply := func() string {
		p, err := e.Plan()
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := e.Apply(p, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	link := filepath.Join(root, "usr/local/bin/ffnvr")
	if out := apply(); !strings.Contains(out, "linked     /usr/local/bin/ffnvr -> "+bin) {
		t.Errorf("%s", out)
	}
	if target, err := os.Readlink(link); err != nil || target != bin {
		t.Errorf("%s %v", target, err)
	}
	if out := apply(); strings.Contains(out, "linked") {
		t.Errorf("a working link stays: %s", out)
	}
	// the binary moved: the broken link is made again
	bin2 := filepath.Join(t.TempDir(), "ffnvr-new")
	os.WriteFile(bin2, nil, 0o755)
	os.Remove(bin)
	bin = bin2
	if out := apply(); !strings.Contains(out, "-> "+bin2) {
		t.Errorf("%s", out)
	}
	// an installed binary is left alone
	os.Remove(link)
	os.WriteFile(link, []byte("installed"), 0o755)
	if out := apply(); strings.Contains(out, "linked") {
		t.Errorf("%s", out)
	}
	if b, _ := os.ReadFile(link); string(b) != "installed" {
		t.Error("overwritten")
	}
	// a binary in a home directory: linked, with a warning
	os.Remove(link)
	e.Opts.Exe = func() (string, error) { return "/home/maciej/ffnvr", nil }
	if out := apply(); !strings.Contains(out, "warning: /home/maciej/ffnvr is under /home") {
		t.Errorf("%s", out)
	}
}

// A new ffnvr binary changes the units of ffnvr's own services: commit restarts the
// worker (and the web panel), which would otherwise keep running the old version.
func TestNewBinaryRestartsOwnServices(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "usr/local/bin/ffnvr")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("version 1"), 0o755)
	var calls []string
	e, _ := New(Options{Root: root, Run: func(a ...string) error { calls = append(calls, strings.Join(a, " ")); return nil }})
	p, _ := e.Plan()
	e.Apply(p, io.Discard)
	b, _ := os.ReadFile(filepath.Join(root, "etc/systemd/system/ffnvr-worker.service"))
	if !strings.Contains(string(b), "Type=simple\nEnvironment=FFNVR_BINARY=sha256:") {
		t.Fatalf("%s", b)
	}
	calls = nil
	if p, _ = e.Plan(); p.Changed() {
		t.Error("the same binary: nothing to do")
	}
	os.WriteFile(bin, []byte("version 2"), 0o755)
	p, _ = e.Plan()
	e.Apply(p, io.Discard)
	if !strings.Contains(strings.Join(calls, "|"), "systemctl restart ffnvr-worker.service") {
		t.Errorf("%v", calls)
	}
}

// The panel saves a view while the shell has the configuration open: the shell's save
// keeps the view, unless the shell changed views itself.
func TestConfigWriters(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.rsc")
	os.WriteFile(file, []byte("/user\nadd user=u1 password=x\n/storage\nadd name=d path=/srv/d\n/nvr\nadd type=local name=nvr01 storage=d\n"), 0o600)
	opts := Options{Root: t.TempDir()}
	shell, _ := New(opts)
	shell.LoadFiles([]string{file})
	loaded := ModTime(file)
	time.Sleep(20 * time.Millisecond)
	err := EditConfig(file, opts, func(s *config.Store) error {
		c, _, _ := script.ParseLine("/", `/camera/position add nvr=nvr01 camera=mon01 lat=1 long=2`)
		_, err := s.Exec(c)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "camera=mon01") {
		t.Fatalf("%s", b)
	}
	// the shell changes a storage and saves: the panel's position stays
	c, _, _ := script.ParseLine("/", `/storage set d comment=x`)
	shell.Store.Exec(c)
	if err := SaveConfig(file, shell.Store, opts, loaded, map[string]bool{"/storage": true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), "camera=mon01") || !strings.Contains(string(b), "comment=x") {
		t.Errorf("%s", b)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Error(st.Mode())
	}
}
