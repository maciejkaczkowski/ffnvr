package modules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"ffnvr/internal/config"
)

// /camera/profile: what many cameras of a local recorder share - the login and the
// address scheme - written once. A camera names its profile and gives what is its
// own (host, channel); a value set on the camera wins over the profile's, unset falls
// back to it.
//
//	/camera/profile
//	add name=tiandy-admin username=admin password=secret stream="rtsp://<host>/<ch>/1" stream-lq="rtsp://<host>/<ch>/2"
//	/camera
//	add nvr=test name=brama profile=tiandy-admin host=192.168.1.3 channel=1 password="secret"
//
// Placeholders of stream= and stream-lq= (in a profile or on a camera):
//
//	<host>      the camera's host (may carry a port)
//	<ch> <ch0>  its channel, counted from 1 / from 0 (DVRIP)
//	<user> <password>  the login, URL-encoded, for devices that want it in the path
//	            (XMeye: user=<user>&password=<password>)
//
// Without <user>/<password> the login goes into the address as user:password@ (unless
// the address has one).

// cameraShared: the values a profile gives and a camera may override.
var cameraShared = []string{"username", "password", "stream", "stream-lq", "engine", "type", "ptz"}

func profileMenu() *config.Menu {
	return &config.Menu{
		Path:    "/camera/profile",
		Kind:    config.Multi,
		NameKey: "name",
		Help:    "shared settings of cameras of local recorders (login, address scheme); a camera's own values win",
		Fields: []config.Field{
			{Name: "name", Required: true},
			{Name: "username", Help: "login of the cameras"},
			{Name: "password", Secret: true, Help: "password of the cameras"},
			{Name: "stream", Help: "address scheme, e.g. rtsp://<host>/<ch>/1; placeholders <host> <ch> <ch0> <user> <password>"},
			{Name: "stream-lq", Help: "address scheme of the LQ stream (empty: made from stream=; none: no LQ)"},
			{Name: "engine", Type: config.TEnum, Enum: CameraEngines, Help: "internal (the default) or go2rtc, as on /camera"},
			{Name: "type", Type: config.TEnum, Enum: CameraTypes, Help: "the make of the cameras, as on /camera"},
			{Name: "ptz", Type: config.TBool, Help: "PTZ control of the cameras, as on /camera (default no)"},
			{Name: "comment"},
		},
		Validate: validateProfile,
	}
}

var (
	placeholder  = regexp.MustCompile(`<([^<>]*)>`)
	placeholders = map[string]bool{"host": true, "ch": true, "ch0": true, "user": true, "password": true}
	hostRe       = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?)(:[0-9]{1,5})?$`)
)

// CameraSource is what a camera of a local recorder connects to: its own values over
// its profile's. From tells where each came from: "camera", "profile <name>" or ""
// (nowhere: the default).
type CameraSource struct {
	Profile                  string
	Host, Channel            string
	Username, Password       string
	Stream, StreamLQ, Engine string // Stream, StreamLQ: the schemes, placeholders not filled
	Type                     string // its make, the protocol it speaks ("": unknown)
	PTZ                      bool   // it is steered (by Type)
	From                     map[string]string
}

// resolveCamera merges a camera with a profile (nil: none).
func resolveCamera(cam, prof *config.Item) CameraSource {
	src := CameraSource{Profile: cam.Val("profile"), Host: cam.Val("host"), Channel: cam.Val("channel"), From: map[string]string{}}
	for _, k := range cameraShared {
		v, ok := cam.Get(k)
		from := "camera"
		if !ok && prof != nil {
			v, ok = prof.Get(k)
			from = "profile " + prof.Val("name")
		}
		if !ok {
			from = ""
		}
		src.From[k] = from
		switch k {
		case "username":
			src.Username = v
		case "password":
			src.Password = v
		case "stream":
			src.Stream = v
		case "stream-lq":
			src.StreamLQ = v
		case "engine":
			src.Engine = v
		case "type":
			src.Type = v
		case "ptz":
			src.PTZ = v == "yes"
		}
	}
	if src.Engine == "" {
		src.Engine = EngineInternal
	}
	return src
}

// ResolveCamera is the source of a camera of a local recorder, with its profile from
// the configuration.
func ResolveCamera(s *config.Store, cam *config.Item) CameraSource {
	var prof *config.Item
	if p := cam.Val("profile"); p != "" {
		prof = find(s, "/camera/profile", "name", p)
	}
	return resolveCamera(cam, prof)
}

// unreservedEscape: everything but A-Z a-z 0-9 - . _ ~ percent-encoded (a login in a
// path must not bring its own & = / ?).
func unreservedEscape(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// address fills a scheme for the camera; masked shows ***** for the password (print).
func (c CameraSource) address(key, scheme string, masked bool) (string, error) {
	if scheme == "" || scheme == NoLQ {
		return scheme, nil
	}
	login := false
	var missing error
	out := placeholder.ReplaceAllStringFunc(scheme, func(m string) string {
		name := m[1 : len(m)-1]
		need := func(field, v string) string {
			if v == "" && missing == nil {
				missing = fmt.Errorf("%s%s uses %s: set %s= on the camera", key, c.from(key), m, field)
			}
			return v
		}
		switch name {
		case "host":
			return need("host", c.Host)
		case "ch":
			return need("channel", c.Channel)
		case "ch0":
			n, err := strconv.Atoi(need("channel", c.Channel))
			if err != nil {
				return ""
			}
			return strconv.Itoa(n - 1)
		case "user":
			login = true
			return unreservedEscape(c.Username)
		case "password":
			login = true
			if masked {
				return "*****"
			}
			return unreservedEscape(c.Password)
		}
		if missing == nil {
			missing = fmt.Errorf("%s%s: unknown placeholder %s (known: <host> <ch> <ch0> <user> <password>)", key, c.from(key), m)
		}
		return m
	})
	if missing != nil {
		return "", missing
	}
	if !login && c.Username != "" {
		pass := c.Password
		if masked && pass != "" {
			pass = "*****"
		}
		out = withCredentials(out, c.Username, pass)
		if masked {
			out = strings.Replace(out, "%2A%2A%2A%2A%2A", "*****", 1) // * is escaped in user:password@
		}
	}
	return out, nil
}

func (c CameraSource) from(key string) string {
	if f := c.From[key]; strings.HasPrefix(f, "profile ") {
		return " (from " + f + ")"
	}
	return ""
}

// Addresses are the camera's HQ and LQ addresses, placeholders filled and the login put
// in; LQ may be "" (made from HQ for the live view) or NoLQ.
func (c CameraSource) Addresses() (hq, lq string, err error) {
	if hq, err = c.address("stream", c.Stream, false); err != nil {
		return "", "", err
	}
	lq, err = c.address("stream-lq", c.StreamLQ, false)
	return hq, lq, err
}

// check: what a camera of a local recorder needs, with the source its values come from.
func (c CameraSource) check() error {
	if c.Stream == "" {
		if c.Profile != "" {
			return fmt.Errorf("stream is required for a camera of a type=local nvr (on the camera or in profile %s)", c.Profile)
		}
		return fmt.Errorf("stream is required for a camera of a type=local nvr")
	}
	if c.Host != "" && !hostRe.MatchString(c.Host) {
		return fmt.Errorf("host %q: a host name or an address, optionally :port", c.Host)
	}
	hq, lq, err := c.Addresses()
	if err != nil {
		return err
	}
	go2rtc := c.Engine == EngineGo2rtc
	if err := checkStream("stream"+c.from("stream"), hq, go2rtc); err != nil {
		return err
	}
	if lq != NoLQ {
		if err := checkStream("stream-lq"+c.from("stream-lq"), lq, go2rtc); err != nil {
			return err
		}
	}
	if c.Password != "" && c.Username == "" && !strings.Contains(c.Stream+c.StreamLQ, "<password>") {
		return fmt.Errorf("password without username: set username= (on the camera or in its profile)")
	}
	return nil
}

// sample: a camera that has everything, to check a profile's schemes on their own.
var sampleCamera = CameraSource{Host: "192.0.2.1", Channel: "1", Username: "user", Password: "pass"}

func validateProfile(s *config.Store, it *config.Item) error {
	if err := checkName("name", it.Val("name")); err != nil {
		return err
	}
	go2rtc := it.Val("engine") == EngineGo2rtc
	for _, key := range []string{"stream", "stream-lq"} {
		v := it.Val(key)
		if v == "" || key == "stream-lq" && v == NoLQ {
			continue
		}
		addr, err := sampleCamera.address(key, v, false) // placeholders known, the result an address
		if err != nil {
			return err
		}
		if err := checkStream(key, addr, go2rtc); err != nil {
			return err
		}
	}
	// the cameras using the profile must stay right with it
	var broken []string
	for _, cam := range s.Items("/camera") {
		if cam.Val("profile") != it.Val("name") {
			continue
		}
		if err := resolveCamera(cam, it).check(); err != nil {
			broken = append(broken, fmt.Sprintf("%s/%s: %v", cam.Val("nvr"), cam.Val("name"), err))
		}
	}
	if len(broken) > 0 {
		return fmt.Errorf("profile %s would break %d camera(s):\n  %s", it.Val("name"), len(broken), strings.Join(broken, "\n  "))
	}
	return nil
}

// cameraDetail: "print detail" of /camera - what a camera of a local recorder really
// uses, and where each value comes from (the password masked).
func cameraDetail(s *config.Store, it *config.Item) []string {
	nvr := find(s, "/nvr", "name", it.Val("nvr"))
	if nvr == nil || remote(nvr) {
		return []string{"addresses from the recorder " + it.Val("nvr")}
	}
	src := ResolveCamera(s, it)
	origin := func(k string) string {
		if f := src.From[k]; f != "" {
			return "  (" + f + ")"
		}
		return "  (default)"
	}
	var out []string
	if src.Username != "" {
		out = append(out, "username="+quoteDetail(src.Username)+origin("username"))
	}
	if src.From["password"] != "" {
		pw := "*****"
		if src.Password == "" {
			pw = `""`
		}
		out = append(out, "password="+pw+origin("password"))
	}
	for _, k := range []string{"stream", "stream-lq"} {
		scheme := src.Stream
		if k == "stream-lq" {
			scheme = src.StreamLQ
		}
		switch {
		case k == "stream-lq" && scheme == "":
			hq, _ := src.address("stream", src.Stream, false)
			if lq := DeriveLQ(hq); lq != "" {
				m, _ := src.address("stream", src.Stream, true)
				out = append(out, "stream-lq="+quoteDetail(DeriveLQ(m))+"  (made from stream)")
			} else {
				out = append(out, "stream-lq=  (none: stream is of no known kind)")
			}
		case scheme == NoLQ:
			out = append(out, "stream-lq=none"+origin(k))
		default:
			a, err := src.address(k, scheme, true)
			if err != nil {
				out = append(out, k+": "+err.Error())
			} else {
				out = append(out, k+"="+quoteDetail(a)+origin(k))
			}
		}
	}
	out = append(out, "engine="+src.Engine+origin("engine"))
	return out
}

func quoteDetail(v string) string {
	if v == "" || strings.ContainsAny(v, " \t\"") {
		return strconv.Quote(v)
	}
	return v
}
