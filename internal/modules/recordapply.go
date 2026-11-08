package modules

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/jobs"
)

// The worker's part of the recording schedule (record.go): every finished segment of a
// camera with a schedule is looked at once, in order, when its alarms are surely in
// the event database (its end + post-alarm + ScheduleMargin):
//
//	everything to keep   the segment stays as it is
//	nothing to keep      it is deleted
//	some of it           the parts to keep are cut out (ffmpeg -c copy, no re-encoding:
//	                     a part begins at the key frame before its start, so up to one
//	                     GOP more is kept), the segment is replaced by them
//
// The parts are named by their start like the segments, so the index, the Archive and
// the player see them as short segments; their ends are noted in pieces.json beside
// the index (a part does not last until the next segment begins).
//
// What the worker did is kept per camera in <storage>/<nvr>/.<camera>/schedule.json:
// the schedule, when it was assigned (older segments are never touched), up to where
// the segments are done, whether the alarms could not be trusted (fallback: the whole
// alarm time kept; the panel raises an alert) and the counts of the day.

// ScheduleMargin: how long after the end of a segment (and its post-alarm) the worker
// waits for late alarms before it decides.
const ScheduleMargin = 2 * time.Minute

// scheduleBudget: the most time one pass may spend cutting; the rest waits for the next.
var scheduleBudget = 40 * time.Second

// SegmentLength of the camera units (-segment_time 900).
const SegmentLength = 15 * time.Minute

// ScheduleState is what the worker keeps about one camera.
type ScheduleState struct {
	Schedule string    `json:"schedule"`
	Since    time.Time `json:"since"` // the schedule was first seen: older segments stay
	Done     time.Time `json:"done"`  // segments that started before are done
	// Fallback: the alarms of the latest alarm time could not be trusted, the whole of it
	// was kept; Reason says why
	Fallback      bool      `json:"fallback,omitempty"`
	FallbackSince time.Time `json:"fallback_since,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	// Waiting: the fallback is only the start of the alarm server or of the subscription
	// within the alarm time (nothing is known of the time before) - it ends by itself
	// with the next whole segment; no alert
	Waiting bool      `json:"waiting,omitempty"`
	Error   string    `json:"error,omitempty"` // the latest failure to cut or delete
	LastRun time.Time `json:"last_run,omitempty"`
	// the counts of one day (Day: 2006-01-02, local)
	Day              string `json:"day,omitempty"`
	Kept             int    `json:"kept,omitempty"`
	Cut              int    `json:"cut,omitempty"`
	Deleted          int    `json:"deleted,omitempty"`
	Freed            int64  `json:"freed,omitempty"`
	FallbackSegments int    `json:"fallback_segments,omitempty"`
}

const (
	scheduleStateFile = "schedule.json"
	piecesFile        = "pieces.json"
)

// ReadScheduleState of a camera of a local recorder (nil: none).
func ReadScheduleState(s *config.Store, nvr, camera string) *ScheduleState {
	n := find(s, "/nvr", "name", nvr)
	if n == nil || remote(n) {
		return nil
	}
	var st ScheduleState
	if !readJSON(filepath.Join(TechDir(localDir(s, n), camera), scheduleStateFile), &st) {
		return nil
	}
	return &st
}

// readPieces: the ends of the parts cut out of segments (name -> unix ms).
func readPieces(tech string) map[string]int64 {
	m := map[string]int64{}
	readJSON(filepath.Join(tech, piecesFile), &m)
	return m
}

// ---------------------------------------------------------------- what to keep

// mergeSpans sorts spans and joins those less than gap apart.
func mergeSpans(in []Span, gap time.Duration) []Span {
	sort.Slice(in, func(i, j int) bool { return in[i].Start.Before(in[j].Start) })
	var out []Span
	for _, s := range in {
		if !s.End.After(s.Start) {
			continue
		}
		if n := len(out); n > 0 && !s.Start.After(out[n-1].End.Add(gap)) {
			if s.End.After(out[n-1].End) {
				out[n-1].End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// keepSpans: what of [modes] is kept. capable: the camera sends alarms (otherwise
// alarm counts as regular); trusted: its alarms of this time surely arrived (otherwise
// the whole alarm time is kept); alarms: the times of its alarms.
func keepSpans(modes []ModeSpan, capable, trusted bool, alarms []Span, pre, post time.Duration) []Span {
	var keep []Span
	for _, m := range modes {
		switch {
		case m.Mode == RecRegular, m.Mode == RecAlarm && (!capable || !trusted):
			keep = append(keep, Span{m.Start, m.End})
		case m.Mode == RecAlarm:
			for _, a := range alarms {
				s, e := a.Start.Add(-pre), a.End.Add(post)
				if s.Before(m.Start) {
					s = m.Start
				}
				if e.After(m.End) {
					e = m.End
				}
				if e.After(s) {
					keep = append(keep, Span{s, e})
				}
			}
		}
	}
	// parts closer than 2 s are one: no files of a frame or two
	return mergeSpans(keep, 2*time.Second)
}

// ---------------------------------------------------------------- can the alarms be trusted

// alarmSource answers whether the alarms of a camera surely reached the event database
// during a time; the state of the alarm server and the database are read once a pass.
type alarmSource struct {
	st *AlarmState
	db *eventdb.DB
}

func openAlarmSource(root string) *alarmSource {
	a := &alarmSource{st: ReadAlarmState(root)}
	if db, ok, err := eventdb.OpenIfExists(eventdb.Path(root)); ok && err == nil {
		a.db = db
	}
	return a
}

func (a *alarmSource) close() {
	if a.db != nil {
		a.db.Close()
	}
}

const schedStamp = "2006-01-02 15:04:05"

// trusted: whether the alarms of a camera of the make typ reached ffnvr all through
// [from, to); else why not.
func (a *alarmSource) trusted(ctx context.Context, nvr, camera, typ string, silence time.Duration, from, to time.Time) (bool, string) {
	ok, why, _ := a.check(ctx, nvr, camera, typ, silence, from, to)
	return ok, why
}

// check is trusted, and whether the only fault is that the alarm server or the
// subscription began within [from, to) (waiting: it ends by itself, nothing is wrong).
func (a *alarmSource) check(ctx context.Context, nvr, camera, typ string, silence time.Duration, from, to time.Time) (ok bool, why string, waiting bool) {
	ok, why = a.checkWhy(ctx, nvr, camera, typ, silence, from, to)
	if !ok && a.st != nil {
		waiting = a.st.Started.After(from) && !a.st.Updated.Before(to)
		if sub, has := a.st.Subs[nvr+"/"+camera]; has && sub.Connected && sub.Since.After(from) && !a.st.Started.After(from) {
			waiting = true
		}
	}
	return ok, why, waiting
}

func (a *alarmSource) checkWhy(ctx context.Context, nvr, camera, typ string, silence time.Duration, from, to time.Time) (bool, string) {
	switch {
	case a.st == nil:
		return false, "the alarm server never ran (ffnvr-alarm.service)"
	case a.st.Started.After(from):
		return false, "the alarm server started at " + a.st.Started.Format(schedStamp)
	case a.st.Updated.Before(to):
		return false, "the alarm server is not running (last seen " + a.st.Updated.Format(schedStamp) + ")"
	case a.db == nil:
		return false, "no event database yet"
	}
	switch typ {
	case "tiandy", "dahua", "hikvision", "uniview":
		sub, ok := a.st.Subs[nvr+"/"+camera]
		switch {
		case !ok:
			return false, "not subscribed to: events=subscribe of /camera"
		case !sub.Connected:
			return false, "the subscription is down: " + or(sub.Error, "not connected")
		case sub.Since.After(from):
			return false, "subscribed again at " + sub.Since.Format(schedStamp)
		}
	case "dvrip":
		if l := a.st.Listeners["alarm-dvrip"]; l == "" || strings.HasPrefix(l, "error") {
			return false, "alarm-dvrip of /ip/service is not listening" + strings.TrimPrefix(" ("+l+")", " ()")
		}
		if silence > 0 {
			last, _, err := a.db.LastCameraEvent(ctx, nvr, camera, to)
			if err != nil {
				return false, "event database: " + err.Error()
			}
			if last.IsZero() {
				return false, "the camera never sent an event"
			}
			if to.Sub(last) > silence {
				return false, fmt.Sprintf("no event of the camera for more than %s (alarm-silence=), the last at %s", jobs.FormatLength(silence), last.Format(schedStamp))
			}
		}
	default:
		return false, "type=" + typ + " sends no alarms ffnvr follows"
	}
	return true, ""
}

// alarms: the times of the alarms of a camera in [from, to).
func (a *alarmSource) alarms(ctx context.Context, sc *Schedule, nvr, camera, typ string, from, to, now time.Time) ([]Span, error) {
	evs, err := a.db.CameraEvents(ctx, nvr, camera, from, to, now)
	if err != nil {
		return nil, err
	}
	var out []Span
	for _, e := range evs {
		if sc.IsAlarm(typ, e.Kind) {
			out = append(out, Span{e.Start, e.End})
		}
	}
	return mergeSpans(out, 0), nil
}

// ---------------------------------------------------------------- the pass of the worker

// cutPieces writes the parts of a segment (in: its file, start: when it began) into
// temporary files in dir; replaced in tests.
var cutPieces = defaultCutPieces

func defaultCutPieces(ctx context.Context, ffmpeg, in string, start time.Time, parts []Span, dir string) ([]string, error) {
	var out []string
	fail := func(err error) ([]string, error) {
		for _, f := range out {
			os.Remove(f)
		}
		return nil, err
	}
	for _, p := range parts {
		tmp, err := os.CreateTemp(dir, ".schedule-*.tmp")
		if err != nil {
			return fail(err)
		}
		tmp.Close()
		out = append(out, tmp.Name())
		args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
		if off := p.Start.Sub(start); off > 0 {
			args = append(args, "-ss", fmt.Sprintf("%.3f", off.Seconds())) // the key frame before
		}
		args = append(args, "-i", in, "-t", fmt.Sprintf("%.3f", p.End.Sub(p.Start).Seconds()),
			"-map", "0", "-c", "copy", "-avoid_negative_ts", "make_zero", "-f", "matroska", tmp.Name())
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		b, err := exec.CommandContext(cctx, ffmpeg, args...).CombinedOutput()
		cancel()
		if err != nil {
			return fail(fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(string(b))))
		}
		if st, err := os.Stat(tmp.Name()); err != nil || st.Size() == 0 {
			return fail(fmt.Errorf("ffmpeg wrote nothing for %s-%s", p.Start.Format("15:04:05"), p.End.Format("15:04:05")))
		}
	}
	return out, nil
}

// segEnd: when a segment ends - the next quarter of an hour (the camera units cut at
// the clock), earlier when the next one began before.
func segEnd(start time.Time, next *time.Time) time.Time {
	end := start.Truncate(SegmentLength).Add(SegmentLength)
	if next != nil && next.Before(end) {
		end = *next
	}
	return end
}

// ApplySchedules is the worker's pass over the cameras of the active local recorders
// with a schedule (run periodically, before MaintainLocalLog).
func ApplySchedules(ctx context.Context, s *config.Store, root, ffmpeg string, now time.Time, logf func(string, ...any)) {
	deadline := time.Now().Add(scheduleBudget)
	var src *alarmSource
	defer func() {
		if src != nil {
			src.close()
		}
	}()
	var entries []eventdb.Entry
	defer func() { SystemLog(root, logf, entries...) }()
	for _, nvr := range s.Items("/nvr") {
		if remote(nvr) || nvr.Val("disabled") == "yes" {
			continue
		}
		dir := localDir(s, nvr)
		if dir == "" {
			continue
		}
		var sum ScheduleState // the counts of this pass, for the system log
		for _, cam := range s.Items("/camera") {
			if cam.Val("nvr") != nvr.Val("name") || checkName("camera", cam.Val("name")) != nil {
				continue
			}
			tech := TechDir(dir, cam.Val("name"))
			name, _ := CameraSchedule(s, cam)
			sc := ReadSchedule(s, name)
			if sc == nil {
				os.Remove(filepath.Join(tech, scheduleStateFile)) // a schedule assigned later starts anew
				continue
			}
			if src == nil {
				src = openAlarmSource(root)
			}
			e := applyCamera(ctx, s, src, ffmpeg, nvr.Val("name"), cam, sc, dir, now, deadline, &sum, logf)
			entries = append(entries, e...)
		}
		if sum.Deleted+sum.Cut > 0 {
			entries = append(entries, eventdb.Entry{Nvr: nvr.Val("name"), Kind: SysRecordSchedule,
				Text: fmt.Sprintf("recording schedule: %s deleted, %s cut, %s freed", plural(sum.Deleted, "segment", "segments"),
					plural(sum.Cut, "segment", "segments"), HumanSize(sum.Freed))})
		}
	}
}

func applyCamera(ctx context.Context, s *config.Store, src *alarmSource, ffmpeg, nvr string, cam *config.Item, sc *Schedule,
	dir string, now, deadline time.Time, sum *ScheduleState, logf func(string, ...any)) (entries []eventdb.Entry) {
	camera := cam.Val("name")
	recDir, tech := filepath.Join(dir, camera), TechDir(dir, camera)
	statePath := filepath.Join(tech, scheduleStateFile)
	var st ScheduleState
	if !readJSON(statePath, &st) || st.Schedule == "" {
		st = ScheduleState{Since: now, Done: now}
		logf("schedule %s/%s: %s assigned, segments from %s on follow it", nvr, camera, sc.Name, now.Format(schedStamp))
	}
	st.Schedule, st.LastRun = sc.Name, now
	if day := now.Format("2006-01-02"); st.Day != day {
		st.Day, st.Kept, st.Cut, st.Deleted, st.Freed, st.FallbackSegments = day, 0, 0, 0, 0, 0
	}
	defer func() {
		if err := os.MkdirAll(tech, 0o755); err == nil {
			if err := writeJSON(statePath, st); err != nil {
				logf("schedule %s/%s: %v", nvr, camera, err)
			}
		}
	}()
	typ := cameraAlarmType(s, cam)
	capable, _ := AlarmCapable(s, cam)
	type seg struct {
		name  string
		start time.Time
	}
	ents, err := os.ReadDir(recDir)
	if err != nil {
		return nil
	}
	var segs []seg
	for _, e := range ents {
		if t, ok := segmentTime(e.Name()); ok && e.Type().IsRegular() {
			segs = append(segs, seg{e.Name(), t})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].start.Before(segs[j].start) })
	var pieces map[string]int64
	for i, sg := range segs {
		if sg.start.Before(st.Done) {
			continue
		}
		var next *time.Time
		if i+1 < len(segs) {
			next = &segs[i+1].start
		}
		end := segEnd(sg.start, next)
		if now.Before(end.Add(sc.Post + ScheduleMargin)) {
			break // and so are the later ones
		}
		path := filepath.Join(recDir, sg.name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if next == nil && now.Sub(info.ModTime()) < ScheduleMargin {
			break // the newest: perhaps still written
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break // the next pass goes on
		}
		modes := sc.Modes(sg.start, end)
		trusted, reason, hasAlarm := true, "", false
		for _, m := range modes {
			hasAlarm = hasAlarm || m.Mode == RecAlarm
		}
		var alarms []Span
		waiting := false
		if hasAlarm && capable {
			if trusted, reason, waiting = src.check(ctx, nvr, camera, typ, sc.Silence, sg.start, end); trusted {
				if alarms, err = src.alarms(ctx, sc, nvr, camera, typ, sg.start.Add(-sc.Post), end.Add(sc.Pre), now); err != nil {
					trusted, reason = false, "event database: "+err.Error()
				}
			}
			switch {
			case !trusted && !st.Fallback:
				st.Fallback, st.FallbackSince, st.Reason, st.Waiting = true, now, reason, waiting
				logf("schedule %s/%s: alarms not trusted, alarm time kept whole: %s", nvr, camera, reason)
				text := "the alarms of " + camera + " cannot be trusted, the alarm time is recorded whole: " + reason
				if waiting {
					text = "waiting for the alarms of " + camera + ", the alarm time is recorded whole until the next whole segment: " + reason
				}
				entries = append(entries, eventdb.Entry{Nvr: nvr, Camera: camera, Kind: SysRecordSchedule,
					Text: "recording schedule " + sc.Name + ": " + text})
			case !trusted:
				st.Reason = reason
				if !waiting && st.Waiting { // it was a start, now it is a fault: from now on
					st.FallbackSince = now
				}
				st.Waiting = waiting
			case st.Fallback:
				logf("schedule %s/%s: alarms trusted again", nvr, camera)
				entries = append(entries, eventdb.Entry{Nvr: nvr, Camera: camera, Kind: SysRecordSchedule,
					Text: "recording schedule " + sc.Name + ": the alarms of " + camera + " arrive again, the alarm mode works"})
				st.Fallback, st.FallbackSince, st.Reason, st.Waiting = false, time.Time{}, "", false
			}
			if !trusted {
				st.FallbackSegments++
			}
		}
		keep := keepSpans(modes, capable, trusted, alarms, sc.Pre, sc.Post)
		st.Done = end
		switch {
		case len(keep) == 1 && !keep[0].Start.After(sg.start.Add(time.Second)) && !keep[0].End.Before(end.Add(-time.Second)):
			st.Kept++
		case len(keep) == 0:
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				st.Error = now.Format(schedStamp) + " " + err.Error()
				logf("schedule %s/%s: %v", nvr, camera, err)
				continue
			}
			st.Deleted++
			sum.Deleted++
			st.Freed += info.Size()
			sum.Freed += info.Size()
		default:
			tmps, err := cutPieces(ctx, ffmpeg, path, sg.start, keep, recDir)
			if err != nil {
				st.Error = now.Format(schedStamp) + " " + sg.name + ": " + err.Error()
				logf("schedule %s/%s: %s kept whole: %v", nvr, camera, sg.name, err)
				st.Kept++
				continue
			}
			if pieces == nil {
				pieces = readPieces(tech)
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				for _, t := range tmps {
					os.Remove(t)
				}
				st.Error = now.Format(schedStamp) + " " + err.Error()
				continue
			}
			var size int64
			for k, t := range tmps {
				name := keep[k].Start.Format(jobs.SegmentLayout) + ".mkv"
				to := filepath.Join(recDir, name)
				if err := os.Rename(t, to); err != nil {
					logf("schedule %s/%s: %v", nvr, camera, err)
					os.Remove(t)
					continue
				}
				os.Chtimes(to, keep[k].End, keep[k].End) // the end, as jobs.Segments reads it
				if fi, err := os.Stat(to); err == nil {
					size += fi.Size()
				}
				pieces[name] = keep[k].End.UnixMilli()
			}
			st.Cut++
			sum.Cut++
			if freed := info.Size() - size; freed > 0 {
				st.Freed += freed
				sum.Freed += freed
			}
		}
	}
	if pieces != nil {
		if err := os.MkdirAll(tech, 0o755); err == nil {
			if err := writeJSON(filepath.Join(tech, piecesFile), pieces); err != nil {
				logf("schedule %s/%s: %v", nvr, camera, err)
			}
		}
	}
	return entries
}

// ---------------------------------------------------------------- status (shell, panel)

// CameraScheduleInfo: the schedule of one camera as the shell and the panel show it.
type CameraScheduleInfo struct {
	Nvr, Camera, Alias string
	Schedule, From     string // From: camera, nvr
	Type               string
	HasAlarm           bool   // its schedule has alarm time
	Capable            bool   // the camera sends alarms
	CapNote            string // why not, or what it needs
	Trusted            bool   // its alarms arrive now
	Reason             string // why not
	Waiting            bool   // not trusted only because the alarm server or the subscription just began
	LastEvent          time.Time
	Events24h          int
	State              *ScheduleState
}

// ScheduleInfos: the cameras of the given local recorders (nil: all) with a schedule.
func ScheduleInfos(ctx context.Context, s *config.Store, root string, nvrs []string, schedule string, now time.Time) []CameraScheduleInfo {
	var src *alarmSource
	defer func() {
		if src != nil {
			src.close()
		}
	}()
	var out []CameraScheduleInfo
	for _, cam := range s.Items("/camera") {
		n := find(s, "/nvr", "name", cam.Val("nvr"))
		if n == nil || remote(n) || (nvrs != nil && !slices.Contains(nvrs, n.Val("name"))) {
			continue
		}
		name, from := CameraSchedule(s, cam)
		sc := ReadSchedule(s, name)
		if sc == nil || (schedule != "" && name != schedule) {
			continue
		}
		in := CameraScheduleInfo{Nvr: n.Val("name"), Camera: cam.Val("name"), Alias: cam.Val("alias"), Schedule: name, From: from,
			Type: ResolveCamera(s, cam).Type, HasAlarm: sc.HasAlarm(), State: ReadScheduleState(s, n.Val("name"), cam.Val("name"))}
		in.Capable, in.CapNote = AlarmCapable(s, cam)
		if in.Capable {
			if src == nil {
				src = openAlarmSource(root)
			}
			// the alarm server writes its state every minute
			in.Trusted, in.Reason, in.Waiting = src.check(ctx, in.Nvr, in.Camera, in.Type, sc.Silence, now.Add(-SegmentLength), now.Add(-2*time.Minute))
			if src.db != nil {
				in.LastEvent, in.Events24h, _ = src.db.LastCameraEvent(ctx, in.Nvr, in.Camera, now)
			}
		}
		out = append(out, in)
	}
	return out
}

// ScheduleStatus: the text of /record/schedule status.
func ScheduleStatus(s *config.Store, root, name string, now time.Time) string {
	var b strings.Builder
	sc := ReadSchedule(s, name)
	if sc == nil {
		return ""
	}
	fmt.Fprintf(&b, "schedule %s: pre-alarm %s, post-alarm %s, alarm-silence %s", name, formatSeconds(sc.Pre), formatSeconds(sc.Post),
		map[bool]string{true: "off", false: jobs.FormatLength(sc.Silence)}[sc.Silence == 0])
	if len(sc.FilterName) > 0 {
		fmt.Fprintf(&b, ", alarm-filters %s", strings.Join(sc.FilterName, ","))
	}
	b.WriteString("\n")
	for i, k := range WeekdayKeys {
		d := sc.Days[(i+1)%7]
		if d == nil {
			fmt.Fprintf(&b, "  %s  (no template) 00:00-24:00=regular\n", k)
			continue
		}
		fmt.Fprintf(&b, "  %s  %-12s %s\n", k, d.Name, FormatDaySpans(d.Full()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	infos := ScheduleInfos(ctx, s, root, nil, name, now)
	if len(infos) == 0 {
		b.WriteString("  used by no camera (record-schedule= of /nvr or /camera)\n")
	}
	for _, in := range infos {
		fmt.Fprintf(&b, "  %s/%s (from %s, type=%s)\n", in.Nvr, in.Camera, in.From, or(in.Type, "-"))
		switch {
		case !in.HasAlarm:
			b.WriteString("    alarm: the schedule has no alarm time\n")
		case !in.Capable:
			b.WriteString("    alarm: " + in.CapNote + "\n")
		default:
			if in.CapNote != "" {
				b.WriteString("    alarm: " + in.CapNote + "\n")
			}
			last := "never"
			if !in.LastEvent.IsZero() {
				last = in.LastEvent.Format(schedStamp) + " (" + jobs.FormatLength(now.Sub(in.LastEvent).Round(time.Minute)+time.Minute) + " ago)"
			}
			fmt.Fprintf(&b, "    events: last %s, %d in 24h\n", last, in.Events24h)
			if in.Trusted {
				b.WriteString("    alarms arrive: yes\n")
			} else {
				b.WriteString("    alarms arrive: NO - " + in.Reason + " (the alarm time is recorded whole)\n")
			}
		}
		if st := in.State; st == nil {
			b.WriteString("    worker: not yet (it starts within a minute; older segments stay)\n")
		} else {
			fmt.Fprintf(&b, "    worker: since %s, done until %s; today %d kept, %d cut, %d deleted, %s freed", st.Since.Format(schedStamp),
				st.Done.Format(schedStamp), st.Kept, st.Cut, st.Deleted, HumanSize(st.Freed))
			if st.FallbackSegments > 0 {
				fmt.Fprintf(&b, ", %d kept whole (alarms not trusted)", st.FallbackSegments)
			}
			b.WriteString("\n")
			if st.Fallback && st.Waiting {
				fmt.Fprintf(&b, "    waiting for data since %s: %s\n", st.FallbackSince.Format(schedStamp), st.Reason)
			} else if st.Fallback {
				fmt.Fprintf(&b, "    FALLBACK since %s: %s\n", st.FallbackSince.Format(schedStamp), st.Reason)
			}
			if st.Error != "" {
				fmt.Fprintf(&b, "    last error: %s\n", st.Error)
			}
		}
	}
	return b.String()
}
