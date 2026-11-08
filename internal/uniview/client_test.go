package uniview

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/uniview/univiewtest"
)

// testVideo is 3 s of H.264 at 10 fps from ffmpeg, or nil.
func testVideo(t *testing.T) []byte {
	t.Helper()
	b, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10",
		"-t", "3", "-c:v", "libx264", "-preset", "ultrafast", "-g", "10", "-pix_fmt", "yuv420p", "-f", "h264", "-").Output()
	if err != nil {
		t.Skip("no ffmpeg")
	}
	return b
}

func TestInfo(t *testing.T) {
	n := univiewtest.New()
	defer n.Close()
	c, err := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := c.DeviceInfo(ctx)
	if err != nil || d.DeviceModel != "NVR302-16S" || d.FirmwareVersion != "NVR-B3601.16.10.250101" || d.SerialNumber == "" {
		t.Errorf("%+v %v", d, err)
	}
	chs, err := c.Channels(ctx)
	if err != nil || len(chs) != 4 || chs[0].Name != "Wejscie glowne" || chs[0].State() != "Connected" || chs[2].State() != "Disconnected" || chs[3].State() != "Empty" ||
		chs[0].AddressInfo.Address != "192.168.1.31" || chs[0].DeviceModel != "IPC322LB" || chs[2].Reason() != "wrong username or password" || chs[0].Reason() != "" {
		t.Errorf("%+v %v", chs, err)
	}
	disks, err := c.Disks(ctx)
	if err != nil || len(disks) != 1 || disks[0].TotalCapacity != 3815447 || disks[0].State() != "Normal" || disks[0].Kind != "LocalHDD" {
		t.Errorf("%+v %v", disks, err)
	}
	// an older recorder: only Storage/Containers, in its own shape
	// as captured from a BCS-P-NVR3204-4K-II (B3121P21C00815)
	n.OldDisks = `{"LocalHDDNums": 4, "LocalHDDs": [{"ID": 1, "RemainCapacity": 0, "TotalCapacity": 3815447, "Manufacturer": "SEAGATE", "Status": 3, "Property": 0, "FormatProgress": 0, "GroupID": 1}, {"ID": 2, "RemainCapacity": 0, "TotalCapacity": 3815447, "Manufacturer": "SEAGATE", "Status": 3, "Property": 0, "FormatProgress": 0, "GroupID": 1}, {"ID": 3, "RemainCapacity": 0, "TotalCapacity": 0, "Manufacturer": "", "Status": 0, "Property": 255, "FormatProgress": 0, "GroupID": 1}, {"ID": 4, "RemainCapacity": 0, "TotalCapacity": 0, "Manufacturer": "", "Status": 0, "Property": 255, "FormatProgress": 0, "GroupID": 1}], "SDNums": 0, "No1ExtendCabinetHDDNums": 0, "ArrayNums": 0, "NASNums": 0, "eSATANums": 0}`
	if disks, err := c.Disks(ctx); err != nil || len(disks) != 2 || disks[0].TotalCapacity != 3815447 || disks[0].RemainCapacity != 0 || disks[0].Kind != "LocalHDD" || disks[1].ID != 2 || disks[0].State() != "Normal" {
		t.Errorf("old: %+v %v", disks, err)
	}
	if up, err := c.Uptime(ctx); err != nil || up != 5371975*time.Second {
		t.Errorf("%v %v", up, err)
	}
	if d, _ := c.DeviceInfo(ctx); d.Build() != "2017-10-28" {
		t.Errorf("%+v", d)
	}
	n.OldDisks = `{"Nums":1,"Containers":[{"Name":"disk","UsedCapacity":500,"Capacity":1000}]}`
	if disks, err := c.Disks(ctx); err != nil || len(disks) != 1 || disks[0].RemainCapacity != 500 {
		t.Errorf("old, used only: %+v %v", disks, err)
	}
	n.OldDisks = `{"Nums":0}`
	if _, err := c.Disks(ctx); err == nil || !strings.Contains(err.Error(), "no disks in the answer") {
		t.Errorf("%v", err)
	}
	n.OldDisks = ""
	if tm, err := c.Time(ctx); err != nil || time.Since(tm) > time.Minute {
		t.Errorf("%v %v", tm, err)
	}
	if u := c.StreamURL(ctx, 2, 0); u != "rtsp://127.0.0.1:554/unicast/c2/s0/live" {
		t.Error(u)
	}
	if _, err := c.Get(ctx, "/LAPI/V1.0/Nothing", nil, nil); !errors.Is(err, ErrDevice) {
		t.Errorf("%v", err)
	}
	bad, _ := NewClient(n.Addr(), "admin", "wrong", false)
	if _, err := bad.DeviceInfo(ctx); err == nil || !strings.Contains(err.Error(), "wrong username or password") {
		t.Errorf("%v", err)
	}
}

func TestOpenPlaysRecording(t *testing.T) {
	n := univiewtest.New()
	defer n.Close()
	n.Video = testVideo(t)
	n.FPS = 10
	from := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)
	n.Recorded[1] = [2]int64{from.Unix(), from.Add(time.Hour).Unix()}
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	ctx := context.Background()

	if _, _, _, err := c.Open(ctx, 2, from, from.Add(time.Hour), 0); !errors.Is(err, ErrEmpty) {
		t.Errorf("channel 2 has no recordings: %v", err)
	}
	r, codec, fps, err := c.Open(ctx, 1, from, from.Add(time.Hour), 8)
	if err != nil || codec != "H264" || fps != 10 {
		t.Fatalf("%s %v %v", codec, fps, err)
	}
	var out bytes.Buffer
	io.Copy(&out, r)
	r.Close()
	if n.SpeedSeen != "8.0" {
		t.Errorf("Speed header: %q", n.SpeedSeen)
	}
	// ffmpeg reads it back: 3 s
	cmd := exec.Command("ffprobe", "-v", "error", "-f", "h264", "-framerate", "10", "-count_frames", "-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", "-")
	cmd.Stdin = &out
	b, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(b)) != "30" {
		t.Errorf("frames: %q %v", b, err)
	}
	var reqs string
	for _, q := range n.Requests {
		reqs += q + "\n"
	}
	if !strings.Contains(reqs, "/RecordURL?Begin=") {
		t.Errorf("%s", reqs)
	}
}

func TestOpenCancel(t *testing.T) {
	n := univiewtest.New()
	defer n.Close()
	n.Video = testVideo(t)
	from := time.Now().Add(-time.Hour)
	n.Recorded[1] = [2]int64{from.Unix(), from.Add(time.Hour).Unix()}
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	ctx, cancel := context.WithCancel(context.Background())
	r, _, _, err := c.Open(ctx, 1, from, from.Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, r); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling does not stop the download")
	}
}

func TestDepacketizer(t *testing.T) {
	var buf bytes.Buffer
	d := newDepacketizer("H265", &buf)
	// IDR_W_RADL (type 19): header 0x26 0x01
	d.push(rtpPacket{Seq: 1, Payload: []byte{49 << 1, 1, 0x80 | 19, 0xAA}})
	d.push(rtpPacket{Seq: 2, Payload: []byte{49 << 1, 1, 0x40 | 19, 0xBB}})
	if !bytes.Equal(buf.Bytes(), []byte{0, 0, 0, 1, 0x26, 0x01, 0xAA, 0xBB}) {
		t.Fatalf("% x", buf.Bytes())
	}
	buf.Reset()
	d = newDepacketizer("H264", &buf)
	d.push(rtpPacket{Seq: 1, Payload: []byte{24, 0, 2, 0x67, 1, 0, 1, 0x68}}) // STAP-A: SPS, PPS
	d.push(rtpPacket{Seq: 2, Payload: []byte{0x7c, 0x85, 1}})                 // FU-A start of an IDR
	d.push(rtpPacket{Seq: 4, Payload: []byte{0x7c, 0x45, 2}})                 // end, but 3 was lost
	d.push(rtpPacket{Seq: 5, Payload: []byte{0x41, 9}})
	if !bytes.Equal(buf.Bytes(), []byte{0, 0, 0, 1, 0x67, 1, 0, 0, 0, 1, 0x68, 0, 0, 0, 1, 0x41, 9}) {
		t.Fatalf("% x", buf.Bytes())
	}
	if measureFPS(0, 3600*24, 25) != 25 || measureFPS(0, 3000*29, 30) != 30 || measureFPS(0, 0, 1) != 25 {
		t.Error("fps")
	}
}

// RTSP servers differ in the form of the Digest answer they accept: every form is
// tried until one is.
func TestRTSPAuthForms(t *testing.T) {
	video := testVideo(t)
	for _, mode := range []string{"", "rfc2069", "path"} {
		n := univiewtest.New()
		n.Video, n.FPS, n.RTSPAuth = video, 10, mode
		from := time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local)
		n.Recorded[2] = [2]int64{from.Unix(), from.Add(4 * time.Hour).Unix()}
		c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
		r, codec, _, err := c.Open(context.Background(), 2, from, from.Add(4*time.Hour), 16)
		if err != nil || codec != "H264" {
			t.Errorf("%q: %v", mode, err)
			n.Close()
			continue
		}
		b, _ := io.ReadAll(r)
		r.Close()
		if len(b) < 1000 {
			t.Errorf("%q: %d bytes", mode, len(b))
		}
		n.Close()
	}
	// a wrong password: every form refused, the message tells what was tried
	n := univiewtest.New()
	defer n.Close()
	n.Video = video
	from := time.Now().Add(-time.Hour)
	n.Recorded[1] = [2]int64{from.Unix(), from.Add(time.Hour).Unix()}
	n.RTSPAuth = "nothing-fits"
	c, _ := NewClient(n.Addr(), univiewtest.User, univiewtest.Pass, false)
	if _, _, _, err := c.Open(context.Background(), 1, from, from.Add(time.Hour), 0); err == nil ||
		!strings.Contains(err.Error(), "wrong username or password") || !strings.Contains(err.Error(), "variant 2") {
		t.Errorf("%v", err)
	}
}
