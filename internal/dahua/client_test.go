package dahua

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/dahua/dahuatest"
)

func TestDeviceChannelsDisks(t *testing.T) {
	n := dahuatest.New()
	defer n.Close()
	c, err := NewClient(n.Addr(), dahuatest.User, dahuatest.Pass, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := c.DeviceInfo(ctx)
	if err != nil || d.Type != "DHI-NVR5216-16P" || d.Serial != "7G0123PAZ" || !strings.HasPrefix(d.Firmware, "4.001") {
		t.Errorf("%+v %v", d, err)
	}
	chs, serr, err := c.Channels(ctx)
	if err != nil || serr != nil || len(chs) != 4 || chs[0].Title != "Brama wjazdowa" || !chs[0].Active() || chs[2].Active() || chs[2].State != "Disconnected" {
		t.Errorf("%+v %v %v", chs, serr, err)
	}
	disks, err := c.Disks(ctx)
	if err != nil || len(disks) != 2 || disks[0].TotalBytes != 4e12 || disks[0].UsedBytes != 1e12 || disks[0].Error || !disks[1].Error || disks[1].State != "Error" {
		t.Errorf("%+v %v", disks, err)
	}
	tm, err := c.Time(ctx)
	if err != nil || tm.Format(TimeLayout) != "2026-10-24 10:00:00" {
		t.Errorf("%v %v", tm, err)
	}
	if u := c.StreamURL(3, 0); u != "rtsp://127.0.0.1:554/cam/realmonitor?channel=3&subtype=0" {
		t.Error(u)
	}
}

func TestRangeAndErrors(t *testing.T) {
	n := dahuatest.New()
	defer n.Close()
	from := time.Date(2026, 10, 24, 8, 0, 0, 0, time.Local)
	n.Ranges["2|2026-10-24 08:00:00|2026-10-24 09:00:00"] = []byte("DHAVclip")
	c, _ := NewClient(n.Addr(), dahuatest.User, dahuatest.Pass, false)
	ctx := context.Background()
	body, err := c.OpenRange(ctx, 2, from, from.Add(time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(body)
	body.Close()
	if string(b) != "DHAVclip" {
		t.Errorf("%q", b)
	}
	if _, err := c.FindRecordings(ctx, Query{Channel: 1, From: from, To: from.Add(time.Hour)}); err == nil || !strings.Contains(err.Error(), "device error") {
		t.Errorf("empty range: %v", err)
	}
	if _, err := c.FindRecordings(ctx, Query{Channel: 2, From: from, To: from.Add(time.Hour)}); err != nil {
		t.Error(err)
	}
	bad, _ := NewClient(n.Addr(), "admin", "wrong", false)
	if _, err := bad.DeviceInfo(ctx); err == nil || !strings.Contains(err.Error(), "wrong username or password") {
		t.Errorf("auth: %v", err)
	}
	if !strings.Contains(strings.Join(n.Requests, " "), "startTime=2026-10-24%2008:00:00") {
		t.Errorf("encoding: %v", n.Requests)
	}
}

// Firmware differ in where they tell which channels have a camera: an NVR with 8 channels
// and cameras on 1-5 (3 is disconnected).
func TestCameraStatesFallbacks(t *testing.T) {
	n := dahuatest.New()
	defer n.Close()
	n.Titles = []string{"A", "B", "C", "D", "E", "Channel6", "Channel7", "Channel8"}
	n.States = map[int]string{1: "Connected", 2: "Connected", 3: "Disconnected", 4: "Connected", 5: "Connected"}
	c, _ := NewClient(n.Addr(), dahuatest.User, dahuatest.Pass, false)
	ctx := context.Background()
	active := func(chs []Channel) string {
		var out []string
		for _, ch := range chs {
			if ch.Active() {
				out = append(out, strconv.Itoa(ch.Number))
			}
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		name          string
		cgi, rpc, all bool
		source, want  string
		statesErr     string
	}{
		{"cgi", true, false, false, "cgi getCameraState", "1,2,4,5", ""},
		{"rpc", false, true, false, "rpc getCameraState", "1,2,4,5", ""},
		{"rpc config", false, false, true, "rpc getCameraAll", "1,2,3,4,5", ""},
		{"nothing", false, false, false, "", "1,2,3,4,5,6,7,8", "Method not found"},
	} {
		n.CGIStates, n.RPCStates, n.RPCCameraAll = tc.cgi, tc.rpc, tc.all
		_, source, _ := c.CameraStates(ctx)
		chs, serr, err := c.Channels(ctx)
		if err != nil || source != tc.source || active(chs) != tc.want || (tc.statesErr == "") != (serr == nil) ||
			(serr != nil && !strings.Contains(serr.Error(), tc.statesErr)) {
			t.Errorf("%s: source %q active %s, states error %v, %v", tc.name, source, active(chs), serr, err)
		}
	}
	// a wrong password fails the RPC login too
	bad, _ := NewClient(n.Addr(), dahuatest.User, "wrong", false)
	if _, _, err := bad.CameraStates(ctx); err == nil || !strings.Contains(err.Error(), "RPC2 login: wrong username or password (invalid password)") {
		t.Errorf("%v", err)
	}
}

func TestRPCInfo(t *testing.T) {
	n := dahuatest.New()
	defer n.Close()
	n.RPCCameraAll = true
	c, _ := NewClient(n.Addr(), dahuatest.User, dahuatest.Pass, false)
	ctx := context.Background()
	r, err := c.RPCInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d, err := r.Device(ctx)
	if err != nil || d.Type != "DHI-NVR5216-16P" || d.Firmware != "4.001.0000000.1" || d.Build != "2023-01-02" || d.Serial != "7G0123PAZ" || d.Name != "NVR" {
		t.Errorf("%+v %v", d, err)
	}
	chs, serr, err := r.Channels(ctx)
	// states over JSON-RPC are not offered by this fake: the CGI tells them
	if err != nil || serr != nil || len(chs) != 4 || chs[0].Title != "Brama wjazdowa" || chs[2].State != "Disconnected" || chs[0].Address != "192.168.1.11" {
		t.Errorf("%+v %v %v", chs, serr, err)
	}
	disks, err := r.Disks(ctx)
	if err != nil || len(disks) != 2 || disks[0].TotalBytes != 4e12 || disks[0].UsedBytes != 1e12 || disks[0].Error || !disks[1].Error ||
		disks[0].Serial != "WDWXC2D535DJEY" || disks[0].Model != "WDCWD23PURZ85C5HY0" || disks[0].State != "Running" {
		t.Errorf("%+v %v", disks, err)
	}
	if tm, err := r.Time(ctx); err != nil || tm.Format(TimeLayout) != "2026-10-24 10:00:00" {
		t.Errorf("%v %v", tm, err)
	}
	n.RPCInfo = false // older firmware
	if _, err := r.Device(ctx); err == nil || !strings.Contains(err.Error(), "Method not found") {
		t.Errorf("%v", err)
	}
	// the CGI also gives the addresses (over JSON-RPC getCameraAll)
	if chs, _, _ := c.Channels(ctx); chs[1].Address != "192.168.1.12" {
		t.Errorf("%+v", chs)
	}
}

// The answers of a DHI-NVR2108-4KS3 (4.005.0000000.6.R) from /nvr probe: getCameraAll
// gives Channel 0 for every camera (the camera's own input) and UniqueChannel for the
// recorder's channel; free channels are disabled entries with 192.168.0.0. RemoteDevice
// is a map keyed uuid:System_CONFIG_NETCAMERA_INFO_<channel from 0>.
const probeCameraAll = `[{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_0","DeviceInfo":{"Address":"192.168.0.21","DeviceType":"TC-C35US SPEC:I8\/A\/E\/Y\/M\/S\/H\/2.7-13.5mm\/V4.0","Enable":true,"HttpPort":80,"Name":"TC-C35US%20SPEC%3AI8%2FA%2FE%2FY%2FM%2FS%2FH%2F2.7-13.5mm%2FV4.","Port":37777,"ProtocolType":"Onvif","SerialNo":"ID2282301080121641170859","VideoInputs":[{"Name":"cam07"}]},"Enable":true,"Type":"Remote","UniqueChannel":0},
{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_1","DeviceInfo":{"Address":"192.168.0.22","DeviceType":"TC-C35US SPEC:I8\/A\/E\/Y\/M\/S\/H\/2.7-13.5mm\/V4.0","Enable":true,"SerialNo":"ID2282301080121631490890"},"Enable":true,"Type":"Remote","UniqueChannel":1},
{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_2","DeviceInfo":{"Address":"192.168.0.28","DeviceType":"TC-C35US SPEC:I8\/A\/E\/Y\/M\/S\/H\/2.7-13.5mm\/V4.0","Enable":true,"SerialNo":"ID2282301080121640100752"},"Enable":true,"Type":"Remote","UniqueChannel":2},
{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_3","DeviceInfo":{"Address":"192.168.0.20","DeviceType":"TC-C35US SPEC:I8\/A\/E\/Y\/M\/S\/H\/2.7-13.5mm\/V4.0","Enable":true,"SerialNo":"ID2282301080121632160957"},"Enable":true,"Type":"Remote","UniqueChannel":3},
{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_4","DeviceInfo":{"Address":"192.168.0.0","DeviceType":"","Enable":false,"Mac":"ff:ff:ff:ff:ff:ff","SerialNo":null},"Enable":false,"Type":"Remote","UniqueChannel":4},
{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_5","DeviceInfo":{"Address":"192.168.0.0","Enable":false},"Enable":false,"Type":"Remote","UniqueChannel":5},
{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_6","DeviceInfo":{"Address":"192.168.0.0","Enable":false},"Enable":false,"Type":"Remote","UniqueChannel":6},
{"Channel":0,"DeviceID":"uuid:System_CONFIG_NETCAMERA_INFO_7","DeviceInfo":{"Address":"192.168.0.0","Enable":false},"Enable":false,"Type":"Remote","UniqueChannel":7},
{"Channel":8,"Type":"Compose","UniqueChannel":8}]`

const probeRemoteDevice = `{"uuid:System_CONFIG_NETCAMERA_INFO_0":{"Address":"192.168.0.21","DeviceType":"TC-C35US SPEC:I8\/A\/E\/Y\/M\/S\/H\/2.7-13.5mm\/V4.0","Enable":true,"SerialNo":"ID2282301080121641170859","Version":"V22.12.230518.63B01E24"},
"uuid:System_CONFIG_NETCAMERA_INFO_1":{"Address":"192.168.0.22","Enable":true,"SerialNo":"ID2282301080121631490890","Version":"V22.12.230518.63B01E24"},
"uuid:System_CONFIG_NETCAMERA_INFO_2":{"Address":"192.168.0.28","Enable":true},
"uuid:System_CONFIG_NETCAMERA_INFO_3":{"Address":"192.168.0.20","Enable":true},
"uuid:System_CONFIG_NETCAMERA_INFO_4":{"Address":"192.168.0.0","Enable":false,"DeviceType":null},
"uuid:System_CONFIG_NETCAMERA_INFO_7":{"Address":"192.168.0.0","Enable":false}}`

func TestCameraAddressesOfProbe(t *testing.T) {
	n := dahuatest.New()
	defer n.Close()
	n.Titles = []string{"cam07", "cam08", "cam06", "cam14", "Channel5", "Channel6", "Channel7", "Channel8"}
	json.Unmarshal([]byte(probeCameraAll), &n.CameraAll)
	n.RPCCameraAll = true
	c, _ := NewClient(n.Addr(), dahuatest.User, dahuatest.Pass, false)
	ctx := context.Background()
	want := map[int]string{1: "192.168.0.21", 2: "192.168.0.22", 3: "192.168.0.28", 4: "192.168.0.20"}
	check := func(what string, a map[int]CameraInfo) {
		t.Helper()
		if len(a) != len(want) {
			t.Errorf("%s: %+v", what, a)
		}
		for ch, addr := range want {
			if a[ch].Address != addr {
				t.Errorf("%s: CH%d %q, want %s", what, ch, a[ch].Address, addr)
			}
		}
	}
	a := c.Addresses(ctx)
	check("getCameraAll", a)
	if a[1].Serial != "ID2282301080121641170859" || !strings.HasPrefix(a[1].Model, "TC-C35US") {
		t.Errorf("%+v", a[1])
	}
	// without getCameraAll: the map of RemoteDevice
	n.RPCCameraAll = false
	var rd map[string]any
	json.Unmarshal([]byte(probeRemoteDevice), &rd)
	n.RemoteDevice = rd
	a = c.Addresses(ctx)
	check("RemoteDevice", a)
	if a[1].Version != "V22.12.230518.63B01E24" {
		t.Errorf("%+v", a[1])
	}
	// states from getCameraAll (no getCameraState): the free channels are not cameras
	n.RPCCameraAll, n.CGIStates, n.RPCStates = true, false, false
	chs, _, _ := c.Channels(ctx)
	if chs[0].State != "Configured" || chs[4].Active() || chs[0].Address != "192.168.0.21" {
		t.Errorf("%+v", chs)
	}
}
