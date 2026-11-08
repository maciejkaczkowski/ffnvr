package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/jobs"
	"ffnvr/internal/module"
)

func downloadStore(t *testing.T) (*config.Store, string) {
	t.Helper()
	root := t.TempDir()
	s := config.NewStore()
	for _, m := range All() {
		if b, ok := m.(module.Binder); ok {
			b.Bind(&module.Context{Store: s, Root: root})
		}
		for _, menu := range m.Menus() {
			s.Register(menu)
		}
	}
	for i, e := range run(t, s,
		`/storage add name=disk1 path=/srv/disk1`,
		`/storage add name=archive path=/srv/archive`,
		`/nvr add type=local name=nvr01 storage=disk1`,
		`/nvr add type=local name=nvr02 storage=disk1`,
		`/camera add name=cam01 nvr=nvr01 stream=rtsp://h/1`,
	) {
		if e != "" {
			t.Fatalf("%d: %s", i, e)
		}
	}
	return s, root
}

func TestDownloadAdd(t *testing.T) {
	s, root := downloadStore(t)
	sp := jobs.Spool{Dir: filepath.Join(root, jobs.Dir)}
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2026-10-20 08:00"`, "missing required parameter end")
	expect(t, s, `/nvr/download add layout=flat camera=cam09 storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, `camera: "cam09" does not exist`)
	expect(t, s, `/nvr/download add layout=flat camera=cam01 nvr=nvr02 storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, "nvr nvr02 has no camera cam01 (it is on: nvr01)")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start=yesterday end="2026-10-20 09:00"`, "expected \"YYYY-MM-DD HH:MM[:SS]\"")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2026-10-20 09:00" end="2026-10-20 08:00"`, "end must be after start")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2099-10-20 09:00" end="2099-10-20 10:00"`, "start is in the future")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive path=../etc start="2026-10-20 08:00" end="2026-10-20 09:00"`, "only letters")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive path=/abs start="2026-10-20 08:00" end="2026-10-20 09:00"`, "must be relative")
	if js, _ := sp.List(); len(js) != 0 {
		t.Fatalf("rejected commands queue nothing: %v", js)
	}
	out, err := s.Exec(cmd(t, `/nvr/download add layout=flat nvr=nvr01 camera=cam01 storage=archive path=case-12/door start="2026-10-20 08:00" end=2026-10-20T09:30:15`))
	if err != nil {
		t.Fatal(err)
	}
	target := "/srv/archive/case-12/door/nvr01_cam01_2026-10-20T08-00-00_2026-10-20T09-30-15.mp4"
	if !strings.Contains(out, "job 1 queued: "+target) || !strings.Contains(out, "worker is not running") {
		t.Errorf("add: %s", out)
	}
	j, err := sp.Get(1)
	if err != nil || j.Source != "/srv/disk1/nvr01/cam01" || j.Target != target || j.Nvr != "nvr01" ||
		!j.Start.Equal(time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)) || j.State != jobs.Queued {
		t.Errorf("job: %+v %v", j, err)
	}
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive path=case-12/door start="2026-10-20 08:00" end=2026-10-20T09:30:15`, "job 1 already writes")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive file=front start="2026-10-20 08:00" end="2026-10-20 08:05"`, "")
	if j, _ := sp.Get(2); j.Target != "/srv/archive/front.mp4" {
		t.Errorf("file=: %s", j.Target)
	}
	if s.Export("/") == "" || strings.Contains(s.Export("/"), "download") {
		t.Errorf("jobs are not configuration:\n%s", s.Export("/"))
	}
}

func TestDownloadPrintCancelRemove(t *testing.T) {
	s, root := downloadStore(t)
	sp := jobs.Spool{Dir: filepath.Join(root, jobs.Dir)}
	expect(t, s, `/nvr/download print`, "")
	if out, _ := s.Exec(cmd(t, "/nvr/download print")); out != "no jobs\n" {
		t.Errorf("%q", out)
	}
	for i := 0; i < 3; i++ {
		expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive file=f`+string(rune('a'+i))+` start="2026-10-20 08:00" end="2026-10-20 09:00"`, "")
	}
	j, _ := sp.Get(1)
	j.State, j.Progress, j.Speed = jobs.Running, 0.42, "8.1x"
	sp.Save(j)
	j, _ = sp.Get(3)
	j.State, j.Error = jobs.Failed, "no recordings between ..."
	sp.Save(j)
	sp.Beat()
	expect(t, s, `/nvr/download cancel 2`, "")
	out, _ := s.Exec(cmd(t, "/nvr/download print"))
	for _, want := range []string{"NVR    CAMERA", " 1  nvr01  cam01   2026-10-20 08:00:00  2026-10-20 09:00:00  running     42% 8.1x",
		"-> /srv/archive/fa.mp4", " 2  nvr01  cam01   2026-10-20 08:00:00  2026-10-20 09:00:00  cancelling", "error: no recordings between ..."} {
		if !strings.Contains(out, want) {
			t.Errorf("print misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "worker is not running") {
		t.Errorf("the worker is alive:\n%s", out)
	}
	expect(t, s, `/nvr/download remove 1`, "job 1 is running, cancel it first")
	expect(t, s, `/nvr/download cancel 3`, "job 3 is already failed")
	expect(t, s, `/nvr/download remove 3`, "")
	expect(t, s, `/nvr/download remove x`, `"x" is not a job number`)
	expect(t, s, `/nvr/download remove`, "give the job numbers")
	expect(t, s, `/nvr/download set 1 comment=x`, "set is not supported here (use: add, cancel, remove, print)")
	if _, err := os.Stat(filepath.Join(root, jobs.Dir, "3.json")); err == nil {
		t.Error("removed")
	}
	ids := (&downloadModule{ctx: &module.Context{Root: root}}).IDs()
	if len(ids) != 2 || ids[0][0] != "1" || !strings.HasPrefix(ids[0][1], "running cam01") {
		t.Errorf("ids: %v", ids)
	}
}

func TestDownloadBlocks(t *testing.T) {
	s, root := downloadStore(t)
	sp := jobs.Spool{Dir: filepath.Join(root, jobs.Dir)}
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2026-10-20 11:50" end="2026-10-20 13:13" block=10s`, "at least 1m")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2026-10-20 11:50" end="2026-10-20 13:13" block=abc`, "expected a length")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2026-10-20 11:50" end="2026-10-20 13:13" block-align=start`, "block-align needs block")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2026-01-01 00:00" end="2026-10-20 00:00" block=1m`, "files, at most 1000 are allowed")

	out, err := s.Exec(cmd(t, `/nvr/download add layout=flat camera=cam01 storage=archive path=case start="2026-10-20 11:50" end="2026-10-20 13:13" block=60m`))
	if err != nil {
		t.Fatal(err)
	}
	want := "job 1 queued: 3 files (block 1h, clock) in /srv/archive/case/\n" +
		"  2026-10-20 11:50:00 - 2026-10-20 12:00:00  nvr01_cam01_2026-10-20T11-50-00_2026-10-20T12-00-00.mp4\n" +
		"  2026-10-20 12:00:00 - 2026-10-20 13:00:00  nvr01_cam01_2026-10-20T12-00-00_2026-10-20T13-00-00.mp4\n" +
		"  2026-10-20 13:00:00 - 2026-10-20 13:13:00  nvr01_cam01_2026-10-20T13-00-00_2026-10-20T13-13-00.mp4\n"
	if !strings.HasPrefix(out, want) {
		t.Errorf("add:\n%s\nwant\n%s", out, want)
	}
	out, _ = s.Exec(cmd(t, `/nvr/download add layout=flat camera=cam01 storage=archive path=case2 file=door.mp4 start="2026-10-20 11:50" end="2026-10-20 13:13" block=1h block-align=start`))
	if !strings.Contains(out, "2 files (block 1h, start)") || !strings.Contains(out, "13:13:00  door_2026-10-20T12-50-00_2026-10-20T13-13-00.mp4") {
		t.Errorf("start alignment, file as prefix:\n%s", out)
	}
	// the same blocks again: refused, an overlapping file with another name is fine
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive path=case start="2026-10-20 12:00" end="2026-10-20 13:00" block=1h`, "job 1 already writes")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive path=case start="2026-10-20 12:00" end="2026-10-20 12:30"`, "")

	j, _ := sp.Get(1)
	j.State, j.Progress = jobs.Running, 0.5
	j.Parts[0].State, j.Parts[1].State = "done", "empty"
	sp.Save(j)
	out, _ = s.Exec(cmd(t, "/nvr/download print"))
	for _, w := range []string{"running  50% 3/3", "-> /srv/archive/case/\n", "3 files, block 1h clock, 1 without recordings"} {
		if !strings.Contains(out, w) {
			t.Errorf("print misses %q:\n%s", w, out)
		}
	}
}

func TestCameraNamePerNvr(t *testing.T) {
	s, root := downloadStore(t)
	sp := jobs.Spool{Dir: filepath.Join(root, jobs.Dir)}
	expect(t, s, `/camera add name=cam01 nvr=nvr01 stream=rtsp://h/x`, "/camera: name=cam01 already exists on nvr=nvr01")
	expect(t, s, `/camera add name=cam01 nvr=nvr02 stream=rtsp://h/2`, "")
	expect(t, s, `/camera add name=cam02 nvr=nvr02 stream=rtsp://h/3`, "")
	expect(t, s, `/camera set cam02 nvr=nvr01`, "")
	expect(t, s, `/camera set cam02 nvr=nvr02`, "")
	expect(t, s, `/camera set 2 name=cam01`, "name=cam01 already exists on nvr=nvr02")
	// the shared name does not select, the number and [find] do
	expect(t, s, `/camera set cam01 stream=rtsp://h/y`, "cam01 is not unique: 0 (nvr=nvr01), 1 (nvr=nvr02); use the number or [find name=cam01 nvr=...]")
	expect(t, s, `/camera set [find name=cam01 nvr=nvr02] stream=rtsp://h/22`, "")
	expect(t, s, `/camera disable 0`, "")
	expect(t, s, `/nvr remove nvr02`, "is used by /camera cam01 (nvr=nvr02)")
	if got := s.Export("/camera"); !strings.Contains(got, "add name=cam01 nvr=nvr01 stream=rtsp://h/1 enabled=no\nadd name=cam01 nvr=nvr02 stream=rtsp://h/22 enabled=yes\n") {
		t.Errorf("export:\n%s", got)
	}
	// both units exist, they differ by the recorder
	expect(t, s, `/camera enable 0`, "")
	us, _ := cameraModule{}.Units(&module.Context{Store: s})
	if len(us) != 3 || us[0].Name != "ffnvr-nvr01-cam01.service" || us[1].Name != "ffnvr-nvr02-cam01.service" {
		t.Errorf("units: %v", us)
	}

	// download: the shared name needs nvr=, a unique one does not
	expect(t, s, `/nvr/download add layout=flat camera=cam01 storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, "camera cam01 is on several nvrs (nvr01, nvr02), give nvr=")
	expect(t, s, `/nvr/download add layout=flat camera=cam01 nvr=nvr02 storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, "")
	expect(t, s, `/nvr/download add layout=flat camera=cam02 storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, "")
	j1, _ := sp.Get(1)
	j2, _ := sp.Get(2)
	if j1.Nvr != "nvr02" || j1.Source != "/srv/disk1/nvr02/cam01" || j2.Nvr != "nvr02" || j2.Source != "/srv/disk1/nvr02/cam02" {
		t.Errorf("jobs: %+v\n%+v", j1, j2)
	}
	// the same camera name on two recorders gives two different default file names
	expect(t, s, `/nvr/download add layout=flat camera=cam01 nvr=nvr01 storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, "")
}

func TestCameraAlias(t *testing.T) {
	s, root := downloadStore(t)
	sp := jobs.Spool{Dir: filepath.Join(root, jobs.Dir)}
	expect(t, s, `/camera add name=ch02 nvr=nvr01 stream=rtsp://h/2 alias="brama 1"`, "only letters, digits")
	expect(t, s, `/camera add name=ch02 nvr=nvr01 stream=rtsp://h/2 alias=brama`, "")
	expect(t, s, `/camera add name=ch03 nvr=nvr01 stream=rtsp://h/3 alias=brama`, "/camera: alias=brama already exists on nvr=nvr01")
	expect(t, s, `/camera add name=ch03 nvr=nvr01 stream=rtsp://h/3 alias=cam01`, "/camera: alias=cam01 is already the name of cam01 on nvr=nvr01")
	expect(t, s, `/camera add name=brama nvr=nvr01 stream=rtsp://h/3`, "/camera: name=brama is already the alias of ch02 on nvr=nvr01")
	expect(t, s, `/camera set ch02 alias=ch02`, "") // its own name: harmless
	expect(t, s, `/camera set ch02 alias=brama`, "")
	// another recorder may use the same alias
	expect(t, s, `/camera add name=ch02 nvr=nvr02 stream=rtsp://h/22 alias=brama`, "")
	// the alias selects like the name; shared across recorders it does not
	expect(t, s, `/camera set brama comment=x`, "brama is not unique: 1 (nvr=nvr01), 2 (nvr=nvr02)")
	expect(t, s, `/camera set [find alias=brama nvr=nvr01] comment=wjazd`, "")
	expect(t, s, `/camera unset 2 alias`, "")
	expect(t, s, `/camera set brama comment=wjazd2`, "")
	if got := s.Export("/camera"); !strings.Contains(got, "add name=ch02 nvr=nvr01 stream=rtsp://h/2 alias=brama enabled=yes comment=wjazd2\n") {
		t.Errorf("export:\n%s", got)
	}
	// the unit and the recording directory keep using the name
	us, _ := cameraModule{}.Units(&module.Context{Store: s})
	found := false
	for _, u := range us {
		found = found || (u.Name == "ffnvr-nvr01-ch02.service" && strings.Contains(u.Content, "/srv/disk1/nvr01/ch02/%%Y"))
	}
	if !found {
		t.Errorf("units: %v", us)
	}

	// download by alias; the file name shows the alias, the source is the name's directory
	out, err := s.Exec(cmd(t, `/nvr/download add layout=flat camera=brama storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`))
	if err != nil || !strings.Contains(out, "/srv/archive/nvr01_brama_2026-10-20T08-00-00_2026-10-20T09-00-00.mp4") {
		t.Fatalf("%v %s", err, out)
	}
	j, _ := sp.Get(1)
	if j.Camera != "ch02" || j.Alias != "brama" || j.Source != "/srv/disk1/nvr01/ch02" {
		t.Errorf("%+v", j)
	}
	if out, _ := s.Exec(cmd(t, "/nvr/download print")); !strings.Contains(out, " nvr01  ch02(brama) ") {
		t.Errorf("print:\n%s", out)
	}
	// ch02 is a name on both recorders
	expect(t, s, `/nvr/download add layout=flat camera=ch02 storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, "camera ch02 is on several nvrs (nvr01, nvr02), give nvr=")
	expect(t, s, `/nvr/download add layout=flat camera=nope storage=archive start="2026-10-20 08:00" end="2026-10-20 09:00"`, `camera: "nope" does not exist in /camera`)
}

// the default layout: a directory per recorder and camera (the alias, when set)
func TestDownloadLayout(t *testing.T) {
	s, _ := downloadStore(t)
	run(t, s, `/camera set [ find name=cam01 ] alias=brama`)
	out, err := s.Exec(cmd(t, `/nvr/download add camera=cam01 storage=archive path=case-12 start="2026-10-20 08:00" end="2026-10-20 09:00"`))
	if err != nil || !strings.Contains(out, "/srv/archive/case-12/nvr01/brama/2026-10-20T08-00-00_2026-10-20T09-00-00.mp4") {
		t.Errorf("default: %v %s", err, out)
	}
	out, err = s.Exec(cmd(t, `/nvr/download add camera=cam01 storage=archive start="2026-10-20 10:00" end="2026-10-20 12:00" block=1h`))
	// the directories name the recorder and the camera: the files only the time
	if err != nil || !strings.Contains(out, "/srv/archive/nvr01/brama/") || !strings.Contains(out, "  2026-10-20T10-00-00_2026-10-20T11-00-00.mp4\n") || strings.Contains(out, "nvr01_brama_") {
		t.Errorf("blocks: %v %s", err, out)
	}
	// a name of one's own still leads the blocks' names
	out, err = s.Exec(cmd(t, `/nvr/download add camera=cam01 storage=archive path=x start="2026-10-20 10:00" end="2026-10-20 12:00" block=1h file=wjazd`))
	if err != nil || !strings.Contains(out, "  wjazd_2026-10-20T10-00-00_2026-10-20T11-00-00.mp4") {
		t.Errorf("blocks with file=: %v %s", err, out)
	}
	out, err = s.Exec(cmd(t, `/nvr/download add camera=cam01 storage=archive layout=flat start="2026-10-20 13:00" end="2026-10-20 14:00"`))
	if err != nil || !strings.Contains(out, "/srv/archive/nvr01_brama_2026-10-20T13-00-00_2026-10-20T14-00-00.mp4") {
		t.Errorf("flat: %v %s", err, out)
	}
	expect(t, s, `/nvr/download add camera=cam01 storage=archive layout=tree start="2026-10-20 13:00" end="2026-10-20 14:00"`, "layout")
}
