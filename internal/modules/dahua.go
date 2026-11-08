package modules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"ffnvr/internal/dahua"
	"ffnvr/internal/jobs"
)

// dahuaRecorder: type=dahua, the Dahua HTTP API: JSON-RPC (api=rpc, the default) or
// the CGI (api=cgi) for reading the recorder; the CGI for downloads either way. In rpc
// mode a method the firmware does not know falls back to the CGI.
type dahuaRecorder struct {
	c    *dahua.Client
	cgi  bool
	info *dahua.RPCInfo
	err  error
}

func (r *dahuaRecorder) rpc(ctx context.Context) *dahua.RPCInfo {
	if r.cgi {
		return nil
	}
	if r.info == nil && r.err == nil {
		r.info, r.err = r.c.RPCInfo(ctx)
	}
	return r.info
}

// Close ends the JSON-RPC session.
func (r *dahuaRecorder) Close() error {
	if r.info != nil {
		r.info.Close()
		r.info = nil
	}
	return nil
}

// fallback: a JSON-RPC answer that is no answer (unknown method) lets the CGI try
func fallback(err error) bool { return err != nil && errors.Is(err, dahua.ErrDevice) }

func (r *dahuaRecorder) Device(ctx context.Context) (Device, error) {
	var d dahua.Device
	var err error
	if info := r.rpc(ctx); info != nil {
		d, err = info.Device(ctx)
	}
	if r.info == nil && !r.cgi && r.err != nil && !fallback(r.err) {
		return Device{}, r.err // the login itself failed
	}
	if r.info == nil || fallback(err) {
		d, err = r.c.DeviceInfo(ctx)
	} else if d.Serial == "" { // some firmware answer it over the CGI only
		if cd, err := r.c.DeviceInfo(ctx); err == nil {
			d.Serial = cd.Serial
		}
	}
	return Device{Type: d.Type, Serial: d.Serial, Firmware: d.Firmware, Name: d.Name, Build: d.Build}, err
}

func (r *dahuaRecorder) Channels(ctx context.Context) ([]Channel, error, error) {
	var chs []dahua.Channel
	var statesErr, err error
	if info := r.rpc(ctx); info != nil {
		chs, statesErr, err = info.Channels(ctx)
	}
	if r.info == nil || fallback(err) {
		chs, statesErr, err = r.c.Channels(ctx)
	}
	out := make([]Channel, len(chs))
	for i, ch := range chs {
		out[i] = Channel{Number: ch.Number, Title: ch.Title, State: ch.State, Address: ch.Address, Detail: ch.Model, Serial: ch.Serial}
	}
	return out, statesErr, err
}

func (r *dahuaRecorder) Disks(ctx context.Context) ([]Disk, error) {
	var ds []dahua.Disk
	var err error
	if info := r.rpc(ctx); info != nil {
		ds, err = info.Disks(ctx)
	}
	if r.info == nil || fallback(err) {
		ds, err = r.c.Disks(ctx)
	}
	out := make([]Disk, len(ds))
	for i, d := range ds {
		st := d.State
		if strings.EqualFold(st, "Running") { // the JSON-RPC word for a working disk
			st = "Success"
		}
		out[i] = Disk{Name: d.Name, State: st, TotalBytes: d.TotalBytes, UsedBytes: d.UsedBytes, Error: d.Error, Model: d.Model, Serial: d.Serial}
	}
	return out, err
}

func (r *dahuaRecorder) Time(ctx context.Context) (time.Time, error) {
	if info := r.rpc(ctx); info != nil {
		if t, err := info.Time(ctx); !fallback(err) {
			return t, err
		}
	}
	return r.c.Time(ctx)
}

// Upgrade installs a firmware file (see dahua.Client.Upgrade).
func (r *dahuaRecorder) Upgrade(ctx context.Context, file io.Reader, name string, size int64, report func(UpgradeProgress)) error {
	return r.c.Upgrade(ctx, file, name, size, func(s dahua.UpgradeState) {
		if report != nil {
			report(UpgradeProgress{Phase: s.State, Percent: s.Percent(), Sent: s.Sent, Total: s.Total})
		}
	})
}

func (r *dahuaRecorder) StreamURL(_ context.Context, ch, sub int) string {
	return r.c.StreamURL(ch, sub)
}

func (r *dahuaRecorder) Open(ctx context.Context, ch int, from, to time.Time) (io.ReadCloser, []string, error) {
	// ask first: loadfile.cgi has no clear answer for a range without recordings
	recs, err := r.c.FindRecordings(ctx, dahua.Query{Channel: ch, From: from, To: to, Types: []string{"dav"}})
	if errors.Is(err, dahua.ErrDevice) || (err == nil && len(recs) == 0) {
		return nil, nil, jobs.ErrEmpty
	}
	if err != nil {
		return nil, nil, err
	}
	body, err := r.c.OpenRange(ctx, ch, from, to, 0)
	return body, []string{"-f", "dhav"}, err
}

func (r *dahuaRecorder) Probe(ctx context.Context) string {
	var b strings.Builder
	for _, q := range [][]string{
		{"magicBox.cgi", "action", "getDeviceType"},
		{"magicBox.cgi", "action", "getSoftwareVersion"},
		{"devVideoInput.cgi", "action", "getCollect"},
		{"configManager.cgi", "action", "getConfig", "name", "ChannelTitle"},
		{"LogicDeviceManager.cgi", "action", "getCameraState", "uniqueChannels[0]", "-1"},
		{"configManager.cgi", "action", "getConfig", "name", "RemoteDevice"},
		{"storageDevice.cgi", "action", "getDeviceAllInfo"},
		{"global.cgi", "action", "getCurrentTime"},
	} {
		out, err := r.c.Raw(ctx, q[0], q[1:]...)
		probeSection(&b, "CGI "+q[0]+" "+strings.Join(q[1:], " "), out, err)
	}
	rpc, err := r.c.LoginRPC(ctx)
	probeSection(&b, "RPC2 login", "", err)
	if err == nil {
		defer rpc.Close()
		for _, m := range []struct {
			method string
			params any
		}{
			{"magicBox.getDeviceType", nil},
			{"magicBox.getSoftwareVersion", nil},
			{"magicBox.getSerialNo", nil},
			{"configManager.getConfig", map[string]any{"name": "ChannelTitle"}},
			{"LogicDeviceManager.getCameraState", map[string]any{"uniqueChannels": []int{-1}}},
			{"LogicDeviceManager.getCameraAll", nil},
			{"StorageDeviceManager.getDeviceInfos", map[string]any{"volume": "InvidualPhysicalVolume"}},
			{"global.getCurrentTime", nil},
			{"configManager.getConfig", map[string]any{"name": "RemoteDevice"}},
		} {
			raw, err := rpc.Call(ctx, m.method, m.params, nil)
			title := "RPC2 " + m.method
			if p, ok := m.params.(map[string]any); ok && p["name"] != nil {
				title += " " + fmt.Sprint(p["name"])
			}
			probeSection(&b, title, string(raw), err)
		}
	}
	states, source, err := r.c.CameraStates(ctx)
	probeSection(&b, "ffnvr: camera states", fmt.Sprintf("source: %s\nstates: %v", source, states), err)
	return b.String()
}

func (r *dahuaRecorder) Reboot(ctx context.Context) error { return r.c.Reboot(ctx) }

func (r *dahuaRecorder) Backup(ctx context.Context) (io.ReadCloser, string, error) {
	body, err := r.c.Backup(ctx)
	return body, ".backup", err
}

func (r *dahuaRecorder) Files(ctx context.Context, ch int, from, to time.Time) ([]RecFile, error) {
	recs, err := r.c.FindRecordings(ctx, dahua.Query{Channel: ch, From: from, To: to, Types: []string{"dav"}})
	if errors.Is(err, dahua.ErrDevice) {
		return nil, nil // the device's way of saying: nothing
	}
	var out []RecFile
	for _, rec := range recs {
		out = append(out, RecFile{Name: rec.FilePath, Start: rec.Start, End: rec.End, Size: rec.Length})
	}
	return out, err
}
