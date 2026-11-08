// Package univiewtest is a fake Uniview recorder for tests: LightAPI over HTTP and
// recordings over RTSP (H.264 sent as RTP over TCP), both with Digest authentication.
package univiewtest

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/digest"
)

const (
	User  = "admin"
	Pass  = "secret"
	realm = "uniview"
	nonce = "2020944489" // numeric, as the recorders give it: also the login handle of main-cgi
)

// Channel of the fake.
type Channel struct {
	Name      string
	Status    int // 0 offline, 1 online, 2 idle
	Address   string
	Model     string
	OffReason int
}

// NVR is the state of the fake recorder; change it under Mu.
type NVR struct {
	Mu        sync.Mutex
	Model     string
	Serial    string
	Firmware  string
	Time      time.Time
	Channels  []Channel
	Disks     string // Data of Storage/Containers/DetailInfos (JSON)
	OldDisks  string // when set: DetailInfos is unknown, Storage/Containers answers this
	Rebooted  bool
	Config    []byte // Config.xml
	OldExport bool   // the export of older recorders: ConfigurationInfo -> URL
	// Recorded: channel -> [begin, end] UTC seconds of its recordings
	Recorded map[int][2]int64
	// Video is the H.264 Annex-B stream played back (any range), FPS its frame rate
	Video     []byte
	FPS       int
	SpeedSeen string // the Speed header of the last PLAY
	// every RTP packet carries the time of its picture (extension 0xabac): the first
	// picture is StampLead seconds before the range's start (its key frame), the next
	// ones follow at FPS
	StampLead int64
	// Hold: how long PLAY waits before it sends (a slow recorder); Active streams are
	// being played now, MaxActive the most at once, Plays all PLAYs
	Hold      time.Duration
	Active    int
	MaxActive int
	Plays     int
	// Days asks for: the calendars of months (DailyDistribution), as "channel year-month"
	DaysAsked []string
	// OldCalendar: the calendar in the form of a BCS-P-NVR3204-4K-II (…/Streams/Records/
	// DailyDistribution, Nums and DailyStatuses), the other form unknown
	OldCalendar bool
	// Segments: channel -> the parts of its recordings that main-cgi tells (cmd 81):
	// {type, begin, end}, type 1 for an event; NoCGI: the recorder has no main-cgi.
	// CGI: the commands asked for ("20", "81 <code> <begin> <end>")
	Segments map[int][][3]int64
	NoCGI    bool
	CGI      []string
	Answer81 string // when set: the answer to every cmd 81 (e.g. a captured one)
	// RTSPAuth: what the RTSP side accepts: "" RFC 2617 (qop, the full URL as uri),
	// "rfc2069" only the older form without qop, "path" the path as the digest uri
	RTSPAuth string
	Requests []string
	// the clock (TimeNTP): Zone is the TimeZone index (15: UTC+1); DST: the recorder
	// takes a written DeviceTime as daylight saving time; NTP the NTPServerInfo;
	// SyncMode the newer LAPI's mode (nil: the older LAPI); TimeSets the DeviceTimes
	// written
	Zone     int
	DST      bool
	NTP      map[string]any
	SyncMode *int
	TimeSets []int64
	// TimeBodies: the TimeNTP documents written
	TimeBodies []string
	IgnoreTime bool // DeviceTime is never taken
	seq        int64
	// Event subscriptions (System/Event/Subscription): by ID; Renewals counts the PUTs,
	// Unsubscribed the DELETEs; Forget: the device forgot them (a restart), renewals fail
	Subs         map[int64]*Sub
	NextSub      int64
	Renewals     int
	Unsubscribed []int64
	Forget       bool
	// Quirky: as an IPC seen on the wire - the subscription gets ID 65280 (CreatedID too),
	// its answer has a comma before }, and pushes come without Host, as text/plain
	// Json={...}, naming Subscribers/0
	Quirky bool
	HTTP   *httptest.Server
	rtsp   net.Listener
}

// New starts a fake recorder with 4 channels.
func New() *NVR {
	n := &NVR{
		Model: "NVR302-16S", Serial: "210235C3EYA123000123", Firmware: "NVR-B3601.16.10.250101",
		Time: time.Now(),
		Channels: []Channel{{"Wejscie glowne", 1, "192.168.1.31", "IPC322LB", 1}, {"IP Camera 02", 1, "192.168.1.32", "IPC2124", 1},
			{"Rampa", 0, "192.168.1.33", "IPC322LB", 2}, {"", 2, "", "", 0}},
		Disks: `{"LocalHDDNum":2,"LocalHDDList":[{"ID":1,"TotalCapacity":3815447,"RemainCapacity":1907723,"Status":3},` +
			`{"ID":2,"TotalCapacity":0,"RemainCapacity":0,"Status":0}],"SDNum":0,"SDList":[]}`,
		Recorded: map[int][2]int64{},
		FPS:      25,
		Zone:     15,
		NTP:      map[string]any{"Enabled": 1, "AddressType": 0, "Address": "192.168.0.254", "Port": 123, "SynchronizeInterval": 60},
		Config:   []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n\t<Config/>"),
	}
	n.HTTP = httptest.NewServer(http.HandlerFunc(n.serveHTTP))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	n.rtsp = ln
	go n.serveRTSP()
	return n
}

// Addr is host:port of the HTTP side.
func (n *NVR) Addr() string { return strings.TrimPrefix(n.HTTP.URL, "http://") }

// RTSPPort is the port the playback URLs point to.
func (n *NVR) RTSPPort() string { _, p, _ := net.SplitHostPort(n.rtsp.Addr().String()); return p }

func (n *NVR) Close() { n.HTTP.Close(); n.rtsp.Close() }

func checkDigest(h, method string) bool {
	if !strings.HasPrefix(h, "Digest ") {
		return false
	}
	p := digest.ParseParams(h[7:])
	ha1 := digest.MD5Hex(User + ":" + realm + ":" + Pass)
	ha2 := digest.MD5Hex(method + ":" + p["uri"])
	return p["response"] == digest.MD5Hex(ha1+":"+nonce+":"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2)
}

const challenge = `Digest realm="` + realm + `", nonce="` + nonce + `", qop="auth"`

// ResourceCode: how a BCS-P-NVR3204-4K-II names the camera of a channel in main-cgi.
func ResourceCode(ch int) string { return fmt.Sprintf("1100%02x00010000%02x", ch, ch) }

// serveCGI answers main-cgi as captured: no Authorization, the login handle in the body.
func (n *NVR) serveCGI(w http.ResponseWriter, r *http.Request) {
	n.Mu.Lock()
	defer n.Mu.Unlock()
	if n.NoCGI {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var req struct {
		Cmd    int             `json:"cmd"`
		Handle json.RawMessage `json:"u32UserLoginHandle"`
		Codes  []string        `json:"astResourceCode"`
		Begin  int64           `json:"u32Begin"`
		End    int64           `json:"u32End"`
	}
	if !bytes.HasPrefix(b, []byte("json=")) || json.Unmarshal(b[5:], &req) != nil {
		fmt.Fprint(w, `{"code":1,"success":false}`)
		return
	}
	if string(req.Handle) != nonce && string(req.Handle) != `"`+nonce+`"` {
		fmt.Fprint(w, `{"code":10,"success":false}`) // not logged in
		return
	}
	switch req.Cmd {
	case 20:
		n.CGI = append(n.CGI, "20")
		var l []string
		for i, c := range n.Channels {
			l = append(l, fmt.Sprintf(`{"stResourceCode":%q,"szResourceName":%q,"u8bIsOnline":%d,"u8bIsBand":1,"u16ChlCap":0}`,
				ResourceCode(i+1), c.Name, map[bool]int{true: 1}[c.Status == 1]))
		}
		fmt.Fprintf(w, `{"SimpleChList":[%s],"code":0,"success":true}`, strings.Join(l, ", "))
	case 81:
		n.CGI = append(n.CGI, fmt.Sprintf("81 %s %d %d", strings.Join(req.Codes, ","), req.Begin, req.End))
		if n.Answer81 != "" {
			fmt.Fprint(w, n.Answer81)
			return
		}
		var l []string
		for ch, segs := range n.Segments {
			if len(req.Codes) != 1 || req.Codes[0] != ResourceCode(ch) {
				continue
			}
			for _, s := range segs {
				if s[1] <= req.End && s[2] >= req.Begin {
					l = append(l, fmt.Sprintf(`{"szRecordFileName":"","u32RecordType":%d,"u32Begin":%d,"u32End":%d}`, s[0], max(s[1], req.Begin), min(s[2], req.End)))
				}
			}
		}
		fmt.Fprintf(w, `{"recordList":[%s],"code":0,"success":true}`, strings.Join(l, ", "))
	default:
		fmt.Fprint(w, `{"code":2,"success":false}`)
	}
}

var zoneHours = []float64{-12, -11, -10, -9, -8, -7, -6, -5, -4.5, -4, -3.5, -3, -2, -1, 0, 1, 2, 3, 3.5, 4, 4.5, 5, 5.5, 5.75,
	6, 6.5, 7, 8, 9, 9.5, 10, 11, 12, 13}

func (n *NVR) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/cgi-bin/main-cgi" {
		n.serveCGI(w, r)
		return
	}
	if !checkDigest(r.Header.Get("Authorization"), r.Method) {
		w.Header().Set("WWW-Authenticate", challenge)
		w.WriteHeader(401)
		return
	}
	n.Mu.Lock()
	defer n.Mu.Unlock()
	n.Requests = append(n.Requests, r.URL.RequestURI())
	code, data := 0, "null"
	p := r.URL.Path
	q := r.URL.Query()
	var ch int
	fmt.Sscanf(strings.TrimPrefix(p, "/LAPI/V1.0/Channels/"), "%d/", &ch)
	switch {
	case p == "/LAPI/V1.0/System/DeviceInfo":
		data = fmt.Sprintf(`{"ID":0,"DeviceName":"NVR","DeviceModel":%q,"SerialNumber":%q,"FirmwareVersion":%q,"HardewareID":"","BuildDate":{"Year":2017,"Month":10,"Day":28,"Hour":"","Minute":""}}`,
			n.Model, n.Serial, n.Firmware)
	case p == "/LAPI/V1.0/System/Reboot" && r.Method == http.MethodPut:
		n.Rebooted = true
		data = "{}"
	case p == "/LAPI/V1.0/System/Configuration" && !n.OldExport,
		p == "/LAPI/V1.0/System/ConfigurationInfo/Config.xml" && n.OldExport && q.Get("TaskID") == "272660233":
		w.Header().Set("Content-Disposition", "attachment; Filename=Config.xml")
		w.Header().Set("Content-type", "application/octet-stream")
		w.Write(n.Config)
		return
	case p == "/LAPI/V1.0/System/ConfigurationInfo" && n.OldExport:
		data = `{"URL": "/LAPI/V1.0/System/ConfigurationInfo/Config.xml?TaskID=272660233"}`
	case p == "/LAPI/V1.0/System/Event/Subscription" && r.Method == http.MethodPost:
		var b struct {
			AddressType int
			IPAddress   string
			Port        int
			Duration    int
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.IPAddress == "" || b.Port == 0 || b.Duration < 30 || b.Duration > 3600 {
			code = 2
			break
		}
		if n.Subs == nil {
			n.Subs = map[int64]*Sub{}
		}
		n.NextSub++
		id := n.NextSub
		if n.Quirky {
			id = 65279 + n.NextSub
		}
		addr := net.JoinHostPort(b.IPAddress, strconv.Itoa(b.Port))
		n.Subs[id] = &Sub{ID: id, Addr: addr, Duration: b.Duration}
		now := time.Now().Unix()
		if n.Quirky {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "{\n\"Response\": {\n\t\"ResponseURL\": \"%s/%d\",\n\t\"CreatedID\": %d, \n\t\"ResponseCode\": 0,\n\t\"SubResponseCode\": 0,\n"+
				"\t\"ResponseString\": \"Succeed\",\n\t\"StatusCode\": 0,\n\t\"StatusString\": \"Succeed\",\n\t\"Data\": {\n\t\t\"ID\": %d,\n"+
				"\t\t\"Reference\": \"%s/Subscription/Subscribers/%d\",\n\t\t\"CurrentTime\": %d,\n\t\t\"TerminationTime\": %d,\n\n\t}\n}\n",
				p, id, id, id, r.Host, id, now, now+int64(b.Duration))
			return
		}
		data = fmt.Sprintf(`{"ID":%d,"Reference":"%s/Subscription/Subscribers/%d","CurrentTime":%d,"TerminationTime":%d}`, id, addr, id, now, now+int64(b.Duration))
	case strings.HasPrefix(p, "/LAPI/V1.0/System/Event/Subscription/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/LAPI/V1.0/System/Event/Subscription/"), 10, 64)
		sub := n.Subs[id]
		if sub == nil || n.Forget {
			code = 4
			break
		}
		switch r.Method {
		case http.MethodPut:
			n.Renewals++
			now := time.Now().Unix()
			data = fmt.Sprintf(`{"Reference":"%s/Subscription/Subscribers/%d","CurrrntTime":%d,"TerminationTime":%d}`, sub.Addr, id, now, now+int64(sub.Duration))
		case http.MethodDelete:
			delete(n.Subs, id)
			n.Unsubscribed = append(n.Unsubscribed, id)
		}
	case p == "/LAPI/V1.0/System/RunInfo":
		data = `{"Runtime": 5371975}`
	case p == "/LAPI/V1.0/System/TimeNTP" && r.Method == http.MethodPut:
		var t struct {
			TimeZone      int
			DeviceTime    int64
			NTPServerInfo map[string]any
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &t); err != nil || t.DeviceTime == 0 {
			code = 2
			break
		}
		n.TimeBodies = append(n.TimeBodies, string(raw))
		if t.NTPServerInfo != nil {
			n.NTP = t.NTPServerInfo
		}
		n.Zone = t.TimeZone
		// synchronizing with NTP, the recorder keeps its clock (DeviceTime is ignored)
		if n.IgnoreTime || n.SyncMode != nil && *n.SyncMode == 3 || n.SyncMode == nil && fmt.Sprint(n.NTP["Enabled"]) == "1" {
			data = "{}"
			break
		}
		shift := int64(zoneHours[t.TimeZone] * 3600)
		if n.DST {
			shift += 3600
		}
		n.Time, n.TimeSets = time.Unix(t.DeviceTime-shift, 0), append(n.TimeSets, t.DeviceTime)
		data = "{}"
	case p == "/LAPI/V1.0/System/TimeNTP":
		b, _ := json.Marshal(map[string]any{"TimeZone": n.Zone, "DeviceTime": n.Time.Unix(), "DateFormat": 0, "HourFormat": 1, "NTPServerInfo": n.NTP})
		data = string(b)
	case p == "/LAPI/V1.0/System/Time/SyncMode" && n.SyncMode != nil && r.Method == http.MethodPut:
		var m struct{ Mode int }
		json.NewDecoder(r.Body).Decode(&m)
		*n.SyncMode = m.Mode
		data = "{}"
	case p == "/LAPI/V1.0/System/Time/SyncMode" && n.SyncMode != nil:
		data = fmt.Sprintf(`{"Mode":%d}`, *n.SyncMode)
	case p == "/LAPI/V1.0/System/Time":
		data = fmt.Sprintf(`{"TimeZone":"GMT+01:00","DeviceTime":%d}`, n.Time.Unix())
	case p == "/LAPI/V1.0/Channels/System/ChannelDetailInfos":
		var infos []string
		for i, c := range n.Channels {
			if c.Status == 2 {
				infos = append(infos, fmt.Sprintf(`{"ID":%d,"Name":%q,"Status":2,"IsPoEPort":0}`, i+1, c.Name))
				continue
			}
			infos = append(infos, fmt.Sprintf(`{"ID":%d,"Name":%q,"Status":%d,"IsPoEPort":0,"AddressInfo":{"AddressType":0,"Address":%q,"Port":80},`+
				`"Manufacturer":"UNIVIEW","DeviceModel":%q,"OffReason":%d}`, i+1, c.Name, c.Status, c.Address, c.Model, c.OffReason))
		}
		data = fmt.Sprintf(`{"Nums":%d,"DetailInfos":[%s]}`, len(infos), strings.Join(infos, ","))
	case p == "/LAPI/V1.0/Storage/Containers/DetailInfos" && n.OldDisks == "":
		data = n.Disks
	case p == "/LAPI/V1.0/Storage/Containers" && n.OldDisks != "":
		data = n.OldDisks
	case strings.HasSuffix(p, "/LiveStreamURL"):
		var sid int
		fmt.Sscanf(p[strings.Index(p, "/Streams/")+len("/Streams/"):], "%d/", &sid)
		data = fmt.Sprintf(`{"URL":"rtsp://10.1.1.1:554/unicast/c%d/s%d/live"}`, ch, sid)
	case strings.HasSuffix(p, "/Records"):
		b, _ := strconv.ParseInt(q.Get("Begin"), 10, 64)
		e, _ := strconv.ParseInt(q.Get("End"), 10, 64)
		var infos []string
		if rec, ok := n.Recorded[ch]; ok && rec[0] < e && rec[1] > b {
			infos = append(infos, fmt.Sprintf(`{"FileName":"f%d","RecordType":64,"Begin":%d,"End":%d}`, ch, max(rec[0], b), min(rec[1], e)))
		}
		data = fmt.Sprintf(`{"Nums":%d,"RecordInfos":[%s]}`, len(infos), strings.Join(infos, ","))
	case strings.HasSuffix(p, "/Records/DailyDistribution") && strings.Contains(p, "/Streams/0/") != n.OldCalendar:
		// a status per day of the month on the recorder's clock (time.Local): 2 recorded
		y, _ := strconv.Atoi(q.Get("Year"))
		m, _ := strconv.Atoi(q.Get("Month"))
		n.DaysAsked = append(n.DaysAsked, fmt.Sprintf("%d %04d-%02d", ch, y, m))
		first := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.Local)
		days := first.AddDate(0, 1, -1).Day()
		st := make([]string, days)
		for d := 0; d < days; d++ {
			st[d] = "0"
			b, e := first.AddDate(0, 0, d).Unix(), first.AddDate(0, 0, d+1).Unix()
			if rec, ok := n.Recorded[ch]; ok && rec[0] < e && rec[1] > b {
				st[d] = "2"
			}
		}
		data = fmt.Sprintf(`{"Num":%d,"DailyStatus":[%s]}`, days, strings.Join(st, ", "))
		if n.OldCalendar { // as captured: 1 for a recorded day
			data = fmt.Sprintf(`{"Nums":%d,"DailyStatuses":[%s]}`, days, strings.ReplaceAll(strings.Join(st, ", "), "2", "1"))
		}
	case strings.HasSuffix(p, "/RecordURL"):
		// an inner address, as recorders behind NAT report: the client must use its own
		data = fmt.Sprintf(`{"URL":"rtsp://admin:secret@10.1.1.1:%s/c%d/b%s/e%s/replay/"}`, n.RTSPPort(), ch, q.Get("Begin"), q.Get("End"))
	default:
		code = 4
	}
	resp := map[string]any{"Response": map[string]any{"ResponseURL": p, "ResponseCode": code, "SubResponseCode": 0,
		"ResponseString": "Succeed", "StatusCode": 0, "Data": json.RawMessage(data)}}
	json.NewEncoder(w).Encode(resp)
}

func (n *NVR) serveRTSP() {
	for {
		c, err := n.rtsp.Accept()
		if err != nil {
			return
		}
		go n.rtspConn(c)
	}
}

func (n *NVR) rtspConn(c net.Conn) {
	defer c.Close()
	r := textproto.NewReader(bufio.NewReader(c))
	n.Mu.Lock()
	sps, pps := paramSets(n.Video)
	n.Mu.Unlock()
	for {
		line, err := r.ReadLine()
		if err != nil {
			return
		}
		h, _ := r.ReadMIMEHeader()
		if line == "" {
			continue
		}
		method := strings.Fields(line)[0]
		cseq := h.Get("Cseq")
		n.Mu.Lock()
		mode := n.RTSPAuth
		n.Mu.Unlock()
		if !checkRTSPDigest(h.Get("Authorization"), method, strings.Fields(line)[1], mode) {
			fmt.Fprintf(c, "RTSP/1.0 401 Unauthorized\r\nCSeq: %s\r\nWWW-Authenticate: %s\r\n\r\n", cseq, challenge)
			continue
		}
		switch method {
		case "OPTIONS", "GET_PARAMETER", "TEARDOWN":
			fmt.Fprintf(c, "RTSP/1.0 200 OK\r\nCSeq: %s\r\n\r\n", cseq)
			if method == "TEARDOWN" {
				return
			}
		case "DESCRIBE":
			sdp := fmt.Sprintf("v=0\r\nm=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n"+
				"a=fmtp:96 packetization-mode=1;sprop-parameter-sets=%s,%s\r\na=control:trackID=1\r\n",
				base64.StdEncoding.EncodeToString(sps), base64.StdEncoding.EncodeToString(pps))
			base := strings.Fields(line)[1]
			fmt.Fprintf(c, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nContent-Base: %s\r\nContent-Length: %d\r\n\r\n%s", cseq, base, len(sdp), sdp)
		case "SETUP":
			fmt.Fprintf(c, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: 42;timeout=60\r\nTransport: RTP/AVP/TCP;interleaved=0-1\r\n\r\n", cseq)
		case "PLAY":
			n.Mu.Lock()
			n.SpeedSeen = h.Get("Speed")
			video, fps, hold := n.Video, n.FPS, n.Hold
			stamp := beginOf(strings.Fields(line)[1]) - n.StampLead
			n.Plays++
			n.Active++
			n.MaxActive = max(n.MaxActive, n.Active)
			n.Mu.Unlock()
			fmt.Fprintf(c, "RTSP/1.0 200 OK\r\nCSeq: %s\r\nSession: 42\r\n\r\n", cseq)
			go func() {
				defer func() { n.Mu.Lock(); n.Active--; n.Mu.Unlock() }()
				time.Sleep(hold)
				stream(c, video, fps, stamp)
			}()
		}
	}
}

// beginOf: the start of the range of a playback address (…/b<unix>/e<unix>/replay/).
func beginOf(uri string) int64 {
	var b int64
	if _, rest, ok := strings.Cut(uri, "/b"); ok {
		fmt.Sscanf(rest, "%d", &b)
	}
	return b
}

// splitNALs cuts an Annex-B stream into NAL units.
func splitNALs(b []byte) [][]byte {
	var out [][]byte
	for _, part := range bytes.Split(b, []byte{0, 0, 1}) {
		part = bytes.TrimRight(part, "\x00")
		if len(part) > 0 {
			out = append(out, part)
		}
	}
	return out
}

func paramSets(video []byte) (sps, pps []byte) {
	for _, nal := range splitNALs(video) {
		switch nal[0] & 0x1f {
		case 7:
			sps = nal
		case 8:
			pps = nal
		}
	}
	return
}

// rtpFrame: an interleaved RTP packet with the extension of Uniview's recordings: the
// time of the picture (stamp, UTC seconds) in the first 4 of its 12 bytes
func rtpFrame(seq uint16, ts uint32, stamp uint32, marker bool, payload []byte) []byte {
	pkt := make([]byte, 28+len(payload))
	pkt[0], pkt[1] = 0x90, 96
	if marker {
		pkt[1] |= 0x80
	}
	binary.BigEndian.PutUint16(pkt[2:], seq)
	binary.BigEndian.PutUint32(pkt[4:], ts)
	binary.BigEndian.PutUint16(pkt[12:], 0xabac)
	binary.BigEndian.PutUint16(pkt[14:], 3)
	binary.BigEndian.PutUint32(pkt[16:], stamp)
	copy(pkt[28:], payload)
	fr := []byte{'$', 0, 0, 0}
	binary.BigEndian.PutUint16(fr[2:], uint16(len(pkt)))
	return append(fr, pkt...)
}

// stream sends the video as fast as possible: one RTP timestamp per picture, large NAL
// units as FU-A, every packet stamped with the time of its picture (from stamp, the
// first one), then an RTCP BYE.
func stream(c net.Conn, video []byte, fps int, stamp int64) {
	var seq uint16
	ts := uint32(1000000)
	step := uint32(90000 / fps)
	pic := 0
	at := func() uint32 { return uint32(stamp + int64(pic/fps)) }
	w := bufio.NewWriter(c)
	for _, nal := range splitNALs(video) {
		typ := nal[0] & 0x1f
		picture := typ == 1 || typ == 5
		if len(nal) <= 1400 {
			w.Write(rtpFrame(seq, ts, at(), picture, nal))
			seq++
		} else {
			hdr, data := nal[0], nal[1:]
			for i := 0; i < len(data); i += 1400 {
				fu := byte(typ)
				if i == 0 {
					fu |= 0x80
				}
				end := min(i+1400, len(data))
				if end == len(data) {
					fu |= 0x40
				}
				w.Write(rtpFrame(seq, ts, at(), picture && end == len(data), append([]byte{hdr&0xe0 | 28, fu}, data[i:end]...)))
				seq++
			}
		}
		if picture {
			ts += step
			pic++
		}
	}
	w.Write([]byte{'$', 1, 0, 8, 0x81, 203, 0, 1, 0, 0, 0, 1}) // RTCP BYE
	w.Flush()
}

// checkRTSPDigest: the answer in the form the mode wants (see NVR.RTSPAuth).
func checkRTSPDigest(h, method, reqURI, mode string) bool {
	if !strings.HasPrefix(h, "Digest ") {
		return false
	}
	p := digest.ParseParams(h[7:])
	ha1 := digest.MD5Hex(User + ":" + realm + ":" + Pass)
	switch mode {
	case "rfc2069":
		return p["qop"] == "" && p["response"] == digest.MD5Hex(ha1+":"+nonce+":"+digest.MD5Hex(method+":"+p["uri"]))
	case "nothing-fits":
		return false
	case "path":
		u, _ := url.Parse(reqURI)
		return p["uri"] == u.RequestURI() && checkDigest(h, method)
	}
	return p["uri"] == reqURI && checkDigest(h, method)
}

// Sub is one subscription to the events of the fake.
type Sub struct {
	ID       int64
	Addr     string // IPAddress:Port pushed to
	Duration int
}

// Push sends a push to every subscriber, as the devices do: POST of JSON to
// NotificationPath+path; body is the JSON without Reference, which is added. It tells
// how many took it (HTTP 200).
func (n *NVR) Push(path string, body map[string]any) int {
	n.Mu.Lock()
	var subs []Sub
	for _, s := range n.Subs {
		subs = append(subs, *s)
	}
	n.Mu.Unlock()
	n.Mu.Lock()
	quirky := n.Quirky
	n.Mu.Unlock()
	ok := 0
	for _, s := range subs {
		m := map[string]any{"Reference": fmt.Sprintf("%s/Subscription/Subscribers/%d", s.Addr, s.ID)}
		for k, v := range body {
			m[k] = v
		}
		if quirky {
			m["Reference"] = n.Addr() + "/Subscription/Subscribers/0"
			b, _ := json.MarshalIndent(m, "", "\t")
			if quirkyPost(s.Addr, "/LAPI/V1.0/System/Event/Notification/"+path, "Json="+string(b)) {
				ok++
			}
			continue
		}
		b, _ := json.Marshal(m)
		resp, err := http.Post("http://"+s.Addr+"/LAPI/V1.0/System/Event/Notification/"+path, "application/json", bytes.NewReader(b))
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			ok++
		}
	}
	return ok
}

// quirkyPost sends a push as the IPC does: HTTP/1.1 without Host, text/plain, its side
// closed right after the request; true when the answer is 200.
func quirkyPost(addr, path, body string) bool {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "POST %s HTTP/1.1\r\nContent-Length: %d\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s", path, len(body), body)
	if tc, ok := c.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// PushAlarm pushes an alarm (Notification/Alarm) of the type, e.g. MotionAlarmOn.
func (n *NVR) PushAlarm(typ string) int {
	n.Mu.Lock()
	n.seq++
	seq := n.seq
	n.Mu.Unlock()
	return n.Push("Alarm", map[string]any{"AlarmInfo": map[string]any{"AlarmType": typ, "TimeStamp": time.Now().Unix(), "AlarmSeq": seq}})
}

// PushChannel pushes an alarm of a video channel, as a recorder does (AlarmSrcType 8,
// AlarmSrcID the channel).
func (n *NVR) PushChannel(typ string, channel int) int {
	n.Mu.Lock()
	n.seq++
	seq := n.seq
	n.Mu.Unlock()
	return n.Push("Alarm", map[string]any{"AlarmInfo": map[string]any{"AlarmType": typ, "TimeStamp": time.Now().Unix(), "AlarmSeq": seq,
		"AlarmSrcType": 8, "AlarmSrcID": channel}})
}

// Subscribed: the subscriptions of the fake now.
func (n *NVR) Subscribed() int {
	n.Mu.Lock()
	defer n.Mu.Unlock()
	return len(n.Subs)
}
