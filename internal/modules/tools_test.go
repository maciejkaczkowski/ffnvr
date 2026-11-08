package modules

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ffnvr/internal/dahua/dahuatest"
	"ffnvr/internal/dvrip/dvriptest"
	"ffnvr/internal/tiandy/tiandytest"
	"ffnvr/internal/uniview/univiewtest"
)

func TestTools(t *testing.T) {
	dh, un, ti, dv := dahuatest.New(), univiewtest.New(), tiandytest.New(), dvriptest.New()
	defer dh.Close()
	defer un.Close()
	defer ti.Close()
	defer dv.Close()
	un.OldExport = true // the older BCS: ConfigurationInfo -> URL
	s, ctx, _ := dahuaStore(t)
	disk := t.TempDir()
	for _, l := range []string{
		`/storage add name=archive path=` + disk,
		`/storage add name=rec path=` + t.TempDir(),
		`/nvr add type=local name=nvr00 storage=rec`,
		`/nvr add type=dahua name=nvr01 host="` + dh.Addr() + `" username=admin password=secret`,
		`/nvr add type=uniview name=nvr02 host="` + un.Addr() + `" username=admin password=secret`,
		`/nvr add type=tiandy name=nvr03 host="` + ti.Addr() + `" http-port=` + strconv.Itoa(ti.HTTPPort()) + ` username=Admin password=secret`,
		`/nvr add type=dvrip name=nvr04 host="` + dv.Addr() + `" username=admin password=secret`,
	} {
		expect(t, s, l, "")
	}
	_ = ctx
	// the syntax of the request: /nvr/tools/reboot nvr=
	for _, n := range []string{"nvr01", "nvr02", "nvr03", "nvr04"} {
		out, err := s.Exec(cmd(t, `/nvr/tools/reboot nvr=`+n))
		if err != nil || out != n+": reboot requested\n" {
			t.Errorf("%s: %q %v", n, out, err)
		}
	}
	if !dh.Rebooted || !un.Rebooted || !ti.Rebooted || !dv.Rebooted {
		t.Errorf("rebooted: dahua %v uniview %v tiandy %v dvrip %v", dh.Rebooted, un.Rebooted, ti.Rebooted, dv.Rebooted)
	}
	want := map[string]string{"nvr01": ".backup", "nvr02": ".xml", "nvr03": ".box", "nvr04": ".zip"}
	content := map[string][]byte{"nvr01": dh.Backup, "nvr02": un.Config, "nvr03": ti.Box, "nvr04": dv.Zip}
	for n, ext := range want {
		out, err := s.Exec(cmd(t, `/nvr/tools/backup nvr=`+n+` storage=archive directory=backup/`+n))
		m := regexp.MustCompile(`saved: (\S+) \((\d+) bytes\)`).FindStringSubmatch(out)
		if err != nil || m == nil || !strings.HasPrefix(m[1], filepath.Join(disk, "backup", n, n+"_")) || !strings.HasSuffix(m[1], ext) {
			t.Errorf("%s: %q %v", n, out, err)
			continue
		}
		if b, _ := os.ReadFile(m[1]); string(b) != string(content[n]) {
			t.Errorf("%s: the file differs: %q", n, b)
		}
	}
	expect(t, s, `/nvr/tools/backup nvr=nvr01`, "backup needs storage=")
	expect(t, s, `/nvr/tools/backup nvr=nvr01 storage=archive directory=../x`, "only letters")
	expect(t, s, `/nvr/tools/reboot nvr=nvr00`, "tools work on remote recorders")
	expect(t, s, `/nvr/tools/reboot`, "nvr= is required")
	expect(t, s, `/nvr/tools/reboot nvr=nope`, `"nope" does not exist`)
	if s.Export("/") == "" || strings.Contains(s.Export("/"), "tools") {
		t.Error("tools are not configuration")
	}
	// the newer BCS: System/Configuration at once
	un.OldExport = false
	if out, err := s.Exec(cmd(t, `/nvr/tools/backup nvr=nvr02 storage=archive`)); err != nil || !strings.Contains(out, filepath.Join(disk, "backup", "nvr02_")) {
		t.Errorf("%q %v", out, err)
	}
	// the firmware: Dahua only
	fw := filepath.Join(t.TempDir(), "DH_NVR.bin")
	os.WriteFile(fw, []byte("firmware"), 0o644)
	if out, err := s.Exec(cmd(t, `/nvr/tools/upgrade nvr=nvr01 file=`+fw)); err != nil || out != "nvr01: firmware DH_NVR.bin installed, the recorder restarts\n" {
		t.Errorf("upgrade: %q %v", out, err)
	}
	if string(dh.Upgraded) != "firmware" || dh.UpgradeName != "DH_NVR.bin" {
		t.Errorf("received %q as %q", dh.Upgraded, dh.UpgradeName)
	}
	expect(t, s, `/nvr/tools/upgrade nvr=nvr02 file=`+fw, "Dahua recorders only")
	expect(t, s, `/nvr/tools/upgrade nvr=nvr01`, "upgrade needs file=")
	expect(t, s, `/nvr/tools/upgrade nvr=nvr01 file=/nonexistent.bin`, "no such file")
	// the clock: every make
	ti.Mu.Lock()
	ti.NTPServer, ti.NTPPort, ti.NTPInterval, ti.NTPOn = "192.168.0.254", 123, 60, true
	ti.Mu.Unlock()
	for _, n := range []string{"nvr01", "nvr02", "nvr03", "nvr04"} {
		out, err := s.Exec(cmd(t, `/nvr/tools/datetime nvr=`+n))
		if err != nil || !strings.HasPrefix(out, n+": clock ") || !strings.Contains(out, "ntp on server 192.168.0.254") {
			t.Errorf("clock %s: %q %v", n, out, err)
		}
	}
	dh.Mu.Lock()
	dh.TimeSets = nil
	dh.Mu.Unlock()
	if out, err := s.Exec(cmd(t, `/nvr/tools/datetime nvr=nvr01 time="2026-11-05 10:03:17"`)); err != nil || !strings.Contains(out, "clock 2026-11-05 10:03:17") {
		t.Errorf("set: %q %v", out, err)
	}
	if out, err := s.Exec(cmd(t, `/nvr/tools/datetime nvr=nvr04 time=now ntp=no ntp-server=pool.ntp.org ntp-interval=30`)); err != nil ||
		!regexp.MustCompile(`\(offset [+-][01]s\)`).MatchString(out) || !strings.Contains(out, "ntp off server pool.ntp.org every 30 min") {
		t.Errorf("sync and ntp: %q %v", out, err)
	}
	if len(dv.TimeSets) != 1 || dh.TimeSets[0] != "rpc 2026-11-05 10:03:17 tolerance=5" {
		t.Errorf("%v %v", dv.TimeSets, dh.TimeSets)
	}
	expect(t, s, `/nvr/tools/datetime nvr=nvr01 time=yesterday`, "invalid time")
	expect(t, s, `/nvr/tools/datetime nvr=nvr01 ntp=yes ntp-server=""`, "")
	expect(t, s, `/nvr/tools/datetime nvr=nvr01 ntp-port=x`, "a positive number expected")
	expect(t, s, `/nvr/tools/datetime nvr=nvr00`, "tools work on remote recorders")
	// a wrong password
	expect(t, s, `/nvr set nvr04 password=wrong`, "")
	expect(t, s, `/nvr/tools/reboot nvr=nvr04`, "login refused")
}
