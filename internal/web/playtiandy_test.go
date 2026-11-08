package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/modules"
	"ffnvr/internal/tiandy"
	"ffnvr/internal/tiandy/tiandytest"
)

func TestPlaybackTiandy(t *testing.T) {
	defer tiandy.CloseSessions()
	video, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", "3", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	n := tiandytest.New()
	defer n.Close()
	n.Recorded[1] = video
	n.Stamp = 10
	n.Hold = 200 * time.Millisecond
	n.Searched = map[int][][3]string{1: {
		{"2026-10-31 23:50:00", "2026-11-01 00:30:00", "Timer"}, // across midnight and the month
		{"2026-11-01 00:30:01", "2026-11-01 01:00:45", "Alarm"}, // motion follows
		{"2026-11-03 10:00:00", "2026-11-03 11:00:00", "Timer"},
	}}
	t.Setenv("TMPDIR", t.TempDir()) // the player's mp4 files: none of an earlier run
	srv, st := setup(t)
	st.exec(t, `/nvr add type=tiandy name=nvr05 host=`+n.Addr()+` http-port=`+strconv.Itoa(n.HTTPPort())+
		` username=`+tiandytest.User+` password=`+tiandytest.Pass+` valid-users="ola,@firma2"`,
		`/camera add name=ch01 nvr=nvr05 channel=1`)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// in the player's tree, with its 4 streams
	_, page := get(t, c, srv.URL+"/?p=archive&n=nvr05")
	contains(t, "tiandy", page, `<a role="tab" href="/?p=archive&amp;n=nvr05&amp;tab=play" aria-selected="true">Playback</a>`, `id="pbdata"`, "4 at once")

	segs := func(from, to time.Time) map[string]any {
		t.Helper()
		resp, err := c.Get(fmt.Sprintf("%s/archive/segments?n=nvr05&c=ch01&from=%d&to=%d", srv.URL, from.Unix(), to.Unix()))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		if resp.StatusCode != 200 {
			t.Fatalf("%d %v", resp.StatusCode, m)
		}
		return m
	}
	searches := func(kind string) int {
		n.Mu.Lock()
		defer n.Mu.Unlock()
		k := 0
		for _, s := range n.Searches {
			if strings.HasPrefix(s, kind) {
				k++
			}
		}
		return k
	}
	// the day as the player asks for it: from 15 minutes before to 15 after
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)
	m := segs(day.Add(-15*time.Minute), day.Add(24*time.Hour+15*time.Minute))
	ss := m["segs"].([]any)
	first, last := ss[0].(map[string]any), ss[len(ss)-1].(map[string]any)
	// 23:50…01:00:45 in pieces of 30 s: one file, the spans joined
	if m["remote"] != true || len(ss) != 142 || first["f"] != fmt.Sprintf("%d-%d", day.Unix()-600, day.Unix()-570) ||
		last["e"].(float64) != float64(day.Unix()+3645) {
		t.Errorf("%d %v %v %v", len(ss), m["remote"], first, last)
	}
	// the alarms are the motion bars of the pieces: a part a second
	for _, x := range ss {
		p := x.(map[string]any)
		mark, _ := p["m"].(string)
		switch b := int64(p["b"].(float64)); {
		case b == day.Unix()+1800 && mark != "0"+strings.Repeat("1", 29),
			b == day.Unix()+3630 && mark != strings.Repeat("1", 15),
			b < day.Unix()+1800 && mark != "":
			t.Errorf("piece at %d: %q", b-day.Unix(), mark)
		}
	}
	if d := fmt.Sprint(m["days"]); d != "[2026-10-31 2026-11-01 2026-11-03]" {
		t.Errorf("days: %s", d)
	}
	// October 31 and November 1 searched (2 is not recorded); the calendars of both months
	if searches("day") != 2 || searches("track") < 2 {
		t.Errorf("searches: %v", n.Searches)
	}
	before := len(n.Searches)
	segs(day, day.Add(24*time.Hour))
	if len(n.Searches) != before {
		t.Errorf("asked again: %v", n.Searches[before:])
	}

	// a piece: an mp4 of the stream (of 2 s: the fake has 3 s, and the recorder goes on
	// past the end of the range; when there is nothing more, its silence ends a stream)
	b := day.Unix() + 600
	resp, err := c.Get(fmt.Sprintf("%s/archive/play?n=nvr05&c=ch01&f=%d-%d", srv.URL, b, b+2))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" || len(body) < 1000 {
		t.Fatalf("play: %d %s %d", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	}
	mp4 := t.TempDir() + "/piece.mp4"
	os.WriteFile(mp4, body, 0o644)
	if out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", mp4).Output(); err == nil {
		if d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); d < 1.5 || d > 2.5 {
			t.Errorf("the piece lasts %v s, want 2: the range's end", d)
		}
	}
	n.Mu.Lock()
	tm := n.Connects[len(n.Connects)-1]
	n.Mu.Unlock()
	_, off := day.Zone()
	if want := fmt.Sprintf("TM:%d:%d:0", b+int64(off), b+2+int64(off)); tm != want {
		t.Errorf("%s, want %s", tm, want)
	}

	// a piece read from the key frame 2 s before its start: kept whole (the key frame
	// too), the player told where it begins
	n.Mu.Lock()
	n.StampLead = 2
	n.Mu.Unlock()
	b = day.Unix() + 1200
	resp, err = c.Get(fmt.Sprintf("%s/archive/play?n=nvr05&c=ch01&f=%d-%d", srv.URL, b, b+2))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Start") != "-2.000" {
		t.Errorf("%d X-Start %q", resp.StatusCode, resp.Header.Get("X-Start"))
	}
	os.WriteFile(mp4, body, 0o644)
	if out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration,start_time", "-of", "csv=p=0", mp4).Output(); err == nil {
		f := strings.Split(strings.TrimSpace(string(out)), ",")
		if d, _ := strconv.ParseFloat(f[len(f)-1], 64); f[0] != "0.000000" || d < 2.5 {
			t.Errorf("the piece from its key frame: %s", out)
		}
	}
	// asked again (a Range of the browser): the same
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/archive/play?n=nvr05&c=ch01&f=%d-%d", srv.URL, b, b+2), nil)
	req.Header.Set("Range", "bytes=0-1")
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode != 206 || resp.Header.Get("X-Start") != "-2.000" {
			t.Errorf("range: %d %q", resp.StatusCode, resp.Header.Get("X-Start"))
		}
	}
	n.Mu.Lock()
	n.StampLead = 0
	n.Mu.Unlock()

	// 8 tiles at once: at most 4 streams of the recorder, one session
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := day.Unix() + 900 + int64(i)*30
			resp, err := c.Get(fmt.Sprintf("%s/archive/play?n=nvr05&c=ch01&f=%d-%d", srv.URL, b, b+2))
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
	if n.MaxActive > modules.PlayStreams || n.MaxActive < 2 || n.Logins != 1 {
		t.Errorf("at once %d, logins %d", n.MaxActive, n.Logins)
	}
	n.Mu.Unlock()
}
