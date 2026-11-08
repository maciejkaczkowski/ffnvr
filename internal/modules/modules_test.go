package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/config"
	"ffnvr/internal/script"
)

func newStore(t *testing.T) *config.Store {
	t.Helper()
	s := config.NewStore()
	for _, m := range All() {
		for _, menu := range m.Menus() {
			s.Register(menu)
		}
	}
	return s
}

func cmd(t *testing.T, line string) script.Command {
	t.Helper()
	c, _, err := script.ParseLine("/", line)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// run executes lines and returns the error of each one ("" for success).
func run(t *testing.T, s *config.Store, lines ...string) []string {
	t.Helper()
	var errs []string
	for _, l := range lines {
		cmd, _, err := script.ParseLine("/", l)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if _, err := s.Exec(cmd); err != nil {
			errs = append(errs, err.Error())
		} else {
			errs = append(errs, "")
		}
	}
	return errs
}

func expect(t *testing.T, s *config.Store, line, want string) {
	t.Helper()
	got := run(t, s, line)[0]
	if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
		t.Errorf("%s:\n  got  %q\n  want %q", line, got, want)
	}
}

func TestStorageRequiredAndChecks(t *testing.T) {
	s := newStore(t)
	expect(t, s, `/storage add name="disk1"`, "/storage: missing required parameter path")
	expect(t, s, `/storage add path="/srv/disk1"`, "/storage: missing required parameter name")
	expect(t, s, `/storage add comment=x`, "missing required parameters name, path")
	expect(t, s, `/storage add name=disk1 path=srv/disk1`, "must be absolute")
	expect(t, s, `/storage add name=disk1 path=/`, "path / is not allowed")
	expect(t, s, `/storage add name="disk1" path="/srv/disk1/"`, "")
	expect(t, s, `/storage add name=disk1 path=/srv/other`, "name=disk1 already exists")
	expect(t, s, `/storage add name=disk2 path=/srv//disk1`, "path=/srv/disk1 is already used by disk1")
	expect(t, s, `/storage set disk1 path=""`, "missing required parameter path")
	expect(t, s, `/storage unset disk1 name`, "missing required parameter name")
	if got := s.Export("/"); got != "/storage\nadd name=disk1 path=/srv/disk1\n" {
		t.Errorf("rejected commands must not change anything, path is normalised:\n%s", got)
	}
}

func TestNvrLocal(t *testing.T) {
	s := newStore(t)
	run(t, s, `/storage add name=disk1 path=/srv/disk1`, `/storage add name=disk2 path=/srv/disk2`)
	expect(t, s, `/nvr add name=nvr01 storage=disk1`, "missing required parameter type")
	expect(t, s, `/nvr add type=remote name=nvr01`, `"remote" is not one of: local`)
	expect(t, s, `/nvr add type=local storage=disk1`, "type=local needs parameter name")
	expect(t, s, `/nvr add type=local name=nvr01`, "type=local needs parameter storage")
	expect(t, s, `/nvr add type=local`, "type=local needs parameters name, storage")
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk9`, `storage: "disk9" does not exist in /storage`)
	expect(t, s, `/nvr add type="local" name="nvr01" storage="disk1"`, "")
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk2`, "name=nvr01 already exists")
	expect(t, s, `/nvr unset nvr01 storage`, "type=local needs parameter storage")
	expect(t, s, `/nvr set nvr01 storage=disk2`, "")
	// a storage in use can be neither removed nor renamed; a free one can
	expect(t, s, `/storage remove disk2`, "/storage: name=disk2 is used by /nvr nvr01")
	expect(t, s, `/storage set disk2 name=disk3`, "name=disk2 is used by /nvr nvr01")
	expect(t, s, `/storage set disk2 path=/srv/d2`, "")
	expect(t, s, `/storage remove disk1`, "")
	expect(t, s, `/nvr disable nvr01`, "")
	want := "/storage\nadd name=disk2 path=/srv/d2\n\n/nvr\nadd type=local name=nvr01 storage=disk2 disabled=yes\n"
	if got := s.Export("/"); got != want {
		t.Errorf("export:\n%s\nwant\n%s", got, want)
	}
	// after the nvr is gone the storage is free
	expect(t, s, `/nvr remove nvr01`, "")
	expect(t, s, `/storage remove disk2`, "")
}
