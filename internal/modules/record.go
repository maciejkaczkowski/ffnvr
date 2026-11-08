package modules

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/jobs"
	"ffnvr/internal/script"
)

// The recording schedule of the cameras of local recorders. A camera records all the
// time (15-minute segments, see the camera units); the worker then keeps, cuts or
// deletes every finished segment as the schedule says (recordapply.go). Three steps:
//
//	/record/day        what one day looks like: time spans and their mode
//	/record/schedule   which day goes with which day of the week, and the alarm settings
//	record-schedule=   of /nvr (every camera of the recorder) or /camera (wins)
//
//	/record/day
//	add name=office default=none spans="08:00-16:00=regular"
//	add name=night  default=alarm spans="06:00-22:00=regular"
//	/record/schedule
//	add name=week week="mon-fri:office,sat-sun:night" pre-alarm=10s post-alarm=60s
//	/nvr
//	set nvr01 record-schedule=week
//
// Modes:
//
//	regular  everything is kept
//	alarm    only what lies around the camera's alarms (pre-alarm before, post-alarm
//	         after): its motion detection and the kinds of alarm-filters=. Only cameras
//	         that send their alarms: type=dvrip (alarm-dvrip of /ip/service),
//	         type=tiandy with events=subscribe and type=dahua with events=subscribe
//	         (its VideoMotion; without events=subscribe a Dahua camera is like any
//	         other), type=hikvision and type=uniview with events=subscribe (VMD,
//	         MotionAlarm); any other camera keeps everything
//	         (alarm counts as regular). When the alarms may not have reached ffnvr (the
//	         alarm server was down, the subscription broke, nothing came for
//	         alarm-silence=) the whole time is kept and the panel raises an alert.
//	none     nothing is kept
//
// A day of the week without a template is recorded all the time. A camera without a
// schedule (neither its own nor its recorder's) keeps everything, as before. The
// schedule works from the moment it is assigned: older recordings are never touched.

// Recording modes of a span of time.
const (
	RecRegular = "regular"
	RecAlarm   = "alarm"
	RecNone    = "none"
)

// RecModes, in the order of the panel's colours.
var RecModes = []string{RecRegular, RecAlarm, RecNone}

// Weekdays as the schedule names them, Monday first; WeekdayKeys[i] is time.Weekday (i+1)%7.
var WeekdayKeys = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// Defaults of /record/schedule.
const (
	DefaultPreAlarm     = 10 * time.Second
	DefaultPostAlarm    = 60 * time.Second
	DefaultAlarmSilence = 24 * time.Hour
)

// alarmCameraTypes: the makes whose own alarms the schedule can follow.
var alarmCameraTypes = map[string]bool{"dvrip": true, "tiandy": true, "dahua": true, "hikvision": true, "uniview": true}

func recordDayMenu() *config.Menu {
	return &config.Menu{
		Path:    "/record/day",
		Kind:    config.Multi,
		NameKey: "name",
		Help:    "recording schedule: what one day looks like (used by mon= ... sun= of /record/schedule)",
		Fields: []config.Field{
			{Name: "name", Required: true},
			{Name: "spans", Type: config.TList, Help: `spans of the day and their mode, e.g. "08:00-16:00=regular,16:00-22:00=alarm"; 24:00 ends the day; spans may not overlap`},
			{Name: "default", Type: config.TEnum, Enum: RecModes, Help: "mode of the time no span covers (default none)"},
			{Name: "comment"},
		},
		Validate: validateRecordDay,
	}
}

func recordScheduleMenu(commands map[string]config.Command) *config.Menu {
	fields := []config.Field{{Name: "name", Required: true}}
	for _, d := range WeekdayKeys {
		fields = append(fields, config.Field{Name: d, Ref: "/record/day:name", Strict: true,
			Help: "template of the day (/record/day); unset: recorded all the time"})
	}
	fields = append(fields,
		config.Field{Name: "week", Help: `shorthand for mon= ... sun=: "mon-fri:office,sat-sun:weekend" (stored as the single days)`},
		config.Field{Name: "pre-alarm", Help: "alarm mode: kept before an alarm, 0s to 5m (default 10s)"},
		config.Field{Name: "post-alarm", Help: "alarm mode: kept after an alarm, 0s to 10m (default 60s)"},
		config.Field{Name: "alarm-filters", Type: config.TList, Ref: "/event/filter:name", Strict: true,
			Help: "alarm mode: besides motion detection, the kinds of these filters count as alarms (/event/filter, action=accept only)"},
		config.Field{Name: "alarm-silence", Help: "alarm mode, cameras that push their alarms (dvrip): no event at all for this long means the alarms may not arrive - the whole time is kept and an alert raised (default 1d; 0: never)"},
		config.Field{Name: "comment"},
	)
	return &config.Menu{
		Path:     "/record/schedule",
		Kind:     config.Multi,
		NameKey:  "name",
		Help:     "recording schedule of the week: record-schedule= of /nvr (all its cameras) or /camera (wins)",
		Fields:   fields,
		Validate: validateRecordSchedule,
		Detail:   recordScheduleDetail,
		Commands: commands,
	}
}

// ---------------------------------------------------------------- day templates

// DaySpan is a span of a day in minutes from midnight, [From, To).
type DaySpan struct {
	From, To int
	Mode     string
}

// DayTemplate is one /record/day.
type DayTemplate struct {
	Name    string
	Default string
	Spans   []DaySpan // sorted, not overlapping
}

func parseDayClock(v string, end bool) (int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok {
		return 0, fmt.Errorf("%q: expected HH:MM", v)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 || mm > 59 || len(m) != 2 {
		return 0, fmt.Errorf("%q: expected HH:MM", v)
	}
	t := hh*60 + mm
	if t > 24*60 || (t == 24*60 && !end) {
		return 0, fmt.Errorf("%q: past the end of the day", v)
	}
	return t, nil
}

// FormatClock writes minutes from midnight as HH:MM (24:00 for the end of the day).
func FormatDayClock(min int) string { return fmt.Sprintf("%02d:%02d", min/60, min%60) }

// ParseDaySpans reads spans= of /record/day.
func ParseDaySpans(v string) ([]DaySpan, error) {
	var out []DaySpan
	for _, e := range splitList(v) {
		rng, mode, ok := strings.Cut(e, "=")
		if !ok {
			return nil, fmt.Errorf("span %q: expected HH:MM-HH:MM=mode (%s)", e, strings.Join(RecModes, ", "))
		}
		mode = strings.TrimSpace(mode)
		if !slices.Contains(RecModes, mode) {
			return nil, fmt.Errorf("span %q: mode %q is not one of %s", e, mode, strings.Join(RecModes, ", "))
		}
		a, b, ok := strings.Cut(rng, "-")
		if !ok {
			return nil, fmt.Errorf("span %q: expected HH:MM-HH:MM=mode", e)
		}
		from, err := parseDayClock(a, false)
		if err != nil {
			return nil, fmt.Errorf("span %q: %v", e, err)
		}
		to, err := parseDayClock(b, true)
		if err != nil {
			return nil, fmt.Errorf("span %q: %v", e, err)
		}
		if to <= from {
			return nil, fmt.Errorf("span %q: the end must come after the start (a span over midnight is two spans, on two days)", e)
		}
		out = append(out, DaySpan{from, to, mode})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	for i := 1; i < len(out); i++ {
		if out[i].From < out[i-1].To {
			return nil, fmt.Errorf("spans %s-%s and %s-%s overlap", FormatDayClock(out[i-1].From), FormatDayClock(out[i-1].To), FormatDayClock(out[i].From), FormatDayClock(out[i].To))
		}
	}
	return out, nil
}

// FormatDaySpans writes spans as spans= takes them; neighbours of the same mode are joined.
func FormatDaySpans(spans []DaySpan) string {
	var parts []string
	for i := 0; i < len(spans); i++ {
		s := spans[i]
		for i+1 < len(spans) && spans[i+1].From == s.To && spans[i+1].Mode == s.Mode {
			s.To = spans[i+1].To
			i++
		}
		parts = append(parts, FormatDayClock(s.From)+"-"+FormatDayClock(s.To)+"="+s.Mode)
	}
	return strings.Join(parts, ",")
}

func validateRecordDay(_ *config.Store, it *config.Item) error {
	if err := checkName("name", it.Val("name")); err != nil {
		return err
	}
	spans, err := ParseDaySpans(it.Val("spans"))
	if err != nil {
		return err
	}
	if len(spans) == 0 {
		it.Unset("spans")
	} else {
		it.Set("spans", FormatDaySpans(spans))
	}
	return nil
}

func readDayTemplate(it *config.Item) *DayTemplate {
	spans, _ := ParseDaySpans(it.Val("spans")) // validated when stored
	return &DayTemplate{Name: it.Val("name"), Default: or(it.Val("default"), RecNone), Spans: spans}
}

// Full returns the whole day, the gaps between the spans filled with the default.
func (d *DayTemplate) Full() []DaySpan {
	var out []DaySpan
	at := 0
	add := func(s DaySpan) {
		if n := len(out); n > 0 && out[n-1].Mode == s.Mode && out[n-1].To == s.From {
			out[n-1].To = s.To
			return
		}
		out = append(out, s)
	}
	for _, s := range d.Spans {
		if s.From > at {
			add(DaySpan{at, s.From, d.Default})
		}
		add(s)
		at = s.To
	}
	if at < 24*60 {
		add(DaySpan{at, 24 * 60, d.Default})
	}
	return out
}

// ---------------------------------------------------------------- week schedules

// parseWeek reads week= ("mon-fri:office,sat-sun:night"): the template of every day
// it names.
func parseWeek(v string) (map[string]string, error) {
	out := map[string]string{}
	idx := func(d string) int {
		for i, k := range WeekdayKeys {
			if k == strings.ToLower(strings.TrimSpace(d)) {
				return i
			}
		}
		return -1
	}
	for _, e := range splitList(v) {
		days, tmpl, ok := strings.Cut(e, ":")
		tmpl = strings.TrimSpace(tmpl)
		if !ok || tmpl == "" {
			return nil, fmt.Errorf("week %q: expected days:template, e.g. mon-fri:office", e)
		}
		a, b, isRange := strings.Cut(days, "-")
		from, to := idx(a), idx(a)
		if isRange {
			to = idx(b)
		}
		if from < 0 || to < 0 {
			return nil, fmt.Errorf("week %q: days are %s, a range like mon-fri", e, strings.Join(WeekdayKeys, " "))
		}
		for i := from; ; i = (i + 1) % 7 {
			d := WeekdayKeys[i]
			if _, dup := out[d]; dup {
				return nil, fmt.Errorf("week %q: %s is named twice", v, d)
			}
			out[d] = tmpl
			if i == to {
				break
			}
		}
	}
	return out, nil
}

// FormatWeek writes the days of a schedule compactly: "mon-fri:office,sat-sun:night".
func FormatWeek(it *config.Item) string {
	var parts []string
	for i := 0; i < 7; {
		t := it.Val(WeekdayKeys[i])
		j := i
		for j+1 < 7 && it.Val(WeekdayKeys[j+1]) == t {
			j++
		}
		if t != "" {
			d := WeekdayKeys[i]
			if j > i {
				d += "-" + WeekdayKeys[j]
			}
			parts = append(parts, d+":"+t)
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// parseAlarmLength reads pre-alarm= and post-alarm=: 10s, 1m, 90 (seconds).
func parseAlarmLength(v string, max time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		f, e := strconv.ParseFloat(v, 64)
		if e != nil {
			return 0, fmt.Errorf("%q: expected a length like 10s, 1m", v)
		}
		d = time.Duration(f * float64(time.Second))
	}
	if d < 0 || d > max {
		return 0, fmt.Errorf("%q: 0s to %s", v, max)
	}
	return d.Round(time.Second), nil
}

func formatSeconds(d time.Duration) string {
	if d%time.Minute == 0 && d > 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%ds", d/time.Second)
}

func parseSilence(v string) (time.Duration, error) {
	if v == "0" || v == "0s" || v == "none" {
		return 0, nil
	}
	d, err := jobs.ParseLength(v)
	if err != nil || d < 10*time.Minute {
		return 0, fmt.Errorf("alarm-silence %q: a length of at least 10m (e.g. 6h, 1d), or 0 for no check", v)
	}
	return d, nil
}

func validateRecordSchedule(s *config.Store, it *config.Item) error {
	if err := checkName("name", it.Val("name")); err != nil {
		return err
	}
	if w := it.Val("week"); w != "" {
		days, err := parseWeek(w)
		if err != nil {
			return err
		}
		for d, t := range days {
			if find(s, "/record/day", "name", t) == nil {
				return fmt.Errorf("week: no /record/day named %q", t)
			}
			it.Set(d, t)
		}
		it.Unset("week") // stored as the single days
	}
	for _, k := range []struct {
		key string
		max time.Duration
	}{{"pre-alarm", 5 * time.Minute}, {"post-alarm", 10 * time.Minute}} {
		if v := it.Val(k.key); v != "" {
			d, err := parseAlarmLength(v, k.max)
			if err != nil {
				return fmt.Errorf("%s %v", k.key, err)
			}
			it.Set(k.key, formatSeconds(d))
		}
	}
	if v := it.Val("alarm-silence"); v != "" {
		d, err := parseSilence(v)
		if err != nil {
			return err
		}
		if d == 0 {
			it.Set("alarm-silence", "0")
		} else {
			it.Set("alarm-silence", jobs.FormatLength(d))
		}
	}
	for _, f := range splitList(it.Val("alarm-filters")) {
		if fi := find(s, "/event/filter", "name", f); fi != nil && fi.Val("action") != "accept" {
			return fmt.Errorf("alarm-filters: %s is action=%s; only filters with action=accept name the kinds that are alarms", f, fi.Val("action"))
		}
	}
	return nil
}

func recordScheduleDetail(s *config.Store, it *config.Item) []string {
	out := []string{"week: " + or(FormatWeek(it), "(no day templates: recorded all the time)")}
	var users []string
	for _, n := range s.Items("/nvr") {
		if n.Val("record-schedule") == it.Val("name") {
			users = append(users, "nvr "+n.Val("name"))
		}
	}
	for _, c := range s.Items("/camera") {
		if c.Val("record-schedule") == it.Val("name") {
			users = append(users, "camera "+c.Val("nvr")+"/"+c.Val("name"))
		}
	}
	if len(users) == 0 {
		out = append(out, "assigned to: nobody")
	} else {
		out = append(out, "assigned to: "+strings.Join(users, ", "))
	}
	return out
}

// Schedule is one /record/schedule, ready to use.
type Schedule struct {
	Name       string
	Days       [7]*DayTemplate // by time.Weekday; nil: recorded all the time
	Pre, Post  time.Duration
	Silence    time.Duration // 0: no check
	Filters    []*EventFilter
	FilterName []string
}

// ReadSchedule reads a schedule of the configuration (nil: none of that name).
func ReadSchedule(s *config.Store, name string) *Schedule {
	it := find(s, "/record/schedule", "name", name)
	if name == "" || it == nil {
		return nil
	}
	sc := &Schedule{Name: name, Pre: DefaultPreAlarm, Post: DefaultPostAlarm, Silence: DefaultAlarmSilence}
	for i, k := range WeekdayKeys {
		if d := find(s, "/record/day", "name", it.Val(k)); d != nil {
			sc.Days[(i+1)%7] = readDayTemplate(d)
		}
	}
	if d, err := parseAlarmLength(it.Val("pre-alarm"), 5*time.Minute); err == nil && it.Val("pre-alarm") != "" {
		sc.Pre = d
	}
	if d, err := parseAlarmLength(it.Val("post-alarm"), 10*time.Minute); err == nil && it.Val("post-alarm") != "" {
		sc.Post = d
	}
	if v := it.Val("alarm-silence"); v != "" {
		if d, err := parseSilence(v); err == nil {
			sc.Silence = d
		}
	}
	for _, f := range splitList(it.Val("alarm-filters")) {
		if fi := find(s, "/event/filter", "name", f); fi != nil && fi.Val("action") == "accept" {
			sc.Filters = append(sc.Filters, readEventFilter(fi))
			sc.FilterName = append(sc.FilterName, f)
		}
	}
	return sc
}

// ModeSpan is a stretch of time with one recording mode.
type ModeSpan struct {
	Start, End time.Time
	Mode       string
}

// Modes returns the modes of [from, to), in order, neighbours of one mode joined.
func (sc *Schedule) Modes(from, to time.Time) []ModeSpan {
	var out []ModeSpan
	add := func(a, b time.Time, mode string) {
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		if !b.After(a) {
			return
		}
		if n := len(out); n > 0 && out[n-1].Mode == mode && !out[n-1].End.Before(a) {
			out[n-1].End = b
			return
		}
		out = append(out, ModeSpan{a, b, mode})
	}
	y, m, d := from.In(time.Local).Date()
	for day := time.Date(y, m, d, 0, 0, 0, 0, time.Local); day.Before(to); {
		next := time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, time.Local)
		tmpl := sc.Days[day.Weekday()]
		if tmpl == nil {
			add(day, next, RecRegular)
		} else {
			for _, s := range tmpl.Full() {
				// minutes added as wall clock time: right on the days the clock changes too
				a := time.Date(day.Year(), day.Month(), day.Day(), 0, s.From, 0, 0, time.Local)
				b := time.Date(day.Year(), day.Month(), day.Day(), 0, s.To, 0, 0, time.Local)
				add(a, b, s.Mode)
			}
		}
		day = next
	}
	return out
}

// HasAlarm reports whether any day of the schedule has an alarm span.
func (sc *Schedule) HasAlarm() bool {
	for _, d := range sc.Days {
		if d == nil {
			continue
		}
		for _, s := range d.Full() {
			if s.Mode == RecAlarm {
				return true
			}
		}
	}
	return false
}

// IsAlarm: an event of this kind is an alarm of a camera of this make.
func (sc *Schedule) IsAlarm(camType, kind string) bool {
	if k, ok := cameraMotionKinds[camType]; ok && strings.EqualFold(k, kind) {
		return true
	}
	for _, f := range sc.Filters {
		if f.matches(kind) {
			return true
		}
	}
	return false
}

// CameraSchedule: the schedule of a camera of a local recorder - its own, else its
// recorder's - and where it comes from ("camera", "nvr"); "" when it has none.
func CameraSchedule(s *config.Store, cam *config.Item) (name, from string) {
	if v := cam.Val("record-schedule"); v != "" {
		return v, "camera"
	}
	if n := find(s, "/nvr", "name", cam.Val("nvr")); n != nil && !remote(n) && n.Val("record-schedule") != "" {
		return n.Val("record-schedule"), "nvr"
	}
	return "", ""
}

// AlarmCapable: the camera sends the alarms the alarm mode follows, and why not.
func AlarmCapable(s *config.Store, cam *config.Item) (bool, string) {
	src := ResolveCamera(s, cam)
	switch {
	case !alarmCameraTypes[src.Type]:
		if src.Type == "" {
			return false, "no type= (dvrip, tiandy, dahua, hikvision, uniview): alarm counts as regular"
		}
		return false, "type=" + src.Type + " sends no alarms ffnvr follows: alarm counts as regular"
	case alarmsOnlySubscribed[src.Type] && cam.Val("events") != "subscribe":
		return false, "type=" + src.Type + " without events=subscribe sends no alarms ffnvr follows: alarm counts as regular"
	case src.Type == "tiandy" && cam.Val("events") != "subscribe":
		return true, "type=tiandy needs events=subscribe: without it no alarm arrives and the whole time is kept"
	}
	return true, ""
}

// recordCommands: the verbs of /record/schedule (status needs the root of the system).
func recordCommands(root func() string) map[string]config.Command {
	return map[string]config.Command{
		"status": {Help: "where the schedule is used, whether the alarms of its cameras arrive, what the worker did",
			Run: func(s *config.Store, _ script.Command, items []*config.Item) (string, error) {
				var b strings.Builder
				for _, it := range items {
					b.WriteString(ScheduleStatus(s, root(), it.Val("name"), time.Now()))
				}
				return b.String(), nil
			}},
	}
}
