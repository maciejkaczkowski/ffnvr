package profile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProfile(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	var ds Dashboards
	if found, err := s.Read("jan", DashboardsFile, &ds); found || err != nil {
		t.Fatalf("none yet: %v %v", found, err)
	}
	err := s.Update("jan", DashboardsFile, &ds, func() error {
		ds.Version = DashboardsVersion
		ds.Dashboards = append(ds.Dashboards, Dashboard{ID: NewID(), Name: "mon01", Size: 4, Tiles: []DashTile{{Position: 1, Nvr: "nvr01", Camera: "ch01"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var back Dashboards
	if found, err := s.Read("jan", DashboardsFile, &back); !found || err != nil || len(back.Dashboards) != 1 || back.Dashboards[0].Tiles[0].Camera != "ch01" {
		t.Fatalf("%v %v %+v", found, err, back)
	}
	id := back.Dashboards[0].ID
	if back.Find(id) == nil || back.Find("x") != nil {
		t.Error("find")
	}
	// fn's error leaves the file alone
	var d2 Dashboards
	if err := s.Update("jan", DashboardsFile, &d2, func() error { d2.Dashboards = nil; return errors.New("no") }); err == nil {
		t.Error("the error")
	}
	var d3 Dashboards
	s.Read("jan", DashboardsFile, &d3)
	if len(d3.Dashboards) != 1 {
		t.Error("written despite the error")
	}
	if fi, _ := os.Stat(filepath.Join(root, Dir, "jan", "dashboards.json")); fi == nil {
		t.Error("the file")
	}
	for _, bad := range [][2]string{{"..", "dashboards"}, {"a/b", "dashboards"}, {"", "dashboards"}, {".hidden", "x"}, {"jan", "../x"}} {
		if _, err := s.Read(bad[0], bad[1], &d3); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	// the profiles of removed users go
	s.Update("ola", DashboardsFile, &Dashboards{}, func() error { return nil })
	s.Prune([]string{"jan"}, t.Logf)
	if u := s.Users(); len(u) != 1 || u[0] != "jan" {
		t.Errorf("%v", u)
	}
}

func TestSettings(t *testing.T) {
	s := New(t.TempDir())
	if st, err := s.Settings("jan"); err != nil || st.Start != "" {
		t.Fatalf("no file: %+v %v", st, err)
	}
	if err := s.SetStart("jan", "archive"); err != nil {
		t.Fatal(err)
	}
	if st, err := s.Settings("jan"); err != nil || st.Start != "archive" || st.Version != SettingsVersion {
		t.Fatalf("%+v %v", st, err)
	}
	if st, _ := s.Settings("ola"); st.Start != "" {
		t.Error("the settings are the user's own")
	}
	// a zoomed live tile: HQ unless the user said otherwise; one setting keeps the other
	if st, _ := s.Settings("jan"); !st.ZoomToHQ() {
		t.Error("zoom to HQ: the default")
	}
	if err := s.SetZoomHQ("jan", false); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Settings("jan"); st.ZoomToHQ() || st.Start != "archive" {
		t.Errorf("zoom keeps the quality: %+v", st)
	}
	s.SetStart("jan", "live")
	if st, _ := s.Settings("jan"); st.ZoomToHQ() || st.Start != "live" {
		t.Errorf("the start page keeps zoom_hq: %+v", st)
	}
	s.SetZoomHQ("jan", true)
	if st, _ := s.Settings("jan"); !st.ZoomToHQ() {
		t.Error("zoom to HQ again")
	}
}
