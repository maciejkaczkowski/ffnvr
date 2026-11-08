package modules

import (
	"strings"
	"testing"
)

func TestEventFilters(t *testing.T) {
	s := newStore(t)
	run(t, s, `/storage add name=x path=/srv/x`)
	errs := run(t, s,
		`/event/filter add name="dahua-nvr" action=drop kinds=VideoMotion,RtspSessionDisconnect,InterVideoAccess,RecordDelete`,
		`/event/filter add name="dahua-ipc" action=accept kinds=VideoMotion`,
		`/event/filter add name=all action=drop`,
		`/event/filter add name=nothing action=accept`,   // 3: accept without kinds keeps nothing
		`/event/filter add name="zła nazwa" action=drop`, // 4: name
		`/event/filter add name=x kinds=A`,               // 5: action required
		`/nvr add type=dahua name=nvr1 host=10.0.0.1 username=a password=b event-filter=dahua-nvr`,
		`/nvr add type=dahua name=ipc1 host=10.0.0.2 username=a password=b event-filter=dahua-ipc`,
		`/nvr add type=dahua name=nvr2 host=10.0.0.3 username=a password=b`,
		`/nvr add type=dahua name=nvr3 host=10.0.0.4 username=a password=b event-filter=missing`, // 9: strict
		`/nvr add type=local name=loc storage=x event-filter=all`,                                // 10
	)
	for i, e := range errs {
		bad := i == 3 || i == 4 || i == 5 || i == 9 || i == 10
		if bad != (e != "") {
			t.Errorf("line %d: %q", i, e)
		}
	}
	if !strings.Contains(errs[10], "event-filter") {
		t.Errorf("local recorder: %s", errs[10])
	}
	if !strings.Contains(errs[3], "needs kinds") {
		t.Errorf("accept without kinds: %s", errs[3])
	}

	es := EventConfig(s)
	nvr, ipc, def := es.FilterFor("dahua-nvr"), es.FilterFor("dahua-ipc"), es.FilterFor("")
	for _, c := range []struct {
		name string
		got  bool
		want bool
	}{
		{"nvr VideoMotion", nvr.Keeps("VideoMotion"), false},
		{"nvr VideoLoss", nvr.Keeps("VideoLoss"), true},
		{"nvr NewFile (a filter replaces the built-in list)", nvr.Keeps("NewFile"), true},
		{"ipc VideoMotion", ipc.Keeps("VideoMotion"), true},
		{"ipc VideoLoss", ipc.Keeps("VideoLoss"), false},
		{"default NewFile", def.Keeps("NewFile"), false},
		{"default VideoMotion", def.Keeps("VideoMotion"), true},
		{"all", es.FilterFor("all").Keeps("NewFile"), true},
		{"unknown name: the default", es.FilterFor("gone").Keeps("NewFile"), false},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v", c.name, c.got)
		}
	}

	// default-filter of /event
	if e := run(t, s, `/event set default-filter=dahua-ipc`); e[0] != "" {
		t.Fatal(e[0])
	}
	if EventConfig(s).FilterFor("").Keeps("VideoLoss") {
		t.Error("default-filter=dahua-ipc keeps only VideoMotion")
	}
	if e := run(t, s, `/event set default-filter=missing`); e[0] == "" {
		t.Error("default-filter must exist")
	}

	// a filter in use cannot be removed, by a recorder or by /event
	if e := run(t, s, `/event/filter remove [find name=dahua-nvr]`); !strings.Contains(e[0], "nvr1") {
		t.Errorf("remove a filter of a recorder: %q", e[0])
	}
	if e := run(t, s, `/event/filter remove [find name=dahua-ipc]`); e[0] == "" {
		t.Error("removed a filter in use")
	}
	run(t, s, `/nvr set [find name=ipc1] event-filter=""`)
	if e := run(t, s, `/event/filter remove [find name=dahua-ipc]`); !strings.Contains(e[0], "/event") {
		t.Errorf("remove the default filter: %q", e[0])
	}
	run(t, s, `/event unset default-filter`)
	if e := run(t, s, `/event/filter remove [find name=dahua-ipc]`); e[0] != "" {
		t.Errorf("remove an unused filter: %s", e[0])
	}

	// export: the filters before the recorders naming them
	out := s.Export("")
	if f, n := strings.Index(out, "/event/filter"), strings.Index(out, "/nvr\n"); f < 0 || n < 0 || f > n {
		t.Errorf("export order:\n%s", out)
	}
}
