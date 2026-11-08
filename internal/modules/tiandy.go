package modules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/jobs"
	"ffnvr/internal/tiandy"
)

// tiandyRecorder: type=tiandy, the native protocol of Tiandy recorders (port 3001).
// Everything about the recorder comes in the burst after a login, read once per use;
// the disks, the clock and the search of recordings come from its web interface (ISAPI).
type tiandyRecorder struct {
	c    *tiandy.Client
	info *tiandy.Info
	err  error
}

func (r *tiandyRecorder) load(ctx context.Context) (*tiandy.Info, error) {
	if r.info == nil && r.err == nil {
		r.info, r.err = r.c.Info(ctx)
	}
	return r.info, r.err
}

func (r *tiandyRecorder) Device(ctx context.Context) (Device, error) {
	in, err := r.load(ctx)
	if err != nil {
		return Device{}, err
	}
	return Device{Type: in.Model, Serial: in.Serial, Firmware: in.Firmware}, nil
}

func (r *tiandyRecorder) Channels(ctx context.Context) ([]Channel, error, error) {
	in, err := r.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	// the web interface knows the cameras: address, model, online
	web := map[int]tiandy.ProxyChannel{}
	webErr := error(nil)
	if pcs, err := r.c.ChannelStatus(ctx); err == nil {
		for _, pc := range pcs {
			if n, err := strconv.Atoi(pc.ID); err == nil {
				web[n] = pc
			}
		}
	} else {
		webErr = err
	}
	var out []Channel
	seen := map[int]bool{}
	for _, ch := range in.Channels {
		st := "Empty" // no camera assigned
		pc, known := web[ch.Number]
		switch {
		case known && pc.Online:
			st = "Connected"
		case known:
			st = "Disconnected"
		case !ch.Configured:
		case !in.OnlineKnown:
			st = "Configured"
		case ch.Online:
			st = "Connected"
		default:
			st = "Disconnected"
		}
		c := Channel{Number: ch.Number, Title: ch.Name, State: st, Address: ch.IP}
		if known {
			c.Address = or(pc.Source.IPAddress, c.Address)
			c.Detail = pc.Source.DevModel
			c.Serial = pc.Source.Barcode
			if c.Title == "" {
				c.Title = pc.Source.ChannelName
			}
		}
		out = append(out, c)
		seen[ch.Number] = true
	}
	for n, pc := range web { // reported by the web interface only
		if !seen[n] {
			st := "Disconnected"
			if pc.Online {
				st = "Connected"
			}
			out = append(out, Channel{Number: n, Title: pc.Source.ChannelName, State: st, Address: pc.Source.IPAddress, Detail: pc.Source.DevModel, Serial: pc.Source.Barcode})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	if len(out) == 0 {
		if webErr != nil {
			return nil, nil, webErr
		}
		return nil, nil, errors.New("the recorder reports no channels")
	}
	return out, nil, nil
}

// Disks: from the web interface (ISAPI); the records after the native login tell
// them unreliably.
func (r *tiandyRecorder) Disks(ctx context.Context) ([]Disk, error) {
	hdds, err := r.c.HDDs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Disk
	for _, h := range hdds {
		st := "Success"
		if !h.Working() {
			st = h.Status
		}
		used := h.UsedSpace
		if used == 0 && h.Capacity > h.FreeSpace {
			used = h.Capacity - h.FreeSpace
		}
		out = append(out, Disk{Name: or(h.Name, "hdd"+h.ID), State: st, TotalBytes: h.Capacity << 20, UsedBytes: used << 20, Error: h.Failed(), Model: h.Type})
	}
	return out, nil
}

func (r *tiandyRecorder) Time(ctx context.Context) (time.Time, error) { return r.c.Time(ctx) }

// StreamURL: Tiandy recorders serve rtsp://<host>:554/<channel>/<stream>, stream 1 the
// main (best) one, 2 the sub stream.
func (r *tiandyRecorder) StreamURL(_ context.Context, ch, sub int) string {
	return fmt.Sprintf("rtsp://%s/%d/%d", net.JoinHostPort(r.c.Host(), "554"), ch, sub+1)
}

func (r *tiandyRecorder) Open(ctx context.Context, ch int, from, to time.Time) (io.ReadCloser, []string, error) {
	body, format, fps, err := r.c.Open(ctx, ch, from, to)
	if errors.Is(err, tiandy.ErrEmpty) {
		return nil, nil, jobs.ErrEmpty
	}
	if err != nil {
		return nil, nil, err
	}
	return body, []string{"-f", format, "-framerate", strconv.FormatFloat(fps, 'f', -1, 64)}, nil
}

func (r *tiandyRecorder) Probe(ctx context.Context) string {
	var b strings.Builder
	in, err := r.load(ctx)
	if in == nil {
		probeSection(&b, "login and information", "", err)
		return b.String()
	}
	probeSection(&b, "records after the login", strings.Join(in.Records, "\n"), err)
	var s strings.Builder
	fmt.Fprintf(&s, "model %q serial %q firmware %q gui %q\n", in.Model, in.Serial, in.Firmware, in.GUI)
	for _, ch := range in.Channels {
		fmt.Fprintf(&s, "channel %d %q camera %s configured %v online %v (known %v)\n",
			ch.Number, ch.Name, ch.IP, ch.Configured, ch.Online, in.OnlineKnown)
	}
	probeSection(&b, "ffnvr: what was understood", s.String(), nil)
	for _, p := range []string{"/ISAPI/ContentMgmt/Storage/hdd/", "/ISAPI/ContentMgmt/InputProxy/channels/status", "/ISAPI/System/time"} {
		raw, err := r.c.ISAPI(ctx, p, nil)
		probeSection(&b, "ISAPI "+p, string(raw), err)
	}
	return b.String()
}

func (r *tiandyRecorder) Reboot(ctx context.Context) error { return r.c.Reboot(ctx) }

func (r *tiandyRecorder) Backup(ctx context.Context) (io.ReadCloser, string, error) {
	body, err := r.c.Backup(ctx)
	return body, ".box", err
}

// Files: the recorder's search (its web interface) by day, the days from its calendar
// of the month. What it tells as spans of a type after another (Alarm, Timer…), cut at
// midnight, is joined where one ends as the next begins: what was recorded without a
// break is one file.
func (r *tiandyRecorder) Files(ctx context.Context, ch int, from, to time.Time) ([]RecFile, error) {
	var recs []tiandy.Record
	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.Local); m.Before(to); m = m.AddDate(0, 1, 0) {
		days, err := r.c.Days(ctx, ch, m)
		if err != nil {
			return nil, tiandySearchErr(err)
		}
		for _, d := range days {
			day := time.Date(m.Year(), m.Month(), d, 0, 0, 0, 0, time.Local)
			if !day.AddDate(0, 0, 1).After(from) || !day.Before(to) {
				continue
			}
			rs, err := r.c.Search(ctx, ch, day)
			if errors.Is(err, tiandy.ErrRefused) {
				rs, err = r.refusedDay(ctx, ch, day)
			}
			if err != nil {
				return nil, tiandySearchErr(err)
			}
			recs = append(recs, rs...)
		}
	}
	var out []RecFile
	for _, f := range joinRecords(recs) {
		if f.End.After(from) && f.Start.Before(to) {
			out = append(out, f)
		}
	}
	return out, nil
}

// unsearched: the type of a day the recorder would not tell, taken as recorded whole
const unsearched = "unsearched"

// refusedDay: a day whose search the recorder refused (too much to tell: much motion).
// Asked by type, Timer and Alarm, it is told in halves; when the halves are not what
// was asked for, the calendar's word stands: the whole day was recorded (until now),
// with the alarms when they came.
func (r *tiandyRecorder) refusedDay(ctx context.Context, ch int, day time.Time) ([]tiandy.Record, error) {
	only := func(typ string) ([]tiandy.Record, bool) {
		rs, err := r.c.SearchType(ctx, ch, day, typ)
		if err != nil {
			return nil, false
		}
		for _, x := range rs {
			if !strings.EqualFold(x.Type, typ) { // the type not obeyed: not to be used
				return nil, false
			}
		}
		return rs, true
	}
	alarms, _ := only("Alarm")
	if timers, ok := only("Timer"); ok && len(timers) > 0 {
		return append(timers, alarms...), nil
	}
	end := day.AddDate(0, 0, 1).Add(-time.Second)
	if now := time.Now(); now.Before(end) {
		end = now
	}
	if !end.After(day) {
		return alarms, nil
	}
	return append([]tiandy.Record{{Start: day, End: end, Type: unsearched}}, alarms...), nil
}

// isEvent: a type of recording made for an alarm (motion), not by the schedule or by hand.
func isEvent(typ string) bool {
	switch strings.ToLower(typ) {
	case "", "timer", "manual", unsearched:
		return false
	}
	return true
}

// Days: the days of a month on which a channel recorded (the recorder's calendar).
func (r *tiandyRecorder) Days(ctx context.Context, ch int, month time.Time) ([]int, error) {
	days, err := r.c.Days(ctx, ch, month)
	return days, tiandySearchErr(err)
}

func tiandySearchErr(err error) error {
	if errors.Is(err, tiandy.ErrNoSearch) {
		return ErrNoSearch
	}
	return err
}

// joinRecords joins spans that follow each other (at most a second apart: the search
// ends a day at 23:59:59) into files named by their types; the spans of alarms are the
// files' events.
func joinRecords(recs []tiandy.Record) []RecFile {
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Start.Before(recs[j].Start) })
	var out []RecFile
	var types []string
	for _, r := range recs {
		n := len(out)
		if n == 0 || r.Start.After(out[n-1].End.Add(time.Second)) {
			out = append(out, RecFile{Start: r.Start, End: r.End})
			types = nil
			n++
		} else if r.End.After(out[n-1].End) {
			out[n-1].End = r.End
		}
		f := &out[n-1]
		if r.Type != "" && !slices.Contains(types, r.Type) {
			types = append(types, r.Type)
			f.Name = strings.Join(types, "+")
		}
		if isEvent(r.Type) && r.End.After(r.Start) {
			if k := len(f.Events); k > 0 && !r.Start.After(f.Events[k-1].End.Add(time.Second)) {
				if r.End.After(f.Events[k-1].End) {
					f.Events[k-1].End = r.End
				}
			} else {
				f.Events = append(f.Events, Span{r.Start, r.End})
			}
		}
	}
	return out
}
