// Package sysd reads the state of systemd services (systemctl show) and their logs
// (journalctl): the camera services of local recorders run there.
package sysd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Unit is the state of one service.
type Unit struct {
	Name        string
	Loaded      bool   // a unit file exists
	ActiveState string // active, inactive, failed, activating (e.g. waiting to restart) ...
	SubState    string // running, dead, auto-restart ...
	Result      string // success, exit-code ...
	Restarts    int    // NRestarts: restarts since the last start by hand
	Memory      int64  // MemoryCurrent in bytes, -1: unknown
	Since       time.Time
	PID         int
}

// Running reports whether the service's process runs.
func (u Unit) Running() bool { return u.ActiveState == "active" && u.SubState == "running" }

// Runner runs a command and returns its output (replaced in tests).
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Systemd talks to systemd through its command line tools.
type Systemd struct {
	Run Runner // nil: the real commands
}

func (s *Systemd) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, name, args...)
	}
	return execRunner(ctx, name, args...)
}

var props = "Id,LoadState,ActiveState,SubState,Result,NRestarts,MemoryCurrent,ActiveEnterTimestamp,MainPID"

// Show reads the state of services, all with one call.
func (s *Systemd) Show(ctx context.Context, names ...string) (map[string]Unit, error) {
	out := map[string]Unit{}
	if len(names) == 0 {
		return out, nil
	}
	args := append([]string{"show", "--timestamp=unix", "-p", props}, names...)
	b, err := s.run(ctx, "systemctl", args...)
	if err != nil && len(b) == 0 {
		return nil, err
	}
	for _, block := range strings.Split(strings.TrimSpace(string(b)), "\n\n") {
		u := Unit{Memory: -1}
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "Id":
				u.Name = v
			case "LoadState":
				u.Loaded = v == "loaded"
			case "ActiveState":
				u.ActiveState = v
			case "SubState":
				u.SubState = v
			case "Result":
				u.Result = v
			case "NRestarts":
				u.Restarts, _ = strconv.Atoi(v)
			case "MemoryCurrent":
				if n, err := strconv.ParseUint(v, 10, 64); err == nil && n < 1<<62 {
					u.Memory = int64(n)
				}
			case "ActiveEnterTimestamp":
				u.Since = parseTimestamp(v)
			case "MainPID":
				u.PID, _ = strconv.Atoi(v)
			}
		}
		if u.Name != "" {
			out[u.Name] = u
		}
	}
	return out, nil
}

// parseTimestamp reads "@1700000000" (--timestamp=unix) or the classic
// "Mon 2026-10-26 10:00:00 CET" of older systemd.
func parseTimestamp(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" || v == "n/a" {
		return time.Time{}
	}
	if sec, ok := strings.CutPrefix(v, "@"); ok {
		if n, err := strconv.ParseInt(sec, 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0)
		}
		return time.Time{}
	}
	if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", v); err == nil {
		return t
	}
	return time.Time{}
}

// Logs returns the last lines of a service's log.
func (s *Systemd) Logs(ctx context.Context, name string, lines int) (string, error) {
	b, err := s.run(ctx, "journalctl", "-u", name, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso", "-q")
	return string(b), err
}
