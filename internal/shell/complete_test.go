package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffnvr/internal/engine"
	"ffnvr/internal/script"
)

func newCompleter(t *testing.T) (*Completer, *engine.Engine, string) {
	t.Helper()
	root := t.TempDir()
	e := newEngine(t, root)
	return &Completer{Store: e.Store, Root: root}, e, root
}

func exec(t *testing.T, e *engine.Engine, lines ...string) {
	t.Helper()
	for _, l := range lines {
		cmd, _, err := script.ParseLine("", l)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.Exec(cmd); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
	}
}

func texts(c []Candidate) string {
	var out []string
	for _, x := range c {
		out = append(out, x.Text)
	}
	return strings.Join(out, " ")
}

func has(c []Candidate, want ...string) bool {
	set := map[string]bool{}
	for _, x := range c {
		set[x.Text] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// apply performs a unique completion on line the way the editor does.
func apply(line string, start int, c Candidate) string {
	r := []rune(line)
	return string(r[:start]) + c.Text + c.Suffix
}

func TestCompletePaths(t *testing.T) {
	c, _, _ := newCompleter(t)
	_, top := c.Complete("/", "")
	if !has(top, "system", "camera", "record", "log", "save", "load", "quit", "diff", "commit") {
		t.Errorf("root: %s", texts(top))
	}
	if has(top, "add", "set") {
		t.Errorf("root is not a menu, verbs must not be offered: %s", texts(top))
	}
	if !has(top, "export") {
		t.Errorf("export works on every level: %s", texts(top))
	}
	start, cs := c.Complete("/", "/system cl")
	if len(cs) != 1 || apply("/system cl", start, cs[0]) != "/system clock " {
		t.Errorf("relative: %v start=%d", cs, start)
	}
	start, cs = c.Complete("/", "/system/cl")
	if len(cs) != 1 || apply("/system/cl", start, cs[0]) != "/system/clock " {
		t.Errorf("absolute: %v start=%d", cs, start)
	}
	_, cs = c.Complete("/system", "")
	if !has(cs, "clock", "identity", "..", "export") || has(cs, "add", "set") {
		t.Errorf("group level: %s", texts(cs))
	}
	// a menu with submenus offers both
	_, cs = c.Complete("/camera", "")
	if !has(cs, "group", "add", "set", "remove", "unset", "enable", "disable", "print", "export") {
		t.Errorf("camera: %s", texts(cs))
	}
	_, cs = c.Complete("/system/clock", "")
	if !has(cs, "set", "print") || has(cs, "add", "remove") {
		t.Errorf("singleton verbs: %s", texts(cs))
	}
	// menus without "disabled" have no enable/disable
	if _, cs = c.Complete("/log", ""); has(cs, "enable") || !has(cs, "add") {
		t.Errorf("log: %s", texts(cs))
	}
	// alias path resolves like the canonical one
	if _, cs = c.Complete("/", "/recording "); !has(cs, "set", "add") {
		t.Errorf("alias: %s", texts(cs))
	}
	if _, cs = c.Complete("/", "/nonsense "); len(cs) != 0 {
		t.Errorf("unknown menu: %s", texts(cs))
	}
	if _, cs = c.Complete("/", "name=x"); len(cs) != 0 {
		t.Errorf("param without verb: %s", texts(cs))
	}
}

func TestCompleteParametersAndValues(t *testing.T) {
	c, _, _ := newCompleter(t)
	cur := "/camera"
	start, cs := c.Complete(cur, "add tr")
	if len(cs) != 1 || apply("add tr", start, cs[0]) != "add transport=" {
		t.Errorf("param name: %v", cs)
	}
	if _, cs = c.Complete(cur, "add name=x "); has(cs, "name") || !has(cs, "url", "transport") {
		t.Errorf("used: %s", texts(cs))
	}
	start, cs = c.Complete(cur, "add transport=u")
	if len(cs) != 1 || apply("add transport=u", start, cs[0]) != "add transport=udp " {
		t.Errorf("enum: %v start=%d", cs, start)
	}
	if _, cs = c.Complete(cur, "add disabled="); texts(cs) != "yes no" {
		t.Errorf("bool: %s", texts(cs))
	}
	start, cs = c.Complete(cur, "add events=motion,t")
	if len(cs) != 1 || apply("add events=motion,t", start, cs[0]) != "add events=motion,tamper" {
		t.Errorf("list: %v start=%d", cs, start)
	}
	if _, cs = c.Complete(cur, "add events=motion,"); has(cs, "motion") || !has(cs, "tamper", "line") {
		t.Errorf("list skip: %s", texts(cs))
	}
	if _, cs = c.Complete(cur, "add comment="); len(cs) != 0 {
		t.Errorf("free text: %s", texts(cs))
	}
	if _, cs = c.Complete(cur, "add bogus="); len(cs) != 0 {
		t.Errorf("unknown key: %s", texts(cs))
	}
	if _, cs = c.Complete(cur, `add comment="na`); len(cs) != 0 {
		t.Errorf("quote: %s", texts(cs))
	}
	if _, cs = c.Complete(cur, "print transport="); len(cs) != 0 {
		t.Errorf("print: %s", texts(cs))
	}
}

func TestCompleteSelectorsAndHybridMenu(t *testing.T) {
	c, e, _ := newCompleter(t)
	exec(t, e,
		"/log add topic=a comment=first",
		"/log add topic=b",
		"/camera add name=front",
		"/camera add name=back",
		"/record add camera=front mode=events",
	)
	_, cs := c.Complete("/log", "remove ")
	if texts(cs) != "0 1" || !strings.Contains(cs[0].Help, "comment=first") {
		t.Errorf("numbers: %v", cs)
	}
	if _, cs = c.Complete("/camera", "set "); !has(cs, "front", "back") {
		t.Errorf("names: %s", texts(cs))
	}
	if _, cs = c.Complete("/camera", "disable b"); texts(cs) != "back" {
		t.Errorf("prefix: %s", texts(cs))
	}
	if _, cs = c.Complete("/camera", "set back "); !has(cs, "url", "fps") || has(cs, "back") {
		t.Errorf("params after selector: %s", texts(cs))
	}
	// hybrid /record: settings and item numbers together, then item fields only
	if _, cs = c.Complete("/record", "set "); !has(cs, "enabled", "path", "0") {
		t.Errorf("hybrid: %s", texts(cs))
	}
	if _, cs = c.Complete("/record", "set 0 "); !has(cs, "mode", "retention") || has(cs, "enabled") {
		t.Errorf("hybrid item: %s", texts(cs))
	}
	if _, cs = c.Complete("/record", "set enabled="); texts(cs) != "yes no" {
		t.Errorf("hybrid setting value: %s", texts(cs))
	}
	_, cs = c.Complete("/system/clock", "unset ti")
	if len(cs) != 1 || cs[0].Suffix != " " {
		t.Errorf("unset: %v", cs)
	}
}

func TestCompleteReferences(t *testing.T) {
	c, e, root := newCompleter(t)
	os.MkdirAll(filepath.Join(root, "sys/class/net/eth9"), 0o755)
	os.MkdirAll(filepath.Join(root, "usr/share/zoneinfo/Europe"), 0o755)
	os.MkdirAll(filepath.Join(root, "usr/share/zoneinfo/posix/Europe"), 0o755)
	for _, f := range []string{"Europe/Warsaw", "Europe/Paris", "UTC", "zone.tab", "posix/Europe/Warsaw"} {
		os.WriteFile(filepath.Join(root, "usr/share/zoneinfo", f), nil, 0o644)
	}
	exec(t, e, "/camera add name=front", "/camera add name=back")
	check := func(cur, line string, want ...string) {
		t.Helper()
		if _, cs := c.Complete(cur, line); !has(cs, want...) {
			t.Errorf("%q: want %v in %s", line, want, texts(cs))
		}
	}
	check("/camera", "add interface=", "eth9")
	check("/record", "add camera=", "front", "back")
	check("/camera/group", "add cameras=front,", "back")
	_, cs := c.Complete("/system/clock", "set time-zone-name=Eu")
	if texts(cs) != "Europe/Paris Europe/Warsaw" {
		t.Errorf("tz: %s", texts(cs))
	}
	if _, cs = c.Complete("/system/clock", "set time-zone-name="); has(cs, "zone.tab", "posix/Europe/Warsaw") || !has(cs, "UTC") {
		t.Errorf("tz all: %s", texts(cs))
	}
}

func TestHelpHintAndFiles(t *testing.T) {
	c, _, _ := newCompleter(t)
	h := c.Help("/camera", "add fps=")
	if len(h) != 1 || !strings.Contains(h[0].Text, "<integer>") {
		t.Errorf("hint: %v", h)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.rsc"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "b.rsc"), nil, 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	_, cs := c.Complete("/", "load "+filepath.Join(dir, "a"))
	if len(cs) != 1 || !strings.HasSuffix(cs[0].Text, "a.rsc") {
		t.Errorf("files: %v", cs)
	}
	_, cs = c.Complete("/", "save "+dir+"/s")
	if len(cs) != 1 || !strings.HasSuffix(cs[0].Text, "sub/") || cs[0].Suffix != "" {
		t.Errorf("dirs keep the cursor attached: %v", cs)
	}
}

func TestSplitLine(t *testing.T) {
	w, p, s, q := splitLine(`add comment="a b" chain=in`)
	if strings.Join(w, "|") != `add|comment="a b"` || p != "chain=in" || s != 18 || q {
		t.Errorf("%q %q %d %v", w, p, s, q)
	}
	_, p, _, q = splitLine(`add comment="a b`)
	if !q || p != `comment="a b` {
		t.Errorf("open quote: %q %v", p, q)
	}
	w, p, s, _ = splitLine("add ")
	if len(w) != 1 || p != "" || s != 4 {
		t.Errorf("trailing space: %q %q %d", w, p, s)
	}
	_, p, _, q = splitLine(`x "a\"b" y`)
	if q || p != "y" {
		t.Errorf("escaped quote: %q %v", p, q)
	}
}
