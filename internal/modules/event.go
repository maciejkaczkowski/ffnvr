package modules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/jobs"
	"ffnvr/internal/module"
	"ffnvr/internal/script"
)

// eventModule: /event - the events of the recorders, received by the alarm server
// (ffnvr alarm, ffnvr-alarm.service) and kept in SQLite (/var/lib/ffnvr/events).
//
//	/event/filter
//	add name=dahua-nvr action=drop kinds=VideoMotion,RtspSessionDisconnect,InterVideoAccess,RecordDelete
//	add name=dahua-ipc action=accept kinds=VideoMotion
//	/nvr
//	set [ find name=nvr02 ] event-filter=dahua-nvr
//	/event
//	set retention=90d gap=5s default-filter=dahua-nvr
//	/event/log print nvr=nvr02 kind=VideoMotion
//	/event/log ack 12,13
//	/event/log ack nvr=nvr02
//
// Tiandy recorders and cameras of a local recorder (events=subscribe of /nvr, /camera:
// the same protocol) send records of a tree, IP\SYSTEM\DESCRIPTION; the kind is the description (INNER\ALARM\MOTIONDETECT),
// the text the whole record with \ for the tabs, the raw notice its JSON (print raw=yes).
// Kinds of filters may hold * (and / for \), e.g. only the alarms of Tiandy cameras:
//
//	/event/filter
//	add name=tiandy-alarms action=accept kinds=*\ALARM\*
//	/camera
//	set [ find name=brama ] type=tiandy events=subscribe event-filter=tiandy-alarms
//
// Hikvision cameras of a local recorder (events=subscribe of /camera: ISAPI alertStream)
// send alerts of an eventType: VMD (motion detection), tamperdetection, linedetection,
// fielddetection, IO...; the kind is the eventType, the text its eventDescription, the
// raw notice its XML. An alert is repeated while the event lasts: the event ends when
// they stop coming (5 s). The heartbeat of the stream (videoloss inactive) is not kept.
//
// Uniview cameras of a local recorder (events=subscribe of /camera: LAPI) push their
// alarms to alarm-lapi of /ip/service; the kind is the alarm type without its On, Off
// or Cleared (MotionAlarmOn ... MotionAlarmOff is one event MotionAlarm), the raw
// notice its JSON. Without a filter of its own (or default-filter= of /event) such a
// camera keeps only its alarms; a filter decides about the pushes of data too
// (Structure, PeopleCount\LineRuleData...), action=accept kinds=* keeps all:
//
//	/ip/service
//	set [ find name=alarm-lapi ] enabled=yes port=8091
//	/event/filter
//	add name=uv-motion action=accept kinds=MotionAlarm
//	/camera
//	set [ find name=wjazd ] type=uniview events=subscribe event-filter=uv-motion
//
// The events come by three ways: an alarm server port (/ip/service alarm-dvrip,
// alarm-smtp: the recorder sends), or a subscription (events=subscribe of /nvr:
// ffnvr connects to the recorder, dahua, dvrip, tiandy and uniview; events=subscribe of
// /camera: to a camera of a local recorder, dahua, hikvision, tiandy and uniview).
//
// Which kinds are kept is told by filters (/event/filter): action=accept keeps only the
// kinds listed, action=drop keeps all but them. A recorder takes the filter of its
// event-filter= (/nvr); recorders without one and senders that are no known recorder
// take default-filter= of /event, and without that DefaultEventIgnore is dropped.
// A camera subscribed to takes the filter of its own event-filter= (/camera), else
// default-filter= of /event. A filter replaces the built-in list, it does not add to it.
type eventModule struct {
	ctx *module.Context
}

func (e *eventModule) Name() string           { return "event" }
func (e *eventModule) Bind(c *module.Context) { e.ctx = c }

// DefaultEventIgnore: codes that are no alarm but come in numbers (a new recording
// file every few minutes, clock adjustments); dropped when no filter is given.
const DefaultEventIgnore = "NewFile,TimeChange,NTPAdjustTime,RtspSessionDisconnect,SnapManual"

// DefaultEventRetention: how long events are kept.
const DefaultEventRetention = 30 * 24 * time.Hour

func (e *eventModule) Menus() []*config.Menu {
	return []*config.Menu{
		{
			Path: "/event",
			Kind: config.Single,
			Help: "events of the recorders (alarm server): how long they are kept and how notices are joined",
			Fields: []config.Field{
				{Name: "retention", Help: "how long events are kept, e.g. 90d (default 30d, at least 1h)"},
				{Name: "gap", Help: "notices of one camera and kind closer than this are one event, e.g. 5s (default 5s, 0s: never joined)"},
				{Name: "default-filter", Ref: "/event/filter:name", Strict: true,
					Help: "the filter (/event/filter) of recorders without event-filter= and of unknown senders; unset: " + DefaultEventIgnore + " are dropped"},
			},
			Validate: func(_ *config.Store, it *config.Item) error {
				_, err := readEventSettings(it, true)
				return err
			},
		},
		{
			Path: "/event/log",
			Kind: config.Multi,
			Help: "the events received: print, ack",
			Fields: []config.Field{
				{Name: "nvr", Ref: "/nvr:name", Help: "the recorder"},
				{Name: "camera", Ref: "/camera:name", Help: "the camera (its name)"},
				{Name: "kind", Help: "e.g. VideoMotion, VideoLoss, Mail, INNER\\ALARM\\MOTIONDETECT (Tiandy), VMD (Hikvision), MotionAlarm (Uniview); * for any text, / for \\ (kind=*/ALARM/*); of the system log Login, LoginFailed, ConfigSaved, RecordingsDeleted, NvrOffline..."},
				{Name: "origin", Type: config.TEnum, Enum: []string{"recorders", "system"}, Help: "recorders: events the recorders sent; system: the system log (sign ins, configuration, deletions, recorders lost)"},
				{Name: "user", Help: "system log: who did it"},
				{Name: "text", Help: "contained in the details or the sender's address"},
				{Name: "from", Help: `started at or after, "YYYY-MM-DD HH:MM[:SS]"`},
				{Name: "to", Help: `started before, "YYYY-MM-DD HH:MM[:SS]"`},
				{Name: "unacked", Type: config.TBool, Help: "print: only those not acknowledged yet"},
				{Name: "count", Type: config.TInt, Help: "print: at most this many, the newest (default 50)"},
				{Name: "raw", Type: config.TBool, Help: "print: under every event the first notice as it came (Tiandy: JSON of the record)"},
			},
			Live: e,
		},
	}
}

// eventFilterMenu: /event/filter, the lists of kinds kept or dropped. It is registered
// with /nvr, before it, so that an exported script defines the filters before the
// recorders naming them (see All).
func eventFilterMenu() *config.Menu {
	return &config.Menu{
		Path:    "/event/filter",
		Kind:    config.Multi,
		NameKey: "name",
		Help:    "which kinds of events are kept: event-filter= of /nvr, default-filter= of /event",
		Fields: []config.Field{
			{Name: "name", Required: true},
			{Name: "action", Type: config.TEnum, Enum: []string{"accept", "drop"}, Required: true,
				Help: "accept: only the kinds listed are kept; drop: the kinds listed are not kept, all others are"},
			{Name: "kinds", Type: config.TList,
				Help: "event kinds as the recorders send them, e.g. VideoMotion,VideoLoss (see /event/log print); * stands for any text and / may be written for \\, e.g. *\\ALARM\\* (the alarms of Tiandy); action=drop without kinds keeps everything"},
			{Name: "comment"},
		},
		Validate: func(_ *config.Store, it *config.Item) error {
			if err := checkName("name", it.Val("name")); err != nil {
				return err
			}
			if it.Val("action") == "accept" && len(filterKinds(it.Val("kinds"))) == 0 {
				return fmt.Errorf("action=accept needs kinds (without them nothing would be kept)")
			}
			return nil
		},
	}
}

// EventFilter decides which kinds of events are kept.
type EventFilter struct {
	Name   string // of /event/filter; "" the built-in one
	Accept bool   // true: only Kinds are kept; false: Kinds are dropped
	Kinds  map[string]bool
}

// Keeps tells whether events of kind are kept (a nil filter keeps everything).
func (f *EventFilter) Keeps(kind string) bool {
	if f == nil {
		return true
	}
	return f.matches(kind) == f.Accept
}

// matches: kind is one of Kinds, or fits one of its patterns (with *).
func (f *EventFilter) matches(kind string) bool {
	if f.Kinds[kind] {
		return true
	}
	for k := range f.Kinds {
		if strings.Contains(k, "*") && KindMatch(k, kind) {
			return true
		}
	}
	return false
}

// KindMatch: a pattern of kinds - * stands for any text (also across the \ of the
// tree of Tiandy, so *\ALARM\* fits INNER\ALARM\MOTIONDETECT), / may be written for
// \ (no quoting needed), letters are compared without case.
func KindMatch(pattern, kind string) bool {
	p := strings.ToLower(strings.ReplaceAll(pattern, "/", `\`))
	k := strings.ToLower(kind)
	// the classic greedy matcher: the last * met takes one more character on a mismatch
	pi, ki, star, mark := 0, 0, -1, 0
	for ki < len(k) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ki
			pi++
		case pi < len(p) && p[pi] == k[ki]:
			pi++
			ki++
		case star >= 0:
			mark++
			pi, ki = star+1, mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func filterKinds(list string) map[string]bool {
	out := map[string]bool{}
	for _, k := range splitList(list) {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = true
		}
	}
	return out
}

// builtinFilter: what is dropped when no filter is given.
func builtinFilter() *EventFilter {
	return &EventFilter{Kinds: filterKinds(DefaultEventIgnore)}
}

func readEventFilter(it *config.Item) *EventFilter {
	return &EventFilter{Name: it.Val("name"), Accept: it.Val("action") == "accept", Kinds: filterKinds(it.Val("kinds"))}
}

// EventSettings of /event and /event/filter.
type EventSettings struct {
	Retention time.Duration
	Gap       time.Duration
	// Default: the filter of recorders without event-filter= and of unknown senders.
	Default *EventFilter
	// Filters of /event/filter by name.
	Filters map[string]*EventFilter
}

// FilterFor returns the filter named by event-filter= of a recorder ("": the default;
// a name that does not exist, too).
func (s EventSettings) FilterFor(name string) *EventFilter {
	if f := s.Filters[name]; name != "" && f != nil {
		return f
	}
	return s.Default
}

func readEventSettings(it *config.Item, normalise bool) (EventSettings, error) {
	s := EventSettings{Retention: DefaultEventRetention, Gap: eventdb.DefaultGap, Default: builtinFilter(), Filters: map[string]*EventFilter{}}
	if v := it.Val("retention"); v != "" {
		d, err := jobs.ParseLength(v)
		if err != nil || d < time.Hour {
			return s, fmt.Errorf("retention %q: expected a length of at least 1h, e.g. 30d", v)
		}
		s.Retention = d
		if normalise {
			it.Set("retention", jobs.FormatLength(d))
		}
	}
	if v := it.Val("gap"); v != "" {
		d, err := parseSeconds(v)
		if err != nil {
			return s, fmt.Errorf("gap %w", err)
		}
		s.Gap = d
		if normalise {
			it.Set("gap", strconv.FormatFloat(d.Seconds(), 'f', -1, 64)+"s")
		}
	}
	return s, nil
}

// EventConfig reads /event and /event/filter (the defaults when /event is wrong).
func EventConfig(s *config.Store) EventSettings {
	ev := single(s, "/event")
	es, err := readEventSettings(ev, false)
	if err != nil {
		es, _ = readEventSettings(config.NewItem(), false)
	}
	for _, it := range s.Items("/event/filter") {
		es.Filters[it.Val("name")] = readEventFilter(it)
	}
	if f := es.Filters[ev.Val("default-filter")]; f != nil {
		es.Default = f
	}
	return es
}

func (e *eventModule) root() string {
	if e.ctx == nil {
		return "/"
	}
	return e.ctx.Root
}

func (e *eventModule) Verbs() []string  { return []string{"print", "ack"} }
func (e *eventModule) IDs() [][2]string { return nil }

func (e *eventModule) filter(p *config.Item) (eventdb.Filter, error) {
	f := eventdb.Filter{Nvr: p.Val("nvr"), Camera: p.Val("camera"), Kind: p.Val("kind"), Unacked: p.Val("unacked") == "yes",
		Origin: p.Val("origin"), User: p.Val("user"), Text: p.Val("text")}
	var err error
	if v := p.Val("from"); v != "" {
		if f.From, err = parseTime("from", v); err != nil {
			return f, err
		}
	}
	if v := p.Val("to"); v != "" {
		if f.To, err = parseTime("to", v); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (e *eventModule) Run(s *config.Store, c script.Command, p *config.Item) (string, error) {
	db, ok, err := eventdb.OpenIfExists(eventdb.Path(e.root()))
	if err != nil {
		return "", fmt.Errorf("/event/log: %w", err)
	}
	if !ok {
		return "no events yet (the alarm server keeps them: /ip/service alarm-dvrip, alarm-smtp, alarm-lapi, events=subscribe of /nvr and /camera)\n", nil
	}
	defer db.Close()
	f, err := e.filter(p)
	if err != nil {
		return "", fmt.Errorf("/event/log: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch c.Verb {
	case "ack":
		var ids []int64
		for _, a := range c.Args {
			for _, v := range strings.Split(a, ",") {
				id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				if err != nil {
					return "", fmt.Errorf("/event/log ack: %q is not an event number", v)
				}
				ids = append(ids, id)
			}
		}
		if ids == nil && f.Nvr == "" && f.Camera == "" && f.Kind == "" && f.From.IsZero() && f.To.IsZero() && f.Origin == "" && f.User == "" && f.Text == "" {
			return "", errors.New("/event/log ack: give numbers (ack 12,13) or a selection (ack nvr=nvr02, ack kind=VideoLoss)")
		}
		user := ShellUser()
		n, err := db.Ack(ctx, ids, f, user, time.Now())
		if err != nil {
			return "", fmt.Errorf("/event/log ack: %w", err)
		}
		if n > 0 {
			db.Log(ctx, eventdb.Entry{Kind: SysEventsAcked, User: user, Address: ShellAddress(), Text: plural(int(n), "event", "events") + " acknowledged in the shell"})
		}
		return fmt.Sprintf("%d acknowledged\n", n), nil
	default: // print
		if len(c.Args) > 0 {
			return "", fmt.Errorf("/event/log print: unexpected %q", c.Args[0])
		}
		f.Limit = 50
		if n, err := strconv.Atoi(p.Val("count")); err == nil && n > 0 {
			f.Limit = n
		}
		evs, err := db.Query(ctx, f)
		if err != nil {
			return "", fmt.Errorf("/event/log: %w", err)
		}
		total, _ := db.Count(ctx, f)
		var b strings.Builder
		b.WriteString(AlarmStateText(e.root()))
		if p.Val("raw") == "yes" {
			b.WriteString(withRaw(FormatEvents(evs), evs))
		} else {
			b.WriteString(FormatEvents(evs))
		}
		if total > len(evs) {
			fmt.Fprintf(&b, "(the newest %d of %d; count=, from=, to= show others)\n", len(evs), total)
		}
		return b.String(), nil
	}
}

// FormatEvents is the table of print (oldest at the bottom: the newest first).
func FormatEvents(evs []eventdb.Event) string {
	if len(evs) == 0 {
		return "no events\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-19s %-9s %-10s %-14s %-16s %-4s %s\n", "#", "START", "LENGTH", "NVR", "CAMERA", "KIND", "ACK", "TEXT")
	for _, e := range evs {
		length := "going"
		if !e.Open() {
			length = jobs.FormatLength(e.Ended.Sub(e.Started).Round(time.Second))
			if e.Ended.Equal(e.Started) {
				length = "-"
			}
		}
		nvr, cam := e.Nvr, e.Camera
		if nvr == "" {
			nvr = "?"
		}
		if cam == "" && e.Channel > 0 {
			cam = "ch" + strconv.Itoa(e.Channel)
		}
		ack := ""
		if !e.AckedAt.IsZero() {
			ack = "yes"
		}
		text := e.Text
		switch {
		case e.System():
			who := e.User
			if e.Device != "" {
				who += "@" + e.Device
			}
			if who != "" {
				text = who + ": " + text
			}
			if nvr == "?" {
				nvr = "-"
			}
		case e.Nvr == "":
			text = strings.TrimSpace("from " + e.Device + " " + text)
		}
		if e.Count > 1 {
			text = strings.TrimSpace(fmt.Sprintf("x%d %s", e.Count, text))
		}
		if len([]rune(text)) > 60 {
			text = string([]rune(text)[:59]) + "…"
		}
		fmt.Fprintf(&b, "%-6d %-19s %-9s %-10s %-14s %-16s %-4s %s\n", e.ID, e.Started.Format("2006-01-02 15:04:05"), length, nvr, cam, e.Kind, ack, text)
	}
	return b.String()
}

// withRaw puts under every row of the table of FormatEvents the raw notice of its event.
func withRaw(table string, evs []eventdb.Event) string {
	lines := strings.SplitAfter(table, "\n")
	if len(lines) < len(evs)+1 {
		return table
	}
	var b strings.Builder
	b.WriteString(lines[0])
	for i, e := range evs {
		b.WriteString(lines[i+1])
		if raw := strings.TrimSpace(e.Raw); raw != "" {
			b.WriteString("       raw: " + strings.Join(strings.Fields(raw), " ") + "\n")
		}
	}
	for _, l := range lines[len(evs)+1:] {
		b.WriteString(l)
	}
	return b.String()
}

// ---------------------------------------------------------------- the alarm server's state

// AlarmStateFile: what the alarm server is doing (written by it, read by print and the panel).
const AlarmStateFile = "/var/lib/ffnvr/events/state.json"

// AlarmState is the state of the alarm server.
type AlarmState struct {
	Started   time.Time                 `json:"started"`
	Updated   time.Time                 `json:"updated"`
	Listeners map[string]string         `json:"listeners"` // alarm-dvrip: ":15002" or an error
	Subs      map[string]SubscribeState `json:"subscriptions"`
	Unknown   map[string]time.Time      `json:"unknown,omitempty"` // senders that are no recorder: last seen
}

// SubscribeState of one recorder with events=subscribe.
type SubscribeState struct {
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since"`
	Error     string    `json:"error,omitempty"`
	LastEvent time.Time `json:"last_event,omitempty"`
}

// ReadAlarmState (nil: the alarm server never ran).
func ReadAlarmState(root string) *AlarmState {
	var st AlarmState
	if !readJSON(filepath.Join(root, AlarmStateFile), &st) {
		return nil
	}
	return &st
}

// WriteAlarmState is for the alarm server.
func WriteAlarmState(root string, st *AlarmState) error {
	p := filepath.Join(root, AlarmStateFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return writeJSON(p, st)
}

// AlarmStateText: a few lines about the alarm server for print.
func AlarmStateText(root string) string {
	st := ReadAlarmState(root)
	if st == nil {
		return "alarm server: never ran\n"
	}
	var b strings.Builder
	if time.Since(st.Updated) > 3*time.Minute {
		fmt.Fprintf(&b, "alarm server: not running (last seen %s)\n", st.Updated.Format("2006-01-02 15:04:05"))
		return b.String()
	}
	var parts []string
	for _, k := range keysOf(st.Listeners) {
		parts = append(parts, k+" "+st.Listeners[k])
	}
	for _, k := range keysOf(st.Subs) {
		s := st.Subs[k]
		if s.Connected {
			parts = append(parts, k+" subscribed")
		} else {
			parts = append(parts, k+" NOT subscribed: "+s.Error)
		}
	}
	fmt.Fprintf(&b, "alarm server: %s\n", strings.Join(parts, "; "))
	if len(st.Unknown) > 0 {
		fmt.Fprintf(&b, "unknown senders (set event-id= of /nvr): %s\n", strings.Join(keysOf(st.Unknown), ", "))
	}
	return b.String()
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// MarshalIndent for the state file (tests).
func (st *AlarmState) String() string { b, _ := json.MarshalIndent(st, "", "  "); return string(b) }
