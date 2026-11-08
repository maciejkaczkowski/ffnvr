package dahua

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Device describes the recorder (magicBox.cgi).
type Device struct {
	Type     string // e.g. DHI-NVR5216-16P-4KS2E
	Serial   string
	Firmware string // software version
	Name     string // machine name
	Build    string // firmware build date (JSON-RPC)
}

// DeviceInfo reads type, serial number, firmware and name; only the type is required,
// the others are left empty when the firmware does not know them.
func (c *Client) DeviceInfo(ctx context.Context) (Device, error) {
	var d Device
	out, err := c.cgi(ctx, "magicBox.cgi", param{"action", "getDeviceType"})
	if err != nil {
		return d, err
	}
	d.Type = parseKV(out)["type"]
	if out, err := c.cgi(ctx, "magicBox.cgi", param{"action", "getSerialNo"}); err == nil {
		d.Serial = parseKV(out)["sn"]
	}
	if out, err := c.cgi(ctx, "magicBox.cgi", param{"action", "getSoftwareVersion"}); err == nil {
		d.Firmware = parseKV(out)["version"]
	}
	if out, err := c.cgi(ctx, "magicBox.cgi", param{"action", "getMachineName"}); err == nil {
		d.Name = parseKV(out)["name"]
	}
	return d, nil
}

// Channel is one video channel of the recorder.
type Channel struct {
	Number int    // from 1
	Title  string // as set on the recorder
	// State of the camera on it: Connected, Disconnected, Connecting, Empty (no camera)
	// from getCameraState; Configured or Disabled when only the camera configuration
	// is known (getCameraAll); "" when the recorder told nothing.
	State   string
	Address string // the camera's address (RemoteDevice, getCameraAll), when known
	Model   string // the camera's device type, when known
	Serial  string // the camera's serial number, when known
	Version string // the camera's firmware, when known
}

// Active reports whether a camera works on the channel. With unknown state every
// channel counts as active.
func (ch Channel) Active() bool {
	return ch.State == "" || strings.EqualFold(ch.State, "Connected") || ch.State == "Configured"
}

// Channels lists the video channels: their number (devVideoInput getCollect, or the
// titles), titles (ChannelTitle) and camera states (CameraStates). A failure to learn
// the states is not fatal: it is returned as statesErr and the states stay "".
func (c *Client) Channels(ctx context.Context) (chs []Channel, statesErr error, err error) {
	n := 0
	if out, err := c.cgi(ctx, "devVideoInput.cgi", param{"action", "getCollect"}); err == nil {
		n, _ = strconv.Atoi(parseKV(out)["result"])
	}
	titles := map[int]string{}
	out, err := c.cgi(ctx, "configManager.cgi", param{"action", "getConfig"}, param{"name", "ChannelTitle"})
	if err != nil && n == 0 {
		return nil, nil, err
	}
	for k, v := range parseKV(out) {
		// table.ChannelTitle[3].Name=Brama
		rest, ok := strings.CutPrefix(k, "table.ChannelTitle[")
		idx, field, ok2 := strings.Cut(rest, "].")
		if !ok || !ok2 || field != "Name" {
			continue
		}
		if i, err := strconv.Atoi(idx); err == nil {
			titles[i+1] = v
			n = max(n, i+1)
		}
	}
	if n == 0 {
		return nil, nil, errors.New("the recorder reports no video channels")
	}
	states, _, statesErr := c.CameraStates(ctx)
	addrs := c.Addresses(ctx)
	return buildChannels(n, titles, states, addrs), statesErr, nil
}

// CameraStates tells which channels have a working camera: channel (from 1) -> state.
// It tries, in order, the CGI getCameraState, the same over JSON-RPC (newer firmware)
// and the camera configuration over JSON-RPC (getCameraAll: Configured/Disabled/Empty).
// source names the one that answered.
func (c *Client) CameraStates(ctx context.Context) (states map[int]string, source string, err error) {
	return c.cameraStates(ctx, nil, false)
}

// cameraStates: rpc is a session to use (nil: one is opened if needed); rpcFirst asks
// JSON-RPC before the CGI.
func (c *Client) cameraStates(ctx context.Context, rpc *RPC, rpcFirst bool) (map[int]string, string, error) {
	var errs []string
	cgi := func() (map[int]string, bool) {
		out, err := c.cgi(ctx, "LogicDeviceManager.cgi", param{"action", "getCameraState"}, param{"uniqueChannels[0]", "-1"})
		if err != nil {
			errs = append(errs, err.Error())
			return nil, false
		}
		if st := parseStatesKV(parseKV(out)); len(st) > 0 {
			return st, true
		}
		errs = append(errs, "cgi getCameraState: no states in the answer")
		return nil, false
	}
	session := func() *RPC {
		if rpc == nil {
			r, err := c.LoginRPC(ctx)
			if err != nil {
				errs = append(errs, err.Error())
				return nil
			}
			rpc = r
		}
		return rpc
	}
	opened := rpc == nil
	defer func() {
		if opened && rpc != nil {
			rpc.Close()
		}
	}()
	rpcStates := func() (map[int]string, bool) {
		r := session()
		if r == nil {
			return nil, false
		}
		var st struct {
			States []map[string]any `json:"states"`
		}
		if _, err := r.Call(ctx, "LogicDeviceManager.getCameraState", map[string]any{"uniqueChannels": []int{-1}}, &st); err != nil {
			errs = append(errs, err.Error())
			return nil, false
		}
		if len(st.States) == 0 {
			errs = append(errs, "rpc getCameraState: no states in the answer")
			return nil, false
		}
		out := map[int]string{}
		for _, s := range st.States {
			if ch, ok := num(get(s, "channel")); ok {
				out[ch+1], _ = get(s, "connectionState").(string)
			}
		}
		return out, true
	}
	if rpcFirst {
		if st, ok := rpcStates(); ok {
			return st, "rpc getCameraState", nil
		}
		if st, ok := cgi(); ok {
			return st, "cgi getCameraState", nil
		}
	} else {
		if st, ok := cgi(); ok {
			return st, "cgi getCameraState", nil
		}
		if st, ok := rpcStates(); ok {
			return st, "rpc getCameraState", nil
		}
	}
	if r := session(); r != nil {
		cams, err := cameraAll(ctx, r)
		if err == nil && len(cams) > 0 {
			out := map[int]string{}
			for ch, cam := range cams {
				switch {
				case cam.address == "" || cam.address == "0.0.0.0":
					out[ch] = "Empty"
				case cam.enabled:
					out[ch] = "Configured"
				default:
					out[ch] = "Disabled"
				}
			}
			return out, "rpc getCameraAll", nil
		} else if err != nil {
			errs = append(errs, err.Error())
		}
	}
	return nil, "", errors.New(strings.Join(errs, "; "))
}

type cameraConf struct {
	address, model, serial, version string
	enabled                         bool
}

// cameraAll reads the camera configuration (LogicDeviceManager.getCameraAll):
// recorder channel (from 1) -> camera. The recorder's channel is UniqueChannel;
// Channel is the camera's own input (0 for all of them on firmware 4.005), so it
// counts only when UniqueChannel is missing. Entries without a device (Type Compose)
// are left out; a disabled entry (Enable false, Address 192.168.0.0) has no camera.
func cameraAll(ctx context.Context, rpc *RPC) (map[int]cameraConf, error) {
	var all struct {
		Camera []map[string]any `json:"camera"`
	}
	if _, err := rpc.Call(ctx, "LogicDeviceManager.getCameraAll", nil, &all); err != nil {
		return nil, err
	}
	out := map[int]cameraConf{}
	for i, cam := range all.Camera {
		ch, ok := num(get(cam, "UniqueChannel"))
		if !ok {
			if ch, ok = num(get(cam, "Channel")); !ok {
				ch = i
			}
		}
		info, _ := get(cam, "DeviceInfo").(map[string]any)
		if info == nil {
			continue // e.g. {"Channel":8,"Type":"Compose","UniqueChannel":8}
		}
		enabled, known := get(cam, "Enable").(bool)
		if !known {
			enabled, _ = get(info, "Enable").(bool)
		}
		out[ch+1] = cameraConf{address: str(get(info, "Address")), model: str(get(info, "DeviceType")),
			serial: str(get(info, "SerialNo")), version: str(get(info, "Version")), enabled: enabled}
	}
	return out, nil
}

// CameraInfo is what the recorder tells about the camera on a channel.
type CameraInfo struct {
	Address, Model, Serial, Version string
}

// Addresses of the cameras by channel (from 1), as far as the recorder tells them
// (JSON-RPC); empty when it does not.
func (c *Client) Addresses(ctx context.Context) map[int]CameraInfo {
	rpc, err := c.LoginRPC(ctx)
	if err != nil {
		return nil
	}
	defer rpc.Close()
	return addresses(ctx, rpc)
}

func usable(addr string, enabled bool) bool {
	return enabled && addr != "" && addr != "0.0.0.0"
}

// addresses asks LogicDeviceManager.getCameraAll (it tells the channel of every
// camera), then configManager.getConfig RemoteDevice (the table the web interface
// edits). Only enabled entries count: a free channel keeps a placeholder address.
func addresses(ctx context.Context, rpc *RPC) map[int]CameraInfo {
	out := map[int]CameraInfo{}
	if cams, err := cameraAll(ctx, rpc); err == nil {
		for ch, cam := range cams {
			if usable(cam.address, cam.enabled) {
				out[ch] = CameraInfo{Address: cam.address, Model: cam.model, Serial: cam.serial, Version: cam.version}
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	return remoteDevices(ctx, rpc)
}

// remoteDevices reads configManager.getConfig RemoteDevice. Firmware 4.005 answers a
// map {"uuid:System_CONFIG_NETCAMERA_INFO_<n>": {Address, Enable, DeviceType,
// SerialNo, Version, ...}}, where n is the recorder's channel from 0 (the DeviceID of
// getCameraAll's entry with UniqueChannel n); other firmware a list of {Channel,
// UniqueChannel, DeviceInfo: {...}}.
func remoteDevices(ctx context.Context, rpc *RPC) map[int]CameraInfo {
	var conf struct {
		Table any `json:"table"`
	}
	if _, err := rpc.Call(ctx, "configManager.getConfig", map[string]any{"name": "RemoteDevice", "onlyLocal": false}, &conf); err != nil {
		return nil
	}
	out := map[int]CameraInfo{}
	add := func(ch int, dev map[string]any) {
		addr := str(get(dev, "Address"))
		enabled, known := get(dev, "Enable").(bool)
		if !known {
			enabled = true
		}
		if ch >= 0 && usable(addr, enabled) {
			out[ch+1] = CameraInfo{Address: addr, Model: str(get(dev, "DeviceType")), Serial: str(get(dev, "SerialNo")), Version: str(get(dev, "Version"))}
		}
	}
	switch t := conf.Table.(type) {
	case map[string]any:
		for key, v := range t {
			dev, _ := v.(map[string]any)
			i := strings.LastIndexByte(key, '_')
			n, err := strconv.Atoi(key[i+1:])
			if dev == nil || i < 0 || err != nil {
				continue // a key that does not tell the channel
			}
			add(n, dev)
		}
	case []any:
		for i, it := range t {
			m, _ := it.(map[string]any)
			ch, ok := num(get(m, "UniqueChannel"))
			if !ok {
				ch = i
			}
			dev, _ := get(m, "DeviceInfo").(map[string]any)
			if dev == nil {
				dev = m
			}
			if _, has := get(m, "Enable").(bool); has {
				dev = merge(dev, map[string]any{"Enable": get(m, "Enable")})
			}
			add(ch, dev)
		}
	}
	return out
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func parseStatesKV(kv map[string]string) map[int]string {
	states := map[int]string{}
	for i := 0; ; i++ {
		chs, ok := kv[fmt.Sprintf("states[%d].channel", i)]
		if !ok {
			return states
		}
		if ch, err := strconv.Atoi(chs); err == nil {
			states[ch+1] = kv[fmt.Sprintf("states[%d].connectionState", i)]
		}
	}
}

// get reads a JSON field regardless of the case of its name (firmware differ).
func get(m map[string]any, key string) any {
	if v, ok := m[key]; ok {
		return v
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return nil
}

func num(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil
	}
	return 0, false
}

// Disk is one storage device of the recorder.
type Disk struct {
	Name       string // /dev/sda
	State      string // Success, Error, ...
	TotalBytes int64
	UsedBytes  int64
	Error      bool   // a partition reports IsError
	Model      string // e.g. WDCWD23PURZ85C5HY0 (JSON-RPC)
	Serial     string // the disk's serial number (JSON-RPC)
}

// Disks reads storageDevice.cgi getDeviceAllInfo.
func (c *Client) Disks(ctx context.Context) ([]Disk, error) {
	out, err := c.cgi(ctx, "storageDevice.cgi", param{"action", "getDeviceAllInfo"})
	if err != nil {
		return nil, err
	}
	kv := parseKV(out)
	byIdx := map[int]*Disk{}
	for k, v := range kv {
		// list.info[0].Name, list.info[0].State, list.info[0].Detail[1].TotalBytes
		rest, ok := strings.CutPrefix(k, "list.info[")
		if !ok {
			continue
		}
		idxS, field, ok := strings.Cut(rest, "].")
		idx, err := strconv.Atoi(idxS)
		if !ok || err != nil {
			continue
		}
		d := byIdx[idx]
		if d == nil {
			d = &Disk{}
			byIdx[idx] = d
		}
		switch {
		case field == "Name":
			d.Name = v
		case field == "State":
			d.State = v
		case strings.HasPrefix(field, "Detail["):
			_, sub, _ := strings.Cut(field, "].")
			switch sub {
			case "TotalBytes":
				d.TotalBytes += parseBytes(v)
			case "UsedBytes":
				d.UsedBytes += parseBytes(v)
			case "IsError":
				d.Error = d.Error || v == "true"
			}
		}
	}
	idx := make([]int, 0, len(byIdx))
	for i := range byIdx {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	disks := make([]Disk, 0, len(idx))
	for _, i := range idx {
		disks = append(disks, *byIdx[i])
	}
	return disks, nil
}

// the device writes sizes as integers or floats ("1000202273280.000000")
func parseBytes(v string) int64 {
	f, _ := strconv.ParseFloat(v, 64)
	return int64(f)
}

// Time reads the clock of the recorder (global.cgi getCurrentTime). Recordings are found
// by the recorder's local time, so a wrong clock shifts every download.
func (c *Client) Time(ctx context.Context) (time.Time, error) {
	out, err := c.cgi(ctx, "global.cgi", param{"action", "getCurrentTime"})
	if err != nil {
		return time.Time{}, err
	}
	return time.ParseInLocation(timeLayoutIn, parseKV(out)["result"], time.Local)
}

// StreamURL is the RTSP address of a channel (without credentials).
func (c *Client) StreamURL(channel, subtype int) string {
	return fmt.Sprintf("rtsp://%s:554/cam/realmonitor?channel=%d&subtype=%d", c.Host(), channel, subtype)
}

func buildChannels(n int, titles map[int]string, states map[int]string, addrs map[int]CameraInfo) []Channel {
	var chs []Channel
	for i := 1; i <= n; i++ {
		st, ok := states[i]
		if !ok && len(states) > 0 {
			st = "Empty" // the states are known, this channel has no camera
		}
		chs = append(chs, Channel{Number: i, Title: titles[i], State: st, Address: addrs[i].Address, Model: addrs[i].Model,
			Serial: addrs[i].Serial, Version: addrs[i].Version})
	}
	return chs
}
