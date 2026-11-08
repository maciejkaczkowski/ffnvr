package web

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/modules"
)

func TestArchivePlayer(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	cache := t.TempDir()
	t.Setenv("TMPDIR", cache)
	srv, st := setup(t)
	disk := findItem(st.e.Store, "/storage", "name", "disk1").Val("path")
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	seg := start.Format("2006-01-02T15-04-05") + ".mkv"
	p := filepath.Join(disk, "nvr01", "cam1", seg)
	// as the recorder writes them: mkv, no sound
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=10",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", p).CombinedOutput(); err != nil {
		t.Skipf("%v %s", err, out)
	}
	modules.MaintainLocal(st.e.Store, time.Now(), t.Logf) // the worker's index of the segments
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	search := "/?p=archive&n=nvr01&tab=search&search=1&c=cam1&from=" + start.Add(-time.Minute).Format("2006-01-02T15:04") + "&to=" + start.Add(time.Hour).Format("2006-01-02T15:04")
	for _, view := range []string{"", "&view=tiles"} {
		_, page := get(t, c, srv.URL+search+view)
		contains(t, "results"+view, page, `data-play="/archive/play?n=nvr01&amp;c=cam1&amp;f=`+seg+`"`, `data-label="brama"`,
			`data-b="`+strconv.FormatInt(start.Unix(), 10)+`"`, `<dialog id="player"`, `id="pl-video"`, `data-rate="8"`,
			`.plmsg[hidden]{display:none}`, // else its dark layer stays on the video
			`data-motion="/archive/motion?n=nvr01&amp;c=cam1&amp;f=`+seg+`"`, `id="pl-motion"`, `data-mv="motion"`, `.plmotion[hidden]{display:none}`,
			// H.265 as it is to a browser that plays it, as the player of the archive does
			`codecs="hvc1.1.6.L150.90"`, `'&hevc=1'`, `vid.src=src(b)`)
	}

	play := srv.URL + "/archive/play?n=nvr01&c=cam1&f=" + seg
	// several at once (a browser asks with ranges): made once
	var wg sync.WaitGroup
	bodies := make([][]byte, 4)
	for i := range bodies {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := c.Get(play)
			if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" {
				t.Errorf("%v %v", err, resp)
				return
			}
			bodies[i], _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	mp4 := bodies[0]
	for _, b := range bodies[1:] {
		if !bytes.Equal(b, mp4) {
			t.Error("the same file for all")
		}
	}
	if !bytes.Equal(mp4[4:8], []byte("ftyp")) || bytes.Index(mp4, []byte("moov")) > bytes.Index(mp4, []byte("mdat")) {
		t.Error("an mp4 with its index in front (faststart)")
	}
	files, _ := filepath.Glob(filepath.Join(cache, "ffnvr-play", "*.mp4"))
	if len(files) != 1 {
		t.Errorf("kept once: %v", files)
	}
	req, _ := http.NewRequest("GET", play, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || !bytes.Equal(b, mp4[100:200]) {
		t.Errorf("jumping: %d %d bytes", resp.StatusCode, len(b))
	}
	// a segment still being written: another mp4
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte{0})
	f.Close()
	get(t, c, play)
	if files, _ := filepath.Glob(filepath.Join(cache, "ffnvr-play", "*.mp4")); len(files) != 2 {
		t.Errorf("made again: %v", files)
	}
	noFollow := *c
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, x := range []struct {
		q    string
		code int
	}{
		{"n=nvr01&c=cam1&f=../../etc/passwd", 400}, {"n=nvr01&c=cam1&f=2020-01-01T00-00-00.mkv", 404}, {"n=nvr09&c=x&f=" + seg, 303},
	} {
		resp, err := noFollow.Get(srv.URL + "/archive/play?" + x.q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != x.code {
			t.Errorf("%s: %d, want %d", x.q, resp.StatusCode, x.code)
		}
	}

	// without archive.play: no buttons, no files
	st.exec(t, `/user/policy add name=bez allow="archive,archive.download"`, `/user set jan policy=bez`)
	_, page := get(t, c, srv.URL+search)
	notContains(t, "without archive.play", page, "data-play=", `<dialog id="player"`)
	contains(t, "without archive.play", page, "fmt=mkv")
	if code, body := get(t, c, play); code != 403 || !strings.Contains(body, "archive.play") {
		t.Errorf("%d %s", code, body)
	}
}
