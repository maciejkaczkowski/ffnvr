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
	"ffnvr/internal/tiandy/tiandytest"
)

func TestNvrTiandy(t *testing.T) {
	fake := tiandytest.New()
	defer fake.Close()
	s, ctx, nm := dahuaStore(t)
	expect(t, s, `/nvr add type=tiandy name=nvr04 host=10.0.0.4 username=Admin`, "type=tiandy needs parameter password")
	expect(t, s, `/nvr add type=tiandy name=nvr04 host=10.0.0.4 username=Admin password=x speed=8`, "speed is not used by type=tiandy")
	expect(t, s, `/nvr add type=tiandy name=nvr04 host=10.0.0.4 username=Admin password=x fps=0`, "fps must be 1..120")
	expect(t, s, `/nvr add type=uniview name=nvr04 host=10.0.0.4 username=Admin password=x fps=25`, "fps is not used by type=uniview")
	expect(t, s, `/nvr add type=dahua name=nvr04 host=10.0.0.4 username=Admin password=x http-port=81`, "http-port is not used by type=dahua")

	out := addNvr(t, s, ctx, nm, `/nvr add type="tiandy" name="nvr04" host="`+fake.Addr()+`" http-port=`+strconv.Itoa(fake.HTTPPort())+` username="Admin" password="secret"`)
	for _, want := range []string{
		"nvr04: TC-R3120 SPEC:I/B/V3.0 fw DVRS_V13.13.20.20250102_SP01 sn 482608C3387A00255; cameras 2/4 online; disks 1: 7.9T/7.9T (99%)",
		`ch01: added, alias Site16 (title "Site16")`,
		`ch02: added, alias Plac_zolty (title "Plac żółty")`,
		`ch03: skipped, Disconnected (title "Channel 3")`,
		`ch04: skipped, Empty (title "Channel 4")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "clock") || strings.Contains(out, "warning") {
		t.Errorf("%s", out)
	}
	// the live addresses of Tiandy: rtsp://<host>:554/<channel>/<1 main, 2 sub>
	if got := s.Export("/camera"); got != "/camera\nadd name=ch01 nvr=nvr04 channel=1 stream=rtsp://127.0.0.1:554/1/1 stream-lq=rtsp://127.0.0.1:554/1/2 alias=Site16 enabled=yes\n"+
		"add name=ch02 nvr=nvr04 channel=2 stream=rtsp://127.0.0.1:554/2/1 stream-lq=rtsp://127.0.0.1:554/2/2 alias=Plac_zolty enabled=yes\n" {
		t.Errorf("%s", got)
	}
	st := ReadStatus(ctx.Root, "nvr04")
	if len(st.Disks) != 1 || st.Disks[0].State != "Success" || st.Disks[0].Error || len(st.ChannelList) != 4 || st.ChannelList[0].Address != "192.168.0.80" ||
		st.ChannelList[0].Detail != "TC-C34WS SPEC:I5/E/Y/2.8mm" || st.ChannelList[0].Serial != "480677C0466A00153" {
		t.Errorf("disks from ISAPI only, cameras with addresses: %+v", st)
	}
	out, err := s.Exec(cmd(t, "/nvr probe nvr04"))
	for _, want := range []string{"== records after the login\n", "DEVMODEL | TC-R3120", "== ISAPI /ISAPI/ContentMgmt/Storage/hdd/\n<?xml", `channel 3 "Channel 3" camera 192.168.0.79 configured true online false`} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("probe misses %q:\n%s", want, out)
		}
	}
	expect(t, s, `/nvr set nvr04 password=wrong`, "")
	expect(t, s, `/nvr scan nvr04`, "login refused: wrong username or password")
}

// From /download to the mp4: login, CONNECT, .sdv demuxed on the fly, the frame rate
// from the stream's SPS.
func TestDownloadFromTiandy(t *testing.T) {
	video, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", "4", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	fake := tiandytest.New()
	defer fake.Close()
	fake.Recorded[1] = video
	s, ctx, nm := dahuaStore(t)
	archive := t.TempDir()
	run(t, s, `/storage add name=archive path=`+archive)
	addNvr(t, s, ctx, nm, `/nvr add type=tiandy name=nvr04 host="`+fake.Addr()+`" http-port=`+strconv.Itoa(fake.HTTPPort())+` username=Admin password=secret`)
	expect(t, s, `/nvr/download add camera=Site16 storage=archive start="2026-10-23 10:00" end="2026-10-23 10:30"`, "")

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
	if j.State != jobs.Done || j.Channel != 1 || j.Remote != "tiandy" {
		t.Fatalf("%+v", j)
	}
	b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration,format_name:stream=codec_name",
		"-of", "default=nw=1", j.Target).Output()
	dur := 0.0
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "duration="); ok {
			dur, _ = strconv.ParseFloat(v, 64)
		}
	}
	if err != nil || !strings.Contains(string(b), "codec_name=h264") || !strings.Contains(string(b), "mp4") || dur < 3.5 || dur > 4.5 {
		t.Errorf("expected a 4 s mp4: %v\n%s", err, b)
	}
	from := time.Date(2026, 10, 23, 10, 0, 0, 0, time.Local)
	_, off := from.Zone() // the range on the recorder's wall clock, as UTC
	wall := from.Unix() + int64(off)
	if len(fake.Connects) != 1 || fake.Connects[0] != "TM:"+strconv.FormatInt(wall, 10)+":"+strconv.FormatInt(wall+1800, 10)+":0" {
		t.Errorf("%v", fake.Connects)
	}
}
