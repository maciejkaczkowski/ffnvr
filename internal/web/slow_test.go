package web

import (
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"ffnvr/internal/modules"
)

// A download over a slow connection lasts longer than the server's limits for pages:
// it goes on while data flows.
func TestSlowDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	t.Setenv("TMPDIR", t.TempDir())
	defer func(r, w time.Duration) { readTimeout, writeTimeout = r, w }(readTimeout, writeTimeout)
	readTimeout, writeTimeout = time.Second, 2*time.Second
	defer func(s time.Duration) { stallTimeout = s }(stallTimeout)
	stallTimeout = 2 * time.Second

	ts, st := setup(t)
	disk := findItem(st.e.Store, "/storage", "name", "disk1").Val("path")
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	seg := start.Format("2006-01-02T15-04-05") + ".mkv"
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25:duration=8",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "8", "-pix_fmt", "yuv420p", filepath.Join(disk, "nvr01", "cam1", seg)).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	modules.MaintainLocal(st.e.Store, time.Now(), t.Logf)

	// the server as ffnvr web runs it
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := HTTPServer("", ts.Config.Handler)
	go hs.Serve(ln)
	defer hs.Close()
	base := "http://" + ln.Addr().String()
	c := client()
	if code, _ := signIn(t, c, base, "jan", "secret"); code != 200 {
		t.Fatal("sign in")
	}

	slow := func(path string) (int64, int64, error) {
		resp, err := c.Get(base + path)
		if err != nil {
			return 0, 0, err
		}
		defer resp.Body.Close()
		var n int64
		buf := make([]byte, 128<<10)
		for {
			k, err := resp.Body.Read(buf)
			n += int64(k)
			if err == io.EOF {
				return n, resp.ContentLength, nil
			}
			if err != nil {
				return n, resp.ContentLength, err
			}
			time.Sleep(time.Duration(k) * time.Second / (1300 << 10)) // ~1.3 MB/s, however small the pieces
		}
	}
	for _, path := range []string{
		"/archive/file?n=nvr01&c=cam1&f=" + seg + "&fmt=mkv",
		"/archive/file?n=nvr01&c=cam1&f=" + seg + "&fmt=mp4",
		"/archive/play?n=nvr01&c=cam1&f=" + seg,
	} {
		began := time.Now()
		n, length, err := slow(path)
		took := time.Since(began)
		if err != nil || n < 4<<20 || (length > 0 && n != length) {
			t.Errorf("%s: %d of %d bytes in %v: %v", path, n, length, took.Round(time.Millisecond), err)
		} else if took < 3*time.Second {
			t.Errorf("%s: %v, not slow enough to test", path, took)
		} else {
			t.Logf("%s: %d bytes in %v", path, n, took.Round(time.Millisecond))
		}
	}

	// a client that stops reading is dropped after stallTimeout, not kept forever
	began := time.Now()
	resp, err := c.Get(base + "/archive/file?n=nvr01&c=cam1&f=" + seg + "&fmt=mkv")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(stallTimeout + 3*time.Second)
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err == nil && n == resp.ContentLength {
		t.Errorf("a stalled download is cut: got all %d bytes", n)
	}
	if took := time.Since(began); took > stallTimeout+10*time.Second {
		t.Errorf("a stalled download is cut after stallTimeout, not %v", took)
	}
}
