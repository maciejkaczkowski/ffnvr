package script

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	src := `# comment
/storage
add type=partition slot="/dev/x y" \
  options="a,b" # trailing
/system/ntp/client/servers
set 0 iburst=true
/record rule remove [ find comment="a b" ]
`
	cmds, err := Parse(strings.NewReader(src), "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 {
		t.Fatalf("got %d commands", len(cmds))
	}
	c := cmds[0]
	if c.Path != "/storage" || c.Verb != "add" || c.Params[1].Value != "/dev/x y" || c.Params[2].Value != "a,b" || c.Line != 3 {
		t.Errorf("bad cmd0: %+v", c)
	}
	if c := cmds[1]; c.Path != "/system/ntp/client/servers" || len(c.Args) != 1 || c.Args[0] != "0" {
		t.Errorf("bad cmd1: %+v", c)
	}
	if c := cmds[2]; c.Path != "/record/rule" || !c.HasFind || c.Find[0].Value != "a b" {
		t.Errorf("bad cmd2: %+v", c)
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{"/x set a=\"b", "foo=bar", "/x remove [find a=b"} {
		if _, err := Parse(strings.NewReader(s), "t"); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func TestParseVerbAsLastPathSegment(t *testing.T) {
	src := "/camera/set enabled=yes\n/record/rule/add name=front\n/camera/stream\nadd name=x\n/camera/stream/remove 0\n"
	cmds, err := Parse(strings.NewReader(src), "t")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ path, verb string }{{"/camera", "set"}, {"/record/rule", "add"}, {"/camera/stream", "add"}, {"/camera/stream", "remove"}}
	if len(cmds) != len(want) {
		t.Fatalf("got %d commands: %+v", len(cmds), cmds)
	}
	for i, w := range want {
		if cmds[i].Path != w.path || cmds[i].Verb != w.verb {
			t.Errorf("cmd %d: %s %s, want %s %s", i, cmds[i].Path, cmds[i].Verb, w.path, w.verb)
		}
	}
	if cmds[0].Params[0].Key != "enabled" || cmds[3].Args[0] != "0" {
		t.Errorf("params/args lost: %+v %+v", cmds[0], cmds[3])
	}
}

func TestFindNameShorthand(t *testing.T) {
	for _, line := range []string{"/camera set [ find=front ] url=/dev/video0", "/camera set [find front] url=/dev/video0"} {
		cmds, err := Parse(strings.NewReader(line), "t")
		if err != nil {
			t.Fatal(err)
		}
		c := cmds[0]
		if !c.HasFind || len(c.Find) != 1 || c.Find[0].Key != "" || c.Find[0].Value != "front" || c.Params[0].Value != "/dev/video0" {
			t.Errorf("%s: %+v", line, c)
		}
	}
}
