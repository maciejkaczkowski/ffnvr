package web

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/fsusage"
	"ffnvr/internal/jobs"
	"ffnvr/internal/modules"
	"ffnvr/internal/sysd"
)

// kinds of badges and dots
const (
	kOK   = "ok"
	kWarn = "warn"
	kErr  = "err"
	kMute = "mute"
)

type kv struct{ K, V string }

type diskView struct {
	Nvr, Name, Cap, Used, State, Kind string
	Model, Serial                     string
	Pct                               float64
	Known                             bool
}

type camView struct {
	Ch, Name, Alias, Address, Detail, Recording string
	Status, Kind                                string
	Uptime, Memory, Restarts                    string
	Details                                     string // link to the camera's page (local cameras): state, recordings, events, log
	Nvr                                         string
}

type nvrView struct {
	Name, Type, Address   string
	Local                 bool
	Kind, Status          string
	CamsOnline, CamsTotal int
	CamsKnown             bool
	DiskPct               float64
	DiskTxt, DiskNote     string
	Retention, Uptime     string
	Info                  []kv
	Disks                 []diskView
	Cameras               []camView
	Offline               string // banner of a remote recorder that does not answer
	OldestAge             time.Duration
	RetentionTarget       string
	Model, Firmware       string
	Build, Serial         string
	Checked               string
	// the clock as last read: the offset against this server ("+95 s"), its kind
	ClockKnown       bool
	Clock, ClockKind string
}

type alertView struct{ Kind, Title, Desc, Time string }

type kpi struct{ Label, Value, Of, Note, Kind string }

// system is everything the System screen shows to one user.
type system struct {
	Nvrs   []nvrView
	Disks  []diskView
	Alerts []alertView
	KPIs   []kpi
}

// HasNvrs: whether there is a local (true) or an external (false) recorder - the side
// menus show the heading of a kind only when there is one of it.
func (s *system) HasNvrs(local bool) bool {
	for _, n := range s.Nvrs {
		if n.Local == local {
			return true
		}
	}
	return false
}

func human(n uint64) string {
	switch {
	case n >= 1e12:
		return strconv.FormatFloat(float64(n)/1e12, 'f', 1, 64) + " TB"
	case n >= 1e9:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + " GB"
	case n >= 1e6:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 0, 64) + " MB"
	}
	return strconv.FormatUint(n/1000, 10) + " kB"
}

// since writes a duration as the panel does: 41 d 6 h, 3 h 12 min, 5 min.
func since(d time.Duration, t map[string]string) string {
	if d < 0 {
		d = 0
	}
	days, hours, mins := int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60
	switch {
	case days > 0:
		return fmt.Sprintf("%d %s %d h", days, t["d"], hours)
	case hours > 0:
		return fmt.Sprintf("%d h %d min", hours, mins)
	}
	return fmt.Sprintf("%d min", mins)
}

func hostUptime(root string) time.Duration {
	b, err := os.ReadFile(filepath.Join(root, "proc/uptime"))
	if err != nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.Fields(string(b) + " 0")[0], 64)
	return time.Duration(f) * time.Second
}

func streamHost(stream string) string {
	if u, err := url.Parse(stream); err == nil {
		return u.Hostname()
	}
	return ""
}

// gather builds the screen of a user: the recorders of valid-users and their storage,
// cameras and alerts.
func (s *Server) gather(ctx context.Context, store *config.Store, user string, t map[string]string) *system {
	sys := &system{}
	now := time.Now()
	names := modules.AccessibleNvrs(store, user)

	// the camera services of local recorders, all with one systemctl call
	var unitNames []string
	for _, n := range names {
		nvr := findItem(store, "/nvr", "name", n)
		if nvr.Val("type") != "local" {
			continue
		}
		for _, c := range store.Items("/camera") {
			if c.Val("nvr") == n {
				unitNames = append(unitNames, modules.UnitName(n, c.Val("name")))
			}
		}
	}
	units := map[string]sysd.Unit{}
	if s.opt.Systemd != nil && len(unitNames) > 0 {
		if u, err := s.opt.Systemd.Show(ctx, unitNames...); err == nil {
			units = u
		} else {
			s.opt.Log.Printf("web: systemd: %v", err)
		}
	}
	for _, n := range names {
		nvr := findItem(store, "/nvr", "name", n)
		var v nvrView
		if nvr.Val("type") == "local" {
			v = s.localNvr(store, nvr, units, now, t, sys)
		} else {
			v = s.remoteNvr(store, nvr, now, t, sys)
		}
		if nvr.Val("disabled") == "yes" {
			v.Kind, v.Status = kMute, t["disabled"]
		}
		sys.Nvrs = append(sys.Nvrs, v)
		sys.Disks = append(sys.Disks, v.Disks...)
	}
	sort.SliceStable(sys.Nvrs, func(i, j int) bool { // local ones first, then by name
		if sys.Nvrs[i].Local != sys.Nvrs[j].Local {
			return sys.Nvrs[i].Local
		}
		return sys.Nvrs[i].Name < sys.Nvrs[j].Name
	})
	sys.KPIs = kpis(sys, t)
	sort.SliceStable(sys.Alerts, func(i, j int) bool { return sys.Alerts[i].Kind == kErr && sys.Alerts[j].Kind != kErr })
	return sys
}

func worst(a, b string) string {
	rank := map[string]int{kOK: 0, kMute: 1, kWarn: 2, kErr: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func (s *Server) localNvr(store *config.Store, nvr *config.Item, units map[string]sysd.Unit, now time.Time, t map[string]string, sys *system) nvrView {
	name := nvr.Val("name")
	v := nvrView{Name: name, Type: t["local"], Local: true, Kind: kOK, Status: t["online"]}
	st := findItem(store, "/storage", "name", nvr.Val("storage"))
	path := ""
	if st != nil {
		path = st.Val("path")
		v.Address = path
	}
	// storage: the filesystem of the storage's path
	disk := diskView{Nvr: name, Name: nvr.Val("storage") + " · " + path}
	if u, err := fsusage.Of(path); err == nil && path != "" {
		disk.Known, disk.Pct = true, u.Percent()
		disk.Cap = human(u.Total)
		disk.Used = fmt.Sprintf(t["used_of"], human(u.Used()), human(u.Total), human(u.Avail))
		disk.Kind, disk.State = kOK, "OK"
		if disk.Pct >= 90 {
			disk.Kind, disk.State = kWarn, t["almost_full"]
			sys.Alerts = append(sys.Alerts, alertView{kWarn, name + ": " + t["storage_full"], fmt.Sprintf("%s · %.0f%%", path, disk.Pct), ""})
		}
		v.DiskPct, v.DiskTxt, v.DiskNote = disk.Pct, fmt.Sprintf("%.0f%%", disk.Pct), human(u.Total)
	} else {
		disk.Kind, disk.State, disk.Used, disk.Cap = kErr, t["unavailable"], t["usage_unknown"], "—"
		v.DiskTxt, v.DiskNote = "—", t["no_data"]
		v.Kind, v.Status = kErr, t["unavailable"]
		sys.Alerts = append(sys.Alerts, alertView{kErr, name + ": " + t["storage_missing"], path, ""})
	}
	v.Disks = []diskView{disk}

	rec := modules.Recordings(store, name)
	v.RetentionTarget = nvr.Val("retention")
	if !rec.Oldest.IsZero() {
		v.OldestAge = now.Sub(rec.Oldest)
	}
	switch {
	case v.RetentionTarget != "" && !rec.Oldest.IsZero():
		v.Retention = fmt.Sprintf("%s (%s %s)", v.RetentionTarget, t["oldest"], since(v.OldestAge, t))
	case v.RetentionTarget != "":
		v.Retention = v.RetentionTarget
	case !rec.Oldest.IsZero():
		v.Retention = fmt.Sprintf("%s (%s)", since(v.OldestAge, t), t["no_limit"])
	default:
		v.Retention = "—"
	}
	up := hostUptime(s.opt.Root)
	if up > 0 {
		v.Uptime = since(up, t)
	} else {
		v.Uptime = "—"
	}

	for _, c := range store.Items("/camera") {
		if c.Val("nvr") != name {
			continue
		}
		addr := c.Val("stream")
		if hq, _, err := modules.ResolveCamera(store, c).Addresses(); err == nil && hq != "" {
			addr = hq // its profile's scheme filled
		}
		cam := camView{Nvr: name, Ch: "—", Name: c.Val("name"), Alias: c.Val("alias"), Address: streamHost(addr),
			Detail: c.Val("group"), Recording: t["continuous"], Uptime: "—", Memory: "—", Restarts: "—"}
		unit := modules.UnitName(name, c.Val("name"))
		cam.Details = "/?p=cam&n=" + url.QueryEscape(name) + "&c=" + url.QueryEscape(c.Val("name"))
		v.CamsTotal++
		if sched, _ := modules.CameraSchedule(store, c); sched != "" && modules.ReadSchedule(store, sched) != nil {
			cam.Recording = fmt.Sprintf(t["rs_recording"], sched)
			// the alarms of the schedule do not arrive: the alarm time is recorded whole
			if st := modules.ReadScheduleState(store, name, c.Val("name")); st != nil && st.Fallback && !st.Waiting && c.Val("enabled") != "no" && nvr.Val("disabled") != "yes" {
				sys.Alerts = append(sys.Alerts, alertView{kWarn, label(c) + ": " + t["rs_alert"], name + " · " + st.Reason, sinceText(st.FallbackSince, now, t)})
			}
		}
		u, known := units[unit]
		switch {
		case c.Val("enabled") == "no" || nvr.Val("disabled") == "yes":
			cam.Kind, cam.Status, cam.Recording = kMute, t["disabled"], "—"
			v.CamsTotal--
		case s.opt.Systemd == nil || !known:
			cam.Kind, cam.Status = kMute, t["unknown"]
		case !u.Loaded:
			cam.Kind, cam.Status = kWarn, t["not_committed"]
			sys.Alerts = append(sys.Alerts, alertView{kWarn, label(c) + ": " + t["not_committed"], name + " · " + t["run_commit"], ""})
		case u.Running():
			cam.Kind, cam.Status = kOK, t["recording"]
			v.CamsOnline++
		default:
			cam.Kind, cam.Status = kErr, t["stopped"]
			switch {
			case u.SubState == "auto-restart" || u.ActiveState == "activating":
				cam.Status = t["restarting"]
			case u.ActiveState == "failed":
				cam.Status = t["failed"]
			}
			sys.Alerts = append(sys.Alerts, alertView{kErr, label(c) + ": " + t["rec_stopped"],
				fmt.Sprintf("%s · %s %d", name, t["restarts"], u.Restarts), sinceText(u.Since, now, t)})
		}
		if known && u.Loaded {
			cam.Restarts = strconv.Itoa(u.Restarts)
			if u.Memory >= 0 {
				cam.Memory = human(uint64(u.Memory))
			}
			if u.Running() && !u.Since.IsZero() {
				cam.Uptime = since(now.Sub(u.Since), t)
			}
		}
		v.Cameras = append(v.Cameras, cam)
	}
	v.CamsKnown = s.opt.Systemd != nil
	for _, c := range v.Cameras {
		v.Kind = worst(v.Kind, map[string]string{kErr: kWarn, kWarn: kWarn}[c.Kind])
	}
	if v.Kind == kWarn && v.Status == t["online"] {
		v.Status = t["warning"]
	}
	v.Info = []kv{
		{t["name"], name}, {t["type"], t["local"]}, {t["storage"], nvr.Val("storage") + " (" + path + ")"},
		{t["retention_set"], or(v.RetentionTarget, t["no_limit"])},
	}
	if rec.Updated.IsZero() {
		v.Info = append(v.Info, kv{t["recordings"], t["not_indexed"]})
	} else {
		v.Info = append(v.Info, kv{t["recordings"], fmt.Sprintf("%d %s, %s", rec.Files, t["files"], human(uint64(rec.Bytes)))},
			kv{t["indexed"], rec.Updated.Format("2006-01-02 15:04:05")})
	}
	if !rec.Oldest.IsZero() {
		v.Info = append(v.Info, kv{t["oldest_rec"], rec.Oldest.Format("2006-01-02 15:04")}, kv{t["newest_rec"], rec.Newest.Format("2006-01-02 15:04")})
	}
	v.Info = append(v.Info, kv{t["host_uptime"], v.Uptime}, kv{t["cams_online"], camsText(v, t)})
	if c := nvr.Val("comment"); c != "" {
		v.Info = append(v.Info, kv{t["comment"], c})
	}
	return v
}

func label(c *config.Item) string {
	if a := c.Val("alias"); a != "" {
		return a
	}
	return c.Val("name")
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func sinceText(at, now time.Time, t map[string]string) string {
	if at.IsZero() {
		return ""
	}
	if now.Sub(at) < 24*time.Hour {
		return t["since"] + " " + at.Format("15:04")
	}
	return t["since"] + " " + at.Format("2006-01-02 15:04")
}

func camsText(v nvrView, t map[string]string) string {
	if !v.CamsKnown {
		return "— / " + strconv.Itoa(v.CamsTotal)
	}
	return fmt.Sprintf("%d / %d", v.CamsOnline, v.CamsTotal)
}

var remoteStates = map[string]string{"Connected": kOK, "Configured": kOK, "Empty": kMute, "": kMute}

func (s *Server) remoteNvr(store *config.Store, nvr *config.Item, now time.Time, t map[string]string, sys *system) nvrView {
	name := nvr.Val("name")
	st := modules.ReadStatus(s.opt.Root, name)
	v := nvrView{Name: name, Type: nvr.Val("type"), Address: nvr.Val("host"), Kind: kOK, Status: t["online"],
		Retention: t["on_recorder"], Uptime: "—", DiskTxt: "—", DiskNote: t["no_data"]}
	v.Info = []kv{{t["name"], name}, {t["type"], nvr.Val("type")}, {t["address"], nvr.Val("host")}}
	cams := map[int]*config.Item{}
	for _, c := range store.Items("/camera") {
		if c.Val("nvr") == name {
			n, _ := strconv.Atoi(c.Val("channel"))
			cams[n] = c
		}
	}
	if st == nil {
		v.Kind, v.Status = kMute, t["not_checked"]
		v.Info = append(v.Info, kv{t["state"], t["not_checked_long"]})
		for n, c := range cams {
			v.Cameras = append(v.Cameras, camView{Nvr: name, Ch: "CH" + strconv.Itoa(n), Name: c.Val("name"), Alias: c.Val("alias"),
				Kind: kMute, Status: t["unknown"], Recording: t["on_recorder"], Uptime: "—", Memory: "—", Restarts: "—"})
		}
		sortCams(v.Cameras)
		v.CamsTotal = len(cams)
		return v
	}
	d := st.Device
	v.Model, v.Firmware, v.Build, v.Serial = d.Type, d.Firmware, d.Build, d.Serial
	v.Checked = st.Checked.Format("2006-01-02 15:04")
	v.Info = append(v.Info, kv{t["model"], or(d.Type, "—")}, kv{t["firmware"], or(d.Firmware, "—")})
	if d.Build != "" {
		v.Info = append(v.Info, kv{t["build"], d.Build})
	}
	v.Info = append(v.Info, kv{t["serial"], or(d.Serial, "—")})
	if d.Name != "" {
		v.Info = append(v.Info, kv{t["device_name"], d.Name})
	}
	if d.Uptime > 0 {
		v.Uptime = since(d.Uptime, t)
		v.Info = append(v.Info, kv{t["uptime"], v.Uptime})
	}
	if st.Error != "" {
		v.Kind, v.Status = kErr, t["offline"]
		last := t["never"]
		if !st.LastOK.IsZero() {
			last = st.LastOK.Format("2006-01-02 15:04")
		}
		v.Offline = fmt.Sprintf(t["offline_banner"], last, st.Error)
		if v.Uptime != "—" {
			v.Uptime = t["offline"]
		}
		sys.Alerts = append(sys.Alerts, alertView{kErr, name + ": " + t["not_connected"], nvr.Val("host") + " · " + st.Error, sinceText(st.LastOK, now, t)})
	}
	// channels: every one the recorder reported, with the camera of the configuration
	for _, ch := range st.ChannelList {
		detail := ch.Detail
		if ch.Serial != "" {
			detail = strings.TrimPrefix(detail+" · "+t["sn"]+" "+ch.Serial, " · ")
		}
		cv := camView{Nvr: name, Ch: "CH" + strconv.Itoa(ch.Number), Name: ch.Title, Address: ch.Address, Detail: detail,
			Recording: t["on_recorder"], Uptime: "—", Memory: "—", Restarts: "—", Status: or(ch.State, t["unknown"])}
		configured := cams[ch.Number] != nil
		if c := cams[ch.Number]; c != nil {
			cv.Name, cv.Alias = c.Val("name"), c.Val("alias")
			if ch.Title != "" && ch.Title != c.Val("alias") {
				cv.Detail = strings.TrimPrefix(cv.Detail+" · "+ch.Title, " · ")
			}
			delete(cams, ch.Number)
		}
		cv.Kind = remoteStates[ch.State]
		if cv.Kind == "" {
			cv.Kind = kErr
		}
		switch {
		case ch.State == "Empty":
			cv.Status, cv.Recording = t["no_camera"], "—"
		case ch.State == "Connected":
			cv.Status = t["online"]
		}
		if ch.State != "Empty" {
			v.CamsTotal++
			if ch.Active() && ch.State != "" {
				v.CamsOnline++
			}
		}
		if cv.Kind == kErr && st.Error == "" && configured {
			sys.Alerts = append(sys.Alerts, alertView{kWarn, or(cv.Alias, cv.Name) + ": " + strings.ToLower(cv.Status), name + " · " + cv.Ch, ""})
		}
		if cv.Kind == kErr {
			cv.Kind = kWarn
			if st.Error == "" {
				v.Kind = worst(v.Kind, kWarn)
			}
		}
		v.Cameras = append(v.Cameras, cv)
	}
	for n, c := range cams { // configured, but not reported by the recorder
		v.Cameras = append(v.Cameras, camView{Nvr: name, Ch: "CH" + strconv.Itoa(n), Name: c.Val("name"), Alias: c.Val("alias"),
			Kind: kMute, Status: t["not_reported"], Recording: t["on_recorder"], Uptime: "—", Memory: "—", Restarts: "—"})
	}
	sortCams(v.Cameras)
	v.CamsKnown = st.StatesError == "" && len(st.ChannelList) > 0 && st.Error == ""
	if st.Error != "" { // what is shown is the last known state
		for i := range v.Cameras {
			v.Cameras[i].Kind = kMute
		}
	}
	if st.StatesError != "" {
		v.Info = append(v.Info, kv{t["cam_states"], t["unknown"] + " (" + st.StatesError + ")"})
	}
	v.Info = append(v.Info, kv{t["channels"], strconv.Itoa(st.Channels)}, kv{t["cams_online"], camsText(v, t)})

	var total, used int64
	bad := 0
	for i, dk := range st.Disks {
		dv := diskView{Nvr: name, Name: or(dk.Name, fmt.Sprintf("%s %d", t["disk"], i+1)), Known: dk.TotalBytes > 0, Kind: kOK, State: "OK",
			Model: dk.Model, Serial: dk.Serial}
		if dk.TotalBytes > 0 {
			dv.Pct = 100 * float64(dk.UsedBytes) / float64(dk.TotalBytes)
			dv.Cap = human(uint64(dk.TotalBytes))
			dv.Used = fmt.Sprintf(t["used_pct"], dv.Pct)
		} else {
			dv.Cap, dv.Used = "—", t["usage_unknown"]
		}
		if dk.Error || (dk.State != "" && dk.State != "Success") {
			dv.Kind, dv.State = kErr, or(dk.State, t["error"])
			bad++
			sys.Alerts = append(sys.Alerts, alertView{kErr, name + ": " + fmt.Sprintf(t["disk_failure"], dv.Name), dv.State, ""})
		}
		if st.Error != "" {
			dv.Kind, dv.State = kMute, t["last_known"]
		}
		total += dk.TotalBytes
		used += dk.UsedBytes
		v.Disks = append(v.Disks, dv)
	}
	if total > 0 && st.Error == "" {
		v.DiskPct = 100 * float64(used) / float64(total)
		v.DiskTxt = fmt.Sprintf("%.0f%%", v.DiskPct)
		v.DiskNote = fmt.Sprintf("%d × %s", len(st.Disks), human(uint64(total/int64(len(st.Disks)))))
	}
	if bad > 0 && st.Error == "" {
		v.Kind, v.Status = worst(v.Kind, kWarn), t["warning"]
	}
	if st.Error == "" {
		v.ClockKnown, v.Clock, v.ClockKind = true, fmt.Sprintf("%+.0f s", st.Clock), kOK
		if math.Abs(st.Clock) >= 60 {
			v.ClockKind = kWarn
		}
	}
	if math.Abs(st.Clock) >= 2 {
		v.Info = append(v.Info, kv{t["clock"], fmt.Sprintf("%+.0f s", st.Clock)})
		if math.Abs(st.Clock) >= 60 && st.Error == "" {
			sys.Alerts = append(sys.Alerts, alertView{kWarn, name + ": " + t["clock_off"], fmt.Sprintf("%+.0f s", st.Clock), ""})
		}
	}
	v.Info = append(v.Info, kv{t["checked"], st.Checked.Format("2006-01-02 15:04:05")})
	if st.Error != "" && !st.LastOK.IsZero() {
		v.Info = append(v.Info, kv{t["last_ok"], st.LastOK.Format("2006-01-02 15:04:05")})
	}
	if v.Kind == kWarn && v.Status == t["online"] {
		v.Status = t["warning"]
	}
	if c := nvr.Val("comment"); c != "" {
		v.Info = append(v.Info, kv{t["comment"], c})
	}
	return v
}

func sortCams(cs []camView) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(cs[i].Ch, "CH"))
		b, _ := strconv.Atoi(strings.TrimPrefix(cs[j].Ch, "CH"))
		return a < b
	})
}

func kpis(sys *system, t map[string]string) []kpi {
	online, notConn := 0, 0
	camsOn, camsAll, camsUnknown := 0, 0, 0
	for _, n := range sys.Nvrs {
		if n.Kind == kErr && !n.Local {
			notConn++
		} else if n.Kind != kMute {
			online++
		}
		camsAll += n.CamsTotal
		if n.CamsKnown {
			camsOn += n.CamsOnline
		} else {
			camsUnknown += n.CamsTotal
		}
	}
	k := []kpi{{Label: t["k_nvrs"], Value: strconv.Itoa(online), Of: t["of"] + " " + strconv.Itoa(len(sys.Nvrs)), Kind: kOK}}
	if notConn > 0 {
		k[0].Note, k[0].Kind = fmt.Sprintf(t["n_not_connected"], notConn), kErr
	}
	ck := kpi{Label: t["k_cams"], Value: strconv.Itoa(camsOn), Of: t["of"] + " " + strconv.Itoa(camsAll), Kind: kOK}
	if off := camsAll - camsOn - camsUnknown; off > 0 {
		ck.Note, ck.Kind = fmt.Sprintf(t["n_offline"], off), kWarn
	}
	if camsUnknown > 0 {
		ck.Note = strings.TrimPrefix(ck.Note+", "+fmt.Sprintf(t["n_unknown"], camsUnknown), ", ")
		ck.Kind = worst(ck.Kind, kWarn)
	}
	k = append(k, ck)
	healthy, errs, warns := 0, 0, 0
	for _, d := range sys.Disks {
		switch d.Kind {
		case kOK:
			healthy++
		case kErr:
			errs++
		case kWarn:
			warns++
		}
	}
	dk := kpi{Label: t["k_disks"], Value: strconv.Itoa(healthy), Of: t["of"] + " " + strconv.Itoa(len(sys.Disks)), Kind: kOK}
	if errs+warns > 0 {
		dk.Note, dk.Kind = fmt.Sprintf(t["n_disk_problems"], errs, warns), kWarn
	}
	k = append(k, dk)
	// the shortest retention: how far back the local recordings reach
	var shortest *nvrView
	for i := range sys.Nvrs {
		n := &sys.Nvrs[i]
		if n.Local && n.OldestAge > 0 && (shortest == nil || n.OldestAge < shortest.OldestAge) {
			shortest = n
		}
	}
	rk := kpi{Label: t["k_retention"], Value: "—", Kind: kMute}
	if shortest != nil {
		rk.Value = strconv.Itoa(int(shortest.OldestAge.Hours() / 24))
		rk.Of = t["days"]
		rk.Note = shortest.Name
		rk.Kind = kOK
		if shortest.RetentionTarget != "" {
			rk.Note += ", " + t["target"] + ": " + shortest.RetentionTarget
			if d, err := jobs.ParseLength(shortest.RetentionTarget); err == nil && shortest.OldestAge < d-24*time.Hour {
				rk.Kind = kWarn
			}
		}
	}
	return append(k, rk)
}
