package web

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/dvrip"
	"ffnvr/internal/dvrip/dvriptest"
	"ffnvr/internal/modules"
)

func TestPlaybackDVRIP(t *testing.T) {
	video, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", "60", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	n := dvriptest.New()
	defer n.Close()
	n.PageSize = 20
	begin := time.Date(2026, 11, 5, 10, 0, 0, 0, time.Local)
	n.Recordings = []dvriptest.Recording{{Channel: 0, Begin: begin, End: begin.Add(time.Minute), Video: video, FPS: 10}}
	t.Setenv("TMPDIR", t.TempDir()) // the player's mp4 files: none of an earlier run
	srv, st := setup(t)
	st.exec(t, `/nvr add type=dvrip name=nvr07 host=`+n.Addr()+
		` username=`+dvriptest.User+` password=`+dvriptest.Pass+` valid-users="ola,@firma2"`,
		`/camera add name=ch01 nvr=nvr07 channel=1`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// in the player's tree, with its 4 streams
	_, page := get(t, c, srv.URL+"/?p=archive&n=nvr07")
	contains(t, "dvrip", page, `<a role="tab" href="/?p=archive&amp;n=nvr07&amp;tab=play" aria-selected="true">Playback</a>`, `id="pbdata"`, "4 at once")

	// the day: the file of a minute in pieces of 30 s
	resp, err := c.Get(fmt.Sprintf("%s/archive/segments?n=nvr07&c=ch01&from=%d&to=%d", srv.URL, begin.Add(-time.Hour).Unix(), begin.Add(time.Hour).Unix()))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	ss, _ := m["segs"].([]any)
	if resp.StatusCode != 200 || m["remote"] != true || len(ss) != 2 ||
		ss[1].(map[string]any)["f"] != fmt.Sprintf("%d-%d", begin.Unix()+30, begin.Unix()+60) {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}

	play := func(b int64) (int, string, []byte) {
		t.Helper()
		resp, err := c.Get(fmt.Sprintf("%s/archive/play?n=nvr07&c=ch01&f=%d-%d", srv.URL, b, b+30))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("X-Start"), body
	}
	duration := func(body []byte) float64 {
		mp4 := t.TempDir() + "/piece.mp4"
		os.WriteFile(mp4, body, 0o644)
		out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", mp4).Output()
		if err != nil {
			return 30 // no ffprobe
		}
		d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		return d
	}
	// the second piece: located, faster than real time, an mp4 of 30 s beginning at its start
	code, start, body := play(begin.Unix() + 30)
	if d := duration(body); code != 200 || len(body) < 1000 || start != "" || d < 29 || d > 31 {
		t.Fatalf("play: %d X-Start %q, %d bytes, %v s", code, start, len(body), d)
	}
	n.Mu.Lock()
	if got := strings.Join(n.Actions, ","); got != "Start,Pause,Locate 2026-11-05 10:00:30,Continue,Fast 4,Stop" {
		t.Errorf("%s", got)
	}
	// a camera whose clock is off (the frames stamped 1:55:22 early): all the same
	n.CameraSkew = -(time.Hour + 55*time.Minute + 22*time.Second)
	n.Mu.Unlock()
	code, start, body = play(begin.Unix())
	if d := duration(body); code != 200 || start != "" || d < 29 || d > 31 {
		t.Errorf("skewed camera: %d X-Start %q, %v s", code, start, d)
	}
	// nothing recorded
	if code, _, _ := play(begin.Unix() + 3600); code != 404 {
		t.Errorf("no recording: %d", code)
	}

	// 6 pieces at once (other ranges: not made before): at most 4 streams of the recorder
	n.Mu.Lock()
	n.CameraSkew, n.MaxPlaying = 0, 0
	n.Mu.Unlock()
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := begin.Unix() + 10 + int64(i)*5
			resp, err := c.Get(fmt.Sprintf("%s/archive/play?n=nvr07&c=ch01&f=%d-%d", srv.URL, b, b+4))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		}(i)
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Errorf("play: %d", code)
		}
	}
	n.Mu.Lock()
	if n.MaxPlaying > modules.PlayStreams || n.MaxPlaying > dvrip.MaxStreams || n.MaxPlaying < 2 {
		t.Errorf("at once %d", n.MaxPlaying)
	}
	n.Mu.Unlock()
}
