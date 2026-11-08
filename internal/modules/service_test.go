package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/module"
)

func TestIPService(t *testing.T) {
	s := newStore(t)
	if out, _ := s.Exec(cmd(t, "/ip/service print")); !strings.Contains(out, "name=www port=80 enabled=no") {
		t.Errorf("the default is there and off:\n%s", out)
	}
	if s.Export("/") != "" {
		t.Errorf("defaults are not exported: %q", s.Export("/"))
	}
	if us, _ := (serviceModule{}).Units(&module.Context{Store: s}); len(us) != 0 {
		t.Errorf("off: no unit: %v", us)
	}
	expect(t, s, `/ip/service/set [find name=www] enabled=true port=80`, "")
	if got := s.Export("/"); got != "/ip/service\nset [ find name=www ] enabled=yes\n" {
		t.Errorf("%q", got)
	}
	expect(t, s, `/ip/service set www port=8080 address=192.168.0.0/16,10.1.2.3`, "")
	if got := s.Export("/"); got != "/ip/service\nset [ find name=www ] port=8080 address=192.168.0.0/16,10.1.2.3 enabled=yes\n" {
		t.Errorf("%q", got)
	}
	us, _ := (serviceModule{}).Units(&module.Context{Store: s})
	if len(us) != 1 || us[0].Name != "ffnvr-www.service" || !strings.Contains(us[0].Content, "ExecStart=/usr/local/bin/ffnvr web -listen :8080 -allow 192.168.0.0/16,10.1.2.3\n") {
		t.Errorf("%v", us)
	}
	expect(t, s, `/ip/service set www port=0`, "port must be 1..65535")
	expect(t, s, `/ip/service set www address=lan`, `address "lan": expected an address or a network`)
	expect(t, s, `/ip/service set www name=web`, "name cannot be changed")
	expect(t, s, `/ip/service add name=ssh port=22`, "'add' is not available here")
	expect(t, s, `/ip/service remove www`, "cannot remove")
	expect(t, s, `/ip/service unset www port`, "")
	expect(t, s, `/ip/service disable www`, "")
	if got := s.Export("/"); got != "/ip/service\nset [ find name=www ] address=192.168.0.0/16,10.1.2.3\n" {
		t.Errorf("unset restores the default, disable switches off: %q", got)
	}
	if p, err := ParseAllow("10.0.0.5, 192.168.1.0/24"); err != nil || len(p) != 2 || p[0].String() != "10.0.0.5/32" {
		t.Errorf("%v %v", p, err)
	}
}

// proxy=: the reverse proxy in front of the panel, on its command line.
func TestWWWProxy(t *testing.T) {
	s := newStore(t)
	expect(t, s, `/ip/service set www enabled=yes proxy=10.0.0.5`, "")
	us, _ := (serviceModule{}).Units(&module.Context{Store: s})
	if !strings.Contains(us[0].Content, "ExecStart=/usr/local/bin/ffnvr web -listen :80 -proxy 10.0.0.5\n") {
		t.Errorf("%s", us[0].Content)
	}
	expect(t, s, `/ip/service set www address=10.0.0.5 proxy=10.0.0.5,10.1.0.0/16`, "")
	us, _ = (serviceModule{}).Units(&module.Context{Store: s})
	if !strings.Contains(us[0].Content, "-listen :80 -allow 10.0.0.5 -proxy 10.0.0.5,10.1.0.0/16\n") {
		t.Errorf("%s", us[0].Content)
	}
	expect(t, s, `/ip/service set www proxy=nginx`, `proxy: address "nginx": expected an address or a network`)
}

func TestWWWWritesBackupStorages(t *testing.T) {
	s := newStore(t)
	expect(t, s, `/storage add name=b1 path=/srv/backup types=backup`, "")
	expect(t, s, `/storage add name=a1 path=/srv/archive types=archive`, "")
	expect(t, s, `/storage add name=x path=/srv/x types=archive,film`, `types: "film" is not one of archive, backup`)
	expect(t, s, `/ip/service set www enabled=yes`, "")
	us, _ := (serviceModule{}).Units(&module.Context{Store: s})
	if !strings.Contains(us[0].Content, "ReadWritePaths=/var/lib/ffnvr /etc/ffnvr -/srv/backup\n") {
		t.Errorf("%s", us[0].Content)
	}
}

// alarm-lapi: the port Uniview cameras push to, chosen by the user like alarm-dvrip and
// alarm-smtp; it goes to the command line of the alarm server.
func TestIPServiceLAPI(t *testing.T) {
	s := newStore(t)
	if out, _ := s.Exec(cmd(t, "/ip/service print")); !strings.Contains(out, "name=alarm-lapi port=8091 enabled=no") {
		t.Errorf("alarm-lapi is there and off:\n%s", out)
	}
	if u := alarmUnit(&module.Context{Store: s}); u != nil {
		t.Errorf("off: no alarm server: %v", u)
	}
	expect(t, s, `/ip/service set alarm-lapi enabled=yes port=18080 address=192.168.0.0/24`, "")
	u := alarmUnit(&module.Context{Store: s})
	if u == nil || !strings.Contains(u.Content, " alarm -lapi :18080 -lapi-allow 192.168.0.0/24\n") {
		t.Fatalf("%v", u)
	}
	expect(t, s, `/ip/service set alarm-dvrip enabled=yes`, "")
	if u := alarmUnit(&module.Context{Store: s}); !strings.Contains(u.Content, " alarm -dvrip :15002 -lapi :18080 -lapi-allow 192.168.0.0/24\n") {
		t.Errorf("%v", u.Content)
	}
	expect(t, s, `/ip/service set alarm-lapi port=70000`, "port must be 1..65535")
}
