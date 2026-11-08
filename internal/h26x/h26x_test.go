package h26x

import (
	"bytes"
	"os/exec"
	"strconv"
	"testing"
)

func nals(es []byte) [][]byte {
	var out [][]byte
	for _, nal := range bytes.Split(es, []byte{0, 0, 1}) {
		if nal = bytes.TrimRight(nal, "\x00"); len(nal) > 0 {
			out = append(out, nal)
		}
	}
	return out
}

func encode(t *testing.T, codec string, fps int) []byte {
	t.Helper()
	f := "h264"
	if codec == "libx265" {
		f = "hevc"
	}
	b, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate="+strconv.Itoa(fps),
		"-t", "1", "-c:v", codec, "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-f", f, "-").Output()
	if err != nil {
		t.Skipf("ffmpeg %s: %v", codec, err)
	}
	return b
}

func TestSPSFrameRate(t *testing.T) {
	for _, fps := range []int{10, 25, 30} {
		f, ok := 0.0, false
		for _, nal := range nals(encode(t, "libx264", fps)) {
			if nal[0]&0x1f == 7 {
				f, ok = SPSFrameRate(nal)
			}
		}
		if !ok || f != float64(fps) {
			t.Errorf("%d fps: %v %v", fps, f, ok)
		}
	}
	if _, ok := SPSFrameRate([]byte{0x67, 0x42}); ok {
		t.Error("too short")
	}
}

func TestDetect(t *testing.T) {
	if f, fps, ok := Detect(encode(t, "libx264", 25)); !ok || f != "h264" || fps != 25 {
		t.Errorf("%s %v %v", f, fps, ok)
	}
	if f, _, ok := Detect(encode(t, "libx265", 25)); !ok || f != "hevc" {
		t.Errorf("%s %v", f, ok)
	}
	if _, _, ok := Detect([]byte{0, 0, 0, 1, 0x41, 1, 2}); ok {
		t.Error("a P slice tells nothing")
	}
}
