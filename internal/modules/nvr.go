package modules

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/jobs"
	"ffnvr/internal/module"
	"ffnvr/internal/script"
)

// nvrModule: /nvr - recorders: local ones record cameras themselves (ffmpeg into a
// /storage), remote ones (dahua, uniview, tiandy, dvrip) record on their own and ffnvr reads from them.
//
//	/nvr
//	add type="local" name="nvr01" storage="disk1"
//	add type="dahua" name="nvr02" host="192.168.1.2" username="admin" password="pass"
//	add type="uniview" name="nvr03" host="192.168.1.3" username="admin" password="pass"
//	add type="tiandy" name="nvr04" host="192.168.1.4" username="Admin" password="pass"
//	add type="dvrip" name="nvr05" host="192.168.1.5" username="admin" password=""
type nvrModule struct {
	ctx *module.Context
}

func (n *nvrModule) Name() string           { return "nvr" }
func (n *nvrModule) Bind(c *module.Context) { n.ctx = c }

func (n *nvrModule) Menus() []*config.Menu {
	// filters and recording schedules first: an exported script defines them before the recorders naming them
	return []*config.Menu{eventFilterMenu(), recordDayMenu(), recordScheduleMenu(recordCommands(n.root)), {
		Path:    "/nvr",
		Kind:    config.Multi,
		NameKey: "name",
		Help:    "recorders",
		Fields: []config.Field{
			{Name: "type", Type: config.TEnum, Enum: []string{"local", "dahua", "uniview", "tiandy", "dvrip"}, Required: true,
				Help: "local: records to a /storage; dahua, uniview: a recorder read over its HTTP API"},
			{Name: "name", Help: "required"},
			{Name: "storage", Ref: "/storage:name", Strict: true, Help: "type=local: where it records"},
			{Name: "retention", Help: "type=local: how long recordings are kept, e.g. 30d (the worker deletes older ones; empty: forever)"},
			{Name: "record-schedule", Ref: "/record/schedule:name", Strict: true, Help: "type=local: recording schedule of all its cameras (/record/schedule); record-schedule= of /camera wins; empty: recorded all the time"},
			{Name: "host", Help: "remote: address, e.g. 192.168.1.2, 192.168.1.2:8080, https://nvr.lan (tiandy: port 3001, dvrip: 34567)"},
			{Name: "username", Help: "remote"},
			{Name: "password", Secret: true, Help: "remote"},
			{Name: "auto-add", Type: config.TBool, Help: "remote: cameras appearing on the recorder are added by the worker (default yes; /nvr scan adds by hand)"},
			{Name: "http-port", Type: config.TInt, Help: "type=tiandy: port of the web interface (ISAPI: disks, camera addresses); default 80"},
			{Name: "api", Type: config.TEnum, Enum: []string{"rpc", "cgi"}, Help: "type=dahua: rpc (JSON-RPC of the web interface, default) or cgi (older firmware)"},
			{Name: "speed", Type: config.TInt, Help: "type=uniview: ask for downloads and the player's pieces this many times faster than real time (RTSP Speed), e.g. 8; the player asks for 8 when unset, 1: real time"},
			{Name: "fps", Type: config.TInt, Help: "type=tiandy: frame rate of downloads (default: from the stream, else 20)"},
			{Name: "events", Type: config.TEnum, Enum: []string{"no", "subscribe"},
				Help: "subscribe: the alarm server connects to the recorder for its events (dahua, dvrip, tiandy: its own protocol, port of host=; uniview: LAPI, it pushes its events to alarm-lapi of /ip/service, which has to be enabled); default no. Events the recorder sends itself (/ip/service alarm-dvrip, alarm-smtp) are taken anyway; cameras of a local recorder: events= of /camera"},
			{Name: "event-filter", Ref: "/event/filter:name", Strict: true,
				Help: "which kinds of its events are kept (/event/filter); unset: default-filter= of /event"},
			{Name: "event-id", Help: "how the recorder introduces itself to the alarm server when its address does not tell (NAT): the serial number (dvrip) or the SMTP login or sender address (e-mail)"},
			{Name: "valid-users", Type: config.TList, Ref: "/user:user,@/user/group:group", Strict: true,
				Help: "who may use it in the web panel: users and @groups, e.g. maciej,@firma2"},
			{Name: "disabled", Type: config.TBool},
			{Name: "comment"},
		},
		Validate: validateNvr,
		Commands: map[string]config.Command{
			"scan": {Help: "read cameras and state of a remote recorder, add the missing cameras",
				Run: n.runScan},
			"probe": {Help: "show the raw answers of a remote recorder (diagnostics)",
				Run: func(_ *config.Store, _ script.Command, items []*config.Item) (string, error) {
					var b strings.Builder
					for _, it := range items {
						if !remote(it) {
							return "", fmt.Errorf("/nvr: %s is type=%s, only remote recorders can be probed", it.Val("name"), it.Val("type"))
						}
						b.WriteString(probeRecorder(it))
					}
					return b.String(), nil
				}},
		},
	}}
}

// per type: the fields that must be set, and those that do not belong to it
var nvrRequired = map[string][]string{
	"local":   {"name", "storage"},
	"dahua":   {"name", "host", "username", "password"},
	"uniview": {"name", "host", "username", "password"},
	"tiandy":  {"name", "host", "username", "password"},
	"dvrip":   {"name", "host", "username"}, // XMeye recorders often have an empty password
}

var nvrOnly = map[string][]string{
	"local":   {"host", "username", "password", "speed", "fps", "api", "http-port", "events", "event-id", "event-filter"},
	"dahua":   {"storage", "retention", "record-schedule", "speed", "fps", "http-port"},
	"uniview": {"storage", "retention", "record-schedule", "fps", "api", "http-port"},
	"tiandy":  {"storage", "retention", "record-schedule", "speed", "api"},
	"dvrip":   {"storage", "retention", "record-schedule", "speed", "fps", "api", "http-port"},
}

func validateNvr(s *config.Store, it *config.Item) error {
	typ := it.Val("type")
	var missing []string
	for _, k := range nvrRequired[typ] {
		if it.Val(k) == "" {
			missing = append(missing, k)
		}
	}
	switch len(missing) {
	case 0:
	case 1:
		return fmt.Errorf("type=%s needs parameter %s", typ, missing[0])
	default:
		return fmt.Errorf("type=%s needs parameters %s", typ, strings.Join(missing, ", "))
	}
	for _, k := range nvrOnly[typ] {
		if it.Val(k) != "" {
			return fmt.Errorf("%s is not used by type=%s", k, typ)
		}
	}
	if s := it.Val("speed"); s != "" {
		if n, _ := strconv.Atoi(s); n < 1 || n > 64 {
			return fmt.Errorf("speed must be 1..64")
		}
	}
	if r := it.Val("retention"); r != "" {
		d, err := jobs.ParseLength(r)
		if err != nil || d < time.Hour {
			return fmt.Errorf("retention %q: expected a length of at least 1h, e.g. 30d", r)
		}
		it.Set("retention", jobs.FormatLength(d))
	}
	if s := it.Val("fps"); s != "" {
		if n, _ := strconv.Atoi(s); n < 1 || n > 120 {
			return fmt.Errorf("fps must be 1..120")
		}
	}
	if it.Val("events") == "subscribe" && !NvrSubscribeTypes[typ] {
		return fmt.Errorf("events=subscribe: type=%s cannot be subscribed to (dahua, dvrip, tiandy, uniview); its e-mails reach /ip/service alarm-smtp", typ)
	}
	if id := strings.ToLower(it.Val("event-id")); id != "" {
		if strings.ContainsAny(id, " \t\"'") {
			return fmt.Errorf("event-id %q: no spaces or quotes", id)
		}
		for _, o := range s.Items("/nvr") {
			if o.Val("name") != it.Val("name") && strings.ToLower(o.Val("event-id")) == id {
				return fmt.Errorf("event-id %q is already the one of %s", id, o.Val("name"))
			}
		}
	}
	if h := it.Val("host"); strings.ContainsAny(h, " \t\"'") {
		return fmt.Errorf("host %q: no spaces or quotes", h)
	}
	return checkName("name", it.Val("name"))
}

// NvrSubscribeTypes: the recorders events=subscribe of /nvr is for (a local one: its
// cameras, events= of /camera).
var NvrSubscribeTypes = map[string]bool{"dahua": true, "dvrip": true, "tiandy": true, "uniview": true}

// remote reports whether a recorder keeps the recordings itself.
func remote(nvr *config.Item) bool { return nvr != nil && nvr.Val("type") != "local" }

func (n *nvrModule) runScan(s *config.Store, _ script.Command, items []*config.Item) (string, error) {
	var b strings.Builder
	for _, it := range items {
		if !remote(it) {
			return b.String(), fmt.Errorf("/nvr: %s is type=%s, only remote recorders can be scanned", it.Val("name"), it.Val("type"))
		}
		out, err := scanRecorder(s, n.root(), it)
		b.WriteString(out)
		if err != nil {
			return b.String(), err
		}
	}
	return b.String(), nil
}

func (n *nvrModule) root() string {
	if n.ctx == nil {
		return "/"
	}
	return n.ctx.Root
}

// AfterCommand: a remote recorder that was just added is scanned for its cameras.
// When that fails the recorder stays, "/nvr scan <name>" tries again.
func (n *nvrModule) AfterCommand(c *module.Context, cmd script.Command) string {
	if cmd.Verb != "add" || c.Store.Canon(cmd.Path) != "/nvr" {
		return ""
	}
	items := c.Store.Items("/nvr")
	if len(items) == 0 || !remote(items[len(items)-1]) {
		return ""
	}
	it := items[len(items)-1]
	out, err := scanRecorder(c.Store, c.Root, it)
	if err != nil {
		out += fmt.Sprintf("warning: %v\n  the recorder is added; check host/username/password and run: /nvr scan %s\n", err, it.Val("name"))
	}
	return out
}

// Describe: the state of remote recorders, as last read (see RefreshStatus).
func (n *nvrModule) Describe(c *module.Context, menu string) []string {
	if menu != "/nvr" {
		return nil
	}
	var out []string
	for _, it := range c.Store.Items("/nvr") {
		line := ""
		if remote(it) {
			line = describeStatus(readStatus(c.Root, it.Val("name")))
		}
		out = append(out, line)
	}
	return out
}
