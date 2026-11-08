package modules

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
)

// The system log: what ffnvr itself did or saw, kept with the events of the recorders
// (eventdb, source "system") so that one page and one set of filters shows both. Every
// process writes its own entries:
//
//	web     Login, LoginFailed, LoginBlocked, Logout, ConfigChanged, EventsAcked,
//	        FirmwareUpgrade, ClockSet
//	shell   ConfigSaved, EventsAcked
//	worker  NvrOffline, NvrOnline, CameraOffline, CameraOnline, DiskError,
//	        RecordingsDeleted, DiskReserve, RecordSchedule
//	alarm   AlarmServer, EventsConnection, EventsPurged
//
// An entry about a recorder names it (nvr=): it shows on that recorder's page and to
// whoever sees the recorder. The others (sign ins, configuration) need the right
// system.events.system.

// Kinds of the system log.
const (
	SysLogin             = "Login"
	SysLoginFailed       = "LoginFailed"
	SysLoginBlocked      = "LoginBlocked"
	SysLogout            = "Logout"
	SysConfigChanged     = "ConfigChanged"
	SysConfigSaved       = "ConfigSaved"
	SysPasswordChanged   = "PasswordChanged" // a user changed their own password (the panel)
	SysEventsAcked       = "EventsAcked"
	SysEventsPurged      = "EventsPurged"
	SysNvrOffline        = "NvrOffline"
	SysNvrOnline         = "NvrOnline"
	SysCameraOffline     = "CameraOffline"
	SysCameraOnline      = "CameraOnline"
	SysDiskError         = "DiskError"
	SysRecordingsDeleted = "RecordingsDeleted"
	SysDiskReserve       = "DiskReserve"
	SysAlarmServer       = "AlarmServer"
	SysEventsConnection  = "EventsConnection"
	SysFirmwareUpgrade   = "FirmwareUpgrade"
	SysClockSet          = "ClockSet"
	// SysRecordSchedule: what the recording schedule deleted and cut, its alarms not
	// trusted / trusted again (worker)
	SysRecordSchedule = "RecordSchedule"
)

// SystemKinds, for the filters and the translations of the panel.
var SystemKinds = []string{SysLogin, SysLoginFailed, SysLoginBlocked, SysLogout, SysConfigChanged, SysConfigSaved, SysPasswordChanged,
	SysEventsAcked, SysEventsPurged, SysNvrOffline, SysNvrOnline, SysCameraOffline, SysCameraOnline, SysDiskError,
	SysRecordingsDeleted, SysDiskReserve, SysAlarmServer, SysEventsConnection, SysFirmwareUpgrade, SysClockSet, SysRecordSchedule}

// SystemLog writes entries of the system log below root. root "" writes nothing (tests,
// callers without a system). A failure is reported through logf and otherwise ignored:
// the log must never stop what it describes.
func SystemLog(root string, logf func(string, ...any), entries ...eventdb.Entry) {
	if root == "" || len(entries) == 0 {
		return
	}
	db, err := eventdb.Open(eventdb.Path(root))
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = db.Log(ctx, entries...)
		cancel()
		db.Close()
	}
	if err != nil && logf != nil {
		logf("system log: %v (%d entries lost)", err, len(entries))
	}
}

// ShellUser: who works in the shell (the user behind sudo, else the login).
func ShellUser() string {
	for _, k := range []string{"SUDO_USER", "USER", "LOGNAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "root"
}

// ShellAddress: where the shell runs from (the SSH client, else the terminal).
func ShellAddress() string {
	if c := os.Getenv("SSH_CLIENT"); c != "" {
		ip, _, _ := strings.Cut(c, " ")
		return ip
	}
	return "console"
}

// statusChanges: what changed between two states of a recorder that is worth a line
// in the system log (prev nil: the first check - nothing to compare with).
func statusChanges(nvr string, prev, cur *NvrStatus, cams map[int]string) []eventdb.Entry {
	if prev == nil || cur == nil {
		return nil
	}
	var out []eventdb.Entry
	wasOK, isOK := prev.Error == "" && !prev.LastOK.IsZero(), cur.Error == ""
	switch {
	case wasOK && !isOK:
		out = append(out, eventdb.Entry{Nvr: nvr, Kind: SysNvrOffline, Text: cur.Error})
	case !wasOK && isOK && prev.Error != "":
		out = append(out, eventdb.Entry{Nvr: nvr, Kind: SysNvrOnline, Text: "reachable again"})
	}
	if !isOK || !wasOK || cur.StatesError != "" || prev.StatesError != "" {
		return out // the channels are not known on both sides
	}
	before := map[int]Channel{}
	for _, ch := range prev.ChannelList {
		before[ch.Number] = ch
	}
	for _, ch := range cur.ChannelList {
		old, ok := before[ch.Number]
		if !ok || old.Active() == ch.Active() || ch.State == "Empty" {
			continue
		}
		e := eventdb.Entry{Nvr: nvr, Channel: ch.Number, Camera: cams[ch.Number], Kind: SysCameraOffline, Text: strings.TrimSpace(ch.Title + " " + ch.State)}
		if ch.Active() {
			e.Kind = SysCameraOnline
		}
		out = append(out, e)
	}
	bad := func(d Disk) bool { return d.Error || (d.State != "" && d.State != "Success") }
	was := map[string]bool{}
	for _, d := range prev.Disks {
		was[d.Name] = bad(d)
	}
	for _, d := range cur.Disks {
		if bad(d) && !was[d.Name] {
			out = append(out, eventdb.Entry{Nvr: nvr, Kind: SysDiskError, Text: strings.TrimSpace(d.Name + " " + d.State)})
		}
	}
	return out
}

// channelCameras: channel -> camera name of a recorder.
func channelCameras(s *config.Store, nvr string) map[int]string {
	out := map[int]string{}
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvr {
			if i, err := strconv.Atoi(c.Val("channel")); err == nil {
				out[i] = c.Val("name")
			}
		}
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
