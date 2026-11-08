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
	"ffnvr/internal/jobs"
	"ffnvr/internal/uniview/univiewtest"
)

func TestNvrUniview(t *testing.T) {
	fake := univiewtest.New()
	defer fake.Close()
	s, ctx, nm := dahuaStore(t)
	expect(t, s, `/nvr add type=uniview name=nvr03 host=10.0.0.3 username=admin`, "type=uniview needs parameter password")
	expect(t, s, `/nvr add type=uniview name=nvr03 host=10.0.0.3 username=admin password=x speed=100`, "speed must be 1..64")
	expect(t, s, `/nvr add type=dahua name=nvr03 host=10.0.0.3 username=admin password=x speed=8`, "speed is not used by type=dahua")

	out := addNvr(t, s, ctx, nm, `/nvr add type="uniview" name="nvr03" host="`+fake.Addr()+`" username="admin" password="secret" speed=8`)
	for _, want := range []string{
		"nvr03: NVR302-16S fw NVR-B3601.16.10.250101 sn 210235C3EYA123000123; cameras 2/4 online; disks 1: 2.0T/4.0T (50%)",
		`ch01: added, alias Wejscie_glowne (title "Wejscie glowne")`,
		`ch02: added (title "IP Camera 02")`,
		`ch03: skipped, Disconnected (wrong username or password) (title "Rampa")`,
		`ch04: skipped, Empty`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// the live address comes from LAPI, with the host ffnvr uses (not the inner 10.1.1.1)
	if got := s.Export("/camera"); !strings.Contains(got, `add name=ch01 nvr=nvr03 channel=1 stream=rtsp://127.0.0.1:554/unicast/c1/s0/live stream-lq=rtsp://127.0.0.1:554/unicast/c1/s1/live alias=Wejscie_glowne`) {
		t.Errorf("%s", got)
	}
	if st := ReadStatus(ctx.Root, "nvr03"); st.ChannelList[0].Address != "192.168.1.31" || st.ChannelList[0].Detail != "UNIVIEW IPC322LB" {
		t.Errorf("camera addresses: %+v", st.ChannelList)
	}
	out, err := s.Exec(cmd(t, "/nvr probe nvr03"))
	for _, want := range []string{"== LAPI /LAPI/V1.0/System/DeviceInfo\n", `"DeviceModel":"NVR302-16S"`, "== LAPI channel 1: playback address",
		"== RTSP channel 1: playing 10 minutes ago\n> OPTIONS rtsp://127.0.0.1:", "< 401 Unauthorized\n  WWW-Authenticate: Digest", "Digest variant 0",
		"> PLAY", "Speed 8.0", "OK: the recording plays",
		"== LAPI channel 1: days recorded this month", "recordings of the last 24 hours (raw)",
		"== main-cgi cmd 20: cameras\n", "1100010001000001", "== main-cgi cmd 81: channel 1 (1100010001000001)",
		"recorded for events in the last 24 hours\n0 of 0 parts"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("probe misses %q:\n%s", want, out)
		}
	}
}

// From /download to the mp4: the worker plays the recording over RTSP and converts it.
func TestDownloadFromUniview(t *testing.T) {
	video, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", "4", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	fake := univiewtest.New()
	defer fake.Close()
	fake.Video, fake.FPS = video, 10
	s, ctx, nm := dahuaStore(t)
	archive := t.TempDir()
	run(t, s, `/storage add name=archive path=`+archive)
	addNvr(t, s, ctx, nm, `/nvr add type=uniview name=nvr03 host="`+fake.Addr()+`" username=admin password=secret`)
	from := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)
	fake.Recorded[1] = [2]int64{from.Unix(), from.Add(30 * time.Minute).Unix()}
	expect(t, s, `/nvr/download add camera=Wejscie_glowne storage=archive start="2026-10-24 08:00" end="2026-10-24 09:00" block=30m`, "")

	sp := jobs.Spool{Dir: filepath.Join(ctx.Root, jobs.Dir)}
	w := &jobs.Worker{Spool: sp, Poll: 20 * time.Millisecond, Log: log.New(io.Discard, "", 0),
		Remote: RemoteSource(func() (*config.Store, error) { return s, nil })}
	wctx, stop := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(wctx) }()
	var j *jobs.Job
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if j, _ = sp.Get(1); j.State.Finished() {
			break
		}
	}
	stop()
	<-done
	if j.State != jobs.Done || j.Parts[0].State != "done" || j.Parts[1].State != "empty" {
		t.Fatalf("%+v", j)
	}
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration,format_name:stream=codec_name",
		"-of", "default=nw=1", j.Parts[0].Target).Output()
	dur := 0.0
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "duration="); ok {
			dur, _ = strconv.ParseFloat(v, 64)
		}
	}
	if err != nil || !strings.Contains(string(b), "codec_name=h264") || !strings.Contains(string(b), "mp4") || dur < 3.5 || dur > 4.5 {
		t.Errorf("expected a 4 s mp4: %v\n%s", err, b)
	}
}
