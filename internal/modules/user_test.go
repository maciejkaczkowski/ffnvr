package modules

import (
	"strings"
	"testing"
	"time"

	"ffnvr/internal/pwhash"
)

const maciejHash = `$6$qznl79kPr.SktcO6$M8Kz7/WsqZstuCcZ5w7/hOo/2vXFuM3bbWpbPd86izZRSuh9aL1lc6qiAYkGw1m8aGws4r01bpIzkqOwGMzbQ/`

func TestUsers(t *testing.T) {
	s := newStore(t)
	for i, err := range run(t, s,
		`/user/group add group="firma1"`,
		`/user/group add group="firma2"`,
		`/user add user="maciej" groups="firma1,firma2" password-hash="\`+strings.ReplaceAll(maciejHash, "$", `\$`)[1:]+`"`,
		`/user add user="jan" groups="firma2" password="secret"`,
	) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	exp := s.Export("/")
	if strings.Contains(exp, "secret") || strings.Contains(exp, "password=") {
		t.Errorf("the password must not be kept:\n%s", exp)
	}
	want := `add user=maciej groups=firma1,firma2 password-hash="\` + strings.ReplaceAll(maciejHash, "$", `\$`)[1:] + "\"\n"
	if !strings.Contains(exp, "/user/group\nadd group=firma1\nadd group=firma2\n") || !strings.Contains(exp, want) {
		t.Errorf("export:\n%s\nwant\n%s", exp, want)
	}
	jan := find(s, "/user", "user", "jan")
	if h := jan.Val("password-hash"); !strings.HasPrefix(h, "$6$") || !pwhash.Verify("secret", h) {
		t.Errorf("jan: %q", h)
	}
	if out, _ := s.Exec(cmd(t, "/user print")); strings.Contains(out, "$6$") || !strings.Contains(out, "password-hash=*****") {
		t.Errorf("print masks the hash:\n%s", out)
	}
	if g, ok := Authenticate(s, "jan", "secret"); !ok || strings.Join(g, ",") != "firma2" {
		t.Errorf("login: %v %v", g, ok)
	}
	if _, ok := Authenticate(s, "jan", "j@n"); ok {
		t.Error("wrong password")
	}

	// changing the password; errors
	expect(t, s, `/user set jan password="Nowe!haslo"`, "")
	if _, ok := Authenticate(s, "jan", "Nowe!haslo"); !ok {
		t.Error("new password")
	}
	expect(t, s, `/user disable jan`, "")
	if _, ok := Authenticate(s, "jan", "Nowe!haslo"); ok {
		t.Error("a disabled user cannot log in")
	}
	expect(t, s, `/user add user=ola`, "password or password-hash is required")
	expect(t, s, `/user add user=ola password=x password-hash="\$6\$a\$b"`, "give password or password-hash, not both")
	expect(t, s, `/user add user=ola password-hash=plain`, "password-hash: only SHA-512")
	expect(t, s, `/user add user=ola password-hash="\$6\$salt\$short"`, "password-hash: hash of $6$: 86 characters")
	expect(t, s, `/user add user=ola password=x groups=firma9`, `groups: "firma9" does not exist in /user/group`)
	expect(t, s, `/user add user="ola nowak" password=x`, "only letters, digits")
	expect(t, s, `/user unset jan password-hash`, "password or password-hash is required")
	expect(t, s, `/user/group remove firma1`, "/user/group: group=firma1 is used by /user maciej")
	expect(t, s, `/user set maciej groups=firma2`, "")
	expect(t, s, `/user/group remove firma1`, "")
}

func TestNvrValidUsers(t *testing.T) {
	s := newStore(t)
	for i, err := range run(t, s,
		`/storage add name=disk1 path=/srv/disk1`,
		`/user/group add group=firma1`,
		`/user/group add group=firma2`,
		`/user add user=maciej groups=firma1 password=a`,
		`/user add user=jan groups=firma2 password=b`,
		`/user add user=ola password=c`,
		`/user add user=ewa groups=firma2 password=d disabled=yes`,
	) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk1 valid-users=maciek`, `valid-users: "maciek" does not exist in /user`)
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk1 valid-users=@firma9`, `valid-users: "@firma9" does not exist in /user/group`)
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk1 valid-users=firma2`, `valid-users: "firma2" does not exist in /user`)
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk1 valid-users="maciej,@firma2"`, "")
	expect(t, s, `/nvr add type=local name=nvr02 storage=disk1`, "")
	if !strings.Contains(s.Export("/nvr"), "add type=local name=nvr01 storage=disk1 valid-users=maciej,@firma2\n") {
		t.Errorf("%s", s.Export("/nvr"))
	}
	for user, want := range map[string]bool{"maciej": true, "jan": true, "ola": false, "ewa": false, "nobody": false} {
		if CanAccess(s, user, "nvr01") != want {
			t.Errorf("%s on nvr01: want %v", user, want)
		}
		if CanAccess(s, user, "nvr02") {
			t.Errorf("%s on nvr02: without valid-users nobody", user)
		}
	}
	if got := strings.Join(AccessibleNvrs(s, "jan"), ","); got != "nvr01" {
		t.Errorf("jan: %s", got)
	}
	// what valid-users names can be neither removed nor renamed
	expect(t, s, `/user remove maciej`, "/user: user=maciej is used by /nvr nvr01")
	expect(t, s, `/user set maciej user=maciek`, "user=maciej is used by /nvr nvr01")
	expect(t, s, `/user/group remove firma2`, "/user/group: group=firma2 is used by /user jan")
	expect(t, s, `/user set jan groups=""`, "")
	expect(t, s, `/user set ewa groups=""`, "")
	expect(t, s, `/user/group remove firma2`, "/user/group: group=firma2 is used by /nvr nvr01")
	expect(t, s, `/user/group remove firma1`, "is used by /user maciej")
	expect(t, s, `/nvr set nvr01 valid-users=maciej`, "")
	expect(t, s, `/user/group remove firma2`, "")
	if CanAccess(s, "jan", "nvr01") {
		t.Error("jan lost the group")
	}
}

func TestSystemSettings(t *testing.T) {
	s := newStore(t)
	if PollInterval(s) != 5*time.Minute || WebBranding(s) != (Branding{Name: "ffnvr", LoginName: true, LoginLogo: true}) {
		t.Errorf("defaults: %v %+v", PollInterval(s), WebBranding(s))
	}
	expect(t, s, `/system/settings set poll-interval=30s`, "at least 1m")
	expect(t, s, `/system/settings set poll-interval=90m`, "")
	expect(t, s, `/system/www set name="Monitoring ACME" login-logo=no`, "")
	expect(t, s, `/system/www set tagline="`+strings.Repeat("x", 61)+`"`, "at most 60 characters")
	if PollInterval(s) != 90*time.Minute || WebBranding(s) != (Branding{Name: "Monitoring ACME", LoginName: true}) {
		t.Errorf("%v %+v", PollInterval(s), WebBranding(s))
	}
	if !strings.Contains(s.Export("/"), "/system/settings\nset poll-interval=1h30m\n\n/system/www\nset name=\"Monitoring ACME\" login-logo=no\n") {
		t.Errorf("%s", s.Export("/"))
	}
}
