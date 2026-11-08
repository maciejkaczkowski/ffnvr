package modules

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestThumbs(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	s := newStore(t)
	disk, root := t.TempDir(), t.TempDir()
	run(t, s, `/storage add name=disk1 path=`+disk, `/nvr add type=local name=nvr01 storage=disk1`, `/camera add name=cam1 nvr=nvr01 stream=rtsp://h/1`)
	seg := "2026-10-29T08-00-00.mkv"
	p := filepath.Join(disk, "nvr01", "cam1", seg)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=10",
		"-t", "2", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", p).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	if PendingThumbs(root) {
		t.Error("nothing asked yet")
	}
	RequestThumb(root, "nvr01", "cam1", seg)
	RequestThumb(root, "nvr01", "cam1", "../../etc/passwd") // never made
	if !PendingThumbs(root) {
		t.Error("asked")
	}
	MakeThumbs(context.Background(), s, root, "ffmpeg", 10, t.Logf)
	thumb := ThumbPath(s, "nvr01", "cam1", seg)
	if thumb != filepath.Join(disk, "nvr01", ".cam1", "thumbs", "2026-10-29T08-00-00.jpg") {
		t.Fatal(thumb)
	}
	b, err := os.ReadFile(thumb)
	if err != nil || len(b) < 500 || b[0] != 0xff || b[1] != 0xd8 {
		t.Fatalf("%d bytes %v", len(b), err)
	}
	if PendingThumbs(root) {
		t.Error("the requests are done")
	}
	// gone with its segment, or when old
	MaintainLocal(s, time.Now(), t.Logf)
	if _, err := os.Stat(thumb); err != nil {
		t.Error("kept while its segment is there")
	}
	MaintainLocal(s, time.Now().Add(ThumbKeep+time.Hour), t.Logf)
	if _, err := os.Stat(thumb); err == nil {
		t.Error("an old thumbnail is deleted")
	}
}
