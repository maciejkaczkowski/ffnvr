package modules

import (
	"ffnvr/internal/fsusage"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRetentionAndIndex(t *testing.T) {
	s := newStore(t)
	disk := t.TempDir()
	run(t, s, `/storage add name=disk1 path=`+disk)
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk1 retention=10m`, "at least 1h")
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk1 retention=48h`, "")
	if !strings.Contains(s.Export("/nvr"), "retention=2d") {
		t.Errorf("%s", s.Export("/nvr"))
	}
	expect(t, s, `/nvr add type=dahua name=nvr02 host=h username=a password=b retention=30d`, "retention is not used by type=dahua")

	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	seg := func(cam string, start time.Time) string {
		p := filepath.Join(disk, "nvr01", cam, start.Format("2006-01-02T15-04-05")+".mkv")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("12345"), 0o644)
		os.Chtimes(p, start.Add(15*time.Minute), start.Add(15*time.Minute))
		return p
	}
	old := seg("cam1", now.Add(-72*time.Hour))
	early := seg("cam1", now.Add(-48*time.Hour-20*time.Minute))
	os.Chtimes(early, now, now) // touched: the name counts, not the timestamp
	// started before now - retention, but ended after it: the extra segment is kept
	spare := seg("cam1", now.Add(-48*time.Hour-10*time.Minute))
	edge := seg("cam1", now.Add(-48*time.Hour+10*time.Minute))
	recent := seg("cam2", now.Add(-time.Hour))
	other := filepath.Join(disk, "nvr01", "cam1", "notes.txt")
	os.WriteFile(other, nil, 0o644)

	if r := Recordings(s, "nvr01"); !r.Updated.IsZero() || r.Files != 0 {
		t.Errorf("no index yet: %+v", r)
	}
	var logged []string
	MaintainLocal(s, now, func(f string, a ...any) { logged = append(logged, f) })
	for p, want := range map[string]bool{old: false, early: false, spare: true, edge: true, recent: true, other: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s: kept %v, want %v", p, err == nil, want)
		}
	}
	if len(logged) != 1 {
		t.Errorf("%v", logged)
	}
	for _, f := range []string{".cam1/segments.json", ".cam1/summary.json", ".cam2/summary.json"} {
		if _, err := os.Stat(filepath.Join(disk, "nvr01", f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	r := Recordings(s, "nvr01")
	if r.Files != 3 || r.Bytes != 15 || !r.Oldest.Equal(now.Add(-48*time.Hour-10*time.Minute)) || !r.Newest.Equal(now.Add(-time.Hour)) || r.Updated.IsZero() {
		t.Errorf("%+v", r)
	}
	// a new segment, and the newest one growing
	seg("cam2", now.Add(-30*time.Minute))
	os.WriteFile(recent, []byte("1234567890"), 0o644) // it was the newest: it grew, its final size counts
	newest := filepath.Join(disk, "nvr01", "cam2", now.Add(-30*time.Minute).Format("2006-01-02T15-04-05")+".mkv")
	MaintainLocal(s, now, func(string, ...any) {})
	os.WriteFile(newest, []byte("1234567"), 0o644)
	MaintainLocal(s, now, func(string, ...any) {})
	if r := Recordings(s, "nvr01"); r.Files != 4 || r.Bytes != 15+5+7 {
		t.Errorf("incremental: %+v", r)
	}
	// without retention nothing is deleted
	expect(t, s, `/nvr unset nvr01 retention`, "")
	MaintainLocal(s, now.Add(1000*time.Hour), func(string, ...any) {})
	if Recordings(s, "nvr01").Files != 4 {
		t.Error("without retention nothing is deleted")
	}
}

// A directory whose modification time does not change when files are added (a mergerfs
// pool writing to another branch): the new segments are indexed all the same, and the
// segment that grew after it was indexed gets its final size.
func TestIndexWithoutDirMtime(t *testing.T) {
	s := newStore(t)
	disk := t.TempDir()
	run(t, s, `/storage add name=disk1 path=`+disk, `/nvr add type=local name=nvr01 storage=disk1`)
	dir := filepath.Join(disk, "nvr01", "cam1")
	os.MkdirAll(dir, 0o755)
	frozen := time.Date(2026, 10, 29, 5, 30, 0, 0, time.Local)
	add := func(name, content string) {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
		os.Chtimes(dir, frozen, frozen) // the directory "does not change"
	}
	add("2026-10-29T05-30-01.mkv", "grow")
	MaintainLocal(s, time.Now(), func(string, ...any) {})
	os.WriteFile(filepath.Join(dir, "2026-10-29T05-30-01.mkv"), []byte("grown to the end"), 0o644)
	add("2026-10-29T05-45-00.mkv", "12345")
	add("2026-10-29T12-30-01.mkv", "123")
	os.Chtimes(dir, frozen, frozen)
	MaintainLocal(s, time.Now(), func(string, ...any) {})
	r := Recordings(s, "nvr01")
	if r.Files != 3 || r.Bytes != 16+5+3 || r.Newest.Format("15:04") != "12:30" {
		t.Errorf("%+v", r)
	}
}

func TestDiskReserve(t *testing.T) {
	s := newStore(t)
	disk := t.TempDir()
	run(t, s, `/storage add name=disk1 path=`+disk, `/nvr add type=local name=nvr01 storage=disk1 retention=30d`)
	expect(t, s, `/system/settings set disk-reserve=lots`, "expected e.g. 500M")
	expect(t, s, `/system/settings set disk-reserve=2gb`, "")
	if !strings.Contains(s.Export("/system"), "disk-reserve=2G") || DiskReserve(s) != 2<<30 {
		t.Errorf("%s", s.Export("/system"))
	}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	var files []string
	for i, cam := range []string{"cam1", "cam2", "cam1", "cam2", "cam1"} {
		p := filepath.Join(disk, "nvr01", cam, now.Add(time.Duration(i-5)*time.Hour).Format("2006-01-02T15-04-05")+".mkv")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
		files = append(files, p)
	}
	// 1G free, each deletion frees 0.5G: two oldest segments go
	free := int64(1 << 30)
	freeSpace = func(string) (int64, error) { return free, nil }
	defer func() {
		freeSpace = func(path string) (int64, error) { u, err := fsusage.Of(path); return int64(u.Avail), err }
	}()
	removed := 0
	orig := freeSpace
	freeSpace = func(p string) (int64, error) {
		n := 0
		for _, f := range files {
			if _, err := os.Stat(f); err != nil {
				n++
			}
		}
		removed = n
		return free + int64(n)*(1<<29), nil
	}
	_ = orig
	var logged []string
	MaintainLocal(s, now, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	for i, want := range []bool{false, false, true, true, true} {
		if _, err := os.Stat(files[i]); (err == nil) != want {
			t.Errorf("segment %d kept %v, want %v", i, err == nil, want)
		}
	}
	if removed != 2 || !strings.Contains(strings.Join(logged, "\n"), "disk reserve disk1: 2 oldest segments deleted to keep 2G free") {
		t.Errorf("%d %v", removed, logged)
	}
	if r := Recordings(s, "nvr01"); r.Files != 3 {
		t.Errorf("the index follows: %+v", r)
	}
	if v, _ := ParseSize("1.5T"); v != 3<<39 || FormatSize(512<<20) != "512M" {
		t.Error("sizes")
	}
}
