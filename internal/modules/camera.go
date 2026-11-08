package modules

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	"ffnvr/internal/config"
	"ffnvr/internal/module"
	"ffnvr/internal/units"
)

// cameraModule: /camera - recording of one stream; every enabled camera of an active
// recorder becomes a systemd service running ffmpeg.
//
//	/camera
//	add name="cam02" nvr="nvr01" stream="rtsp://192.168.0.59:554/live0.264"
type cameraModule struct{}

func (cameraModule) Name() string { return "camera" }

func (cameraModule) Menus() []*config.Menu {
	return []*config.Menu{profileMenu(), { // profiles first: an exported script defines them before the cameras naming them
		Path:      "/camera",
		Kind:      config.Multi,
		NameKey:   "name",
		NameScope: "nvr", // the same name may be used on another recorder
		AliasKey:  "alias",
		Help:      "cameras recorded by an nvr",
		Fields: []config.Field{
			{Name: "name", Required: true, Help: "unique per nvr; recordings and the service use it"},
			{Name: "alias", Help: "optional friendly name, unique per nvr (also among names)"},
			{Name: "nvr", Required: true, Ref: "/nvr:name", Strict: true},
			{Name: "stream", Help: "rtsp://host:port/path (required on a type=local nvr); engine=go2rtc: any go2rtc source, e.g. dvrip://user:pass@host?channel=0&subtype=0"},
			{Name: "stream-lq", Help: "the low quality (sub) stream, for live view (rtsp://, with engine=go2rtc any go2rtc source); empty: made from stream= when it is a Uniview, Tiandy, Dahua, Hikvision, XMeye or dvrip:// address; none: no LQ"},
			{Name: "engine", Type: config.TEnum, Enum: CameraEngines, Help: "type=local: who connects to the camera - internal (ffmpeg, rtsp:// only; the default) or go2rtc (ffmpeg records go2rtc's copy on 127.0.0.1, the live view shares the connection)"},
			{Name: "channel", Type: config.TInt, Help: "channel on a remote recorder (required there); type=local: <ch> of the address scheme"},
			{Name: "profile", Ref: "/camera/profile:name", Strict: true, Help: "type=local: shared settings (/camera/profile); the camera's own values win"},
			{Name: "host", Help: "type=local: <host> of the address scheme, e.g. 192.168.1.2 or 192.168.1.2:8554"},
			{Name: "username", Help: "type=local: login of the camera (overrides the profile's)"},
			{Name: "password", Secret: true, KeepEmpty: true, Help: `type=local: password of the camera (overrides the profile's; "" overrides with none)`},
			{Name: "group", Help: "free text, shown in the service description"},
			{Name: "enabled", Type: config.TBool, Help: "default yes"},
			{Name: "comment"},
			{Name: "type", Type: config.TEnum, Enum: CameraTypes, Help: "a camera of a local recorder: its make, the protocol it speaks - dahua, dvrip (XMeye), hikvision, tiandy, uniview (also from a profile); a camera of a remote recorder takes its recorder's type"},
			{Name: "ptz", Type: config.TBool, Help: "PTZ control of the camera: a camera of a local recorder is steered by its type= (default no, also from a profile); one of a remote recorder through the recorder (default: yes when the recorder can)"},
			{Name: "ptz-host", Help: "a camera of a local recorder: address of its protocol for the PTZ control and events=subscribe, may carry a port (default: host=, else the host of the stream address, with the protocol's own port)"},
			{Name: "ptz-channel", Type: config.TInt, Help: "a camera of a local recorder: channel of the PTZ control (default: channel=, else 1 - the camera itself)"},
			{Name: "ptz-swap-horizontal", Type: config.TBool, Help: "PTZ control: left and right swapped (a camera mounted upside down or mirrored)"},
			{Name: "ptz-swap-vertical", Type: config.TBool, Help: "PTZ control: up and down swapped"},
			{Name: "events", Type: config.TEnum, Enum: []string{"no", "subscribe"},
				Help: "a camera of a local recorder: subscribe - the alarm server connects to the camera for its events (type=tiandy: its own protocol, port 3001 or the one of ptz-host=; type=dahua: eventManager over HTTP, port 80 or the one of ptz-host=; type=hikvision: ISAPI alertStream over HTTP, port 80 or the one of ptz-host=, 443 is HTTPS; type=uniview: LAPI over HTTP, port 80 or the one of ptz-host=, the camera pushes its events to alarm-lapi of /ip/service, which has to be enabled); default no"},
			{Name: "record-schedule", Ref: "/record/schedule:name", Strict: true,
				Help: "a camera of a local recorder: its recording schedule (/record/schedule), over the one of its recorder"},
			{Name: "event-filter", Ref: "/event/filter:name", Strict: true,
				Help: "a camera of a local recorder: which kinds of its events are kept (/event/filter) - subscribed to (events=subscribe) or pushed by it (alarm-dvrip, alarm-smtp: known by its address), e.g. kinds=*/ALARM/* for Tiandy, kinds=VideoMotion,VideoBlind for Dahua, kinds=VMD,tamperdetection for Hikvision, kinds=MotionAlarm,MaskImageAlarm for Uniview (a Uniview camera without a filter keeps only its alarms, no pushes of data such as people counting; kinds=* keeps them too); unset: default-filter= of /event"},
		},
		Validate: validateCamera,
		Detail:   cameraDetail,
	}}
}

func validateCamera(s *config.Store, it *config.Item) error {
	nvr := find(s, "/nvr", "name", it.Val("nvr"))
	if remote(nvr) {
		if it.Val("channel") == "" {
			return fmt.Errorf("channel is required for a camera of a type=%s nvr", nvr.Val("type"))
		}
		for _, k := range []string{"engine", "profile", "host", "username", "password", "type", "ptz-host", "ptz-channel", "events", "event-filter", "record-schedule"} {
			if _, ok := it.Get(k); ok {
				return fmt.Errorf("%s is only for cameras of a type=local nvr (a %s recorder gives the addresses and its login is on /nvr)", k, nvr.Val("type"))
			}
		}
		for _, key := range []string{"stream", "stream-lq"} {
			if key == "stream-lq" && it.Val(key) == NoLQ {
				continue
			}
			if err := checkStream(key, it.Val(key), false); err != nil {
				return err
			}
		}
	} else if err := ResolveCamera(s, it).check(); err != nil {
		return err
	} else if err := checkLocalPTZ(s, it); err != nil {
		return err
	} else if err := checkCameraEvents(s, it); err != nil {
		return err
	}
	if n, err := strconv.Atoi(it.Val("channel")); it.Val("channel") != "" && (err != nil || n < 1) {
		return fmt.Errorf("channel must be 1 or more")
	}
	if err := checkName("name", it.Val("name")); err != nil {
		return err
	}
	if err := checkName("alias", it.Val("alias")); err != nil {
		return err
	}
	for _, r := range it.Val("group") + it.Val("username") + it.Val("password") {
		if unicode.IsControl(r) {
			return fmt.Errorf("group, username, password: control characters are not allowed")
		}
	}
	if it.Val("enabled") == "" {
		it.Set("enabled", "yes")
	}
	return nil
}

// checkLocalPTZ: ptz-host= needs ptz=yes or events=subscribe (it is the address of the
// camera's protocol, which both use), ptz-channel= needs ptz=yes, and ptz=yes needs
// type= and an address.
func checkLocalPTZ(s *config.Store, it *config.Item) error {
	if h := it.Val("ptz-host"); strings.ContainsAny(h, " \t\"'/") {
		return fmt.Errorf("ptz-host %q: a host, optionally with a port", h)
	}
	src := ResolveCamera(s, it)
	if !src.PTZ {
		if it.Val("ptz-host") != "" && it.Val("events") != "subscribe" {
			return fmt.Errorf("ptz-host is used with ptz=yes or events=subscribe")
		}
		if it.Val("ptz-channel") != "" {
			return fmt.Errorf("ptz-channel is used with ptz=yes")
		}
		return nil
	}
	if src.Type == "" {
		return fmt.Errorf("ptz=yes needs type= (dahua, dvrip, tiandy, uniview): the protocol of the camera")
	}
	if _, ok := PTZCapabilities(src.Type); !ok {
		return fmt.Errorf("ptz=yes: cameras of type=%s have no PTZ control yet (dahua, dvrip, tiandy, uniview)", src.Type)
	}
	if _, _, err := localPTZ(s, it); err != nil {
		return errors.New(strings.TrimPrefix(err.Error(), ErrPTZ.Error()+": "))
	}
	return nil
}

// CameraSubscribeTypes: the makes of cameras of local recorders whose events can be
// subscribed to (events=subscribe of /camera).
var CameraSubscribeTypes = map[string]bool{"dahua": true, "hikvision": true, "tiandy": true, "uniview": true}

// checkCameraEvents: events=subscribe needs a make that can be subscribed to and an
// address. (event-filter= is for the events pushed by the camera too.)
func checkCameraEvents(s *config.Store, it *config.Item) error {
	if it.Val("events") != "subscribe" {
		return nil
	}
	src := ResolveCamera(s, it)
	if !CameraSubscribeTypes[src.Type] {
		if src.Type == "" {
			return fmt.Errorf("events=subscribe needs type= (dahua, hikvision, tiandy, uniview): the protocol of the camera")
		}
		return fmt.Errorf("events=subscribe: cameras of type=%s cannot be subscribed to yet (dahua, hikvision, tiandy, uniview)", src.Type)
	}
	if host, _, _ := cameraDevice(it, src); host == "" {
		return fmt.Errorf("events=subscribe needs the address of the camera: host=, ptz-host= or a stream address")
	}
	return nil
}

// CameraEvents: a camera of a local recorder, as the alarm server knows it: subscribed
// to (events=subscribe of /camera) or sending its events itself.
type CameraEvents struct {
	Nvr, Camera        string
	Type               string // its make, the protocol
	Host               string // may carry a port; without one the protocol's own
	Username, Password string
	Filter             string // event-filter= ("": default-filter= of /event)
	Subscribe          bool   // events=subscribe and the make can be subscribed to
}

// SubscribedCameras: the enabled cameras of the active local recorders with
// events=subscribe, in the order of the configuration.
func SubscribedCameras(s *config.Store) []CameraEvents {
	var out []CameraEvents
	for _, c := range LocalCameras(s) {
		if c.Subscribe {
			out = append(out, c)
		}
	}
	return out
}

// LocalCameras: the enabled cameras of the active local recorders with an address (the
// one of their protocol: ptz-host=, host=, the host of the stream address), in the order
// of the configuration - the senders of events the alarm server knows them by.
func LocalCameras(s *config.Store) []CameraEvents {
	nvrs := map[string]*config.Item{}
	for _, n := range s.Items("/nvr") {
		nvrs[n.Val("name")] = n
	}
	var out []CameraEvents
	for _, cam := range s.Items("/camera") {
		n := nvrs[cam.Val("nvr")]
		if cam.Val("enabled") == "no" || n == nil || n.Val("disabled") == "yes" || remote(n) {
			continue
		}
		src := ResolveCamera(s, cam)
		host, user, pass := cameraDevice(cam, src)
		if host == "" {
			continue
		}
		out = append(out, CameraEvents{Nvr: n.Val("name"), Camera: cam.Val("name"), Type: src.Type, Host: host,
			Username: user, Password: pass, Filter: cam.Val("event-filter"),
			Subscribe: cam.Val("events") == "subscribe" && CameraSubscribeTypes[src.Type]})
	}
	return out
}

var unitTemplate = template.Must(template.New("camera").Parse(`[Unit]
Description=NVR: {{.Nvr}}, Group: {{.Group}}, Camera: {{.Camera}}
After=network-online.target
Wants=network-online.target
{{- if .Go2rtc}}
After={{.Go2rtc}}
Wants={{.Go2rtc}}
{{- end}}

[Service]
Type=simple
User={{.User}}
MemoryAccounting=yes
{{- if .MemoryMax}}
MemoryMax={{.MemoryMax}}
{{- end}}
{{- range .Pre}}
ExecStartPre={{.}}
{{- end}}
ExecStart=/usr/bin/ffmpeg -use_wallclock_as_timestamps 1 -analyzeduration 1000000 -probesize 1000000 -fflags +igndts -rtsp_transport tcp -timeout 10000000 -loglevel warning -i {{.Stream}} -an -vcodec copy -strict -2 -movflags +faststart+frag_keyframe+empty_moov -f segment -segment_atclocktime 1 -reset_timestamps 1 -strftime 1 -segment_time 900 {{.RecordDir}}/%%Y-%%m-%%dT%%H-%%M-%%S.mkv
Restart=always
RestartSec={{.RestartSec}}
{{- range .Hardening}}
{{.}}
{{- end}}

[Install]
WantedBy=multi-user.target
`))

// unitData is what the unit template sees; every value is already escaped for systemd.
type unitData struct {
	Nvr, Group, Camera, Stream string
	Go2rtc                     string // engine=go2rtc: the unit of go2rtc, which the stream comes from
	RestartSec                 int
	RecordDir                  string // recordings: <storage path>/<nvr>/<camera>
	// confinement of /system/systemd
	User, MemoryMax string
	Pre, Hardening  []string // ExecStartPre= commands; directives after RestartSec=
}

// systemdEscape protects specifiers (%) and variable expansion ($) in unit file values.
func systemdEscape(s string) string {
	return strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
}

// UnitName is the service of a camera.
func UnitName(nvr, camera string) string {
	return units.Prefix + nvr + "-" + camera + units.Suffix
}

func (cameraModule) Units(c *module.Context) ([]units.Unit, error) {
	nvrs := map[string]*config.Item{}
	for _, n := range c.Store.Items("/nvr") {
		nvrs[n.Val("name")] = n
	}
	storages := map[string]string{}
	for _, s := range c.Store.Items("/storage") {
		storages[s.Val("name")] = s.Val("path")
	}
	h := SystemdHardening(c.Store)
	var out []units.Unit
	for _, cam := range c.Store.Items("/camera") {
		n := nvrs[cam.Val("nvr")]
		if cam.Val("enabled") == "no" || n == nil || n.Val("disabled") == "yes" || remote(n) {
			continue // a remote recorder records its cameras itself
		}
		storage, ok := storages[n.Val("storage")]
		if !ok {
			return nil, fmt.Errorf("camera %s: nvr %s has no storage", cam.Val("name"), n.Val("name"))
		}
		nvr, name := n.Val("name"), cam.Val("name")
		group := cam.Val("group")
		if group == "" {
			group = "-"
		}
		src := ResolveCamera(c.Store, cam)
		hq, _, err := src.Addresses()
		if err != nil {
			return nil, fmt.Errorf("camera %s: %w", name, err)
		}
		d := unitData{
			Nvr: nvr, Camera: name, Group: systemdEscape(group), Stream: systemdEscape(hq),
			RecordDir: path.Join(storage, nvr, name), RestartSec: 10,
			User: h.User, MemoryMax: h.MemoryMax,
		}
		d.Pre, d.Hardening = h.PreCommands(d.RecordDir), h.Directives(d.RecordDir)
		if src.Engine == EngineGo2rtc {
			// go2rtc holds the connection to the camera; ffmpeg records its copy. go2rtc
			// comes back within a second or two after a restart (a changed camera), so
			// the recording does not wait the usual 10 s
			d.Stream = Go2rtcRecordURL(c.Store, nvr, name)
			d.Go2rtc, d.RestartSec = Go2rtcUnit, 2
		}
		var b strings.Builder
		if err := unitTemplate.Execute(&b, d); err != nil {
			return nil, err
		}
		out = append(out, units.Unit{Name: UnitName(nvr, name), Content: b.String()})
	}
	if c.Live && len(out) > 0 {
		if err := h.CheckServiceUser(c.Root); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CameraEngines: who connects to a camera of a local recorder.
var CameraEngines = []string{EngineInternal, EngineGo2rtc}

const (
	EngineInternal = "internal" // ffmpeg itself (rtsp://)
	EngineGo2rtc   = "go2rtc"   // go2rtc; ffmpeg records its RTSP copy on 127.0.0.1
)

// go2rtcSchemes: the sources accepted with engine=go2rtc - network sources only; the
// ones that run programs (exec:, ffmpeg:, echo:, expr:) are not.
var go2rtcSchemes = map[string]bool{"rtsp": true, "rtsps": true, "rtmp": true, "rtmps": true, "http": true, "https": true,
	"dvrip": true, "onvif": true, "tapo": true, "bubble": true, "isapi": true}

func checkStream(key, stream string, go2rtc bool) error {
	for _, r := range stream {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`"'\\`, r) {
			return fmt.Errorf("%s %q: spaces, quotes and backslashes are not allowed (use %%20 etc.)", key, stream)
		}
	}
	if stream == "" {
		return nil
	}
	u, err := url.Parse(stream)
	if go2rtc {
		if err != nil || !go2rtcSchemes[u.Scheme] || u.Host == "" {
			return fmt.Errorf("%s %q: expected a network source of go2rtc: %s://host...", key, stream, strings.Join(sortedKeys(go2rtcSchemes), ", "))
		}
		return nil
	}
	if err != nil || (u.Scheme != "rtsp" && u.Scheme != "rtsps") || u.Host == "" {
		if err == nil && go2rtcSchemes[u.Scheme] && u.Host != "" {
			return fmt.Errorf("%s %q: expected rtsp://host[:port]/path (%s:// needs engine=go2rtc)", key, stream, u.Scheme)
		}
		return fmt.Errorf("%s %q: expected rtsp://host[:port]/path", key, stream)
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
