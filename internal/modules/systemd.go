package modules

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"ffnvr/internal/config"
)

// /system/systemd: how the recording services (ffmpeg of the cameras of local recorders,
// ffnvr-<nvr>-<camera>.service) are confined. ffmpeg parses what comes from the network,
// so it is the part most worth locking in.
//
//	/system/systemd
//	set security=high
//	set security=custom user=ffnvr private-devices=yes protect-kernel=yes
//	set memory-max=300M extra="CPUQuota=50%,IOWeight=50"
//
// security=none (the default) writes the units as before (root, a memory limit only),
// so an upgrade restarts nothing. basic sandboxes ffmpeg as root, high also runs it as an
// unprivileged account with a syscall filter; custom is basic changed by the fields.

// SystemdMenu is the path of the menu.
const SystemdMenu = "/system/systemd"

// SecurityLevels of security=.
var SecurityLevels = []string{"none", "basic", "high", "custom"}

// DefaultServiceUser runs the recordings with security=high.
const DefaultServiceUser = "ffnvr"

// DefaultMemoryMax of a recording service.
const DefaultMemoryMax = "200M"

// minMemoryMax: below it ffmpeg is killed while starting.
const minMemoryMax = 16 << 20

// extraKeys are the directives extra= may add: resources and sandboxing only. Nothing
// that runs a command, sets the account or environment, grants capabilities or opens
// more of the filesystem for writing.
var extraKeys = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`
		CPUQuota CPUWeight CPUAffinity AllowedCPUs Nice
		IOWeight IOSchedulingClass IOSchedulingPriority IOReadBandwidthMax IOWriteBandwidthMax
		MemoryHigh MemoryLow MemorySwapMax TasksMax LimitNOFILE LimitCORE OOMScoreAdjust OOMPolicy UMask
		TimeoutStartSec TimeoutStopSec
		NoNewPrivileges ProtectSystem ProtectHome PrivateTmp PrivateDevices PrivateIPC PrivateUsers PrivateNetwork
		ProtectProc ProcSubset ProtectKernelTunables ProtectKernelModules ProtectKernelLogs
		ProtectControlGroups ProtectClock ProtectHostname
		RestrictNamespaces RestrictRealtime RestrictSUIDSGID LockPersonality MemoryDenyWriteExecute
		RestrictAddressFamilies RestrictFileSystems SystemCallFilter SystemCallArchitectures SystemCallErrorNumber
		CapabilityBoundingSet RemoveIPC KeyringMode DevicePolicy DeviceAllow
		IPAddressAllow IPAddressDeny SocketBindAllow SocketBindDeny
		ReadOnlyPaths InaccessiblePaths`) {
		extraKeys[k] = true
	}
}

// customFields: the fields custom takes (for basic and high they are ignored).
var customFields = []string{"user", "no-new-privileges", "protect-system", "protect-home", "private-tmp",
	"private-devices", "protect-kernel", "restrict", "system-call-filter", "drop-capabilities"}

func systemdMenu() *config.Menu {
	return &config.Menu{
		Path: SystemdMenu,
		Kind: config.Single,
		Help: "confinement of the recording services (ffmpeg of the cameras of local recorders); " +
			"\"print detail\" shows what the units get",
		Fields: []config.Field{
			{Name: "security", Type: config.TEnum, Enum: SecurityLevels, Help: "none (default: as before, root without a sandbox), " +
				"basic (root in a sandbox: the system read-only, only the camera's directory writable), " +
				"high (basic + an own account, no devices, kernel and syscalls locked; needs the account, see user=), " +
				"custom (basic changed by the fields below)"},
			{Name: "memory-max", Help: "memory limit of one recording, e.g. 200M, 1G, or unlimited (every level; default 200M)"},
			{Name: "extra", Type: config.TList, Help: "more directives for [Service], every level, e.g. \"CPUQuota=50%,IOWeight=50\": " +
				"resources and sandboxing only (no Exec*, User, Environment...)"},
			{Name: "user", Help: "high: the account (default ffnvr); custom: default root. Create it with: " +
				"useradd --system --no-create-home --shell /usr/sbin/nologin ffnvr"},
			{Name: "no-new-privileges", Type: config.TBool, Help: "custom: NoNewPrivileges (default yes)"},
			{Name: "protect-system", Type: config.TEnum, Enum: []string{"no", "yes", "full", "strict"}, Help: "custom: ProtectSystem (default strict: everything read-only but the camera's directory)"},
			{Name: "protect-home", Type: config.TEnum, Enum: []string{"no", "yes", "read-only", "tmpfs"}, Help: "custom: ProtectHome (default yes; read-only by itself when the recordings are below /home)"},
			{Name: "private-tmp", Type: config.TBool, Help: "custom: PrivateTmp (default yes)"},
			{Name: "private-devices", Type: config.TBool, Help: "custom: PrivateDevices (default no)"},
			{Name: "protect-kernel", Type: config.TBool, Help: "custom: ProtectKernelTunables/Modules/Logs, ProtectControlGroups, ProtectClock, ProtectHostname (default no)"},
			{Name: "restrict", Type: config.TBool, Help: "custom: RestrictNamespaces, RestrictRealtime, RestrictSUIDSGID, LockPersonality, network families IPv4/IPv6/unix (default no)"},
			{Name: "system-call-filter", Type: config.TBool, Help: "custom: SystemCallFilter=@system-service, MemoryDenyWriteExecute (default no)"},
			{Name: "drop-capabilities", Type: config.TBool, Help: "custom: CapabilityBoundingSet= empty, no root powers even as root (default no)"},
		},
		Validate: validateSystemd,
		Detail:   systemdDetail,
	}
}

func validateSystemd(_ *config.Store, it *config.Item) error {
	if v := it.Val("memory-max"); v != "" {
		if strings.EqualFold(v, "unlimited") {
			it.Set("memory-max", "unlimited")
		} else {
			n, err := ParseSize(v)
			if err != nil {
				return fmt.Errorf("memory-max: %w (or unlimited)", err)
			}
			if n < minMemoryMax {
				return fmt.Errorf("memory-max %s: at least 16M", v)
			}
			it.Set("memory-max", FormatSize(n))
		}
	}
	if u := it.Val("user"); u != "" {
		if err := checkUnixName(u); err != nil {
			return err
		}
		if u == "root" && it.Val("security") == "high" {
			return errors.New("security=high runs the recordings as an unprivileged account: user=root is not allowed (security=custom is)")
		}
	}
	for _, e := range splitList(it.Val("extra")) {
		if err := checkExtra(e); err != nil {
			return err
		}
	}
	return nil
}

// checkUnixName: the names useradd accepts by default.
func checkUnixName(u string) error {
	if len(u) > 32 {
		return fmt.Errorf("user %q: at most 32 characters", u)
	}
	for i, r := range u {
		if !(r >= 'a' && r <= 'z' || r == '_' || i > 0 && (r >= '0' && r <= '9' || r == '-')) {
			return fmt.Errorf("user %q: lower case letters, digits, _ and - (a letter or _ first)", u)
		}
	}
	return nil
}

func checkExtra(e string) error {
	k, _, ok := strings.Cut(e, "=")
	k = strings.TrimSpace(k)
	if !ok || k == "" {
		return fmt.Errorf("extra %q: expected Directive=value, e.g. CPUQuota=50%%", e)
	}
	if strings.IndexFunc(e, unicode.IsControl) >= 0 || strings.Contains(e, `\`) {
		return fmt.Errorf("extra %q: control characters and backslashes are not allowed", e)
	}
	if !extraKeys[k] {
		return fmt.Errorf("extra %q: %s cannot be set here (only resources and sandboxing: CPUQuota, IOWeight, MemoryHigh, ProtectProc, SystemCallFilter...)", e, k)
	}
	return nil
}

// Hardening is the confinement of the recording services (/system/systemd).
type Hardening struct {
	Level     string // none, basic, high, custom
	User      string // the account ("root" or another one)
	MemoryMax string // "" = unlimited

	NoNewPrivileges bool
	ProtectSystem   string // no, yes, full, strict
	ProtectHome     string // no, yes, read-only, tmpfs
	PrivateTmp      bool
	PrivateDevices  bool
	ProtectKernel   bool
	Restrict        bool
	SyscallFilter   bool
	DropCaps        bool
	Extra           []string
}

// presets of the levels (custom starts from basic).
func preset(level string) Hardening {
	switch level {
	case "basic", "custom":
		return Hardening{Level: level, User: "root", NoNewPrivileges: true, ProtectSystem: "strict", ProtectHome: "yes", PrivateTmp: true}
	case "high":
		return Hardening{Level: level, User: DefaultServiceUser, NoNewPrivileges: true, ProtectSystem: "strict", ProtectHome: "yes",
			PrivateTmp: true, PrivateDevices: true, ProtectKernel: true, Restrict: true, SyscallFilter: true, DropCaps: true}
	}
	return Hardening{Level: "none", User: "root", ProtectSystem: "no", ProtectHome: "no"}
}

// SystemdHardening reads /system/systemd.
func SystemdHardening(s *config.Store) Hardening {
	it := single(s, SystemdMenu)
	h := preset(it.Val("security"))
	switch h.Level {
	case "high":
		h.User = or(it.Val("user"), DefaultServiceUser)
	case "custom":
		h.User = or(it.Val("user"), "root")
		flag := func(key string, v *bool) {
			if x := it.Val(key); x != "" {
				*v = x == "yes"
			}
		}
		flag("no-new-privileges", &h.NoNewPrivileges)
		flag("private-tmp", &h.PrivateTmp)
		flag("private-devices", &h.PrivateDevices)
		flag("protect-kernel", &h.ProtectKernel)
		flag("restrict", &h.Restrict)
		flag("system-call-filter", &h.SyscallFilter)
		flag("drop-capabilities", &h.DropCaps)
		h.ProtectSystem = or(it.Val("protect-system"), h.ProtectSystem)
		h.ProtectHome = or(it.Val("protect-home"), h.ProtectHome)
	}
	switch v := it.Val("memory-max"); v {
	case "":
		h.MemoryMax = DefaultMemoryMax
	case "unlimited":
	default:
		h.MemoryMax = v
	}
	for _, e := range splitList(it.Val("extra")) {
		k, v, _ := strings.Cut(e, "=")
		h.Extra = append(h.Extra, strings.TrimSpace(k)+"="+strings.TrimSpace(v))
	}
	return h
}

// Sandboxed: the unit is not the plain one of security=none.
func (h Hardening) Sandboxed() bool { return h.Level != "none" }

// Unprivileged: ffmpeg does not run as root.
func (h Hardening) Unprivileged() bool { return h.User != "root" }

// PreCommands are the ExecStartPre= of a recording into dir. Sandboxed, they run with
// full privileges ("+"): with ProtectSystem= the sandbox cannot create the directory, and
// only root can hand it over to the account. ffmpeg, started after them, finds it and
// gets it writable (ReadWritePaths=, see Directives).
func (h Hardening) PreCommands(dir string) []string {
	if !h.Sandboxed() {
		return []string{"/bin/mkdir -p " + dir}
	}
	out := []string{"+/bin/mkdir -p " + dir}
	if h.Unprivileged() {
		out = append(out, "+/bin/chown "+h.User+": "+dir) // "user:": the account's own group
	}
	return out
}

// homeDirs are hidden by ProtectHome=yes and tmpfs.
var homeDirs = []string{"/home", "/root", "/run/user"}

// Directives of [Service] after Restart= for a recording into dir (none for security=none,
// but extra=).
func (h Hardening) Directives(dir string) []string {
	var out []string
	add := func(l ...string) { out = append(out, l...) }
	if h.Sandboxed() {
		if h.NoNewPrivileges {
			add("NoNewPrivileges=yes")
		}
		if h.ProtectSystem != "no" {
			add("ProtectSystem=" + h.ProtectSystem)
		}
		if ph := h.ProtectHome; ph != "no" {
			if (ph == "yes" || ph == "tmpfs") && below(dir, homeDirs) {
				add("# read-only, not " + ph + ": the recordings are below " + strings.Join(homeDirs, ", "))
				ph = "read-only"
			}
			add("ProtectHome=" + ph)
		}
		if h.PrivateTmp {
			add("PrivateTmp=yes")
		}
		if h.PrivateDevices {
			add("PrivateDevices=yes")
		}
		// "-": a missing directory is skipped. systemd applies ReadWritePaths= also to the
		// "+" commands (only the other sandboxing is off there), so without it the
		// ExecStartPre= that creates the directory of a new camera fails with 226/NAMESPACE
		add("ReadWritePaths=-" + dir)
		if h.ProtectKernel {
			add("ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectKernelLogs=yes",
				"ProtectControlGroups=yes", "ProtectClock=yes", "ProtectHostname=yes", "ProtectProc=invisible")
		}
		if h.Restrict {
			// AF_NETLINK: the resolver of a camera given by its host name may ask for it
			add("RestrictNamespaces=yes", "RestrictRealtime=yes", "RestrictSUIDSGID=yes", "LockPersonality=yes",
				"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK")
		}
		if h.SyscallFilter {
			add("SystemCallArchitectures=native", "SystemCallFilter=@system-service", "SystemCallFilter=~@privileged @resources",
				"SystemCallErrorNumber=EPERM", "MemoryDenyWriteExecute=yes")
		}
		if h.DropCaps {
			add("CapabilityBoundingSet=", "AmbientCapabilities=")
		}
	}
	add(h.Extra...)
	return out
}

// below reports whether dir is one of roots or inside one.
func below(dir string, roots []string) bool {
	dir = filepath.Clean(dir)
	for _, r := range roots {
		if dir == r || strings.HasPrefix(dir, r+"/") {
			return true
		}
	}
	return false
}

// userExists looks the account up in <root>/etc/passwd.
func userExists(root, name string) (bool, error) {
	f, err := os.Open(filepath.Join(root, "etc/passwd"))
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if n, _, _ := strings.Cut(sc.Text(), ":"); n == name {
			return true, nil
		}
	}
	return false, sc.Err()
}

// CheckServiceUser: an unprivileged account must exist before the units name it
// (systemd would fail every recording with status 217/USER).
func (h Hardening) CheckServiceUser(root string) error {
	if !h.Unprivileged() {
		return nil
	}
	ok, err := userExists(root, h.User)
	if err != nil {
		return fmt.Errorf("%s: %w", SystemdMenu, err)
	}
	if !ok {
		return fmt.Errorf("%s security=%s: the account %s does not exist; create it first: useradd --system --no-create-home --shell /usr/sbin/nologin %s",
			SystemdMenu, h.Level, h.User, h.User)
	}
	return nil
}

// systemdDetail: "print detail" shows what a recording unit gets.
func systemdDetail(s *config.Store, it *config.Item) []string {
	h := SystemdHardening(s)
	dir := "<storage>/<nvr>/<camera>"
	out := []string{"recording units (ffnvr-<nvr>-<camera>.service) get:", "  User=" + h.User}
	out = append(out, "  MemoryAccounting=yes")
	if h.MemoryMax != "" {
		out = append(out, "  MemoryMax="+h.MemoryMax)
	}
	for _, p := range h.PreCommands(dir) {
		out = append(out, "  ExecStartPre="+p)
	}
	out = append(out, "  Restart=always")
	for _, d := range h.Directives(dir) {
		out = append(out, "  "+d)
	}
	if h.Level != "custom" {
		var ignored []string
		for _, k := range customFields {
			if _, set := it.Get(k); set && !(k == "user" && h.Level == "high") {
				ignored = append(ignored, k)
			}
		}
		if len(ignored) > 0 {
			out = append(out, "ignored (only for security=custom): "+strings.Join(ignored, ", "))
		}
	}
	return out
}
