package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"ffnvr/internal/config"
	"ffnvr/internal/module"
	"ffnvr/internal/units"
)

// liveModule: /system/live - live view through go2rtc, which ffnvr configures and
// runs on localhost only (ffnvr-go2rtc.service); the web panel is its reverse proxy
// (MSE over WebSocket, HLS) for signed in users.
//
//	/system/live
//	set enabled=yes go2rtc=/usr/local/bin/go2rtc port=1984
//
// Every enabled camera gets two streams: <nvr>/<camera>/hq (its stream=) and
// <nvr>/<camera>/lq (stream-lq=). Addresses of remote recorders come from their API
// (scan), with the recorder's username and password added.
//
// Cameras of a local recorder with engine=go2rtc are recorded through go2rtc too: it
// holds the one connection to the camera (any of its sources, e.g. dvrip://) and
// ffmpeg records its RTSP copy, rtsp://127.0.0.1:<rtsp-port>/<nvr>/<camera>/hq. go2rtc
// then runs even with the live view off, and its RTSP server listens on 127.0.0.1
// only while such a camera exists.
//
// Transcoding: a browser that does not decode H.265 (e.g. Firefox, or Chrome without
// hardware support) cannot play the streams of an H.265 camera - HQ, and on some
// cameras LQ too. With transcode=yes every stream of a camera gets an H.264 twin,
// <nvr>/<camera>/tc-hq and tc-lq: go2rtc's ffmpeg turns the stream into H.264 (HQ scaled
// to transcode-height, LQ to transcode-lq-height or kept as it is). go2rtc starts it
// only while somebody watches it, so it costs nothing until a browser asks for it; the
// panel lets only the users and @groups of transcode-users have it, and at most
// transcode-max HQ and transcode-lq-max LQ such streams at once (viewers of the same
// stream share one; 0 turns that quality off). The twin reads its stream from go2rtc's
// RTSP server on 127.0.0.1, which then runs.
//
//	/system/live
//	set transcode=yes transcode-users="jan,@firma1" transcode-height=720 transcode-max=2 transcode-lq-max=4
type liveModule struct{}

func (liveModule) Name() string { return "live" }

// GoConfig is where the go2rtc configuration is written.
const GoConfig = "/etc/ffnvr/go2rtc.yaml"

func (liveModule) Menus() []*config.Menu {
	return []*config.Menu{{
		Path: "/system/live",
		Kind: config.Single,
		Help: "live view (go2rtc on localhost, proxied by the web panel)",
		Fields: []config.Field{
			{Name: "enabled", Type: config.TBool, Help: "run go2rtc (default no)"},
			{Name: "go2rtc", Help: "the go2rtc binary (default /usr/local/bin/go2rtc)"},
			{Name: "port", Type: config.TInt, Help: "its API port on 127.0.0.1 (default 1984)"},
			{Name: "rtsp-port", Type: config.TInt, Help: "its RTSP port on 127.0.0.1, for cameras recorded with engine=go2rtc (default 8554)"},
			{Name: "map-tiles", Help: "tiles of the map, e.g. https://tile.openstreetmap.org/{z}/{x}/{y}.png (the default)"},
			{Name: "transcode", Type: config.TBool, Help: "H.264 for browsers without H.265 (HQ and LQ), transcoded by go2rtc's ffmpeg on demand (default no)"},
			{Name: "transcode-users", Type: config.TList, Ref: "/user:user,@/user/group:group", Strict: true,
				Help: "who may have it: users and @groups (as valid-users of /nvr); empty: nobody"},
			{Name: "transcode-height", Type: config.TInt, Help: fmt.Sprintf("the height of transcoded HQ, %d..%d (default %d)", TranscodeMinHeight, TranscodeMaxHeight, DefaultTranscodeHeight)},
			{Name: "transcode-max", Type: config.TInt, Help: fmt.Sprintf("the most transcoded HQ streams at once, 0..%d (default %d; viewers of one stream share it; 0: HQ is not transcoded)", TranscodeMaxStreams, DefaultTranscodeMax)},
			{Name: "transcode-lq-height", Type: config.TInt, Help: fmt.Sprintf("the height of transcoded LQ, %d..%d, or 0: its own size (the default)", TranscodeMinHeight, TranscodeMaxHeight)},
			{Name: "transcode-lq-max", Type: config.TInt, Help: fmt.Sprintf("the most transcoded LQ streams at once, 0..%d (default %d; 0: LQ is not transcoded)", TranscodeMaxStreams, DefaultTranscodeLQMax)},
			{Name: "transcode-hardware", Type: config.TEnum, Enum: TranscodeHardware, Help: "the encoder: none (software, libx264; the default), auto, or vaapi (Intel/AMD), cuda (NVIDIA), v4l2m2m, rkmpp"},
		},
		Validate: func(_ *config.Store, it *config.Item) error {
			if p := it.Val("go2rtc"); p != "" && (!strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \t\"'%$")) {
				return fmt.Errorf("go2rtc %q: an absolute path without spaces", p)
			}
			if m := it.Val("map-tiles"); m != "" && (!strings.HasPrefix(m, "https://") || !strings.Contains(m, "{z}") || strings.ContainsAny(m, " \"'<>")) {
				return fmt.Errorf("map-tiles %q: an https:// address with {z}, {x} and {y}", m)
			}
			for _, k := range []string{"port", "rtsp-port"} {
				if p := it.Val(k); p != "" {
					if n, _ := strconv.Atoi(p); n < 1 || n > 65535 {
						return fmt.Errorf("%s must be 1..65535", k)
					}
				}
			}
			for _, k := range []string{"transcode-height", "transcode-lq-height"} {
				h := it.Val(k)
				if h == "" || h == "0" && k == "transcode-lq-height" {
					continue
				}
				if n, _ := strconv.Atoi(h); n < TranscodeMinHeight || n > TranscodeMaxHeight || n%2 != 0 {
					return fmt.Errorf("%s must be an even number %d..%d", k, TranscodeMinHeight, TranscodeMaxHeight)
				}
			}
			for _, k := range []string{"transcode-max", "transcode-lq-max"} {
				if m := it.Val(k); m != "" {
					if n, err := strconv.Atoi(m); err != nil || n < 0 || n > TranscodeMaxStreams {
						return fmt.Errorf("%s must be 0..%d", k, TranscodeMaxStreams)
					}
				}
			}
			if l := liveOf(it); l.Port == l.RTSPPort {
				return fmt.Errorf("port and rtsp-port must differ (both %d)", l.Port)
			}
			return nil
		},
	}, snapshotMenu()}
}

// Live are the settings of the live view.
type Live struct {
	Enabled  bool
	Binary   string
	Port     int
	RTSPPort int // for the cameras recorded with engine=go2rtc
	MapTiles string
	// Transcode: H.264 for browsers without H.265 (see liveModule)
	Transcode Transcode
}

// Transcode are the settings of the transcoded streams.
type Transcode struct {
	Enabled  bool
	Users    []string // users and @groups
	HQ, LQ   TranscodeQuality
	Hardware string // "" (software) or a hardware engine of go2rtc ("auto": its detection)
}

// TranscodeQuality: the transcoding of one quality (hq, lq).
type TranscodeQuality struct {
	Height int // 0: the stream's own size
	Max    int // the most such streams at once; 0: not transcoded
}

// Of returns the settings of a quality (hq or lq).
func (t Transcode) Of(quality string) TranscodeQuality {
	if quality == "lq" {
		return t.LQ
	}
	return t.HQ
}

// Limits and defaults of transcoding.
const (
	DefaultTranscodeHeight = 720
	TranscodeMinHeight     = 144
	TranscodeMaxHeight     = 2160
	DefaultTranscodeMax    = 2
	DefaultTranscodeLQMax  = 4
	TranscodeMaxStreams    = 32
)

// TranscodeHardware: the values of transcode-hardware (go2rtc's #hardware engines).
var TranscodeHardware = []string{"none", "auto", "vaapi", "cuda", "v4l2m2m", "rkmpp"}

// DefaultMapTiles: OpenStreetMap.
const DefaultMapTiles = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"

// LiveSettings reads /system/live.
func LiveSettings(s *config.Store) Live { return liveOf(single(s, "/system/live")) }

func liveOf(it *config.Item) Live {
	l := Live{Enabled: it.Val("enabled") == "yes", Binary: or(it.Val("go2rtc"), "/usr/local/bin/go2rtc"), Port: 1984, RTSPPort: 8554,
		MapTiles: or(it.Val("map-tiles"), DefaultMapTiles)}
	if p, err := strconv.Atoi(it.Val("port")); err == nil {
		l.Port = p
	}
	if p, err := strconv.Atoi(it.Val("rtsp-port")); err == nil {
		l.RTSPPort = p
	}
	l.Transcode = Transcode{Enabled: it.Val("transcode") == "yes", Users: splitList(it.Val("transcode-users")),
		HQ: TranscodeQuality{Height: DefaultTranscodeHeight, Max: DefaultTranscodeMax}, LQ: TranscodeQuality{Max: DefaultTranscodeLQMax}}
	for k, v := range map[string]*int{"transcode-height": &l.Transcode.HQ.Height, "transcode-max": &l.Transcode.HQ.Max,
		"transcode-lq-height": &l.Transcode.LQ.Height, "transcode-lq-max": &l.Transcode.LQ.Max} {
		if n, err := strconv.Atoi(it.Val(k)); err == nil {
			*v = n
		}
	}
	if hw := it.Val("transcode-hardware"); hw != "" && hw != "none" {
		l.Transcode.Hardware = hw
	}
	return l
}

// Transcoding: the live view is on and so is transcode=.
func (l Live) Transcoding() bool { return l.Enabled && l.Transcode.Enabled }

// TranscodeName is the go2rtc stream of a camera's quality (hq, lq) transcoded to H.264.
func TranscodeName(nvr, camera, quality string) string { return LiveName(nvr, camera, "tc-"+quality) }

// transcodeSource is the go2rtc source of a transcoded stream: its ffmpeg reads the
// stream (by name, from go2rtc's own RTSP server) and makes H.264 of it, the video only
// (the live view is muted), scaled to the height (the width keeps the aspect, even) or
// of its own size (height 0).
func (l Live) transcodeSource(stream string, height int) string {
	src := "ffmpeg:" + stream + "#video=h264"
	if height > 0 {
		src += "#width=-2#height=" + strconv.Itoa(height)
	}
	switch l.Transcode.Hardware {
	case "":
	case "auto":
		src += "#hardware"
	default:
		src += "#hardware=" + l.Transcode.Hardware
	}
	return src
}

// CanTranscode tells whether a user may watch transcoded streams: transcoding is on and
// transcode-users names the user or one of the user's groups (@group).
func CanTranscode(s *config.Store, user string) bool {
	l := LiveSettings(s)
	if !l.Transcoding() {
		return false
	}
	u := find(s, "/user", "user", user)
	if u == nil || u.Val("disabled") == "yes" {
		return false
	}
	return listsUser(l.Transcode.Users, user, splitList(u.Val("groups")))
}

// listsUser: a list of users and @groups (valid-users, transcode-users) names the user or
// one of the groups.
func listsUser(list []string, user string, groups []string) bool {
	for _, v := range list {
		if v == user {
			return true
		}
		if len(v) > 1 && v[0] == '@' && slices.Contains(groups, v[1:]) {
			return true
		}
	}
	return false
}

// Go2rtcUnit is the service of go2rtc.
const Go2rtcUnit = units.Prefix + "go2rtc" + units.Suffix

// recordedByGo2rtc: the cameras recorded through go2rtc (engine=go2rtc of an enabled
// camera of an enabled local recorder), as "<nvr>/<camera>".
func recordedByGo2rtc(s *config.Store) map[string]bool {
	out := map[string]bool{}
	for _, cam := range s.Items("/camera") {
		nvr := find(s, "/nvr", "name", cam.Val("nvr"))
		if cam.Val("enabled") == "no" || nvr == nil || nvr.Val("disabled") == "yes" || remote(nvr) || ResolveCamera(s, cam).Engine != EngineGo2rtc {
			continue
		}
		out[nvr.Val("name")+"/"+cam.Val("name")] = true
	}
	return out
}

// RecordsViaGo2rtc: some camera is recorded through go2rtc (so go2rtc runs).
func RecordsViaGo2rtc(s *config.Store) bool { return len(recordedByGo2rtc(s)) > 0 }

// Go2rtcRecordURL is what ffmpeg records for a camera with engine=go2rtc: go2rtc's
// RTSP copy of its stream= on localhost.
func Go2rtcRecordURL(s *config.Store, nvr, camera string) string {
	return "rtsp://127.0.0.1:" + strconv.Itoa(LiveSettings(s).RTSPPort) + "/" + LiveName(nvr, camera, "hq")
}

// API is the address of go2rtc's API.
func (l Live) API() string { return "127.0.0.1:" + strconv.Itoa(l.Port) }

// LiveName is the go2rtc stream of a camera; quality is hq or lq.
func LiveName(nvr, camera, quality string) string { return nvr + "/" + camera + "/" + quality }

// withCredentials puts the recorder's username and password into an address that
// has none.
func withCredentials(raw, user, pass string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || user == "" {
		return raw
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

// LiveStreams: go2rtc stream name -> source, for every enabled camera of an enabled
// recorder. Qualities without an address are left out.
func LiveStreams(s *config.Store) map[string]string {
	out := map[string]string{}
	for _, cam := range s.Items("/camera") {
		nvr := find(s, "/nvr", "name", cam.Val("nvr"))
		if nvr == nil || nvr.Val("disabled") == "yes" || cam.Val("enabled") == "no" {
			continue
		}
		srcs := map[string]string{"hq": cam.Val("stream"), "lq": cam.Val("stream-lq")}
		if !remote(nvr) { // its own values over its profile's, the login put in
			hq, lq, err := ResolveCamera(s, cam).Addresses()
			if err != nil {
				continue
			}
			srcs["hq"], srcs["lq"] = hq, lq
		}
		switch srcs["lq"] {
		case NoLQ: // turned off
			srcs["lq"] = ""
		case "": // made from the HQ address when it is of a known kind (not stored)
			srcs["lq"] = DeriveLQ(srcs["hq"])
		}
		switch nvr.Val("type") {
		case "dahua", "uniview", "tiandy":
			for q, v := range srcs {
				if v != "" {
					srcs[q] = withCredentials(v, nvr.Val("username"), nvr.Val("password"))
				}
			}
		case "dvrip": // go2rtc speaks DVRIP itself
			host := nvr.Val("host")
			if _, _, err := net.SplitHostPort(host); err != nil {
				host = net.JoinHostPort(host, "34567")
			}
			ch, _ := strconv.Atoi(cam.Val("channel"))
			for i, q := range []string{"hq", "lq"} {
				u := url.URL{Scheme: "dvrip", Host: host, User: url.UserPassword(nvr.Val("username"), nvr.Val("password")),
					RawQuery: fmt.Sprintf("channel=%d&subtype=%d", ch-1, i)}
				srcs[q] = u.String()
			}
		}
		for q, v := range srcs {
			if v != "" {
				out[LiveName(nvr.Val("name"), cam.Val("name"), q)] = v
			}
		}
	}
	return out
}

// HasLive reports which qualities of a camera can be watched.
func HasLive(s *config.Store, nvr, camera string) (hq, lq bool) {
	st := LiveStreams(s)
	_, hq = st[LiveName(nvr, camera, "hq")]
	_, lq = st[LiveName(nvr, camera, "lq")]
	return
}

// yamlString quotes a value for YAML: single quotes, nothing escaped but the quote
// itself (doubled) - no backslash escapes to misread.
func yamlString(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

// goConfig is the go2rtc configuration (YAML). Only the API, on localhost; no RTSP,
// WebRTC or SRTP server.
func goConfig(l Live, streams map[string]string, rtsp bool) string {
	names := make([]string, 0, len(streams))
	for n := range streams {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# written by ffnvr (/system/live): changes are overwritten\n")
	fmt.Fprintf(&b, "api:\n  listen: %s\n", yamlString(l.API()))
	rtspListen := "''" // none
	if rtsp {          // for the recordings (engine=go2rtc): localhost only
		rtspListen = yamlString("127.0.0.1:" + strconv.Itoa(l.RTSPPort))
	}
	b.WriteString("rtsp:\n  listen: " + rtspListen + "\nwebrtc:\n  listen: ''\nsrtp:\n  listen: ''\nlog:\n  level: warn\n")
	if len(names) == 0 {
		b.WriteString("streams: {}\n")
		return b.String()
	}
	b.WriteString("streams:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s: %s\n", yamlString(n), yamlString(streams[n]))
	}
	return b.String()
}

// Units: go2rtc and its configuration; the unit carries a hash of the configuration, so
// a changed camera restarts it on commit.
func (liveModule) Units(c *module.Context) ([]units.Unit, error) {
	l := LiveSettings(c.Store)
	rec := recordedByGo2rtc(c.Store)
	if !l.Enabled && len(rec) == 0 {
		return nil, nil
	}
	streams := LiveStreams(c.Store)
	if !l.Enabled { // only for the recordings: only their cameras
		for name := range streams {
			if i := strings.LastIndexByte(name, '/'); i < 0 || !rec[name[:i]] {
				delete(streams, name)
			}
		}
	}
	if l.Transcoding() { // every stream gets its H.264 twin (started only when watched)
		for name := range streams {
			i := strings.LastIndexByte(name, '/')
			if i < 0 {
				continue
			}
			q := name[i+1:]
			if tq := l.Transcode.Of(q); (q == "hq" || q == "lq") && tq.Max > 0 {
				streams[name[:i]+"/tc-"+q] = l.transcodeSource(name, tq.Height)
			}
		}
	}
	// the RTSP server on 127.0.0.1: for the recordings and for ffmpeg of the transcoding
	conf := goConfig(l, streams, len(rec) > 0 || l.Transcoding())
	sum := sha256.Sum256([]byte(conf))
	unit := `[Unit]
Description=ffnvr: go2rtc for live view (localhost only)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=FFNVR_GO2RTC_CONFIG=` + hex.EncodeToString(sum[:])[:16] + `
ExecStart=` + l.Binary + ` -config ` + GoConfig + `
Restart=always
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
DynamicUser=yes
User=ffnvr-go2rtc
SyslogIdentifier=ffnvr-go2rtc
LoadCredential=go2rtc.yaml:` + GoConfig + `

[Install]
WantedBy=multi-user.target
`
	// DynamicUser cannot read the 0600 file directly: go2rtc reads it through the
	// credential systemd hands over (%d: the credentials directory)
	unit = strings.Replace(unit, "-config "+GoConfig, "-config %d/go2rtc.yaml", 1)
	if l.Transcoding() && l.Transcode.Hardware != "" {
		// the GPU (/dev/dri) belongs to these groups
		unit = strings.Replace(unit, "User=ffnvr-go2rtc\n", "User=ffnvr-go2rtc\nSupplementaryGroups=video render\n", 1)
	}
	if c.Live {
		if _, err := os.Stat(l.Binary); err != nil {
			return nil, fmt.Errorf("go2rtc (live view, engine=go2rtc cameras): %s not found (install go2rtc or set /system/live go2rtc=)", l.Binary)
		}
	}
	return []units.Unit{
		{Name: Go2rtcUnit, Content: unit},
		{Name: GoConfig, Content: conf},
	}, nil
}
