package modules

import (
	"context"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/dvrip/dvriptest"
	"ffnvr/internal/jobs"
)

func TestNvrDvrip(t *testing.T) {
	fake := dvriptest.New()
	defer fake.Close()
	s, ctx, nm := dahuaStore(t)
	expect(t, s, `/nvr add type=dvrip name=nvr05 host=10.0.0.5`, "type=dvrip needs parameter username")
	expect(t, s, `/nvr add type=dvrip name=nvr05 host=10.0.0.5 username=admin fps=25`, "fps is not used by type=dvrip")

	fake.ExtraEmpty = 28 // room for 32 channels, as the recorder answers
	out := addNvr(t, s, ctx, nm, `/nvr add type="dvrip" name="nvr05" host="`+fake.Addr()+`" username="admin" password="secret"`)
	for _, want := range []string{
		"nvr05: NBD6808T-PL fw V4.02.R11.34531119.12001.130000.00000 sn a1b2c3d4e5f6a7b8; cameras 1/4 online; disks 1: 443G/1.0T (44%)",
		`ch01: added, alias Brama (title "Brama")`,
		`ch02: skipped, Empty (title "D02")`,
		`ch04: skipped, Disconnected (title "Magazyn")`,
	} {
		if strings.Contains(out, "ch05") {
			t.Errorf("only the recorder's channels:\n%s", out)
		}
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if got := s.Export("/camera"); got != "/camera\nadd name=ch01 nvr=nvr05 channel=1 alias=Brama enabled=yes\n" {
		t.Errorf("%s", got)
	}
	out, err := s.Exec(cmd(t, "/nvr probe nvr05"))
	for _, want := range []string{"== DVRIP 1020 SystemInfo\n", `"HardWare":"NBD6808T-PL"`, "== DVRIP 1042 NetWork.ChnStatus"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("probe misses %q:\n%s", want, out)
		}
	}
	expect(t, s, `/nvr set nvr05 password=wrong`, "")
	expect(t, s, `/nvr scan nvr05`, "Ret=205 (wrong username or password)")
	// an empty password, as XMeye recorders come
	expect(t, s, `/nvr add type=dvrip name=nvr06 host=10.0.0.6 username=admin`, "")
}

func TestDownloadFromDvrip(t *testing.T) {
	video, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", "10", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	fake := dvriptest.New()
	defer fake.Close()
	b := time.Date(2026, 10, 26, 8, 0, 0, 0, time.Local)
	fake.Recordings = []dvriptest.Recording{
		{Channel: 0, Begin: b, End: b.Add(10 * time.Second), Video: video, FPS: 10},
		{Channel: 0, Begin: b.Add(10 * time.Second), End: b.Add(20 * time.Second), Video: video, FPS: 10},
	}
	s, ctx, nm := dahuaStore(t)
	archive := t.TempDir()
	run(t, s, `/storage add name=archive path=`+archive)
	addNvr(t, s, ctx, nm, `/nvr add type=dvrip name=nvr05 host="`+fake.Addr()+`" username=admin password=secret`)
	expect(t, s, `/nvr/download add camera=Brama storage=archive start="2026-10-26 08:00:04" end="2026-10-26 08:00:16"`, "")

	sp := jobs.Spool{Dir: filepath.Join(ctx.Root, jobs.Dir)}
	w := &jobs.Worker{Spool: sp, Poll: 20 * time.Millisecond, Log: log.New(io.Discard, "", 0),
		Remote: RemoteSource(func() (*config.Store, error) { return s, nil })}
	wctx, stop := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(wctx) }()
	var j *jobs.Job
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j, _ = sp.Get(1); j.State.Finished() {
			break
		}
	}
	stop()
	<-done
	if j.State != jobs.Done || j.Remote != "dvrip" {
		t.Fatalf("%+v", j)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration,format_name:stream=codec_name",
		"-of", "default=nw=1", j.Target).Output()
	dur := 0.0
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "duration="); ok {
			dur, _ = strconv.ParseFloat(v, 64)
		}
	}
	// :04 to :16 across the two files, cut at I frames (one per second)
	if err != nil || !strings.Contains(string(out), "codec_name=h264") || dur < 11.5 || dur > 12.5 {
		t.Errorf("expected a 12 s mp4: %v\n%s", err, out)
	}
}
