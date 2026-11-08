package modules

import (
	"context"
	"fmt"
	"os"
	"testing"
)

func TestRealGo2rtc(t *testing.T) {
	port := os.Getenv("REAL_GO2RTC_PORT")
	if port == "" {
		t.Skip()
	}
	s, root := newStore(t), t.TempDir()
	for i, err := range run(t, s,
		`/storage add name=disk1 path=/srv/disk1`, `/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=cam1 nvr=nvr01 stream=rtsp://a/1 stream-lq=rtsp://a/1s`,
		`/camera add name=cam265 nvr=nvr01 stream=rtsp://a/2 stream-lq=rtsp://a/2s`,
		`/camera add name=gone nvr=nvr01 stream=rtsp://a/3 stream-lq=rtsp://a/3s`,
		`/system/live set enabled=yes port=`+port, `/system/snapshot set enabled=yes`) {
		if err != "" {
			t.Fatalf("%d: %s", i, err)
		}
	}
	TakeSnapshots(context.Background(), s, root, "ffmpeg", func(f string, a ...any) { fmt.Printf("log: "+f+"\n", a...) })
	for _, c := range []string{"cam1", "cam265", "gone"} {
		sc := ReadSnapState(root).Cams["nvr01/"+c]
		info := "-"
		if fi, err := os.Stat(SnapshotPath(root, "nvr01", c)); err == nil {
			info = fmt.Sprint(fi.Size(), " B")
		}
		e := sc.Error
		if len(e) > 150 {
			e = e[:150] + "…"
		}
		fmt.Printf("%-7s ok=%-5v via=%-6s picture=%s err=%s\n", c, sc.OK, sc.Via, info, e)
	}
}
