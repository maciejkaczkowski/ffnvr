package modules

import (
	"strings"
	"testing"

	"ffnvr/internal/module"
)

func TestTranscodeConfig(t *testing.T) {
	s := newStore(t)
	for _, l := range []string{
		`/storage add name=disk1 path=/srv/disk1`,
		`/nvr add type=local name=nvr01 storage=disk1`,
		`/camera add name=cam1 nvr=nvr01 stream="rtsp://192.168.10.21/s0" stream-lq="rtsp://192.168.10.21/s1"`,
		`/user/group add group=firma1`,
		`/user/group add group=firma2`,
		`/user add user=jan groups=firma1 password=x`,
		`/user add user=ola groups=firma2 password=x`,
		`/user add user=ewa password=x`,
		`/system/live set enabled=yes`,
	} {
		expect(t, s, l, "")
	}
	ctx := &module.Context{Store: s}
	us, _ := liveModule{}.Units(ctx)
	if conf := us[1].Content; strings.Contains(conf, "/tc-") || !strings.Contains(conf, "rtsp:\n  listen: ''\n") {
		t.Errorf("off by default:\n%s", conf)
	}
	if CanTranscode(s, "jan") {
		t.Error("off by default")
	}

	expect(t, s, `/system/live set transcode-height=721`, "even number")
	expect(t, s, `/system/live set transcode-height=100`, "even number")
	expect(t, s, `/system/live set transcode-max=-1`, "transcode-max must be 0..")
	expect(t, s, `/system/live set transcode-lq-max=33`, "transcode-lq-max must be 0..")
	expect(t, s, `/system/live set transcode-lq-height=301`, "transcode-lq-height must be an even number")
	expect(t, s, `/system/live set transcode-users=nobody`, "does not exist")
	expect(t, s, `/system/live set transcode-users=@nogroup`, "does not exist")
	expect(t, s, `/system/live set transcode-hardware=gpu`, "is not one of")
	expect(t, s, `/system/live set transcode=yes transcode-users="ewa,@firma1" transcode-max=3`, "")
	us, _ = liveModule{}.Units(ctx)
	conf := us[1].Content
	for _, want := range []string{"  'nvr01/cam1/tc-hq': 'ffmpeg:nvr01/cam1/hq#video=h264#width=-2#height=720'\n",
		"  'nvr01/cam1/tc-lq': 'ffmpeg:nvr01/cam1/lq#video=h264'\n", "rtsp:\n  listen: '127.0.0.1:8554'\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("config misses %q:\n%s", want, conf)
		}
	}
	if strings.Count(conf, "/tc-") != 2 {
		t.Errorf("one twin per stream:\n%s", conf)
	}
	if strings.Contains(us[0].Content, "SupplementaryGroups") {
		t.Error("software: no GPU groups")
	}
	for user, want := range map[string]bool{"jan": true, "ewa": true, "ola": false, "nobody": false} {
		if CanTranscode(s, user) != want {
			t.Errorf("CanTranscode(%s) != %v", user, want)
		}
	}
	expect(t, s, `/user set [find user=jan] disabled=yes`, "")
	if CanTranscode(s, "jan") {
		t.Error("a disabled user")
	}
	expect(t, s, `/system/live set transcode-height=480 transcode-hardware=vaapi`, "")
	us, _ = liveModule{}.Units(ctx)
	if !strings.Contains(us[1].Content, "'ffmpeg:nvr01/cam1/hq#video=h264#width=-2#height=480#hardware=vaapi'") {
		t.Errorf("height and engine:\n%s", us[1].Content)
	}
	if !strings.Contains(us[0].Content, "User=ffnvr-go2rtc\nSupplementaryGroups=video render\n") {
		t.Errorf("the GPU's groups:\n%s", us[0].Content)
	}
	expect(t, s, `/system/live set transcode-hardware=auto transcode-lq-height=360`, "")
	us, _ = liveModule{}.Units(ctx)
	if !strings.Contains(us[1].Content, "#height=480#hardware'") || !strings.Contains(us[1].Content, "'ffmpeg:nvr01/cam1/lq#video=h264#width=-2#height=360#hardware'") {
		t.Errorf("auto: go2rtc detects; LQ scaled:\n%s", us[1].Content)
	}
	if l := LiveSettings(s).Transcode; l.HQ.Max != 3 || l.HQ.Height != 480 || l.LQ.Max != DefaultTranscodeLQMax || l.LQ.Height != 360 || l.Hardware != "auto" {
		t.Errorf("%+v", l)
	}
	if l := LiveSettings(s).Transcode; l.Of("lq") != l.LQ || l.Of("hq") != l.HQ {
		t.Error("Of")
	}
	// a quality with max 0 is not transcoded
	expect(t, s, `/system/live set transcode-max=0`, "")
	us, _ = liveModule{}.Units(ctx)
	if strings.Contains(us[1].Content, "/tc-hq") || !strings.Contains(us[1].Content, "/tc-lq") {
		t.Errorf("HQ off:\n%s", us[1].Content)
	}
	// the twins are not among the live streams the panel offers by itself
	for _, q := range []string{"hq", "lq"} {
		if _, ok := LiveStreams(s)[TranscodeName("nvr01", "cam1", q)]; ok {
			t.Error("tc is not in LiveStreams")
		}
	}
	// the live view off: nothing is transcoded
	expect(t, s, `/system/live set enabled=no`, "")
	if CanTranscode(s, "ewa") {
		t.Error("live view off")
	}
	// a referenced group cannot go away
	expect(t, s, `/system/live set enabled=yes`, "")
	if got := run(t, s, `/user/group remove [find group=firma1]`)[0]; got == "" {
		t.Error("a group used by transcode-users must stay")
	}
}
