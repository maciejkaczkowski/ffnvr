package modules

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"ffnvr/internal/config"
	"ffnvr/internal/script"
)

// Policies of the web panel: what a user may do there (the recorders a user may use
// at all are still chosen by valid-users of /nvr).
//
//	/user/policy
//	add name=viewer allow="live,archive"
//	add name=operator allow="live.*,archive.*,system" deny="archive.download"
//	add name=admin allow="*" deny="system.nvr.reboot"
//	/user
//	set [ find user=jan ] policy=viewer
//
// The rights form a tree (see Permissions). An entry of allow= or deny= is
//
//	live          that right alone
//	live.*        that right and every right below it
//	*             everything
//
// For every right the most specific matching entry decides (live.map.edit before
// live.map.* before live.* before *); of two equally specific entries deny wins; no
// entry: denied. A right is granted only with all its parents: live.map.edit is
// nothing without live.map and live.
//
// A user without policy= has no rights: everything is denied until a policy is given,
// e.g. add name=admin allow="live.*,archive.*,system.*".

// The rights checked by the web panel.
const (
	PermLive        = "live"
	PermLiveViews   = "live.views"
	PermLiveMap     = "live.map"
	PermLiveMapEdit = "live.map.edit"
	PermLiveThumbs  = "live.thumbs"
	PermLivePTZ     = "live.ptz"
	PermLivePTZEdit = "live.ptz.edit"
	PermArchive     = "archive"
	PermArchiveExp  = "archive.export"
	PermArchivePlay = "archive.play"
	PermArchiveDown = "archive.download"
	PermArchiveJobs = "archive.jobs"
	PermSystem      = "system"
	PermSystemLogs  = "system.logs"
	// PermSystemRecord: changing the recording schedules of local recorders
	PermSystemRecord = "system.record"
	PermSystemReboot = "system.nvr.reboot"
	PermSystemBackup = "system.nvr.backup"
	// PermSystemFirmware: uploading firmware to remote recorders (Dahua)
	PermSystemFirmware = "system.nvr.firmware"
	// PermSystemClock: setting the clock and the NTP settings of remote recorders
	PermSystemClock  = "system.nvr.clock"
	PermSystemEvents = "system.events"
	// PermSystemEventsAck: acknowledging events of the event log
	PermSystemEventsAck = "system.events.ack"
	// PermSystemEventsSystem: the system log about no recorder (sign ins, configuration)
	PermSystemEventsSystem = "system.events.system"
)

// Permission is one right of the web panel.
type Permission struct{ Name, Help string }

// Permissions lists the rights, parents before their children.
var Permissions = []Permission{
	{PermLive, "Live module: watching the cameras"},
	{PermLiveViews, "saving and removing own views (tile layouts)"},
	{PermLiveMap, "the map of the cameras"},
	{PermLiveMapEdit, "placing and turning cameras on the map"},
	{PermLiveThumbs, "thumbnails of all cameras (/system/snapshot)"},
	{PermLivePTZ, "PTZ: turning, zooming and focusing cameras, their light, wiper and heater"},
	{PermLivePTZEdit, "saving a camera's PTZ direction swap (ptz-swap-horizontal/vertical of /camera)"},
	{PermArchive, "Archive module: searching recordings, thumbnails, motion"},
	{PermArchiveExp, "copying recordings into archive storages (jobs)"},
	{PermArchivePlay, "playing local recordings in the panel"},
	{PermArchiveDown, "downloading recorded files and finished jobs"},
	{PermArchiveJobs, "cancelling and retrying jobs"},
	{PermSystem, "System module: overview, recorders, cameras, disks, jobs, alerts"},
	{PermSystemLogs, "logs of the camera services"},
	{PermSystemRecord, "recording schedules of local recorders: day templates, week schedules, assigning them to recorders and cameras"},
	{PermSystemReboot, "rebooting remote recorders"},
	{PermSystemBackup, "configuration backups of remote recorders"},
	{PermSystemFirmware, "firmware updates of remote recorders (Dahua: uploading a .bin file, the recorder restarts)"},
	{PermSystemClock, "the clock of remote recorders: setting the time, the NTP server"},
	{PermSystemEvents, "the event log: events received by the alarm server, their pictures"},
	{PermSystemEventsAck, "acknowledging events"},
	{PermSystemEventsSystem, "the system log: sign ins, configuration changes, deletions (entries about a recorder come with system.events)"},
}

func isPermission(name string) bool {
	for _, p := range Permissions {
		if p.Name == name {
			return true
		}
	}
	return false
}

// policyEntries: the values allow= and deny= accept: "*", every right and "x.*" for
// every right or branch with rights below it.
func policyEntries() []string {
	out := []string{"*"}
	seen := map[string]bool{}
	for _, p := range Permissions {
		parts := strings.Split(p.Name, ".")
		for i := 1; i < len(parts); i++ { // branches: system.nvr.* of system.nvr.reboot
			if b := strings.Join(parts[:i], ".") + ".*"; !seen[b] {
				seen[b] = true
				out = append(out, b)
			}
		}
		out = append(out, p.Name)
	}
	return out
}

// Policy is a set of allow and deny entries.
type Policy struct{ Allow, Deny []string }

// specificity of an entry for a right: -1 when it does not match; an exact right
// beats a wildcard of the same depth.
func specificity(entry, perm string) int {
	if entry == "*" {
		return 0
	}
	if base, ok := strings.CutSuffix(entry, ".*"); ok {
		if perm == base || strings.HasPrefix(perm, base+".") {
			return 2 * (strings.Count(base, ".") + 1)
		}
		return -1
	}
	if entry == perm {
		return 2*(strings.Count(perm, ".")+1) + 1
	}
	return -1
}

// decide: the decision of the entries for one right, without its parents.
func (p Policy) decide(perm string) bool {
	best, allowed := -1, false
	for _, e := range p.Allow {
		if s := specificity(e, perm); s > best {
			best, allowed = s, true
		}
	}
	for _, e := range p.Deny {
		if s := specificity(e, perm); s >= best && s >= 0 {
			best, allowed = s, false
		}
	}
	return allowed
}

// Can tells whether the policy grants a right: the right and all its parents that are
// rights themselves.
func (p Policy) Can(perm string) bool {
	if !p.decide(perm) {
		return false
	}
	for i := strings.LastIndexByte(perm, '.'); i > 0; i = strings.LastIndexByte(perm[:i], '.') {
		if parent := perm[:i]; isPermission(parent) && !p.decide(parent) {
			return false
		}
	}
	return true
}

// Rights are the granted rights of a user, by name (templates: index .Can "live.map").
type Rights map[string]bool

// Rights of the policy.
func (p Policy) Rights() Rights {
	r := Rights{}
	for _, x := range Permissions {
		if p.Can(x.Name) {
			r[x.Name] = true
		}
	}
	return r
}

// Any tells whether at least one of the modules (live, archive, system) is granted.
func (r Rights) Any() bool { return r[PermLive] || r[PermArchive] || r[PermSystem] }

func policyOf(it *config.Item) Policy {
	return Policy{Allow: splitList(it.Val("allow")), Deny: splitList(it.Val("deny"))}
}

// UserRights returns the rights of a user of the web panel: none for an unknown or
// disabled user and none without policy=.
func UserRights(s *config.Store, user string) Rights {
	u := find(s, "/user", "user", user)
	if u == nil || u.Val("disabled") == "yes" {
		return Rights{}
	}
	pol := find(s, "/user/policy", "name", u.Val("policy"))
	if pol == nil {
		return Rights{} // no policy= (a strict reference: no other way)
	}
	return policyOf(pol).Rights()
}

func policyMenu() *config.Menu {
	entries := policyEntries()
	return &config.Menu{
		Path:    "/user/policy",
		Kind:    config.Multi,
		NameKey: "name",
		Help:    "what users may do in the web panel; rights: " + strings.Join(entries[1:], ", "),
		Fields: []config.Field{
			{Name: "name", Required: true},
			{Name: "allow", Type: config.TEnumList, Enum: entries, Help: "granted rights: right, branch.* or *"},
			{Name: "deny", Type: config.TEnumList, Enum: entries, Help: "exceptions of allow (the more specific entry wins)"},
			{Name: "comment"},
		},
		Validate: validatePolicy,
		Commands: map[string]config.Command{
			"rights": {Help: "show the rights a policy grants", Run: func(_ *config.Store, _ script.Command, items []*config.Item) (string, error) {
				var b strings.Builder
				for _, it := range items {
					fmt.Fprintf(&b, "policy=%s\n", it.Val("name"))
					b.WriteString(rightsTable(policyOf(it).Rights()))
				}
				return b.String(), nil
			}},
		},
	}
}

func validatePolicy(_ *config.Store, it *config.Item) error {
	if err := checkName("name", it.Val("name")); err != nil {
		return err
	}
	p := policyOf(it)
	for _, a := range p.Allow {
		if slices.Contains(p.Deny, a) {
			return fmt.Errorf("%s is both in allow and in deny", a)
		}
	}
	// an allowed right that its parents make unreachable is a mistake
	for _, a := range p.Allow {
		base := strings.TrimSuffix(a, ".*")
		if a == "*" || !isPermission(base) || p.Can(base) || !p.decide(base) {
			continue
		}
		var need []string
		for i := strings.LastIndexByte(base, '.'); i > 0; i = strings.LastIndexByte(base[:i], '.') {
			if parent := base[:i]; isPermission(parent) && !p.decide(parent) {
				for _, d := range p.Deny {
					if specificity(d, parent) >= 0 {
						return fmt.Errorf("allow: %s has no effect, deny=%s takes %s", a, d, parent)
					}
				}
				need = append([]string{parent}, need...)
			}
		}
		return fmt.Errorf("allow: %s has no effect without %s (allow it too, or use %s.*)", a, strings.Join(need, ", "), need[0])
	}
	return nil
}

// rightsTable: every right, indented by depth, with yes/no.
func rightsTable(r Rights) string {
	var b strings.Builder
	for _, p := range Permissions {
		depth := strings.Count(p.Name, ".")
		if strings.HasPrefix(p.Name, "system.nvr.") {
			depth = 1 // system.nvr is a branch, not a right
		}
		v := "no"
		if r[p.Name] {
			v = "yes"
		}
		name := strings.Repeat("  ", depth+1) + p.Name
		fmt.Fprintf(&b, "%-24s %-3s  %s\n", name, v, p.Help)
	}
	return b.String()
}

func userRightsCommand(s *config.Store, _ script.Command, items []*config.Item) (string, error) {
	var b strings.Builder
	for _, u := range items {
		pol := u.Val("policy")
		switch {
		case u.Val("disabled") == "yes":
			pol += " (disabled: no rights)"
		case pol == "":
			pol = "- (none: no rights)"
		}
		fmt.Fprintf(&b, "user=%s policy=%s\n", u.Val("user"), pol)
		b.WriteString(rightsTable(UserRights(s, u.Val("user"))))
	}
	if b.Len() == 0 {
		return "", errors.New("no such user")
	}
	return b.String(), nil
}
