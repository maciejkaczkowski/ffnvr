package modules

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/tiandy"
)

// The synchronous player of the panel plays the local recorders and, of the remote
// ones, Dahua, Tiandy, Uniview and DVRIP (XMeye).
//
// Dahua: the files are searched (mediaFileFind) and a range is read as a DHAV stream
// (loadfile.cgi), which ffmpeg repacks. A captured session of the recorder's own web
// client showed what the recorder keeps (hourly .dav files of H.265 or H.264 in DHAV,
// channels counted from 0 in RPC2) and that it plays 4 channels at once.
//
// Tiandy: a captured session of the recorder's web client showed the search by day
// over ISAPI (dailyDistribution for the days of a month, dailySearch for the spans of a
// day) and the playing over the native protocol: one logged-in session, a stream per
// channel on a connection of its own (CONNECT with a virtual channel, the range on the
// recorder's wall clock), every picture stamped with its time (internal/tiandy). A day
// of one channel is an answer of some hundred kB: the player asks for days, not months.
// The spans recorded for an alarm (Alarm, beside Timer) are the motion bars of the
// timeline, as the recorder's own player marks them. A day of much motion is too much
// for some recorders to tell (Device Error): it is asked by type, or taken as recorded
// whole (tiandyRecorder.refusedDay).
//
// Uniview: a captured session of the recorder's web client showed the calendar of a
// month over LAPI (Records/DailyDistribution: a status for every day), the search of a
// day, and the playing over RTSP: the address of a range from LAPI
// (Streams/RecordURL?Begin=&End=, rtsp://…/c<channel>/b<from>/e<to>/replay/s0/), then
// OPTIONS, DESCRIBE, SETUP and PLAY of an RTSP session per channel (the web client
// tunnels it through a WebSocket; ffnvr speaks to port 554 as the downloads do). Every
// RTP packet carries the time of its picture in UTC seconds (internal/uniview), so the
// piece ends at its end and the player learns where its first picture is. The search of
// a day is LAPI's Records, as for the downloads; LAPI tells nothing of events (a
// BCS-P-NVR3204-4K-II answers a day as one recording), so what was recorded for an
// event, red on the recorder's timeline, is asked of main-cgi (cmd 81, see
// internal/uniview) and becomes the motion bars, as the alarms of Tiandy. At most
// PlayStreams streams of one recorder are read at once, downloads included
// (uniview.MaxStreams).
//
// DVRIP (XMeye): a captured session of the XMeye client playing a channel and jumping
// by minutes showed the playing over the native protocol: the file searched
// (OPFileQuery), a data connection of the session claiming it (Claim), Start on the
// control connection, and for a jump Pause, Locate and Continue; the frames are those
// of a download (internal/dvrip). The recorder sends at the speed of playing unless
// asked for more: a second capture, the client speeding up, showed Fast with a Value
// (4: about 10 times real time), which the player asks for - a piece of 30 s in some
// 3 s. The frames are stamped by the camera's clock, which need not be the recorder's
// (in the first capture it was 1:55 behind): the file's first key frame tells how far,
// and the piece's first picture is told on the recorder's clock (FirstFrame). The
// month is searched as a whole, as Dahua's. At most PlayStreams streams of one recorder
// are read at once, downloads included (dvrip.MaxStreams).

// PlayStreams: the most streams the player reads from one remote recorder at once (the
// Tiandy, Uniview and DVRIP clients keep to as many for downloads and the player
// together).
const PlayStreams = 4

// Playable: the panel's player plays the recordings of this recorder.
func Playable(s *config.Store, nvrName string) bool {
	nvr := find(s, "/nvr", "name", nvrName)
	return nvr != nil && (!remote(nvr) || PlayableType(nvr.Val("type")))
}

// PlayableType: the panel's player plays the remote recorders of this type.
func PlayableType(typ string) bool {
	return typ == "dahua" || typ == "tiandy" || typ == "uniview" || typ == "dvrip"
}

// SearchedByDay: the player asks this recorder for the files of a day at a time and for
// the days of a month apart (PlayDays); the others are asked for a month.
func SearchedByDay(s *config.Store, nvrName string) bool {
	nvr := find(s, "/nvr", "name", nvrName)
	return nvr != nil && (nvr.Val("type") == "tiandy" || nvr.Val("type") == "uniview")
}

// dayLister: a recorder that tells the days of a month with recordings.
type dayLister interface {
	Days(ctx context.Context, channel int, month time.Time) ([]int, error)
}

// PlayDays: the days ("2006-01-02") of a month on which a camera of a recorder searched
// by day recorded.
func PlayDays(ctx context.Context, s *config.Store, nvrName, camera string, month time.Time) ([]string, error) {
	nvr, ch, err := playCamera(s, nvrName, camera)
	if err != nil {
		return nil, err
	}
	r, err := newRecorder(nvr)
	if err != nil {
		return nil, err
	}
	dl, ok := r.(dayLister)
	if !ok {
		return nil, fmt.Errorf("%s is not searched by day", nvrName)
	}
	days, err := dl.Days(ctx, ch, month)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range days {
		out = append(out, time.Date(month.Year(), month.Month(), d, 0, 0, 0, 0, time.Local).Format("2006-01-02"))
	}
	return out, nil
}

// playCamera: a remote recorder that plays and the channel of its camera.
func playCamera(s *config.Store, nvrName, camera string) (*config.Item, int, error) {
	nvr := find(s, "/nvr", "name", nvrName)
	if nvr == nil || !remote(nvr) || !Playable(s, nvrName) {
		return nil, 0, fmt.Errorf("%s is not a playable remote recorder", nvrName)
	}
	var cam *config.Item
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvrName && c.Val("name") == camera {
			cam = c
		}
	}
	if cam == nil {
		return nil, 0, fmt.Errorf("nvr %s has no camera %s", nvrName, camera)
	}
	ch, err := strconv.Atoi(cam.Val("channel"))
	if err != nil || ch < 1 {
		return nil, 0, fmt.Errorf("camera %s has no channel", camera)
	}
	return nvr, ch, nil
}

// PlayFiles lists the recorded files of a camera of a playable recorder overlapping
// [from, to); a remote recorder is asked.
func PlayFiles(ctx context.Context, s *config.Store, nvrName, camera string, from, to time.Time) ([]RecFile, error) {
	if !Playable(s, nvrName) {
		return nil, fmt.Errorf("the recordings of %s are not played in the panel", nvrName)
	}
	if err := checkName("camera", camera); err != nil {
		return nil, err
	}
	return FindFiles(ctx, s, nvrName, camera, from, to)
}

// OpenPlay reads [from, to) of a camera of a playable remote recorder: the stream and
// the ffmpeg arguments of its format (jobs.ErrEmpty: nothing recorded there).
func OpenPlay(ctx context.Context, s *config.Store, nvrName, camera string, from, to time.Time) (io.ReadCloser, []string, error) {
	nvr, ch, err := playCamera(s, nvrName, camera)
	if err != nil {
		return nil, nil, err
	}
	r, err := newRecorder(nvr)
	if err != nil {
		return nil, nil, err
	}
	if d, ok := r.(*dahuaRecorder); ok {
		// straight to loadfile.cgi: the player knows from its search of the month that
		// there are recordings, and a recorder allows only a few searches at once - one
		// per piece of 30 s would make it answer "no recording" when it is busy
		body, err := d.c.OpenRange(ctx, ch, from, to, 0)
		return body, []string{"-f", "dhav"}, err
	}
	if u, ok := r.(univiewRecorder); ok {
		return u.play(ctx, ch, from, to)
	}
	if d, ok := r.(*dvripRecorder); ok {
		return d.play(ctx, ch, from, to)
	}
	return r.Open(ctx, ch, from, to)
}

// PlayStart: the time of the first picture of a stream read for the player, from its
// start (a DHAV stream of Dahua, or a stamped one of Tiandy) or from the stream itself
// (one of Uniview, whose times came in RTP, or of DVRIP, whose camera's clock is
// reckoned: FirstFrame); ok false: not told.
func PlayStart(body io.Reader, head []byte) (time.Time, bool) {
	if t, ok := DHAVStart(head); ok {
		return t, true
	}
	if t, ok := tiandy.StreamStart(head); ok {
		return t, true
	}
	if f, ok := body.(interface{ FirstFrame() (time.Time, bool) }); ok {
		return f.FirstFrame()
	}
	return time.Time{}, false
}

// DHAVStart reads the time of the first video frame of a DHAV stream from the start of
// it: a range read from a recorder begins at a key frame near, not at, the time asked
// for. A header has the time in whole seconds and a counter of milliseconds (16 bits):
// where the next second begins in the head, the counter tells how far before it the
// first frame was. ok false: no DHAV header there.
func DHAVStart(head []byte) (time.Time, bool) {
	type frame struct {
		t  time.Time
		ms uint16
	}
	var fs []frame
	for i := 0; i+22 <= len(head); {
		j := bytes.Index(head[i:], []byte("DHAV"))
		if j < 0 || i+j+22 > len(head) {
			break
		}
		h := head[i+j:]
		// 0xfd: a key frame, 0xfc: another video frame, 0xf0/0xf1: audio, 0xf2: data
		if h[4] == 0xfd || h[4] == 0xfc {
			d := binary.LittleEndian.Uint32(h[16:20])
			t := time.Date(int(d>>26&0x3f)+2000, time.Month(d>>22&0x0f), int(d>>17&0x1f),
				int(d>>12&0x1f), int(d>>6&0x3f), int(d&0x3f), 0, time.Local)
			if t.Month() == time.Month(d>>22&0x0f) && d>>22&0x0f != 0 { // a real date
				if len(fs) == 0 || t.Sub(fs[0].t) >= 0 && t.Sub(fs[0].t) <= 3*time.Second {
					fs = append(fs, frame{t, binary.LittleEndian.Uint16(h[20:22])})
				}
			}
		}
		i += j + 4
	}
	if len(fs) == 0 {
		return time.Time{}, false
	}
	for _, f := range fs[1:] {
		if f.t.Sub(fs[0].t) == time.Second {
			// the first frame of the next second; the counter wraps
			if back := time.Duration(f.ms-fs[0].ms) * time.Millisecond; back > 0 && back <= time.Second {
				return f.t.Add(-back), true
			}
			break
		}
	}
	return fs[0].t, true
}
