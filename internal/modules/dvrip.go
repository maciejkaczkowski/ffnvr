package modules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/dvrip"
	"ffnvr/internal/jobs"
)

// dvripRecorder: type=dvrip, HiSilicon/Xiongmai recorders (XMeye, iCSee) over DVRIP
// ("Sofia", port 34567). One logged in session serves the questions of one use; Close
// ends it.
type dvripRecorder struct {
	c *dvrip.Client
	s *dvrip.Session
}

func (r *dvripRecorder) session(ctx context.Context) (*dvrip.Session, error) {
	if r.s == nil {
		s, err := r.c.Login(ctx)
		if err != nil {
			return nil, err
		}
		r.s = s
	}
	return r.s, nil
}

func (r *dvripRecorder) Close() error {
	if r.s != nil {
		r.s.Close()
		r.s = nil
	}
	return nil
}

func (r *dvripRecorder) Device(ctx context.Context) (Device, error) {
	s, err := r.session(ctx)
	if err != nil {
		return Device{}, err
	}
	d, err := s.Device()
	return Device{Type: d.HardWare, Serial: d.SerialNo, Firmware: d.SoftWareVersion, Build: d.BuildTime,
		Uptime: time.Duration(d.RunMinutes) * time.Minute}, err
}

func (r *dvripRecorder) Channels(ctx context.Context) ([]Channel, error, error) {
	s, err := r.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	chs, statesErr, err := s.Channels()
	var out []Channel
	for _, ch := range chs {
		st := ch.Status
		switch {
		case st == "NoConfig":
			st = "Empty"
		case st == "" || strings.EqualFold(st, "Connected"):
		default:
			st = "Disconnected (" + st + ")"
			if strings.EqualFold(ch.Status, "Disconnected") {
				st = "Disconnected"
			}
		}
		detail := ""
		if ch.CurRes != "" {
			detail = ch.CurRes
			if ch.MaxRes != "" && ch.MaxRes != ch.CurRes {
				detail += " (max " + ch.MaxRes + ")"
			}
		}
		out = append(out, Channel{Number: ch.Number, Title: ch.Title, State: st, Detail: detail})
	}
	return out, statesErr, err
}

func (r *dvripRecorder) Disks(ctx context.Context) ([]Disk, error) {
	s, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := s.Storage()
	var out []Disk
	for _, p := range parts {
		out = append(out, Disk{Name: fmt.Sprintf("disk%d.%d", p.Disk, p.Part), State: "Success",
			TotalBytes: p.TotalMB << 20, UsedBytes: (p.TotalMB - p.RemainMB) << 20})
	}
	return out, err
}

func (r *dvripRecorder) Time(ctx context.Context) (time.Time, error) {
	s, err := r.session(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return s.Time()
}

// StreamURL: the RTSP address of these recorders carries the password, it is not put
// into the configuration.
func (r *dvripRecorder) StreamURL(context.Context, int, int) string { return "" }

func (r *dvripRecorder) Open(ctx context.Context, ch int, from, to time.Time) (io.ReadCloser, []string, error) {
	body, format, fps, err := r.c.Open(ctx, ch, from, to)
	if errors.Is(err, dvrip.ErrEmpty) {
		return nil, nil, jobs.ErrEmpty
	}
	if err != nil {
		return nil, nil, err
	}
	return body, []string{"-f", format, "-framerate", strconv.FormatFloat(fps, 'f', -1, 64)}, nil
}

// play reads [from, to) of a channel for the player (dvrip.Client.OpenPlay): the
// stream knows the time of its first picture (FirstFrame).
func (r *dvripRecorder) play(ctx context.Context, ch int, from, to time.Time) (io.ReadCloser, []string, error) {
	body, format, fps, err := r.c.OpenPlay(ctx, ch, from, to)
	if errors.Is(err, dvrip.ErrEmpty) {
		return nil, nil, jobs.ErrEmpty
	}
	if err != nil {
		return nil, nil, err
	}
	return body, []string{"-f", format, "-framerate", strconv.FormatFloat(fps, 'f', -1, 64)}, nil
}

func (r *dvripRecorder) Probe(ctx context.Context) string {
	var b strings.Builder
	s, err := r.session(ctx)
	probeSection(&b, "login", "", err)
	if err != nil {
		return b.String()
	}
	defer r.Close()
	for _, q := range []struct {
		id   uint16
		name string
	}{
		{dvrip.SysInfo, "SystemInfo"},
		{dvrip.ChannelTitle, "ChannelTitle"},
		{dvrip.ConfigGet, "NetWork.ChnStatus"},
		{dvrip.SysInfo, "StorageInfo"},
		{dvrip.TimeQuery, "OPTimeQuery"},
	} {
		raw, err := s.Raw(q.id, q.name)
		probeSection(&b, fmt.Sprintf("DVRIP %d %s", q.id, q.name), raw, err)
	}
	now := time.Now()
	files, err := s.Files(0, now.Add(-24*time.Hour), now, "h264")
	var fl strings.Builder
	for _, f := range files {
		fmt.Fprintf(&fl, "%s - %s %s\n", f.BeginTime, f.EndTime, f.FileName)
	}
	probeSection(&b, "DVRIP channel 1: recordings of the last 24 hours", fl.String(), err)
	return b.String()
}

func (r *dvripRecorder) Reboot(ctx context.Context) error { return r.c.Reboot(ctx) }

func (r *dvripRecorder) Backup(ctx context.Context) (io.ReadCloser, string, error) {
	body, err := r.c.Backup(ctx)
	return body, ".zip", err
}

func (r *dvripRecorder) Files(ctx context.Context, ch int, from, to time.Time) ([]RecFile, error) {
	s, err := r.session(ctx)
	if err != nil {
		return nil, err
	}
	fs, err := s.Files(ch-1, from, to, "h264")
	var out []RecFile
	for _, f := range fs {
		b, e1 := time.ParseInLocation(dvrip.TimeLayout, f.BeginTime, time.Local)
		e, e2 := time.ParseInLocation(dvrip.TimeLayout, f.EndTime, time.Local)
		if e1 != nil || e2 != nil {
			continue
		}
		size, _ := strconv.ParseUint(strings.TrimPrefix(f.FileLength, "0x"), 16, 64)
		out = append(out, RecFile{Name: f.FileName, Start: b, End: e, Size: int64(size) << 10}) // KB
	}
	return out, err
}
