package web

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/modules"
)

// An H.265 segment as one Uniview IPC records (its RTSP description has sprop-sps and
// sprop-pps, no sprop-vps): the hvcC of the mkv has no VPS, the key frames carry
// VPS, SPS and PPS. ffmpeg opens it with "VPS 0 does not exist", a browser cannot
// decode the mp4 copied from it. The player gets one whose hvcC has the VPS again;
// a browser without H.265 gets H.264, decoded from the sets of the frames.
func TestArchivePlayHEVCWithoutVPS(t *testing.T) {
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
	ts := filepath.Join(t.TempDir(), "a.ts")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=6",
		"-c:v", "libx265", "-x265-params", "keyint=20:min-keyint=20:repeat-headers=1:log-level=error", "-pix_fmt", "yuv420p", "-f", "mpegts", ts).CombinedOutput(); err != nil {
		t.Skipf("no libx265: %v %s", err, out)
	}
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-i", ts, "-c", "copy", p).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	mkv, _ := os.ReadFile(p)
	if err := os.WriteFile(p, dropVPS(t, mkv), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("ffmpeg", "-hide_banner", "-i", p).CombinedOutput(); !strings.Contains(string(out), "VPS 0 does not exist") {
		t.Fatalf("not as recorded: %s", out)
	}
	modules.MaintainLocal(st.e.Store, time.Now(), t.Logf)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	play := srv.URL + "/archive/play?n=nvr01&c=cam1&f=" + seg
	fetch := func(q string) []byte {
		resp, err := c.Get(play + q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "video/mp4" {
			t.Fatalf("%s: %d %s", q, resp.StatusCode, b)
		}
		return b
	}
	check := func(name string, mp4 []byte, codec string, vps bool) {
		f := filepath.Join(t.TempDir(), name+".mp4")
		os.WriteFile(f, mp4, 0o644)
		out, _ := exec.Command("ffprobe", "-v", "error", "-count_frames", "-select_streams", "v", "-show_entries", "stream=codec_name,codec_tag_string,nb_read_frames", "-of", "default=nw=1", f).CombinedOutput()
		if !strings.Contains(string(out), "codec_name="+codec) || !strings.Contains(string(out), "nb_read_frames=60") {
			t.Errorf("%s: %s", name, out)
		}
		if strings.Contains(string(out), "does not exist") {
			t.Errorf("%s: the header lacks a set: %s", name, out)
		}
		if codec == "hevc" && (!strings.Contains(string(out), "codec_tag_string=hvc1") || hvcCTypes(mp4)[32] != vps) {
			t.Errorf("%s: tag or VPS in hvcC: %s %v", name, out, hvcCTypes(mp4))
		}
	}
	check("hevc", fetch("&hevc=1"), "hevc", true)
	check("h264", fetch(""), "h264", false)
}

// dropVPS: the mkv without the VPS in its CodecPrivate (hvcC); the bytes it took are
// an EBML Void after it, so that nothing moves.
func dropVPS(t *testing.T, mkv []byte) []byte {
	t.Helper()
	i := bytes.Index(mkv, []byte{0x63, 0xa2})
	if i < 0 || mkv[i+2]&0x80 == 0 {
		t.Fatal("no CodecPrivate of one byte of size")
	}
	size := int(mkv[i+2] & 0x7f)
	data := mkv[i+3 : i+3+size]
	out := append([]byte{}, data[:22]...)
	out = append(out, data[22]-1)
	for p, n := 23, int(data[22]); n > 0; n-- {
		q := p + 3
		for k := int(binary.BigEndian.Uint16(data[p+1:])); k > 0; k-- {
			q += 2 + int(binary.BigEndian.Uint16(data[q:]))
		}
		if data[p]&0x3f != 32 {
			out = append(out, data[p:q]...)
		}
		p = q
	}
	gone := size - len(out)
	if gone < 3 {
		t.Fatal("no VPS in the hvcC")
	}
	el := append([]byte{0x63, 0xa2, 0x80 | byte(len(out))}, out...)
	el = append(el, 0xec, 0x80|byte(gone-2))
	el = append(el, make([]byte, gone-2)...)
	res := append([]byte{}, mkv[:i]...)
	res = append(res, el...)
	return append(res, mkv[i+3+size:]...)
}

// hvcCTypes: the NAL types of the arrays of the first hvcC box of an mp4.
func hvcCTypes(mp4 []byte) map[int]bool {
	i := bytes.Index(mp4, []byte("hvcC"))
	if i < 0 || i+4+23 > len(mp4) {
		return nil
	}
	d := mp4[i+4 : i-4+int(binary.BigEndian.Uint32(mp4[i-4:]))]
	types := map[int]bool{}
	p := 23
	for n := int(d[22]); n > 0 && p+3 <= len(d); n-- {
		types[int(d[p]&0x3f)] = true
		q := p + 3
		for k := int(binary.BigEndian.Uint16(d[p+1:])); k > 0 && q+2 <= len(d); k-- {
			q += 2 + int(binary.BigEndian.Uint16(d[q:]))
		}
		p = q
	}
	return types
}
