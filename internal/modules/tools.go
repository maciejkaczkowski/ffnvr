package modules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/module"
	"ffnvr/internal/script"
)

// toolsModule: /nvr/tools - actions on recorders (an action menu, not configuration):
//
//	/nvr/tools/reboot nvr=nvr02
//	/nvr/tools/backup nvr=nvr02 storage=archive directory=backup/nvr02
//	/nvr/tools/upgrade nvr=nvr02 file=/tmp/DH_NVR2X-4KS3_V4.005.bin
//	/nvr/tools/datetime nvr=nvr02                         (the clock, the zone, NTP)
//	/nvr/tools/datetime nvr=nvr02 time=now                (this server's time)
//	/nvr/tools/datetime nvr=nvr02 time="2026-11-05 10:00:00"
//	/nvr/tools/datetime nvr=nvr02 ntp=yes ntp-server=pool.ntp.org ntp-interval=60
//
// The web panel offers the same (Maintenance; the firmware upgrade on the Firmware
// page and on the recorder's page).
type toolsModule struct{ ctx *module.Context }

// Bind keeps the context (the root of the recorders' status).
func (t *toolsModule) Bind(c *module.Context) { t.ctx = c }

func (toolsModule) Name() string { return "tools" }

func (t *toolsModule) Menus() []*config.Menu {
	return []*config.Menu{{
		Path: "/nvr/tools",
		Kind: config.Multi,
		Help: "actions on remote recorders: reboot, backup, upgrade (firmware, Dahua), datetime (the clock and NTP)",
		Fields: []config.Field{
			{Name: "nvr", Ref: "/nvr:name", Strict: true, Help: "the recorder"},
			{Name: "storage", Ref: "/storage:name", Strict: true, Help: "backup: where the file goes"},
			{Name: "directory", Help: "backup: a subdirectory of the storage, e.g. backup/nvr02 (default: backup)"},
			{Name: "file", Help: "upgrade: the firmware file (.bin) on this server"},
			{Name: "time", Help: "datetime: now (this server's time) or YYYY-MM-DD HH:MM:SS (this server's zone)"},
			{Name: "ntp", Type: config.TEnum, Enum: []string{"yes", "no"}, Help: "datetime: synchronize with an NTP server"},
			{Name: "ntp-server", Help: "datetime: the NTP server's address"},
			{Name: "ntp-port", Help: "datetime: the NTP server's port (default: unchanged)"},
			{Name: "ntp-interval", Help: "datetime: minutes between synchronizations (default: unchanged)"},
		},
		Live: t,
	}}
}

func (*toolsModule) Verbs() []string  { return []string{"reboot", "backup", "upgrade", "datetime"} }
func (*toolsModule) IDs() [][2]string { return nil }

func (t *toolsModule) Run(s *config.Store, c script.Command, p *config.Item) (string, error) {
	if len(c.Args) > 0 {
		return "", fmt.Errorf("/nvr/tools: unexpected %q", c.Args[0])
	}
	name := p.Val("nvr")
	if name == "" {
		return "", errors.New("/nvr/tools: nvr= is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	switch c.Verb {
	case "reboot":
		if err := RebootNvr(ctx, s, name); err != nil {
			return "", fmt.Errorf("/nvr/tools: %s: %w", name, err)
		}
		return fmt.Sprintf("%s: reboot requested\n", name), nil
	case "datetime":
		out, err := t.clock(ctx, s, name, p)
		if err != nil {
			return "", fmt.Errorf("/nvr/tools: %s: %w", name, err)
		}
		return out, nil
	case "upgrade":
		path := p.Val("file")
		if path == "" {
			return "", errors.New("/nvr/tools: upgrade needs file=")
		}
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("/nvr/tools: %w", err)
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return "", fmt.Errorf("/nvr/tools: %w", err)
		}
		uctx, ucancel := context.WithTimeout(context.Background(), UpgradeTimeout)
		defer ucancel()
		if err := UpgradeNvr(uctx, s, name, f, filepath.Base(path), fi.Size(), nil); err != nil {
			return "", fmt.Errorf("/nvr/tools: %s: %w", name, err)
		}
		return fmt.Sprintf("%s: firmware %s installed, the recorder restarts\n", name, filepath.Base(path)), nil
	default: // backup
		st := find(s, "/storage", "name", p.Val("storage"))
		if st == nil {
			return "", errors.New("/nvr/tools: backup needs storage=")
		}
		dir := or(p.Val("directory"), "backup")
		if err := checkRelPath("directory", dir); err != nil {
			return "", fmt.Errorf("/nvr/tools: %w", err)
		}
		body, file, err := BackupNvr(ctx, s, name)
		if err != nil {
			return "", fmt.Errorf("/nvr/tools: %s: %w", name, err)
		}
		defer body.Close()
		target := filepath.Join(st.Val("path"), dir, file)
		n, err := saveFile(target, body)
		if err != nil {
			return "", fmt.Errorf("/nvr/tools: %w", err)
		}
		return fmt.Sprintf("%s: configuration saved: %s (%d bytes)\n", name, target, n), nil
	}
}

func saveFile(target string, r io.Reader) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	tmp := target + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = errors.New("the recorder sent an empty file")
	}
	if err != nil {
		os.Remove(tmp)
		return n, err
	}
	return n, os.Rename(tmp, target)
}

func toolRecorder(s *config.Store, name string) (recorder, error) {
	nvr := find(s, "/nvr", "name", name)
	if nvr == nil {
		return nil, fmt.Errorf("no such nvr: %s", name)
	}
	if !remote(nvr) {
		return nil, fmt.Errorf("%s is type=local: tools work on remote recorders", name)
	}
	return newRecorder(nvr)
}

// RebootNvr restarts a remote recorder.
func RebootNvr(ctx context.Context, s *config.Store, name string) error {
	r, err := toolRecorder(s, name)
	if err != nil {
		return err
	}
	if cl, ok := r.(io.Closer); ok {
		defer cl.Close()
	}
	return r.Reboot(ctx)
}

// UpgradeTimeout: the most a firmware upgrade may take (upload and installation).
var UpgradeTimeout = 30 * time.Minute

// UpgradeProgress is the state of a firmware upgrade: Phase is what the recorder told
// last ("" before it tells anything; Preparing, Downloading, Upgrading, Succeeded or a
// failure), Percent the whole upgrade (sending the file is the first half), Sent and
// Total the bytes of the file.
type UpgradeProgress struct {
	Phase       string
	Percent     int
	Sent, Total int64
}

// upgrader: a recorder whose firmware can be updated from ffnvr.
type upgrader interface {
	Upgrade(ctx context.Context, file io.Reader, name string, size int64, report func(UpgradeProgress)) error
}

// CanUpgrade tells whether ffnvr can update the firmware of a recorder (Dahua for now).
func CanUpgrade(nvr *config.Item) bool {
	return nvr != nil && remote(nvr) && nvr.Val("type") == "dahua"
}

// UpgradeNvr installs a firmware file on a remote recorder; report (may be nil) gets
// the progress. After a success the recorder restarts by itself.
func UpgradeNvr(ctx context.Context, s *config.Store, name string, file io.Reader, fileName string, size int64, report func(UpgradeProgress)) error {
	r, err := toolRecorder(s, name)
	if err != nil {
		return err
	}
	if cl, ok := r.(io.Closer); ok {
		defer cl.Close()
	}
	u, ok := r.(upgrader)
	if !ok {
		return fmt.Errorf("%s: firmware updates are supported for Dahua recorders only", name)
	}
	return u.Upgrade(ctx, file, fileName, size, report)
}

// BackupNvr exports the configuration of a remote recorder: the file and the name to
// give it (<nvr>_<time><ext>).
func BackupNvr(ctx context.Context, s *config.Store, name string) (io.ReadCloser, string, error) {
	r, err := toolRecorder(s, name)
	if err != nil {
		return nil, "", err
	}
	body, ext, err := r.Backup(ctx)
	if cl, ok := r.(io.Closer); ok && err != nil {
		cl.Close()
	}
	if err != nil {
		return nil, "", err
	}
	file := name + "_" + time.Now().Format("2006-01-02T15-04-05") + strings.ToLower(ext)
	return body, file, nil
}

// BackupTo saves the configuration of a remote recorder into a storage directory: the
// file's path and size.
func BackupTo(ctx context.Context, s *config.Store, nvr, storage, dir string) (string, int64, error) {
	st := find(s, "/storage", "name", storage)
	if st == nil {
		return "", 0, fmt.Errorf("no such storage: %s", storage)
	}
	dir = or(dir, "backup")
	if err := checkRelPath("directory", dir); err != nil {
		return "", 0, err
	}
	body, file, err := BackupNvr(ctx, s, nvr)
	if err != nil {
		return "", 0, err
	}
	defer body.Close()
	target := filepath.Join(st.Val("path"), dir, file)
	n, err := saveFile(target, body)
	return target, n, err
}

// clock: /nvr/tools/datetime - shows the clock, sets it (time=) and NTP (ntp=, ntp-*).
func (t *toolsModule) clock(ctx context.Context, s *config.Store, name string, p *config.Item) (string, error) {
	var c ClockInfo
	var err error
	setNTP := p.Val("ntp") != "" || p.Val("ntp-server") != "" || p.Val("ntp-port") != "" || p.Val("ntp-interval") != ""
	if v := p.Val("time"); v != "" {
		var at time.Time
		if at, err = ParseClock(v); err != nil {
			return "", err
		}
		if v == "now" {
			c, err = SyncNvrTime(ctx, s, name)
		} else {
			c, err = SetNvrTime(ctx, s, name, at)
		}
		if err != nil {
			return "", err
		}
	}
	if setNTP {
		cur, err := NvrClock(ctx, s, name)
		if err != nil {
			return "", err
		}
		n := NTPSettings{Enabled: cur.NTP.Enabled, Server: cur.NTP.Server}
		if v := p.Val("ntp"); v != "" {
			n.Enabled = v == "yes"
		}
		if v := p.Val("ntp-server"); v != "" {
			n.Server = v
		}
		for _, f := range []struct {
			key string
			to  *int
		}{{"ntp-port", &n.Port}, {"ntp-interval", &n.Interval}} {
			if v := p.Val(f.key); v != "" {
				if *f.to, err = strconv.Atoi(v); err != nil || *f.to <= 0 {
					return "", fmt.Errorf("%s=%s: a positive number expected", f.key, v)
				}
			}
		}
		if c, err = SetNvrNTP(ctx, s, name, n); err != nil {
			return "", err
		}
	}
	if c.Time.IsZero() {
		if c, err = NvrClock(ctx, s, name); err != nil {
			return "", err
		}
	}
	if t.ctx != nil && (p.Val("time") != "" || setNTP) {
		NoteClock(t.ctx.Root, name, c)
	}
	return FormatClock(name, c), nil
}
