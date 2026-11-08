package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/script"
)

func TestPolicyMatching(t *testing.T) {
	for _, c := range []struct {
		allow, deny string
		yes, no     []string
	}{
		{"live,archive", "", []string{"live", "archive"}, []string{"live.map", "live.views", "archive.export", "system"}},
		{"live.*", "", []string{"live", "live.views", "live.map", "live.map.edit"}, []string{"archive"}},
		{"live,live.map,live.map.edit", "", []string{"live", "live.map", "live.map.edit"}, []string{"live.views"}},
		// the more specific entry wins, both ways
		{"*", "system.nvr.reboot", []string{"live.map.edit", "system", "system.nvr.backup"}, []string{"system.nvr.reboot"}},
		{"*", "system.nvr.*", []string{"system", "system.logs"}, []string{"system.nvr.reboot", "system.nvr.backup"}},
		{"*,system.nvr.backup", "system.nvr.*", []string{"system.nvr.backup"}, []string{"system.nvr.reboot"}},
		{"live.*,archive.*", "archive.download,live.map.*", []string{"archive.export", "live.views"}, []string{"archive.download", "live.map", "live.map.edit"}},
		// a denied parent takes its children
		{"*", "live", []string{"archive"}, []string{"live", "live.views", "live.map.edit"}},
		{"live.map.edit", "", nil, []string{"live", "live.map", "live.map.edit"}},
		// same depth: deny wins
		{"live.*", "live.*", nil, []string{"live"}},
		{"", "", nil, []string{"live", "archive", "system"}},
	} {
		p := Policy{Allow: splitList(c.allow), Deny: splitList(c.deny)}
		for _, x := range c.yes {
			if !p.Can(x) {
				t.Errorf("allow=%q deny=%q: %s should be granted", c.allow, c.deny, x)
			}
		}
		for _, x := range c.no {
			if p.Can(x) {
				t.Errorf("allow=%q deny=%q: %s should be denied", c.allow, c.deny, x)
			}
		}
	}
}

func TestPolicyMenu(t *testing.T) {
	s := newStore(t)
	for i, err := range run(t, s,
		`/user/policy add name="read-only" allow="live,archive" comment="podgląd"`,
		`/user/policy add name=operator allow="live.*,archive.*,system" deny=archive.download`,
		`/user/policy add name=admin allow=*`,
		`/user add user=maciej password=a policy=admin`,
		`/user add user=jan password=b policy=read-only`,
		`/user add user=ola password=c`,
		`/user add user=ewa password=d policy=admin disabled=yes`,
	) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	exp := s.Export("/")
	for _, want := range []string{
		"/user/policy\nadd name=read-only allow=live,archive comment=podgląd\n",
		"add name=operator allow=live.*,archive.*,system deny=archive.download\n",
		"add name=admin allow=*\n",
		"add user=jan policy=read-only password-hash=",
	} {
		if !strings.Contains(exp, want) {
			t.Errorf("export lacks %q:\n%s", want, exp)
		}
	}
	// policies come before the users naming them: the export loads again
	s2, cur := newStore(t), "/"
	for _, l := range strings.Split(exp, "\n") {
		c, path, err := script.ParseLine(cur, l)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if cur = path; c.Verb == "" {
			continue
		}
		if _, err := s2.Exec(c); err != nil {
			t.Fatalf("%s: %v\n%s", l, err, exp)
		}
	}
	if got := UserRights(s2, "jan"); !got[PermLive] || got[PermLiveMap] {
		t.Errorf("after reload: %v", got)
	}

	if r := UserRights(s, "jan"); !r[PermLive] || !r[PermArchive] || r[PermSystem] || r[PermArchiveExp] {
		t.Errorf("jan: %v", r)
	}
	if r := UserRights(s, "ola"); r.Any() || len(r) != 0 {
		t.Errorf("without policy no rights: %v", r)
	}
	expect(t, s, `/user/policy add name=full allow="live.*,archive.*,system.*"`, "")
	if r := (Policy{Allow: []string{"live.*", "archive.*", "system.*"}}).Rights(); len(r) != len(Permissions) {
		t.Errorf("live.*,archive.*,system.* is everything: %v", r)
	}
	if r := UserRights(s, "ewa"); r.Any() {
		t.Errorf("disabled: %v", r)
	}
	if r := UserRights(s, "nikt"); r.Any() {
		t.Errorf("unknown: %v", r)
	}

	expect(t, s, `/user/policy add name=x allow=live.foo`, `allow: "live.foo" is not one of`)
	expect(t, s, `/user/policy add name=x allow=live.map.edit`, "allow: live.map.edit has no effect without live, live.map (allow it too, or use live.*)")
	expect(t, s, `/user/policy add name=x allow=live,live.map.edit`, "allow: live.map.edit has no effect without live.map")
	expect(t, s, `/user/policy add name=x allow=live deny=live`, "live is both in allow and in deny")
	expect(t, s, `/user/policy add name=x allow=live,live.map.edit deny=live.map`, "allow: live.map.edit has no effect, deny=live.map takes live.map")
	expect(t, s, `/user/policy add name=x allow=live.* deny=live.map`, "") // the branch with an exception
	expect(t, s, `/user set jan policy=nic`, `policy: "nic" does not exist in /user/policy`)
	expect(t, s, `/user/policy remove read-only`, "/user/policy: name=read-only is used by /user jan")
	expect(t, s, `/user/policy set operator allow=*`, "")
	if r := UserRights(s, "nikt"); r.Any() {
		t.Errorf("unknown: %v", r)
	}

	out, err := s.Exec(cmd(t, `/user rights jan`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"user=jan policy=read-only\n", "  live ", "    live.map.edit ", "    system.nvr.reboot "} {
		if !strings.Contains(out, want) {
			t.Errorf("rights lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "live.views") || !regexpLine(out, "live", "yes") || !regexpLine(out, "system", "no") {
		t.Errorf("rights:\n%s", out)
	}
	out, _ = s.Exec(cmd(t, `/user rights ola`))
	if !strings.Contains(out, "policy=- (none: no rights)") || !regexpLine(out, "live", "no") || !regexpLine(out, "system.nvr.reboot", "no") {
		t.Errorf("ola:\n%s", out)
	}
	out, _ = s.Exec(cmd(t, `/user/policy rights operator`))
	if !strings.HasPrefix(out, "policy=operator\n") || !regexpLine(out, "archive.download", "no") || !regexpLine(out, "system.nvr.reboot", "yes") {
		t.Errorf("policy rights:\n%s", out)
	}
}

// regexpLine: a line of the rights table names the right with the value.
func regexpLine(out, perm, val string) bool {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == perm && f[1] == val {
			return true
		}
	}
	return false
}
