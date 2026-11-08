package dahua

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reading the recorder over JSON-RPC (/RPC2), as the web interface of newer firmware
// does (see github.com/H3xano/dahua-rpc-api): one login, then the methods below.
// Downloads still go through the CGI (loadfile.cgi).

// RPCInfo is a JSON-RPC session for reading the recorder; Close it.
type RPCInfo struct {
	c   *Client
	rpc *RPC
}

// RPCInfo logs in over JSON-RPC.
func (c *Client) RPCInfo(ctx context.Context) (*RPCInfo, error) {
	rpc, err := c.LoginRPC(ctx)
	if err != nil {
		return nil, err
	}
	return &RPCInfo{c, rpc}, nil
}

func (r *RPCInfo) Close() { r.rpc.Close() }

// Call runs any method (diagnostics); raw is the answer.
func (r *RPCInfo) Call(ctx context.Context, method string, params any) ([]byte, error) {
	return r.rpc.Call(ctx, method, params, nil)
}

func (r *RPCInfo) params(ctx context.Context, method string, params any) (map[string]any, error) {
	var out map[string]any
	_, err := r.rpc.Call(ctx, method, params, &out)
	return out, err
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return fmt.Sprint(int64(x))
	}
	return ""
}

// Device: magicBox.getDeviceType, getSerialNo, getSoftwareVersion, getMachineName.
func (r *RPCInfo) Device(ctx context.Context) (Device, error) {
	var d Device
	p, err := r.params(ctx, "magicBox.getDeviceType", nil)
	if err != nil {
		return d, err
	}
	d.Type = str(get(p, "type"))
	if p, err := r.params(ctx, "magicBox.getSerialNo", nil); err == nil {
		d.Serial = str(get(p, "sn"))
	}
	if p, err := r.params(ctx, "magicBox.getSoftwareVersion", nil); err == nil {
		if v, ok := get(p, "version").(map[string]any); ok {
			d.Firmware = str(get(v, "Version"))
			d.Build = str(get(v, "BuildDate"))
		} else {
			d.Firmware = str(get(p, "version"))
		}
	}
	if p, err := r.params(ctx, "magicBox.getMachineName", nil); err == nil {
		d.Name = str(get(p, "name"))
	}
	return d, nil
}

// Channels: titles (configManager.getConfig ChannelTitle), states (getCameraState,
// JSON-RPC first) and camera addresses (getCameraAll).
func (r *RPCInfo) Channels(ctx context.Context) ([]Channel, error, error) {
	var conf struct {
		Table []map[string]any `json:"table"`
	}
	if _, err := r.rpc.Call(ctx, "configManager.getConfig", map[string]any{"name": "ChannelTitle"}, &conf); err != nil {
		return nil, nil, err
	}
	titles := map[int]string{}
	for i, t := range conf.Table {
		titles[i+1] = str(get(t, "Name"))
	}
	n := len(conf.Table)
	if n == 0 {
		return nil, nil, errors.New("the recorder reports no video channels")
	}
	states, _, statesErr := r.c.cameraStates(ctx, r.rpc, true)
	return buildChannels(n, titles, states, addresses(ctx, r.rpc)), statesErr, nil
}

// Disks: StorageDeviceManager.getDeviceInfos (the volume name has the typo of the web
// interface). Firmware 4.005 answers {"device":[{Name, State: "Running", Capacity,
// Partitions:[{Total, Remain}], SerialNo, Module}]}; older ones the shape of the CGI
// ({"info":[{Name, State, Detail:[{TotalBytes, UsedBytes, IsError}]}]}).
func (r *RPCInfo) Disks(ctx context.Context) ([]Disk, error) {
	p, err := r.params(ctx, "StorageDeviceManager.getDeviceInfos", map[string]any{"volume": "InvidualPhysicalVolume"})
	if err != nil {
		return nil, err
	}
	var list []any
	for _, k := range []string{"device", "info", "list"} {
		if l, ok := get(p, k).([]any); ok {
			list = l
			break
		}
	}
	var out []Disk
	for _, it := range list {
		m, _ := it.(map[string]any)
		d := Disk{Name: str(get(m, "Name")), State: str(get(m, "State")), Serial: str(get(m, "SerialNo")), Model: str(get(m, "Module"))}
		if parts, ok := get(m, "Partitions").([]any); ok { // 4.005
			for _, x := range parts {
				pm, _ := x.(map[string]any)
				total, remain := int64(numF(get(pm, "Total"))), int64(numF(get(pm, "Remain")))
				d.TotalBytes += total
				d.UsedBytes += total - remain
			}
			if d.TotalBytes == 0 {
				d.TotalBytes = int64(numF(get(m, "Capacity")))
			}
		} else if details, ok := get(m, "Detail").([]any); ok {
			for _, x := range details {
				dm, _ := x.(map[string]any)
				d.TotalBytes += int64(numF(get(dm, "TotalBytes")))
				d.UsedBytes += int64(numF(get(dm, "UsedBytes")))
				if e, _ := get(dm, "IsError").(bool); e {
					d.Error = true
				}
			}
		} else {
			d.TotalBytes, d.UsedBytes = int64(numF(get(m, "TotalBytes"))), int64(numF(get(m, "UsedBytes")))
		}
		if st := strings.ToLower(d.State); st != "" && st != "running" && st != "success" {
			d.Error = true
		}
		out = append(out, d)
	}
	return out, nil
}

func numF(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscan(x, &f)
		return f
	}
	return 0
}

// Time: global.getCurrentTime.
func (r *RPCInfo) Time(ctx context.Context) (time.Time, error) {
	p, err := r.params(ctx, "global.getCurrentTime", nil)
	if err != nil {
		return time.Time{}, err
	}
	return time.ParseInLocation(timeLayoutIn, str(get(p, "time")), time.Local)
}
