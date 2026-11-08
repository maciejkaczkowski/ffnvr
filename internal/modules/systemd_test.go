package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ffnvr/internal/script"
)

func cameraUnit(t *testing.T, lines ...string) string {
	t.Helper()
	c := withNvr(t)
	expect(t, c.Store, camLine, "")
	for _, l := range lines {
		expect(t, c.Store, l, "")
	}
	us, err := cameraModule{}.Units(c)
	if err != nil || len(us) != 1 {
		t.Fatalf("%v %v", us, err)
	}
	return us[0].Content
}

func has(t *testing.T, unit string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(unit, w) {
			t.Errorf("missing %q:\n%s", w, unit)
		}
	}
}

func hasNot(t *testing.T, unit string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(unit, w) {
			t.Errorf("unexpected %q:\n%s", w, unit)
		}
	}
}

const recDir = "/srv/disk1/nvr01/cam02"

// security=none (the default) leaves the unit exactly as it was: an upgrade restarts
// no recording.
func TestSystemdNoneKeepsTheUnit(t *testing.T) {
	want, _ := os.ReadFile("testdata/ffnvr-nvr01-cam02.service")
	if got := cameraUnit(t); got != string(want) {
		t.Errorf("default:\n%s", got)
	}
	if got := cameraUnit(t, `/system/systemd set security=none`); got != string(want) {
		t.Errorf("none:\n%s", got)
	}
	// the fields of custom change nothing at another level
	if got := cameraUnit(t, `/system/systemd set private-devices=yes user=ffnvr`); got != string(want) {
		t.Errorf("custom fields with security=none:\n%s", got)
	}
}

func TestSystemdBasic(t *testing.T) {
	u := cameraUnit(t, `/system/systemd set security=basic`)
	has(t, u, "User=root\n", "MemoryMax=200M\n",
		"ExecStartPre=+/bin/mkdir -p "+recDir+"\n",
		"Restart=always\nRestartSec=10\nNoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=yes\nPrivateTmp=yes\nReadWritePaths=-"+recDir+"\n\n[Install]")
	hasNot(t, u, "chown", "PrivateDevices", "SystemCallFilter")
}

func TestSystemdHigh(t *testing.T) {
	u := cameraUnit(t, `/system/systemd set security=high`)
	has(t, u, "User=ffnvr\n",
		"ExecStartPre=+/bin/mkdir -p "+recDir+"\nExecStartPre=+/bin/chown ffnvr: "+recDir+"\nExecStart=",
		"NoNewPrivileges=yes\n", "ProtectSystem=strict\n", "ProtectHome=yes\n", "PrivateTmp=yes\n", "PrivateDevices=yes\n",
		"ReadWritePaths=-"+recDir+"\n", "ProtectKernelTunables=yes\n", "RestrictNamespaces=yes\n",
		"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK\n", "SystemCallFilter=@system-service\n",
		"MemoryDenyWriteExecute=yes\n", "CapabilityBoundingSet=\n")
	u = cameraUnit(t, `/system/systemd set security=high user=nvrrec`)
	has(t, u, "User=nvrrec\n", "+/bin/chown nvrrec: ")

	c := withNvr(t)
	expect(t, c.Store, `/system/systemd set security=high user=root`, "user=root is not allowed")
	expect(t, c.Store, `/system/systemd set user=root security=custom`, "")
	expect(t, c.Store, `/system/systemd set security=high`, "user=root is not allowed") // checked on every change
}

func TestSystemdCustom(t *testing.T) {
	// nothing set: basic
	basic := cameraUnit(t, `/system/systemd set security=basic`)
	if u := cameraUnit(t, `/system/systemd set security=custom`); u != basic {
		t.Errorf("custom without fields is basic:\n%s", u)
	}
	u := cameraUnit(t, `/system/systemd set security=custom user=ffnvr protect-home=read-only private-tmp=no `+
		`protect-system=full private-devices=yes system-call-filter=yes`)
	has(t, u, "User=ffnvr\n", "+/bin/chown ffnvr: ", "ProtectSystem=full\n", "ProtectHome=read-only\n",
		"PrivateDevices=yes\n", "SystemCallFilter=@system-service\n")
	hasNot(t, u, "PrivateTmp", "ProtectKernelTunables", "RestrictNamespaces", "CapabilityBoundingSet")

	u = cameraUnit(t, `/system/systemd set security=custom no-new-privileges=no protect-system=no protect-home=no private-tmp=no`)
	hasNot(t, u, "NoNewPrivileges", "ProtectSystem", "ProtectHome", "PrivateTmp")
	has(t, u, "ReadWritePaths=-"+recDir+"\n", "User=root\n")
}

func TestSystemdMemoryAndExtra(t *testing.T) {
	u := cameraUnit(t, `/system/systemd set memory-max=1g`)
	has(t, u, "MemoryAccounting=yes\nMemoryMax=1G\n")
	u = cameraUnit(t, `/system/systemd set memory-max=Unlimited`)
	has(t, u, "MemoryAccounting=yes\n") // still counted: the panel shows it
	hasNot(t, u, "MemoryMax")

	// extra= also at security=none, after everything else
	u = cameraUnit(t, `/system/systemd set extra="CPUQuota=50%,IOWeight=50"`)
	has(t, u, "RestartSec=10\nCPUQuota=50%\nIOWeight=50\n\n[Install]")
	u = cameraUnit(t, `/system/systemd set security=basic extra="RestrictAddressFamilies = AF_INET AF_INET6"`)
	has(t, u, "ReadWritePaths=-"+recDir+"\nRestrictAddressFamilies=AF_INET AF_INET6\n")

	c := withNvr(t)
	s := c.Store
	expect(t, s, `/system/systemd set memory-max=8M`, "at least 16M")
	expect(t, s, `/system/systemd set memory-max=lots`, "memory-max: size")
	expect(t, s, `/system/systemd set extra="ExecStartPre=/bin/sh -c id"`, "ExecStartPre cannot be set here")
	expect(t, s, `/system/systemd set extra="User=root"`, "User cannot be set here")
	expect(t, s, `/system/systemd set extra="AmbientCapabilities=CAP_SYS_ADMIN"`, "cannot be set here")
	expect(t, s, `/system/systemd set extra="ReadWritePaths=/etc"`, "cannot be set here")
	expect(t, s, `/system/systemd set extra=CPUQuota`, "expected Directive=value")
	expect(t, s, `/system/systemd set user=Ffnvr`, "lower case letters")
	expect(t, s, `/system/systemd set user=-x`, "lower case letters")
	expect(t, s, `/system/systemd set security=paranoid`, `is not one of`)
	expect(t, s, `/system/systemd set memory-max=300m security=high`, "")
	if got := s.Export(SystemdMenu); !strings.Contains(got, "memory-max=300M security=high") {
		t.Errorf("export:\n%s", got)
	}
}

// ProtectHome=yes hides /home: recordings there get read-only, with ReadWritePaths= on top.
func TestSystemdRecordingsBelowHome(t *testing.T) {
	c := withNvr(t)
	run(t, c.Store, `/storage add name=h path=/home/nvr/rec`, `/nvr set nvr01 storage=h`, camLine, `/system/systemd set security=high`)
	us, err := cameraModule{}.Units(c)
	if err != nil || len(us) != 1 {
		t.Fatalf("%v %v", us, err)
	}
	has(t, us[0].Content, "ProtectHome=read-only\n", "ReadWritePaths=-/home/nvr/rec/nvr01/cam02\n")
	hasNot(t, us[0].Content, "ProtectHome=yes")
}

// On the real system the account must exist, or every recording fails with 217/USER.
func TestSystemdServiceUserMustExist(t *testing.T) {
	c := withNvr(t)
	c.Live = true
	os.MkdirAll(filepath.Join(c.Root, "etc"), 0o755)
	os.WriteFile(filepath.Join(c.Root, "etc/passwd"), []byte("root:x:0:0::/root:/bin/sh\n"), 0o644)
	run(t, c.Store, camLine, `/system/systemd set security=high`)
	if _, err := (cameraModule{}).Units(c); err == nil || !strings.Contains(err.Error(), "useradd --system") {
		t.Errorf("missing account: %v", err)
	}
	os.WriteFile(filepath.Join(c.Root, "etc/passwd"), []byte("root:x:0:0::/root:/bin/sh\nffnvr:x:998:998::/nonexistent:/usr/sbin/nologin\n"), 0o644)
	if _, err := (cameraModule{}).Units(c); err != nil {
		t.Error(err)
	}
	// root needs no check
	run(t, c.Store, `/system/systemd set security=basic`)
	os.Remove(filepath.Join(c.Root, "etc/passwd"))
	if _, err := (cameraModule{}).Units(c); err != nil {
		t.Error(err)
	}
}

func TestSystemdPrintDetail(t *testing.T) {
	s := newStore(t)
	run(t, s, `/system/systemd set security=high private-tmp=no`)
	exec := func(l string) string {
		c, _, err := script.ParseLine("/", l)
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.Exec(c)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := exec(`/system/systemd print detail`)
	for _, w := range []string{"security: high", "User=ffnvr", "ExecStartPre=+/bin/chown ffnvr: <storage>/<nvr>/<camera>",
		"SystemCallFilter=@system-service", "ignored (only for security=custom): private-tmp"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q:\n%s", w, got)
		}
	}
	if got := exec(`/system/systemd print`); strings.Contains(got, "User=") {
		t.Errorf("print without detail:\n%s", got)
	}
}

// A new camera: its directory does not exist yet. systemd (seen in 257) applies
// ReadWritePaths= also to the "+" ExecStartPre= that creates it, and a missing path fails
// the namespace (226/NAMESPACE) - unless it is "-", skipped when missing.
func TestSystemdNewCameraDirectory(t *testing.T) {
	for _, level := range []string{"basic", "high", "custom"} {
		u := cameraUnit(t, `/system/systemd set security=`+level)
		has(t, u, "ExecStartPre=+/bin/mkdir -p "+recDir+"\n", "ReadWritePaths=-"+recDir+"\n")
		hasNot(t, u, "ReadWritePaths=/")
	}
}
