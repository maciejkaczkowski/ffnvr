package modules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/jobs"
	"ffnvr/internal/uniview"
)

// univiewRecorder: type=uniview, the Uniview LightAPI (also OEM recorders, e.g. BCS
// Point); recordings come over RTSP.
type univiewRecorder struct {
	c     *uniview.Client
	speed float64 // RTSP Speed header for downloads, 0: none
}

func (r univiewRecorder) Device(ctx context.Context) (Device, error) {
	d, err := r.c.DeviceInfo(ctx)
	dev := Device{Type: d.DeviceModel, Serial: d.SerialNumber, Firmware: d.FirmwareVersion, Name: d.DeviceName, Build: d.Build()}
	if err == nil {
		if up, err := r.c.Uptime(ctx); err == nil {
			dev.Uptime = up
		}
	}
	return dev, err
}

func (r univiewRecorder) Channels(ctx context.Context) ([]Channel, error, error) {
	chs, err := r.c.Channels(ctx)
	out := make([]Channel, len(chs))
	for i, ch := range chs {
		st := ch.State()
		if r := ch.Reason(); r != "" && st == "Disconnected" {
			st += " (" + r + ")"
		}
		out[i] = Channel{Number: ch.ID, Title: ch.Name, State: st, Address: ch.AddressInfo.Address,
			Detail: strings.TrimSpace(ch.Manufacturer + " " + ch.DeviceModel)}
	}
	return out, nil, err // the states always come with the channels
}

func (r univiewRecorder) Disks(ctx context.Context) ([]Disk, error) {
	ds, err := r.c.Disks(ctx)
	out := make([]Disk, len(ds))
	for i, d := range ds {
		total, remain := d.TotalCapacity<<20, d.RemainCapacity<<20 // MB
		st := d.State()
		if st == "Normal" {
			st = "Success" // what describeStatus treats as healthy
		}
		out[i] = Disk{Name: fmt.Sprintf("%s%d", d.Kind, d.ID), State: st, TotalBytes: total, UsedBytes: total - remain,
			Error: d.Status == 5}
	}
	return out, err
}

func (r univiewRecorder) Time(ctx context.Context) (time.Time, error) { return r.c.Time(ctx) }

func (r univiewRecorder) StreamURL(ctx context.Context, ch, sub int) string {
	return r.c.StreamURL(ctx, ch, sub)
}

func (r univiewRecorder) Open(ctx context.Context, ch int, from, to time.Time) (io.ReadCloser, []string, error) {
	body, codec, fps, err := r.c.Open(ctx, ch, from, to, r.speed)
	if errors.Is(err, uniview.ErrEmpty) {
		return nil, nil, jobs.ErrEmpty
	}
	if err != nil {
		return nil, nil, err
	}
	format := "h264"
	if codec == "H265" {
		format = "hevc"
	}
	// a raw Annex-B stream has no timestamps: the frame rate measured from RTP gives them
	return body, []string{"-f", format, "-framerate", strconv.FormatFloat(fps, 'f', -1, 64)}, nil
}

func (r univiewRecorder) Probe(ctx context.Context) string {
	var b strings.Builder
	for _, p := range []string{
		"/LAPI/V1.0/System/DeviceInfo",
		"/LAPI/V1.0/Channels/System/ChannelDetailInfos",
		"/LAPI/V1.0/Storage/Containers/DetailInfos",
		"/LAPI/V1.0/Storage/Containers",
		"/LAPI/V1.0/System/Time",
		"/LAPI/V1.0/System/RunInfo",
	} {
		raw, err := r.c.Get(ctx, p, nil, nil)
		probeSection(&b, "LAPI "+p, string(raw), err)
	}
	now := time.Now()
	// the calendar in both forms recorders know, and the search's raw answer (the types
	// of the recordings: what an event is)
	month := url.Values{"Year": {strconv.Itoa(now.Year())}, "Month": {strconv.Itoa(int(now.Month()))}}
	for _, p := range []string{"/LAPI/V1.0/Channels/1/Media/Video/Streams/0/Records/DailyDistribution",
		"/LAPI/V1.0/Channels/1/Media/Video/Streams/Records/DailyDistribution"} {
		raw, err := r.c.Get(ctx, p, month, nil)
		probeSection(&b, "LAPI "+p+" (this month)", string(raw), err)
	}
	days, err := r.c.Days(ctx, 1, now)
	probeSection(&b, "LAPI channel 1: days recorded this month", fmt.Sprint(days), err)
	day := url.Values{"Begin": {strconv.FormatInt(now.Add(-24*time.Hour).Unix(), 10)}, "End": {strconv.FormatInt(now.Unix(), 10)}}
	raw, err := r.c.Get(ctx, "/LAPI/V1.0/Channels/1/Media/Video/Streams/0/Records", day, nil)
	probeSection(&b, "LAPI channel 1: recordings of the last 24 hours (raw)", string(raw), err)
	recs, err := r.c.Records(ctx, 1, 0, now.Add(-time.Hour), now)
	probeSection(&b, "LAPI channel 1: recordings of the last hour", fmt.Sprintf("%+v", recs), err)
	// main-cgi with the handle of ffnvr's LAPI session: the cameras' codes, the kinds of
	// the recordings (events)
	cams, err := r.c.CGI(ctx, map[string]any{"cmd": 20})
	probeSection(&b, "main-cgi cmd 20: cameras", cams, err)
	if code, err := r.c.ResourceCode(ctx, 1); err == nil {
		raw, err := r.c.CGI(ctx, map[string]any{"cmd": 81, "astResourceCode": []string{code}, "u32RecordType": 0,
			"u32StoretypeCount": 0, "u32Begin": now.Add(-24 * time.Hour).Unix(), "u32End": now.Unix()})
		probeSection(&b, "main-cgi cmd 81: channel 1 ("+code+"), the last 24 hours", raw, err)
	}
	segs, err := r.c.Segments(ctx, 1, now.Add(-24*time.Hour), now)
	ev := 0
	for _, s := range segs {
		if s.Type != 0 {
			ev++
		}
	}
	probeSection(&b, "channel 1: recorded for events in the last 24 hours", fmt.Sprintf("%d of %d parts", ev, len(segs)), err)
	u, err := r.c.RecordURL(ctx, 1, now.Add(-10*time.Minute), now.Add(-9*time.Minute))
	probeSection(&b, "LAPI channel 1: playback address", u, err)
	if err == nil {
		probeSection(&b, "RTSP channel 1: playing 10 minutes ago", r.c.Trace(ctx, u, r.speed), nil)
	}
	return b.String()
}

func (r univiewRecorder) Reboot(ctx context.Context) error { return r.c.Reboot(ctx) }

func (r univiewRecorder) Backup(ctx context.Context) (io.ReadCloser, string, error) {
	body, err := r.c.Backup(ctx)
	return body, ".xml", err
}

// PlaySpeed: how many times faster than real time the player asks a Uniview recorder
// to send a piece when its speed is not set (a piece of 30 s at real time would take
// 30 s to make; recorders that do not know the Speed header send at real time).
const PlaySpeed = 8

// play reads [from, to) of a channel for the player (uniview.Client.OpenPlay): the
// stream knows the time of its first picture (FirstFrame).
func (r univiewRecorder) play(ctx context.Context, ch int, from, to time.Time) (io.ReadCloser, []string, error) {
	speed := r.speed
	if speed <= 0 {
		speed = PlaySpeed
	}
	body, codec, fps, err := r.c.OpenPlay(ctx, ch, from, to, speed)
	if errors.Is(err, uniview.ErrEmpty) {
		return nil, nil, jobs.ErrEmpty
	}
	if err != nil {
		return nil, nil, err
	}
	format := "h264"
	if codec == "H265" {
		format = "hevc"
	}
	return body, []string{"-f", format, "-framerate", strconv.FormatFloat(fps, 'f', -1, 64)}, nil
}

// Days: the days of a month on which a channel recorded (the recorder's calendar).
func (r univiewRecorder) Days(ctx context.Context, ch int, month time.Time) ([]int, error) {
	return r.c.Days(ctx, ch, month)
}

// Files lists the recordings of a channel (LAPI) with what was recorded for an event,
// as the recorder's own timeline marks it red (main-cgi; without it, none marked).
func (r univiewRecorder) Files(ctx context.Context, ch int, from, to time.Time) ([]RecFile, error) {
	recs, err := r.c.Records(ctx, ch, 0, from, to)
	if errors.Is(err, uniview.ErrDevice) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RecFile
	for _, rec := range recs {
		out = append(out, RecFile{Name: rec.FileName, Start: time.Unix(rec.Begin, 0), End: time.Unix(rec.End, 0)})
	}
	if len(out) == 0 {
		return out, nil
	}
	segs, err := r.c.Segments(ctx, ch, from, to)
	if err != nil {
		return out, nil // the recordings play all the same
	}
	for i := range out {
		for _, s := range segs {
			if s.Type != 0 && s.End.After(out[i].Start) && s.Begin.Before(out[i].End) {
				// the end second of a segment is recorded too
				out[i].Events = append(out[i].Events, Span{Start: s.Begin, End: s.End.Add(time.Second)})
			}
		}
	}
	return out, nil
}
