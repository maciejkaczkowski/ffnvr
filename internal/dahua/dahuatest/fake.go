// Package dahuatest is a fake Dahua recorder for tests: it requires Digest
// authentication and answers the CGI calls used by package dahua.
package dahuatest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	User  = "admin"
	Pass  = "secret"
	realm = "Login to TEST"
	nonce = "abc123"
)

// NVR is the state of the fake recorder; change it between calls under Mu.
type NVR struct {
	Mu       sync.Mutex
	Type     string
	Serial   string
	Firmware string
	Time     string         // "2026-10-24 10:00:00"
	Titles   []string       // channel titles, the number of channels
	States   map[int]string // channel (from 1) -> connection state (missing: no camera)
	// which interfaces tell the states (firmware differ)
	CGIStates    bool  // LogicDeviceManager.cgi getCameraState (default)
	RPCStates    bool  // RPC2 LogicDeviceManager.getCameraState
	RPCCameraAll bool  // RPC2 LogicDeviceManager.getCameraAll
	RPCInfo      bool  // RPC2 magicBox.*, ChannelTitle, getDeviceInfos, getCurrentTime (default)
	RemoteDevice any   // RPC2 configManager.getConfig RemoteDevice: its table; nil: refused
	CameraAll    []any // RPC2 getCameraAll: its camera list as given; nil: built from States
	RPCDisks     []any
	Rebooted     bool
	Backup       []byte            // the configuration export (configFileExport.backup)
	Disks        string            // the raw answer of getDeviceAllInfo
	Ranges       map[string][]byte // "channel|start|end" -> loadfile.cgi body; missing: empty body
	// Recorded: the files of a channel as [start, end] ("2006-01-02 15:04:05"), found by
	// overlapping the search (instead of Ranges); Load makes the loadfile.cgi body of a
	// range, held LoadHold before it is sent (MaxLoads: the most loads at once).
	Recorded map[int][][2]string
	Load     func(channel int, start, end string) []byte
	LoadHold time.Duration
	MaxLoads int
	loads    int
	searches map[string]*search // Recorded: the searches by object
	Requests []string           // RequestURIs seen
	PTZ      []string           // PTZ commands seen: "rpc start Up ch2 5" / "cgi stop ZoomTele ch1 5"
	NoRPCPTZ bool               // ptz.factory.instance is refused (firmware without RPC PTZ)
	// the firmware upgrade (/RPC2_Upgrade): the file received and its name; UpgradeFail
	// is the state reported instead of Succeeded (e.g. FeatureAPIUnmatch); UpgradeStep
	// the pause between the reports; NoNotify refuses the WebSocket
	Upgraded    []byte
	UpgradeName string
	UpgradeFail string
	UpgradeStep time.Duration
	NoNotify    bool
	KeepAlives  int
	// the clock: NTP is the table of the NTP configuration (both interfaces); TimeSets
	// the times set ("rpc ..." / "cgi ...")
	NTP      map[string]any
	TimeSets []string
	subs     []*wsConn // subscribed to upgrader.attachState
	objects  int
	found    string // the range of the running search
	Server   *httptest.Server
}

// New starts a fake recorder with 4 channels.
func New() *NVR {
	n := &NVR{
		Type: "DHI-NVR5216-16P", Serial: "7G0123PAZ", Firmware: "4.001.0000000.1, Build Date 2023-01-02",
		Time:   "2026-10-24 10:00:00",
		Titles: []string{"Brama wjazdowa", "Channel2", "Plac żółty", "IPC"},
		States: map[int]string{1: "Connected", 2: "Connected", 3: "Disconnected", 4: "Connected"},
		Disks: "list.info[0].Detail[0].Path=/dev/sda1\r\nlist.info[0].Detail[0].TotalBytes=4000000000000.000000\r\n" +
			"list.info[0].Detail[0].UsedBytes=1000000000000.000000\r\nlist.info[0].Detail[0].IsError=false\r\n" +
			"list.info[0].Name=/dev/sda\r\nlist.info[0].State=Success\r\n" +
			"list.info[1].Detail[0].TotalBytes=4000000000000\r\nlist.info[1].Detail[0].UsedBytes=3000000000000\r\n" +
			"list.info[1].Detail[0].IsError=true\r\nlist.info[1].Name=/dev/sdb\r\nlist.info[1].State=Error\r\n",
		Ranges: map[string][]byte{},
		NTP: map[string]any{"Enable": true, "Address": "192.168.0.254", "Port": 123, "UpdatePeriod": 60,
			"TimeZone": 2, "TimeZoneDesc": "Warsaw"},
		Backup:    []byte("MWPZWJGS1umvOsVQhpg+P/RZiC4OEY5POdwEXKKmFqltDiQJ6N3J4kCibugQ4We4"),
		CGIStates: true,
		RPCInfo:   true,
		// the answer of firmware 4.005.0000000.5.R (DHI-NVR2108-S3), as captured
		RPCDisks: []any{
			map[string]any{"BUS": "SATA", "Capacity": 4e12, "Module": "WDCWD23PURZ85C5HY0", "Name": "/dev/sda", "State": "Running",
				"SerialNo": "WDWXC2D535DJEY", "Partitions": []any{
					map[string]any{"Name": "/dev/sda0", "Remain": 1.5e12, "Status": "LvAvailable", "Total": 2e12},
					map[string]any{"Name": "/dev/sda1", "Remain": 1.5e12, "Status": "LvAvailable", "Total": 2e12}}},
			map[string]any{"Name": "/dev/sdb", "State": "Error", "Capacity": 4e12, "Partitions": []any{
				map[string]any{"Remain": 1e12, "Total": 4e12}}},
		},
	}
	n.Server = httptest.NewServer(http.HandlerFunc(n.serve))
	return n
}

// Addr is host:port of the fake recorder.
func (n *NVR) Addr() string { return strings.TrimPrefix(n.Server.URL, "http://") }

func (n *NVR) Close() { n.Server.Close() }

func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func authParams(h string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(h, "Digest "), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			m[k] = strings.Trim(v, `"`)
		}
	}
	return m
}

func (n *NVR) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/RPC2_Login" || r.URL.Path == "/RPC2" {
		n.serveRPC(w, r)
		return
	}
	if r.URL.Path == "/webappoverwebsocket" {
		n.serveNotify(w, r)
		return
	}
	if r.URL.Path == "/RPC2_Upgrade" {
		n.serveUpgrade(w, r)
		return
	}
	if c, err := r.Cookie("WebClientHttpSessionID"); err == nil && r.URL.Path == "/cgi-bin/configFileExport.backup" {
		if c.Value != "S1" || r.URL.Query().Get("action") != "All" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/binarytet-stream;charset=utf-8")
		w.Write(n.Backup)
		return
	}
	p := authParams(r.Header.Get("Authorization"))
	ha1 := md5hex(User + ":" + realm + ":" + Pass)
	ha2 := md5hex(r.Method + ":" + r.RequestURI)
	if p["response"] != md5hex(ha1+":"+nonce+":"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2) || p["uri"] != r.RequestURI {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", qop="auth", nonce="%s", opaque=""`, realm, nonce))
		w.WriteHeader(401)
		return
	}
	if r.URL.Path == "/cgi-bin/loadfile.cgi" && n.Load != nil {
		n.load(w, r)
		return
	}
	n.Mu.Lock()
	defer n.Mu.Unlock()
	n.Requests = append(n.Requests, r.RequestURI)
	q, _ := url.ParseQuery(r.URL.RawQuery)
	action := q.Get("action")
	switch r.URL.Path + "?" + action {
	case "/cgi-bin/ptz.cgi?start", "/cgi-bin/ptz.cgi?stop":
		n.PTZ = append(n.PTZ, fmt.Sprintf("cgi %s %s ch%s %s", action, q.Get("code"), q.Get("channel"), q.Get("arg2")))
		fmt.Fprint(w, "OK\r\n")
	case "/cgi-bin/magicBox.cgi?getDeviceType":
		fmt.Fprintf(w, "type=%s\r\n", n.Type)
	case "/cgi-bin/magicBox.cgi?getSerialNo":
		fmt.Fprintf(w, "sn=%s\r\n", n.Serial)
	case "/cgi-bin/magicBox.cgi?getSoftwareVersion":
		fmt.Fprintf(w, "version=%s\r\n", n.Firmware)
	case "/cgi-bin/magicBox.cgi?getMachineName":
		fmt.Fprint(w, "name=NVR\r\n")
	case "/cgi-bin/global.cgi?getCurrentTime":
		fmt.Fprintf(w, "result=%s\r\n", n.Time)
	case "/cgi-bin/devVideoInput.cgi?getCollect":
		fmt.Fprintf(w, "result=%d\r\n", len(n.Titles))
	case "/cgi-bin/global.cgi?setCurrentTime":
		n.Time = q.Get("time")
		n.TimeSets = append(n.TimeSets, "cgi "+n.Time)
		fmt.Fprint(w, "OK\r\n")
	case "/cgi-bin/configManager.cgi?setConfig":
		for k := range q {
			if f, ok := strings.CutPrefix(k, "NTP."); ok {
				var v any = q.Get(k)
				if f == "Enable" {
					v = q.Get(k) == "true"
				} else if i, err := strconv.Atoi(q.Get(k)); err == nil && f != "Address" {
					v = i
				}
				n.NTP[f] = v
			}
		}
		fmt.Fprint(w, "OK\r\n")
	case "/cgi-bin/configManager.cgi?getConfig":
		if q.Get("name") == "NTP" {
			keys := make([]string, 0, len(n.NTP))
			for k := range n.NTP {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(w, "table.NTP.%s=%v\r\n", k, n.NTP[k])
			}
			return
		}
		for i, t := range n.Titles {
			fmt.Fprintf(w, "table.ChannelTitle[%d].Name=%s\r\n", i, t)
		}
	case "/cgi-bin/LogicDeviceManager.cgi?getCameraState":
		if !n.CGIStates {
			fmt.Fprint(w, "Error\r\nBad Request!\r\n")
			return
		}
		i := 0
		for ch := 1; ch <= len(n.Titles); ch++ {
			if st, ok := n.States[ch]; ok {
				fmt.Fprintf(w, "states[%d].channel=%d\r\nstates[%d].connectionState=%s\r\n", i, ch-1, i, st)
				i++
			}
		}
	case "/cgi-bin/storageDevice.cgi?getDeviceAllInfo":
		fmt.Fprint(w, n.Disks)
	case "/cgi-bin/mediaFileFind.cgi?factory.create":
		if n.Recorded != nil { // an object of its own for every search
			n.objects++
			fmt.Fprintf(w, "result=%d\r\n", 5000+n.objects)
			return
		}
		fmt.Fprint(w, "result=4242\r\n")
	case "/cgi-bin/mediaFileFind.cgi?findFile":
		if n.Recorded != nil {
			ch, _ := strconv.Atoi(q.Get("condition.Channel"))
			sr := &search{channel: q.Get("condition.Channel")}
			for _, f := range n.Recorded[ch] {
				if f[1] > q.Get("condition.StartTime") && f[0] < q.Get("condition.EndTime") {
					sr.files = append(sr.files, f)
				}
			}
			if len(sr.files) == 0 {
				fmt.Fprint(w, "Error\r\nBad Request!\r\n")
				return
			}
			if n.searches == nil {
				n.searches = map[string]*search{}
			}
			n.searches[q.Get("object")] = sr
			fmt.Fprint(w, "OK\r\n")
			return
		}
		key := q.Get("condition.Channel") + "|" + q.Get("condition.StartTime") + "|" + q.Get("condition.EndTime")
		if _, ok := n.Ranges[key]; !ok {
			fmt.Fprint(w, "Error\r\nBad Request!\r\n") // nothing recorded
			return
		}
		n.found = key
		fmt.Fprint(w, "OK\r\n")
	case "/cgi-bin/mediaFileFind.cgi?findNextFile":
		if sr := n.searches[q.Get("object")]; n.Recorded != nil {
			if sr == nil {
				fmt.Fprint(w, "found=0\r\n")
				return
			}
			count, _ := strconv.Atoi(q.Get("count"))
			batch := sr.files
			if count > 0 && len(batch) > count {
				batch = batch[:count]
			}
			sr.files = sr.files[len(batch):]
			fmt.Fprintf(w, "found=%d\r\n", len(batch))
			for i, f := range batch {
				fmt.Fprintf(w, "items[%d].Channel=%s\r\nitems[%d].StartTime=%s\r\nitems[%d].EndTime=%s\r\nitems[%d].Type=dav\r\n"+
					"items[%d].FilePath=/mnt/dvr/%d.dav\r\nitems[%d].Length=1000\r\n", i, sr.channel, i, f[0], i, f[1], i, i, i, i)
			}
			return
		}
		if n.found == "" {
			fmt.Fprint(w, "found=0\r\n")
			return
		}
		f := strings.Split(n.found, "|")
		n.found = ""
		fmt.Fprintf(w, "found=1\r\nitems[0].Channel=%s\r\nitems[0].StartTime=%s\r\nitems[0].EndTime=%s\r\n"+
			"items[0].Type=dav\r\nitems[0].FilePath=/mnt/dvr/x.dav\r\nitems[0].Length=1\r\n", f[0], f[1], f[2])
	case "/cgi-bin/mediaFileFind.cgi?close", "/cgi-bin/mediaFileFind.cgi?destroy":
		fmt.Fprint(w, "OK\r\n")
	case "/cgi-bin/loadfile.cgi?startLoad":
		w.Write([]byte{0, 1, 2, 0xff}) // the preamble the client drops
		w.Write(n.Ranges[q.Get("channel")+"|"+q.Get("startTime")+"|"+q.Get("endTime")])
	default:
		w.WriteHeader(404)
	}
}

// load answers loadfile.cgi with Load, outside the lock: loads run at once.
func (n *NVR) load(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n.Mu.Lock()
	n.Requests = append(n.Requests, r.RequestURI)
	n.loads++
	if n.loads > n.MaxLoads {
		n.MaxLoads = n.loads
	}
	hold := n.LoadHold
	n.Mu.Unlock()
	defer func() { n.Mu.Lock(); n.loads--; n.Mu.Unlock() }()
	ch, _ := strconv.Atoi(q.Get("channel"))
	var body []byte
	recorded := n.Recorded == nil
	for _, f := range n.Recorded[ch] { // a range without recordings: nothing, as the recorders do
		if f[1] > q.Get("startTime") && f[0] < q.Get("endTime") {
			recorded = true
		}
	}
	if recorded {
		body = n.Load(ch, q.Get("startTime"), q.Get("endTime"))
	}
	select {
	case <-time.After(hold):
	case <-r.Context().Done():
		return
	}
	w.Write([]byte{0, 1, 2, 0xff}) // the preamble the client drops
	w.Write(body)
}

type search struct {
	channel string
	files   [][2]string
}

func md5upper(s string) string { return strings.ToUpper(md5hex(s)) }

// serveRPC: the JSON-RPC login (challenge, then the hash) and a few methods.
func (n *NVR) serveRPC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method  string         `json:"method"`
		Params  map[string]any `json:"params"`
		ID      int            `json:"id"`
		Session any            `json:"session"`
		Object  any            `json:"object"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	n.Mu.Lock()
	defer n.Mu.Unlock()
	n.Requests = append(n.Requests, "RPC "+req.Method)
	reply := func(v map[string]any) {
		v["id"] = req.ID
		json.NewEncoder(w).Encode(v)
	}
	if r.URL.Path == "/RPC2_Login" {
		pw, _ := req.Params["password"].(string)
		if req.Session == nil {
			reply(map[string]any{"result": false, "session": "S1", "error": map[string]any{"code": 268632079, "message": "Component error: login challenge!"},
				"params": map[string]any{"encryption": "Default", "random": "R1", "realm": realm}})
			return
		}
		if pw != md5upper(User+":R1:"+md5upper(User+":"+realm+":"+Pass)) {
			reply(map[string]any{"result": false, "error": map[string]any{"code": 268632085, "message": "invalid password"}})
			return
		}
		reply(map[string]any{"result": true, "session": "S1", "params": nil})
		return
	}
	if req.Session != "S1" {
		reply(map[string]any{"result": false, "error": map[string]any{"code": 287637505, "message": "Invalid session in request data!"}})
		return
	}
	switch {
	case req.Method == "LogicDeviceManager.getCameraState" && n.RPCStates:
		var st []map[string]any
		for ch := 1; ch <= len(n.Titles); ch++ {
			if v, ok := n.States[ch]; ok {
				st = append(st, map[string]any{"channel": ch - 1, "connectionState": v})
			}
		}
		reply(map[string]any{"result": true, "params": map[string]any{"states": st}})
	case req.Method == "LogicDeviceManager.getCameraAll" && n.RPCCameraAll:
		if n.CameraAll != nil {
			reply(map[string]any{"result": true, "params": map[string]any{"camera": n.CameraAll}})
			return
		}
		// the shape of firmware 4.005: Channel is the camera's input (0), UniqueChannel the
		// recorder's channel; a free channel is a disabled entry with 192.168.0.0
		var cams []map[string]any
		for ch := 1; ch <= len(n.Titles); ch++ {
			addr, enabled := "192.168.0.0", false
			if st, ok := n.States[ch]; ok {
				addr, enabled = fmt.Sprintf("192.168.1.%d", 10+ch), st != "Disabled"
			}
			cams = append(cams, map[string]any{"Channel": 0, "UniqueChannel": ch - 1, "Enable": enabled, "Type": "Remote",
				"DeviceID": fmt.Sprintf("uuid:System_CONFIG_NETCAMERA_INFO_%d", ch-1),
				"DeviceInfo": map[string]any{"Address": addr, "Enable": enabled, "DeviceType": "IPC-TEST", "SerialNo": fmt.Sprintf("SN%03d", ch),
					"Password": "camsecret"}})
		}
		cams = append(cams, map[string]any{"Channel": len(n.Titles), "Type": "Compose", "UniqueChannel": len(n.Titles)})
		reply(map[string]any{"result": true, "params": map[string]any{"camera": cams}})
	case n.RPCInfo && req.Method == "magicBox.getDeviceType":
		reply(map[string]any{"result": true, "params": map[string]any{"type": n.Type}})
	case n.RPCInfo && req.Method == "magicBox.getSerialNo":
		reply(map[string]any{"result": true, "params": map[string]any{"sn": n.Serial}})
	case n.RPCInfo && req.Method == "magicBox.getSoftwareVersion":
		reply(map[string]any{"result": true, "params": map[string]any{"version": map[string]any{
			"Version": strings.SplitN(n.Firmware, ",", 2)[0], "BuildDate": "2023-01-02", "WebVersion": "3.2.1"}}})
	case n.RPCInfo && req.Method == "magicBox.getMachineName":
		reply(map[string]any{"result": true, "params": map[string]any{"name": "NVR"}})
	case req.Method == "configManager.getConfig" && req.Params["name"] == "RemoteDevice" && n.RemoteDevice != nil:
		reply(map[string]any{"result": true, "params": map[string]any{"table": n.RemoteDevice}})
	case n.RPCInfo && req.Method == "configManager.getConfig" && req.Params["name"] == "ChannelTitle":
		var table []any
		for _, t := range n.Titles {
			table = append(table, map[string]any{"Name": t})
		}
		reply(map[string]any{"result": true, "params": map[string]any{"table": table}})
	case n.RPCInfo && req.Method == "StorageDeviceManager.getDeviceInfos":
		reply(map[string]any{"result": true, "params": map[string]any{"info": n.RPCDisks}})
	case n.RPCInfo && req.Method == "global.getCurrentTime":
		reply(map[string]any{"result": true, "params": map[string]any{"time": n.Time}})
	case req.Method == "magicBox.reboot":
		n.Rebooted = true
		reply(map[string]any{"result": true})
	case req.Method == "ptz.factory.instance" && !n.NoRPCPTZ:
		ch, _ := req.Params["channel"].(float64)
		n.objects++
		reply(map[string]any{"result": 80680000 + int(ch)})
	case (req.Method == "ptz.start" || req.Method == "ptz.stop") && req.Object != nil:
		obj, _ := req.Object.(float64)
		n.PTZ = append(n.PTZ, fmt.Sprintf("rpc %s %v ch%d %v", strings.TrimPrefix(req.Method, "ptz."), req.Params["code"], int(obj)-80680000+1, req.Params["arg1"]))
		reply(map[string]any{"result": true})
	case req.Method == "ptz.destroy":
		n.objects--
		reply(map[string]any{"result": true})
	case n.RPCInfo && req.Method == "global.setCurrentTime":
		n.Time, _ = req.Params["time"].(string)
		n.TimeSets = append(n.TimeSets, fmt.Sprintf("rpc %s tolerance=%v", n.Time, req.Params["tolerance"]))
		reply(map[string]any{"result": true})
	case n.RPCInfo && req.Method == "configManager.getConfig" && req.Params["name"] == "NTP":
		reply(map[string]any{"result": true, "params": map[string]any{"table": n.NTP}})
	case n.RPCInfo && req.Method == "configManager.setConfig" && req.Params["name"] == "NTP":
		if t, ok := req.Params["table"].(map[string]any); ok {
			if a, _ := t["Address"].(string); a == "invalid" {
				reply(map[string]any{"result": true, "params": map[string]any{"options": []any{"ValidateFailed"}}})
				return
			}
			n.NTP = t
		}
		reply(map[string]any{"result": true, "params": map[string]any{"options": []any{}}})
	case req.Method == "global.keepAlive":
		n.KeepAlives++
		reply(map[string]any{"result": true, "params": map[string]any{"timeout": 60}})
	case req.Method == "global.logout":
		reply(map[string]any{"result": true})
	default:
		reply(map[string]any{"result": false, "error": map[string]any{"code": 268894210, "message": "Method not found"}})
	}
}

// Instances counts the PTZ instances not destroyed.
func (n *NVR) Instances() int {
	n.Mu.Lock()
	defer n.Mu.Unlock()
	return n.objects
}
