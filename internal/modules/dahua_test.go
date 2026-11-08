package modules

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/dahua/dahuatest"
	"ffnvr/internal/jobs"
	"ffnvr/internal/module"
)

func TestAliasFromTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Brama wjazdowa": "Brama_wjazdowa", "Plac żółty": "Plac_zolty", "  Hala A/B ": "Hala_A_B",
		"Channel2": "", "IPC": "", "IP Camera 01": "", "IPCamera3": "", "CAM 12": "", "Kanał 3": "", "D1": "", "": "", "***": "", "Wejście-2.1": "Wejscie-2.1",
	} {
		if got := AliasFromTitle(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// dahuaStore: the real modules with a context bound to a temporary root.
func dahuaStore(t *testing.T) (*config.Store, *module.Context, *nvrModule) {
	t.Helper()
	s := config.NewStore()
	ctx := &module.Context{Store: s, Root: t.TempDir()}
	var nm *nvrModule
	for _, m := range All() {
		if b, ok := m.(module.Binder); ok {
			b.Bind(ctx)
		}
		if n, ok := m.(*nvrModule); ok {
			nm = n
		}
		for _, menu := range m.Menus() {
			s.Register(menu)
		}
	}
	s.Describe = func(menu string) []string { return nm.Describe(ctx, menu) }
	return s, ctx, nm
}

func addNvr(t *testing.T, s *config.Store, ctx *module.Context, nm *nvrModule, line string) string {
	t.Helper()
	c := cmd(t, line)
	if _, err := s.Exec(c); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return nm.AfterCommand(ctx, c)
}

func TestNvrDahuaValidation(t *testing.T) {
	s, _, _ := dahuaStore(t)
	expect(t, s, `/nvr add type=dahua name=nvr02 host=10.0.0.1 username=admin`, "type=dahua needs parameter password")
	expect(t, s, `/nvr add type=dahua name=nvr02 host=10.0.0.1 username=admin password=x storage=disk1`, `storage: "disk1" does not exist`)
	run(t, s, `/storage add name=disk1 path=/srv/disk1`)
	expect(t, s, `/nvr add type=dahua name=nvr02 host=10.0.0.1 username=admin password=x storage=disk1`, "storage is not used by type=dahua")
	expect(t, s, `/nvr add type=local name=nvr01 storage=disk1 host=10.0.0.1`, "host is not used by type=local")
	expect(t, s, `/nvr add type=dahua name=nvr02 host="10.0.0.1 x" username=admin password=x`, "no spaces")
	expect(t, s, `/nvr add type=dahua name=nvr02 host=10.0.0.1 username=admin password="p@ss w"`, "")
	if out, _ := s.Exec(cmd(t, "/nvr print")); strings.Contains(out, "p@ss") || !strings.Contains(out, "password=*****") {
		t.Errorf("the password is a secret:\n%s", out)
	}
	if !strings.Contains(s.Export("/nvr"), `password="p@ss w"`) {
		t.Errorf("export keeps it: %s", s.Export("/nvr"))
	}
	// cameras of a remote recorder: channel instead of stream
	expect(t, s, `/camera add name=ch01 nvr=nvr02`, "channel is required for a camera of a type=dahua nvr")
	expect(t, s, `/camera add name=ch01 nvr=nvr02 channel=0`, "channel must be 1 or more")
	expect(t, s, `/camera add name=ch01 nvr=nvr02 channel=1`, "")
	run(t, s, `/nvr add type=local name=nvr01 storage=disk1`)
	// a local camera may have a channel (the <ch> of an address scheme, see profile.go)
	expect(t, s, `/camera add name=c1 nvr=nvr01 stream=rtsp://h/1 channel=1`, "")
	expect(t, s, `/camera add name=ch02 nvr=nvr02 channel=2 host=10.0.0.1`, "host is only for cameras of a type=local nvr")
	// remote cameras get no local recording service
	us, err := cameraModule{}.Units(&module.Context{Store: s})
	if err != nil || len(us) != 1 || us[0].Name != "ffnvr-nvr01-c1.service" {
		t.Errorf("%v %v", us, err)
	}
	expect(t, s, `/nvr scan nvr01`, "only remote recorders can be scanned")
}

func TestNvrDahuaAddScans(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	s, ctx, nm := dahuaStore(t)
	out := addNvr(t, s, ctx, nm, `/nvr add type="dahua" name="nvr02" host="`+fake.Addr()+`" username="admin" password="secret"`)
	for _, want := range []string{
		"nvr02: DHI-NVR5216-16P fw 4.001.0000000.1 sn 7G0123PAZ; cameras 3/4 online; disks 2: 4.0T/8.0T (50%), 1 FAILING",
		`ch01: added, alias Brama_wjazdowa (title "Brama wjazdowa")`,
		`ch02: added (title "Channel2")`,
		`ch03: skipped, Disconnected (title "Plac żółty")`,
		`ch04: added (title "IPC")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	host := strings.Split(fake.Addr(), ":")[0]
	want := "/camera\n" +
		"add name=ch01 nvr=nvr02 channel=1 stream=\"rtsp://" + host + ":554/cam/realmonitor?channel=1&subtype=0\" stream-lq=\"rtsp://" + host +
		":554/cam/realmonitor?channel=1&subtype=1\" alias=Brama_wjazdowa enabled=yes\n"
	if got := s.Export("/camera"); !strings.HasPrefix(got, want) || strings.Count(got, "add ") != 3 {
		t.Errorf("cameras:\n%s\nwant first\n%s", got, want)
	}
	// print shows the state of the recorder
	if out, _ := s.Exec(cmd(t, "/nvr print")); !strings.Contains(out, "cameras 3/4 online") || !strings.Contains(out, "checked ") {
		t.Errorf("print:\n%s", out)
	}

	// later: channel 3 is back, titles changed; the user named ch02 and removed ch04
	fake.Mu.Lock()
	fake.States[3] = "Connected"
	fake.Titles[0] = "Nowa brama"
	fake.Titles[1] = "Parking"
	fake.Time = time.Now().Add(-5 * time.Minute).Format("2006-01-02 15:04:05")
	fake.Mu.Unlock()
	expect(t, s, `/camera set [find name=ch02 nvr=nvr02] alias=wjazd`, "")
	expect(t, s, `/camera remove [find name=ch04 nvr=nvr02]`, "")
	out, err := s.Exec(cmd(t, "/nvr scan nvr02"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ch01: exists as ch01\n", "ch02: exists as ch02\n", `ch03: added, alias Plac_zolty (title "Plac żółty")`,
		"ch04: added", "s (downloads are shifted!)"} {
		if !strings.Contains(out, want) {
			t.Errorf("rescan misses %q:\n%s", want, out)
		}
	}
	exp := s.Export("/camera")
	if !strings.Contains(exp, "alias=Brama_wjazdowa") || !strings.Contains(exp, "alias=wjazd") || strings.Contains(exp, "Nowa_brama") || strings.Contains(exp, "Parking") {
		t.Errorf("aliases set by hand or earlier stay:\n%s", exp)
	}
}

func TestNvrDahuaUnreachable(t *testing.T) {
	fake := dahuatest.New()
	s, ctx, nm := dahuaStore(t)
	out := addNvr(t, s, ctx, nm, `/nvr add type=dahua name=nvr02 host="`+fake.Addr()+`" username=admin password=wrong`)
	if !strings.Contains(out, "warning: /nvr: nvr02") || !strings.Contains(out, "wrong username or password") || !strings.Contains(out, "run: /nvr scan nvr02") {
		t.Errorf("%s", out)
	}
	if len(s.Items("/nvr")) != 1 || len(s.Items("/camera")) != 0 {
		t.Error("the recorder stays, without cameras")
	}
	expect(t, s, `/nvr set nvr02 password=secret`, "")
	expect(t, s, `/nvr scan nvr02`, "")
	fake.Close()
	// the worker's periodic check records the failure, print shows it with the last good state
	var logged []string
	RefreshStatus(context.Background(), s, ctx.Root, func(f string, a ...any) { logged = append(logged, f) })
	if len(logged) != 0 {
		t.Errorf("checked a moment ago: not due yet, %v", logged)
	}
	st := readStatus(ctx.Root, "nvr02")
	st.Checked = st.Checked.Add(-6 * time.Minute) // the default poll interval is 5m
	writeStatus(ctx.Root, "nvr02", st)
	RefreshStatus(context.Background(), s, ctx.Root, func(f string, a ...any) { logged = append(logged, f) })
	out, _ = s.Exec(cmd(t, "/nvr print"))
	if len(logged) != 1 || !strings.Contains(out, "UNREACHABLE:") || !strings.Contains(out, "(last ok ") || !strings.Contains(out, "cameras 3/4 online") {
		t.Errorf("%v\n%s", logged, out)
	}
	if s.Export("/") == "" || strings.Contains(s.Export("/"), "scan") {
		t.Error("scan is not configuration")
	}
}

func TestDownloadFromDahua(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	s, ctx, nm := dahuaStore(t)
	run(t, s, `/storage add name=archive path=/srv/archive`)
	addNvr(t, s, ctx, nm, `/nvr add type=dahua name=nvr02 host="`+fake.Addr()+`" username=admin password=secret`)
	out, err := s.Exec(cmd(t, `/nvr/download add camera=Brama_wjazdowa storage=archive start="2026-10-24 08:00" end="2026-10-24 10:00" block=1h`))
	if err != nil || !strings.Contains(out, "  2026-10-24T08-00-00_2026-10-24T09-00-00.mp4") {
		t.Fatalf("%v\n%s", err, out)
	}
	j, _ := jobs.Spool{Dir: filepath.Join(ctx.Root, jobs.Dir)}.Get(1)
	if j.Remote != "dahua" || j.Channel != 1 || j.Source != "" || j.Nvr != "nvr02" {
		t.Errorf("%+v", j)
	}
	// the worker's source: the configuration is read through load
	fake.Ranges["1|2026-10-24 08:00:00|2026-10-24 09:00:00"] = []byte("DHAVvideo")
	src := RemoteSource(func() (*config.Store, error) { return s, nil })
	r, input, err := src(context.Background(), j, j.Parts[0])
	if err != nil || strings.Join(input, " ") != "-f dhav" {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "DHAVvideo" {
		t.Errorf("%q", b)
	}
	if _, _, err := src(context.Background(), j, j.Parts[1]); !errors.Is(err, jobs.ErrEmpty) {
		t.Errorf("nothing recorded 09-10: %v", err)
	}
	j.Nvr = "nvr09"
	if _, _, err := src(context.Background(), j, j.Parts[0]); err == nil || !strings.Contains(err.Error(), "not a remote recorder") {
		t.Errorf("%v", err)
	}
}

// The case of an NVR2108 with 8 channels and cameras on 1-5 whose firmware answers the
// camera states only over JSON-RPC: before, every channel counted as online.
func TestNvrDahuaStatesOverRPC(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	fake.Titles = []string{"Site09", "Site10", "Site11", "Site12", "Site13", "Channel6", "Channel7", "Channel8"}
	fake.States = map[int]string{1: "Connected", 2: "Connected", 3: "Connected", 4: "Connected", 5: "Connected"}
	fake.CGIStates, fake.RPCStates = false, true
	s, ctx, nm := dahuaStore(t)
	out := addNvr(t, s, ctx, nm, `/nvr add type=dahua name=nvr01 host="`+fake.Addr()+`" username=admin password=secret`)
	if !strings.Contains(out, "cameras 5/8 online") || !strings.Contains(out, "ch05: added, alias Site13") ||
		!strings.Contains(out, "ch06: skipped, Empty") || len(s.Items("/camera")) != 5 || strings.Contains(out, "warning") {
		t.Errorf("%s", out)
	}
}

func TestNvrDahuaStatesUnknown(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	fake.CGIStates = false // and no RPC methods either
	s, ctx, nm := dahuaStore(t)
	out := addNvr(t, s, ctx, nm, `/nvr add type=dahua name=nvr01 host="`+fake.Addr()+`" username=admin password=secret`)
	for _, want := range []string{"4 channels, camera state unknown", "warning: the recorder did not tell which channels have a camera",
		"Method not found", "/nvr probe nvr01"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if len(s.Items("/camera")) != 4 {
		t.Errorf("without states every channel is taken")
	}
	if out, _ := s.Exec(cmd(t, "/nvr print")); !strings.Contains(out, "camera state unknown") || strings.Contains(out, "online") {
		t.Errorf("print must not claim the cameras are online:\n%s", out)
	}
	fake.RPCCameraAll = true
	out, err := s.Exec(cmd(t, "/nvr probe nvr01"))
	for _, want := range []string{"== CGI magicBox.cgi action getDeviceType\ntype=DHI-NVR5216-16P", "== CGI LogicDeviceManager.cgi action getCameraState uniqueChannels[0] -1\nERROR: ",
		"== RPC2 LogicDeviceManager.getCameraState\nERROR: ", `"Password":*****`, "source: rpc getCameraAll"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("probe misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "camsecret") || strings.Contains(out, "secret\"") {
		t.Errorf("passwords leak:\n%s", out)
	}
}

// api=cgi reads the same recorder over the CGI; the firmware text is the CGI's
func TestNvrDahuaCGI(t *testing.T) {
	fake := dahuatest.New()
	defer fake.Close()
	fake.RPCInfo = false // JSON-RPC would not even be asked
	s, ctx, nm := dahuaStore(t)
	out := addNvr(t, s, ctx, nm, `/nvr add type=dahua name=nvr02 api=cgi host="`+fake.Addr()+`" username=admin password=secret`)
	if !strings.Contains(out, "fw 4.001.0000000.1, Build Date 2023-01-02") || !strings.Contains(out, "cameras 3/4 online") {
		t.Errorf("%s", out)
	}
	for _, q := range fake.Requests {
		if strings.HasPrefix(q, "RPC magicBox") {
			t.Errorf("api=cgi asked JSON-RPC: %s", q)
		}
	}
	expect(t, s, `/nvr add type=uniview name=nvr03 host=h username=a password=b api=rpc`, "api is not used by type=uniview")
	// rpc on firmware without the JSON-RPC methods: the CGI answers
	fake2 := dahuatest.New()
	defer fake2.Close()
	fake2.RPCInfo = false
	out = addNvr(t, s, ctx, nm, `/nvr add type=dahua name=nvr04 host="`+fake2.Addr()+`" username=admin password=secret`)
	if !strings.Contains(out, "fw 4.001.0000000.1, Build Date 2023-01-02") || !strings.Contains(out, "ch01: added") {
		t.Errorf("fallback: %s", out)
	}
}
