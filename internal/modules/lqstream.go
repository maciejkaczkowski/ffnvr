package modules

import (
	"regexp"
	"strings"
)

// The low quality (sub) stream of a camera that has none in the configuration is made
// from its high quality address when that one is of a known kind - the recorders' and
// cameras' own address schemes, the same ones the scan of a recorder uses. Only exact
// matches: an address of an unknown kind gives no LQ (a wrong one would leave the
// tiles of the live view black), and stream-lq=none turns it off for a camera.
//
//	Uniview   rtsp://host:554/unicast/c3/s0/live          -> .../unicast/c3/s1/live
//	Uniview   rtsp://host:554/media/video1 (a camera)     -> .../media/video2
//	Tiandy    rtsp://host:554/3/1                         -> rtsp://host:554/3/2
//	Dahua     rtsp://host:554/cam/realmonitor?channel=3&subtype=0 -> subtype=1 (also when
//	          subtype is missing: the main stream)
//	Hikvision rtsp://host:554/Streaming/Channels/301      -> .../Streaming/Channels/302
//	XMeye     rtsp://host:554/user=a&password=&channel=3&stream=0.sdp -> stream=1.sdp
//	          (also user=a_password=_channel=3_stream=0.sdp?real_stream)
//	DVRIP     dvrip://user:pass@host?channel=2&subtype=0  -> subtype=1 (engine=go2rtc)

// NoLQ is the stream-lq value that turns the LQ stream (and its making) off.
const NoLQ = "none"

var (
	lqUniview   = regexp.MustCompile(`^(rtsps?://[^/?#]+/unicast/c\d+/s)0(/live)$`)
	lqUniviewIP = regexp.MustCompile(`^(rtsps?://[^/?#]+/media/video)1$`)
	lqTiandy    = regexp.MustCompile(`^(rtsps?://[^/?#]+/\d+/)1$`)
	lqDahua     = regexp.MustCompile(`^rtsps?://[^/?#]+/cam/realmonitor\?([^#]*)$`)
	lqHik       = regexp.MustCompile(`^(rtsps?://[^/?#]+/(?i:Streaming/Channels)/\d*)01$`)
	lqXMeye     = regexp.MustCompile(`^(rtsps?://[^/?#]+/[^?#]*[&_/]stream=)0(\.sdp(?:\?real_stream)?)$`)
	lqDVRIP     = regexp.MustCompile(`^dvrip://[^?#]+\?([^#]*)$`)
	subtype0    = regexp.MustCompile(`(^|&)subtype=0(&|$)`)
)

// DeriveLQ makes the LQ address from the HQ one; "" when it is of no known kind.
func DeriveLQ(hq string) string {
	switch {
	case hq == "":
		return ""
	case lqUniview.MatchString(hq):
		return lqUniview.ReplaceAllString(hq, "${1}1${2}")
	case lqUniviewIP.MatchString(hq):
		return lqUniviewIP.ReplaceAllString(hq, "${1}2")
	case lqTiandy.MatchString(hq):
		return lqTiandy.ReplaceAllString(hq, "${1}2")
	case lqHik.MatchString(hq):
		return lqHik.ReplaceAllString(hq, "${1}02")
	case lqXMeye.MatchString(hq):
		return lqXMeye.ReplaceAllString(hq, "${1}1${2}")
	}
	for _, re := range []*regexp.Regexp{lqDahua, lqDVRIP} {
		m := re.FindStringSubmatch(hq)
		if m == nil {
			continue
		}
		q := m[1]
		switch {
		case subtype0.MatchString(q):
			return strings.TrimSuffix(hq, q) + subtype0.ReplaceAllString(q, "${1}subtype=1${2}")
		case !strings.Contains("&"+q, "&subtype="): // no subtype: the main stream
			return hq + "&subtype=1"
		}
		return "" // another subtype: already a sub stream, or something else
	}
	return ""
}
