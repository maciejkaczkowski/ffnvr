package sysd

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestShow(t *testing.T) {
	var gotArgs []string
	s := &Systemd{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte(`Id=ffnvr-nvr01-cam1.service
LoadState=loaded
ActiveState=active
SubState=running
Result=success
NRestarts=3
MemoryCurrent=52428800
ActiveEnterTimestamp=@1761470000
MainPID=1234

Id=ffnvr-nvr01-cam2.service
LoadState=loaded
ActiveState=activating
SubState=auto-restart
Result=exit-code
NRestarts=17
MemoryCurrent=[not set]
ActiveEnterTimestamp=
MainPID=0

Id=ffnvr-nvr01-gone.service
LoadState=not-found
ActiveState=inactive
SubState=dead
MemoryCurrent=18446744073709551615
`), nil
	}}
	units, err := s.Show(context.Background(), "ffnvr-nvr01-cam1.service", "ffnvr-nvr01-cam2.service", "ffnvr-nvr01-gone.service")
	if err != nil || len(units) != 3 {
		t.Fatal(units, err)
	}
	if strings.Join(gotArgs[:5], " ") != "systemctl show --timestamp=unix -p "+props {
		t.Errorf("%v", gotArgs)
	}
	a := units["ffnvr-nvr01-cam1.service"]
	if !a.Running() || a.Restarts != 3 || a.Memory != 50<<20 || a.PID != 1234 || !a.Since.Equal(time.Unix(1761470000, 0)) {
		t.Errorf("%+v", a)
	}
	b := units["ffnvr-nvr01-cam2.service"]
	if b.Running() || b.Restarts != 17 || b.Memory != -1 || !b.Since.IsZero() || b.SubState != "auto-restart" {
		t.Errorf("%+v", b)
	}
	if c := units["ffnvr-nvr01-gone.service"]; c.Loaded || c.Memory != -1 {
		t.Errorf("%+v", c)
	}
	if !parseTimestamp("Mon 2026-10-26 10:00:00 UTC").Equal(time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)) {
		t.Error("classic timestamp")
	}
}

func TestLogs(t *testing.T) {
	s := &Systemd{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte(name + " " + strings.Join(args, " ")), nil
	}}
	out, _ := s.Logs(context.Background(), "ffnvr-nvr01-cam1.service", 200)
	if out != "journalctl -u ffnvr-nvr01-cam1.service -n 200 --no-pager -o short-iso -q" {
		t.Error(out)
	}
}
