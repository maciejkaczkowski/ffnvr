package modules

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"ffnvr/internal/module"
)

const camLine = `/camera/add name="cam02" nvr="nvr01" stream="rtsp://192.168.0.59:554/live0.264" enabled="true"`

func withNvr(t *testing.T) *module.Context {
	t.Helper()
	s := newStore(t)
	for i, err := range run(t, s, `/storage add name=disk1 path=/srv/disk1`, `/nvr add type=local name=nvr01 storage=disk1`) {
		if err != "" {
			t.Fatalf("line %d: %s", i, err)
		}
	}
	return &module.Context{Store: s, Root: t.TempDir()}
}

func TestCameraRequiredAndChecks(t *testing.T) {
	c := withNvr(t)
	s := c.Store
	expect(t, s, `/camera add name=c1 nvr=nvr01`, "stream is required for a camera of a type=local nvr")
	expect(t, s, `/camera add stream=rtsp://h/x`, "missing required parameters name, nvr")
	expect(t, s, `/camera add name=c1 nvr=nvr9 stream=rtsp://h/x`, `nvr: "nvr9" does not exist in /nvr`)
	expect(t, s, `/camera add name=c1 nvr=nvr01 stream=http://h/x`, "expected rtsp://")
	expect(t, s, `/camera add name=c1 nvr=nvr01 stream="rtsp://h/a b"`, "spaces, quotes")
	expect(t, s, `/camera add name="c 1" nvr=nvr01 stream=rtsp://h/x`, "only letters, digits")
	expect(t, s, `/camera add name=-c nvr=nvr01 stream=rtsp://h/x`, "only letters, digits")
	expect(t, s, `/camera add name=c1 nvr=nvr01 stream=rtsp://h/x enabled=maybe`, "not a boolean")
	expect(t, s, camLine, "")
	expect(t, s, `/camera add name=c2 nvr=nvr01 stream=rtsp://h/x group="Hala A"`, "")
	expect(t, s, `/camera unset c2 nvr`, "missing required parameter nvr")
	expect(t, s, `/nvr remove nvr01`, "/nvr: name=nvr01 is used by /camera cam02")
	want := "/camera\nadd name=cam02 nvr=nvr01 stream=rtsp://192.168.0.59:554/live0.264 enabled=yes\n" +
		"add name=c2 nvr=nvr01 stream=rtsp://h/x group=\"Hala A\" enabled=yes\n"
	if got := s.Export("/camera"); got != want {
		t.Errorf("enabled defaults to yes:\n%s\nwant\n%s", got, want)
	}
	// enable/disable work on "enabled"; unset brings the default back
	expect(t, s, `/camera disable c2`, "")
	if !strings.Contains(s.Export("/camera"), `group="Hala A" enabled=no`) {
		t.Errorf("disable: %s", s.Export("/camera"))
	}
	expect(t, s, `/camera unset c2 enabled`, "")
	if !strings.Contains(s.Export("/camera"), `group="Hala A" enabled=yes`) {
		t.Errorf("unset enabled: %s", s.Export("/camera"))
	}
}

func TestCameraUnit(t *testing.T) {
	c := withNvr(t)
	expect(t, c.Store, camLine, "")
	us, err := cameraModule{}.Units(c)
	if err != nil || len(us) != 1 {
		t.Fatalf("%v %v", us, err)
	}
	want, _ := os.ReadFile("testdata/ffnvr-nvr01-cam02.service")
	if us[0].Name != "ffnvr-nvr01-cam02.service" || us[0].Content != string(want) {
		t.Errorf("%s:\n%s", us[0].Name, us[0].Content)
	}
}

func TestCameraUnitsFollowState(t *testing.T) {
	c := withNvr(t)
	run(t, c.Store,
		`/camera add name=a nvr=nvr01 stream=rtsp://u:p%40ss$x@h/x group="50% $HOME"`,
		`/camera add name=b nvr=nvr01 stream=rtsp://h/b enabled=no`,
	)
	us, _ := cameraModule{}.Units(c)
	if len(us) != 1 || us[0].Name != "ffnvr-nvr01-a.service" {
		t.Fatalf("only enabled cameras get a unit: %v", us)
	}
	// % and $ must reach ffmpeg literally, not as systemd specifiers or variables
	if !strings.Contains(us[0].Content, "-i rtsp://u:p%%40ss$$x@h/x ") || !strings.Contains(us[0].Content, "Group: 50%% $$HOME,") {
		t.Errorf("escaping:\n%s", us[0].Content)
	}
	// a disabled nvr records nothing
	run(t, c.Store, `/nvr disable nvr01`)
	if us, _ := (cameraModule{}).Units(c); len(us) != 0 {
		t.Errorf("disabled nvr: %v", us)
	}
}

func TestCameraRecordDirFollowsStorage(t *testing.T) {
	c := withNvr(t)
	expect(t, c.Store, `/storage add name=disk2 path="/data/rec/"`, "")
	expect(t, c.Store, `/storage add name=bad path="/data/rec 1"`, "only letters, digits and / _ - .")
	expect(t, c.Store, `/storage add name=bad path="/data/%Y"`, "only letters, digits and / _ - .")
	expect(t, c.Store, camLine, "")
	expect(t, c.Store, `/nvr set nvr01 storage=disk2`, "")
	us, _ := cameraModule{}.Units(c)
	for _, want := range []string{"ExecStartPre=/bin/mkdir -p /data/rec/nvr01/cam02\n",
		" /data/rec/nvr01/cam02/%%Y-%%m-%%dT%%H-%%M-%%S.mkv\n"} {
		if len(us) != 1 || !strings.Contains(us[0].Content, want) {
			t.Errorf("missing %q:\n%v", want, us)
		}
	}
}

// The generated command line is accepted by the installed ffmpeg: with the stream down
// it fails on the connection, not on an option (-stimeout is gone since ffmpeg 5).
func TestCameraUnitRunsWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	c := withNvr(t)
	expect(t, c.Store, `/camera add name=cam nvr=nvr01 stream=rtsp://127.0.0.1:1/live`, "")
	us, _ := cameraModule{}.Units(c)
	var args []string
	for _, l := range strings.Split(us[0].Content, "\n") {
		if cmd, ok := strings.CutPrefix(l, "ExecStart=/usr/bin/ffmpeg "); ok {
			args = strings.Fields(strings.ReplaceAll(cmd, "%%", "%"))
		}
	}
	if len(args) == 0 {
		t.Fatal("no ExecStart")
	}
	out, _ := exec.Command("ffmpeg", append([]string{"-nostdin"}, args...)...).CombinedOutput()
	if strings.Contains(string(out), "Unrecognized option") || strings.Contains(string(out), "Option not found") ||
		!strings.Contains(string(out), "Connection refused") {
		t.Errorf("ffmpeg does not take the command line:\n%s", out)
	}
}
