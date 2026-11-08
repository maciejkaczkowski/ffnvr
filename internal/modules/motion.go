package modules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/eventdb"
)

// Motion bars of local recordings: a segment is cut into MotionParts equal parts and
// each part is marked when there was motion in it. Where the motion comes from depends
// on the make of the camera (type= of /camera, MotionKinds):
//
//	dvrip   its alarms MotionDetect (alarm-dvrip: the camera pushes them)
//	tiandy  its alarms INNER\ALARM\MOTIONDETECT (events=subscribe)
//	dahua   its alarms VideoMotion, only with events=subscribe (without it the
//	        detector, as before)
//	hikvision  its alarms VMD (motion detection), only with events=subscribe
//	uniview its alarms MotionAlarm (MotionAlarmOn to MotionAlarmOff), only with
//	        events=subscribe
//	others  the detector of /system/motion-detector (motiondetect.go), run by the
//	        worker on the segments the panel shows as tiles
//
// All of it is in the event database: the detector writes every range it finds as an
// event MotionDetect (source detector) and marks every segment it looked at, with the
// settings it looked with (MotionChecked, also when nothing moved); other settings look
// again. The bars are drawn from the events only. (Before, the detector's bars were
// files beside the thumbnails, <storage>/<nvr>/.<camera>/motion/*.json: they are no
// longer read, and go with their segments.)

// MotionParts is the number of parts of a bar.
const MotionParts = 30

// Motion of a segment.
type Motion struct {
	Parts    string  `json:"parts"`    // one character per part: 1 motion, 0 none
	Duration float64 `json:"duration"` // seconds
}

// The kinds of motion events of the makes of cameras whose own alarms are their motion.
var cameraMotionKinds = map[string]string{
	"dvrip":  eventdb.KindMotion,
	"tiandy": `INNER\ALARM\MOTIONDETECT`,
	"dahua":  "VideoMotion",
	// the alerts of the motion detection; the smart events (linedetection,
	// fielddetection) count only through alarm-filters= of the schedule
	"hikvision": "VMD",
	// MotionAlarmOn and MotionAlarmOff, one event
	"uniview": "MotionAlarm",
}

// alarmsOnlySubscribed: the makes whose own alarms are followed only when they are
// subscribed to (events=subscribe of /camera); without it they are cameras like any
// other (the detector, alarm recorded as regular), as they were before they could be.
var alarmsOnlySubscribed = map[string]bool{"dahua": true, "hikvision": true, "uniview": true}

// cameraAlarmType: the make of a camera of a local recorder as far as its own alarms
// go - its type=, "" for a make whose alarms are followed only when subscribed to and
// that is not.
func cameraAlarmType(s *config.Store, cam *config.Item) string {
	typ := ResolveCamera(s, cam).Type
	if alarmsOnlySubscribed[typ] && cam.Val("events") != "subscribe" {
		return ""
	}
	return typ
}

// MotionKinds: the kinds of events that are the motion of a camera of a local recorder,
// and whether the detector makes them (false: the camera's own alarms).
func MotionKinds(s *config.Store, cam *config.Item) (kinds []string, detector bool) {
	if k, ok := cameraMotionKinds[cameraAlarmType(s, cam)]; ok {
		return []string{k}, false
	}
	return []string{eventdb.KindMotion}, true
}

// parseSeconds reads a short length: 4s, 2.5s, 500ms, 4 (seconds); 0 to 5 minutes.
func parseSeconds(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		f, e := strconv.ParseFloat(v, 64)
		if e != nil {
			return 0, fmt.Errorf("%q: expected a length like 4s, 2.5s, 0s", v)
		}
		d = time.Duration(f * float64(time.Second))
	}
	if d < 0 || d > 5*time.Minute {
		return 0, fmt.Errorf("%q: 0s to 5m", v)
	}
	return d, nil
}

// localCamera: the camera of a local recorder (nil: none).
func localCamera(s *config.Store, nvr, camera string) *config.Item {
	if n := find(s, "/nvr", "name", nvr); n == nil || remote(n) {
		return nil
	}
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == nvr && c.Val("name") == camera {
			return c
		}
	}
	return nil
}

// MotionBars returns the bars of segments of a camera of a local recorder that are
// known, by name, and the segments the detector has still to look at (pending).
func MotionBars(ctx context.Context, s *config.Store, root, nvr, camera string, files []RecFile) (map[string]Motion, []string, error) {
	cam := localCamera(s, nvr, camera)
	if cam == nil {
		return nil, nil, fmt.Errorf("%s is no camera of a local recorder %s", camera, nvr)
	}
	if len(files) == 0 {
		return map[string]Motion{}, nil, nil
	}
	kinds, detector := MotionKinds(s, cam)
	from, to := files[0].Start, files[0].End
	for _, f := range files {
		from, to = minTime(from, f.Start), maxTime(to, f.End)
	}
	var spans []eventdb.Span
	var checked map[string]eventdb.Checked
	db, ok, err := eventdb.OpenIfExists(eventdb.Path(root))
	if err != nil {
		return nil, nil, err
	}
	if ok {
		defer db.Close()
		if spans, err = db.Spans(ctx, nvr, camera, kinds, from, to, time.Now()); err != nil {
			return nil, nil, err
		}
		if detector {
			if checked, err = db.MotionChecked(ctx, nvr, camera, from, to); err != nil {
				return nil, nil, err
			}
		}
	}
	sig := MotionDetectorSettings(s).Signature()
	out := map[string]Motion{}
	var pending []string
	for _, f := range files {
		a, b := f.Start, f.End
		if detector {
			c, ok := checked[f.Name]
			if !ok || c.Settings != sig {
				pending = append(pending, f.Name)
				continue
			}
			a, b = c.Start, c.End
		}
		if !b.After(a) {
			b = a.Add(time.Second)
		}
		out[f.Name] = Motion{Parts: spanParts(spans, a, b, MotionParts), Duration: b.Sub(a).Seconds()}
	}
	return out, pending, nil
}

// ReadMotion returns the bar of one segment (ok false: the detector has to look at it).
func ReadMotion(ctx context.Context, s *config.Store, root, nvr, camera, segment string) (Motion, bool) {
	start, ok := segmentTime(segment)
	if !ok {
		return Motion{}, false
	}
	end := start.Add(15 * time.Minute)
	if files, err := LocalFiles(s, nvr, camera, start, start.Add(time.Second)); err == nil {
		for _, f := range files {
			if f.Name == segment {
				end = f.End
			}
		}
	}
	bars, _, err := MotionBars(ctx, s, root, nvr, camera, []RecFile{{Name: segment, Start: start, End: end}})
	m, ok := bars[segment]
	return m, err == nil && ok
}

// MotionSpans: the motion of a camera of a local recorder in [from, to), from the
// event database only (the player: the detector is not asked, what it found is shown).
func MotionSpans(ctx context.Context, s *config.Store, root, nvr, camera string, from, to time.Time) ([]Span, error) {
	cam := localCamera(s, nvr, camera)
	if cam == nil {
		return nil, fmt.Errorf("%s is no camera of a local recorder %s", camera, nvr)
	}
	db, ok, err := eventdb.OpenIfExists(eventdb.Path(root))
	if err != nil || !ok {
		return nil, err
	}
	defer db.Close()
	kinds, _ := MotionKinds(s, cam)
	spans, err := db.Spans(ctx, nvr, camera, kinds, from, to, time.Now())
	out := make([]Span, len(spans))
	for i, sp := range spans {
		out[i] = Span{sp.Start, sp.End}
	}
	return out, err
}

// MakeMotion lets the detector look at a segment and writes what it found into the
// event database (nothing for a camera whose motion is its own alarms, nor for a
// segment looked at with these settings already).
func MakeMotion(ctx context.Context, s *config.Store, root, ffmpeg, nvr, camera, segment string) error {
	seg, err := LocalSegment(s, nvr, camera, segment)
	cam := localCamera(s, nvr, camera)
	start, _ := segmentTime(segment)
	if err != nil || cam == nil {
		return fmt.Errorf("not a segment")
	}
	if _, detector := MotionKinds(s, cam); !detector {
		return nil
	}
	if _, ok := ReadMotion(ctx, s, root, nvr, camera, segment); ok {
		return nil
	}
	d := MotionDetectorSettings(s)
	dctx, cancel := context.WithTimeout(ctx, motionTimeout)
	defer cancel()
	det, err := d.Detect(dctx, ffmpeg, seg)
	if err != nil {
		return err
	}
	duration := det.Duration
	if duration <= 0 {
		duration = 15 * 60
	}
	ranges := make([]eventdb.DetectedRange, 0, len(det.Ranges))
	for _, r := range det.Ranges {
		ranges = append(ranges, eventdb.DetectedRange{Start: start.Add(secs(r.Start)), End: start.Add(secs(r.End)), Peak: r.Peak})
	}
	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		return err
	}
	defer db.Close()
	return db.SetMotion(ctx, nvr, camera, segment, start, start.Add(secs(duration)), ranges, det.Method, d.Signature(), time.Now())
}

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// spanParts marks the n equal parts of [a, b) a span overlaps.
func spanParts(spans []eventdb.Span, a, b time.Time, n int) string {
	parts := []byte(strings.Repeat("0", n))
	length := b.Sub(a)
	for _, sp := range spans {
		if !sp.Start.Before(b) || !sp.End.After(a) {
			continue
		}
		for i := 0; i < n; i++ {
			pa := a.Add(length * time.Duration(i) / time.Duration(n))
			pb := a.Add(length * time.Duration(i+1) / time.Duration(n))
			if sp.Start.Before(pb) && sp.End.After(pa) {
				parts[i] = '1'
			}
		}
	}
	return string(parts)
}

// rangeParts marks the parts of [0, duration) a range overlaps.
func rangeParts(ranges []MotionRange, duration float64, n int) string {
	parts := []byte(strings.Repeat("0", n))
	for _, r := range ranges {
		for i := 0; i < n; i++ {
			a, b := float64(i)*duration/float64(n), float64(i+1)*duration/float64(n)
			if r.Start < b && r.End > a {
				parts[i] = '1'
			}
		}
	}
	return string(parts)
}

// cleanMotion deletes the bars of the files of before (the event database keeps them
// now) whose segment is gone.
func cleanMotion(recDir, camera string) {
	dir := filepath.Join(TechDir(recDir, camera), "motion")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		seg := filepath.Join(recDir, camera, strings.TrimSuffix(e.Name(), ".json")+".mkv")
		if _, err := os.Stat(seg); err != nil {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
