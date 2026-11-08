package modules

import (
	"strings"
	"testing"
)

func TestDeriveLQ(t *testing.T) {
	for _, c := range []struct{ hq, lq string }{
		// the user's examples
		{"rtsp://192.168.0.169:554/unicast/c7/s0/live", "rtsp://192.168.0.169:554/unicast/c7/s1/live"},
		{"rtsp://192.168.0.75:554/12/1", "rtsp://192.168.0.75:554/12/2"},
		{"rtsp://192.168.0.29:554/cam/realmonitor?channel=4&subtype=0", "rtsp://192.168.0.29:554/cam/realmonitor?channel=4&subtype=1"},
		// credentials and ports kept
		{"rtsp://admin:Ha%40slo@10.0.0.1:8554/unicast/c1/s0/live", "rtsp://admin:Ha%40slo@10.0.0.1:8554/unicast/c1/s1/live"},
		{"rtsp://u:p@nvr.example:10554/3/1", "rtsp://u:p@nvr.example:10554/3/2"},
		// Dahua: subtype first, or none at all (the main stream)
		{"rtsp://10.0.0.2/cam/realmonitor?subtype=0&channel=1", "rtsp://10.0.0.2/cam/realmonitor?subtype=1&channel=1"},
		{"rtsp://10.0.0.2/cam/realmonitor?channel=1", "rtsp://10.0.0.2/cam/realmonitor?channel=1&subtype=1"},
		// others
		{"rtsp://10.0.0.3/media/video1", "rtsp://10.0.0.3/media/video2"},
		{"rtsp://10.0.0.4:554/Streaming/Channels/101", "rtsp://10.0.0.4:554/Streaming/Channels/102"},
		{"rtsp://10.0.0.4/Streaming/channels/1601", "rtsp://10.0.0.4/Streaming/channels/1602"},
		{"rtsp://10.0.0.5:554/user=admin&password=&channel=2&stream=0.sdp", "rtsp://10.0.0.5:554/user=admin&password=&channel=2&stream=1.sdp"},
		{"rtsp://10.0.0.5:554/user=admin_password=x_channel=1_stream=0.sdp?real_stream", "rtsp://10.0.0.5:554/user=admin_password=x_channel=1_stream=1.sdp?real_stream"},
		{"dvrip://admin:@192.168.0.49?channel=0&subtype=0", "dvrip://admin:@192.168.0.49?channel=0&subtype=1"},
		// nothing safe to make
		{"", ""},
		{"rtsp://10.0.0.1/unicast/c1/s1/live", ""},                  // already the sub stream
		{"rtsp://10.0.0.1/3/2", ""},                                 // already the sub stream
		{"rtsp://10.0.0.2/cam/realmonitor?channel=1&subtype=1", ""}, // already the sub stream
		{"rtsp://10.0.0.2/cam/realmonitor?channel=1&subtype=2", ""}, // the third stream: not ours to guess
		{"rtsp://10.0.0.6/live/main", ""},                           // unknown
		{"rtsp://10.0.0.6/h264/ch1/main/av_stream", ""},             // unknown
		{"rtsp://10.0.0.6/1", ""},                                   // too little to tell
		{"rtsp://10.0.0.1/unicast/c1/s0/live?x=1", ""},              // not exactly the scheme
		{"rtsp://10.0.0.6/Streaming/Channels/103", ""},
		{"dvrip://10.0.0.7?channel=0&subtype=1", ""},
	} {
		if got := DeriveLQ(c.hq); got != c.lq {
			t.Errorf("%q: got %q, want %q", c.hq, got, c.lq)
		}
	}
}

func TestLiveStreamsDerivedLQ(t *testing.T) {
	s := newStore(t)
	for i, e := range run(t, s,
		`/storage add name=d1 path=/tmp`,
		`/nvr add type=local name=loc storage=d1`,
		`/camera add name=uv nvr=loc stream=rtsp://192.168.0.169:554/unicast/c7/s0/live`,
		`/camera add name=own nvr=loc stream=rtsp://10.0.0.1/3/1 stream-lq=rtsp://10.0.0.1/3/3`,
		`/camera add name=off nvr=loc stream=rtsp://10.0.0.1/4/1 stream-lq=none`,
		`/camera add name=odd nvr=loc stream=rtsp://10.0.0.1/live/main`,
		`/camera add name=xm nvr=loc engine=go2rtc stream="dvrip://admin:@192.168.0.49?channel=0&subtype=0"`,
		`/nvr add type=dahua name=dh host=192.168.0.29 username=admin password=tajne`,
		`/camera add name=ch04 nvr=dh channel=4 stream="rtsp://192.168.0.29:554/cam/realmonitor?channel=4&subtype=0"`,
	) {
		if e != "" {
			t.Fatalf("line %d: %s", i, e)
		}
	}
	st := LiveStreams(s)
	for name, want := range map[string]string{
		"loc/uv/lq":  "rtsp://192.168.0.169:554/unicast/c7/s1/live",
		"loc/own/lq": "rtsp://10.0.0.1/3/3", // the configuration wins
		"loc/xm/lq":  "dvrip://admin:@192.168.0.49?channel=0&subtype=1",
		"dh/ch04/lq": "rtsp://admin:tajne@192.168.0.29:554/cam/realmonitor?channel=4&subtype=1", // the recorder's credentials
		"loc/off/hq": "rtsp://10.0.0.1/4/1",
	} {
		if st[name] != want {
			t.Errorf("%s: got %q, want %q", name, st[name], want)
		}
	}
	for _, name := range []string{"loc/off/lq", "loc/odd/lq"} {
		if v, ok := st[name]; ok {
			t.Errorf("%s: %q, want none", name, v)
		}
	}
	if hq, lq := HasLive(s, "loc", "uv"); !hq || !lq {
		t.Errorf("uv: hq %v lq %v", hq, lq)
	}
	// made, not stored
	if exp := s.Export("/camera"); strings.Contains(exp, "s1/live") || strings.Contains(exp, "subtype=1") {
		t.Errorf("derived addresses were written:\n%s", exp)
	}
	expect(t, s, `/camera add name=bad nvr=loc stream=none`, "expected rtsp://")
}
