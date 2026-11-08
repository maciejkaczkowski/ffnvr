// Package tiandytest is a fake Tiandy recorder for tests: the native protocol with its
// two step login, the welcome burst (records taken from a TC-R3120) and downloads by
// time range in the .sdv container.
package tiandytest

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"time"

	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"ffnvr/internal/digest"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
)

const (
	User = "Admin"
	Pass = "secret"
	key  = "K3yK3y"
)

var (
	magicCmd  = []byte{0xf1, 0xf5, 0xea, 0xf5}
	magicData = []byte{0xf1, 0xf5, 0xea, 0xf9}
)

// NVR is the state of the fake; change it under Mu.
type NVR struct {
	Mu sync.Mutex
	// Burst: the records sent after the login (fields separated by tabs, without the
	// address); the CMDID record is added.
	Burst []string
	// Recorded: channel (from 1) -> the elementary H.264 stream played for any range
	// of that channel; missing: the recorder stays silent.
	Recorded map[int][]byte
	// SilentConnects: how many CONNECTs are ignored first (the race of real devices)
	SilentConnects int
	Connects       []string // the TM fields of CONNECTs seen
	// Stamp: the pictures carry the recorder's stamps (the TDST SEI), Stamp of them a
	// second, the first one of the second the range of the CONNECT begins with; and
	// what is played stops at the end of the range, like the real thing does not
	Stamp int
	// StampLead: the stamps begin this many seconds before the range (a key frame before it)
	StampLead int64
	// Hold: how long a stream waits before its data
	Hold        time.Duration
	Logins      int      // logins that succeeded
	StreamIDs   []string // the virtual channels of the CONNECTs
	CmdIDs      []string // their CMDIDs
	Active      int      // streams being sent now…
	MaxActive   int      // …and the most at once
	Disconnects []string // the virtual channels of DISCONNECTs
	// Searched: channel (from 1) -> the recorded spans the search tells: start and end
	// ("2006-01-02 15:04:05") and the type ("": Alarm), cut at midnight like the real thing
	Searched map[int][][3]string
	// SearchBusy: how many searches are refused with "Device Error" first
	SearchBusy int
	// SearchLimit: an answer longer than this is refused with "Device Error" (0: none),
	// like a day of much motion on a TC-R3105; TypeFilter: recType is obeyed
	SearchLimit int
	TypeFilter  bool
	Searches    []string // "track <ch> <year>-<month>" and "day <date> chanNo <n>"
	ln          net.Listener
	// the web interface (ISAPI, Digest): the answers, "" for 404 (the time: now)
	HddXML, ChannelsXML, TimeXML string
	HTTP                         *httptest.Server
	Rebooted                     bool
	Box                          []byte // the configuration export (.box)
	// the clock (when TimeXML is ""): Skew against time.Now, TimeMode (""=manual), Zone
	// (""=CST-1:00:00); the NTP server of /CGI/System/Network/NTP and its switch
	// (CommonEnable 45076; NoSwitch: not answered); TimeSets: the PUTs
	Skew        time.Duration
	TimeMode    string
	Zone        string
	NTPServer   string
	NTPPort     int
	NTPInterval int
	NTPOn       bool
	NoSwitch    bool
	TimeSets    []string
	token       string
	// conns: the logged-in connections, which Push sends to
	conns map[net.Conn]bool
}

const realm = "tiandy"

func (n *NVR) serveHTTP(w http.ResponseWriter, r *http.Request) {
	h := r.Header.Get("Authorization")
	p := digest.ParseParams(strings.TrimPrefix(h, "Digest "))
	ha1 := digest.MD5Hex(User + ":" + realm + ":" + Pass)
	ha2 := digest.MD5Hex(r.Method + ":" + p["uri"])
	if !strings.HasPrefix(h, "Digest ") || p["response"] != digest.MD5Hex(ha1+":"+"nnn"+":"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2) {
		w.Header().Set("WWW-Authenticate", `Digest realm="`+realm+`", nonce="nnn", qop="auth"`)
		w.WriteHeader(401)
		return
	}
	n.Mu.Lock()
	defer n.Mu.Unlock()
	switch {
	case r.URL.Path == "/ISAPI/System/reboot" && r.Method == http.MethodPut:
		n.Rebooted = true
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus version="2.0"><requestURL>/ISAPI/System/reboot</requestURL><statusCode>1</statusCode><statusString>OK</statusString><subStatusCode>ok</subStatusCode></ResponseStatus>`)
		return
	case r.URL.Path == "/CGI/UploadCheck" && r.Method == http.MethodGet:
		n.token = "NdvuNhaHL0ST5EOwsdG6Bz2Q"
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><uploadCheck><token>`+n.token+`</token></uploadCheck>`)
		return
	case r.URL.Path == "/CGI/UploadCheck":
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus version="2.0"><statusCode>1</statusCode><statusString>OK</statusString></ResponseStatus>`)
		return
	case strings.HasPrefix(r.URL.Path, "/CGI/System/S/configData/export/") && r.Method == http.MethodPost:
		r.ParseForm()
		if n.token == "" || r.PostForm.Get("csrf_token") != n.token {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment")
		w.Write(n.Box)
		return
	}
	if r.Method == http.MethodPut && (r.URL.Path == "/ISAPI/System/time" || r.URL.Path == "/CGI/System/Network/NTP") {
		raw, _ := io.ReadAll(r.Body)
		var t struct {
			Mode, Local, Zone, Server string
			Port, Interval            int
		}
		var doc struct {
			TimeMode  string `xml:"timeMode"`
			LocalTime string `xml:"localTime"`
			TimeZone  string `xml:"timeZone"`
			NTPServer string `xml:"NTPServer"`
			PortNO    int    `xml:"PortNO"`
			Inerval   int    `xml:"Inerval"`
		}
		if xml.Unmarshal(raw, &doc) != nil {
			w.WriteHeader(400)
			return
		}
		t.Mode, t.Local, t.Zone = doc.TimeMode, doc.LocalTime, doc.TimeZone
		if r.URL.Path == "/CGI/System/Network/NTP" {
			n.NTPServer, n.NTPPort, n.NTPInterval = doc.NTPServer, doc.PortNO, doc.Inerval
			n.TimeSets = append(n.TimeSets, fmt.Sprintf("ntp %s:%d/%d", doc.NTPServer, doc.PortNO, doc.Inerval))
		} else {
			if t.Local != "" {
				lt, err := time.ParseInLocation("2006-01-02T15:04:05", t.Local, time.Local)
				if err != nil {
					w.WriteHeader(400)
					return
				}
				n.Skew = time.Until(lt)
			}
			n.TimeMode, n.Zone = t.Mode, t.Zone
			n.TimeSets = append(n.TimeSets, fmt.Sprintf("time %s %q %s", t.Mode, t.Local, t.Zone))
		}
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus version="2.0"><requestURL>`+r.URL.Path+`</requestURL><statusCode>1</statusCode><statusString>OK</statusString><subStatusCode>ok</subStatusCode></ResponseStatus>`)
		return
	}
	if r.URL.Path == "/CGI/System/CommonEnable/channels/1/Type/45076" && !n.NoSwitch {
		if r.Method == http.MethodPut {
			var ce struct {
				Type   int  `xml:"type"`
				Enable bool `xml:"enable"`
			}
			raw, _ := io.ReadAll(r.Body)
			if xml.Unmarshal(raw, &ce) != nil || ce.Type != 45076 {
				w.WriteHeader(400)
				return
			}
			n.NTPOn = ce.Enable
			n.TimeSets = append(n.TimeSets, fmt.Sprintf("switch %v", ce.Enable))
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus version="2.0"><requestURL>`+r.URL.Path+`</requestURL><statusCode>1</statusCode><statusString>OK</statusString><subStatusCode>ok</subStatusCode></ResponseStatus>`)
			return
		}
		fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<CommonEnable>\n<type>45076</type>\n<enable>%v</enable>\n<value>0</value>\n</CommonEnable>\n", n.NTPOn)
		return
	}
	if r.URL.Path == "/CGI/System/Network/NTP" && n.NTPServer != "" {
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><NTP><NTPServer>%s</NTPServer><PortNO>%d</PortNO><Inerval>%d</Inerval></NTP>`, n.NTPServer, n.NTPPort, n.NTPInterval)
		return
	}
	if r.Method == http.MethodPost && (r.URL.Path == "/ISAPI/ContentMgmt/dailySearch" || strings.HasSuffix(r.URL.Path, "/dailyDistribution")) {
		n.search(w, r)
		return
	}
	body := map[string]string{
		"/ISAPI/ContentMgmt/Storage/hdd/":               n.HddXML,
		"/ISAPI/ContentMgmt/InputProxy/channels/status": n.ChannelsXML,
		"/ISAPI/System/time":                            n.TimeXML,
	}[r.URL.Path]
	if r.URL.Path == "/ISAPI/System/time" && body == "" { // the clock: now, unless set
		mode, zone := n.TimeMode, n.Zone
		if mode == "" {
			mode = "manual"
		}
		if zone == "" {
			zone = "CST-1:00:00"
		}
		body = `<?xml version="1.0" encoding="UTF-8"?><Time><timeMode>` + mode + `</timeMode><localTime>` + time.Now().Add(n.Skew).Format("2006-01-02T15:04:05") +
			`</localTime><timeZone>` + zone + `</timeZone></Time>`
	}
	if body == "" {
		w.WriteHeader(404)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, body)
}

// HTTPPort of the web interface.
func (n *NVR) HTTPPort() int {
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(n.HTTP.URL, "http://"))
	port := 0
	fmt.Sscan(p, &port)
	return port
}

// New starts a fake recorder with the channels and disk of a TC-R3120.
func New() *NVR {
	n := &NVR{
		Burst: []string{
			"IP\tINNER\tVERSION\tDVRS_V13.13.20.20250102_SP01\tGUI_V13.13.0.20240924\tV4.4.3.240802.AFC5D089",
			"INNER\tPARAGET\tDEVMODEL\tTC-R3120 SPEC:I/B/V3.0",
			"IP\tINNER\tPARAGET\tBARCODE\t482608C3387A00255\t482608C3387A0025532732",
			// the disk records are unreliable: here the same disk twice
			"PROXY\tINNER\tPARAGET\tDISK\tINFO\t0\t7577600\t46080\t2\t4\t0",
			"PROXY\tINNER\tPARAGET\tDISK\tINFO\t0\t7577600\t46080\t2\t4\t0",
			"IP\tINNER\tPARAGET\tDEVCOMMONNAME\t0\t0:3\tSite16\tPlac żółty\tChannel 3\tChannel 4",
			"IP\tINNER\tPARAGET\tDIGITALCHANNEL\t0\t1\t0\t192.168.0.80\t\t0\t3000\t0\t1",
			"IP\tINNER\tPARAGET\tDIGITALCHANNEL\t1\t1\t0\t192.168.0.78\t\t0\t3000\t0\t1",
			"IP\tINNER\tPARAGET\tDIGITALCHANNEL\t2\t1\t0\t192.168.0.79\t\t0\t3000\t0\t1",
			"IP\tINNER\tPARAGET\tDIGITALCHANNEL\t3\t0\t0\t0.0.0.0\t\t0\t3000\t0\t1",
			"PROXY\tINNER\tCMD\tCHANNELCONNECT\t0:1\t1:1\t2:0\t3:0\t4:0\t5:0",
		},
		Recorded: map[int][]byte{},
		Box:      []byte("TPKG001\x00\xf0\x1d\x00\x00ProductModule"),
		// as captured from a TC-R3120: a full, recording disk reported as "formating"
		HddXML: "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<hddList>\r\n<hdd>\r\n<id>0</id>\r\n<hddName>SATA1</hddName>\r\n<hddType>SATA</hddType>\r\n" +
			"<capacity>7577600</capacity>\r\n<usedSpace>7531520</usedSpace>\r\n<freeSpace>46080</freeSpace>\r\n<status>formating</status>\r\n<property>Record</property>\r\n</hdd>\r\n</hddList>\r\n",
		ChannelsXML: `<?xml version="1.0" encoding="UTF-8"?><InputProxyChannelStatusList version="1.0">` +
			`<InputProxyChannelStatus><id>1</id><sourceInputPortDescriptor><channelName>Site16</channelName><ipAddress>192.168.0.80</ipAddress><Mac>3C:DA:6D:18:9C:FB</Mac><DevModel>TC-C34WS SPEC:I5/E/Y/2.8mm</DevModel><Barcode>480677C0466A00153</Barcode><adminPortNo>3000</adminPortNo></sourceInputPortDescriptor><online>true</online></InputProxyChannelStatus>` +
			`<InputProxyChannelStatus><id>2</id><sourceInputPortDescriptor><channelName>Plac</channelName><ipAddress>192.168.0.78</ipAddress><DevModel>TC-C32</DevModel></sourceInputPortDescriptor><online>true</online></InputProxyChannelStatus>` +
			`<InputProxyChannelStatus><id>3</id><sourceInputPortDescriptor><channelName>Channel 3</channelName><ipAddress>192.168.0.79</ipAddress></sourceInputPortDescriptor><online>false</online></InputProxyChannelStatus>` +
			`</InputProxyChannelStatusList>`,
	}
	n.HTTP = httptest.NewServer(http.HandlerFunc(n.serveHTTP))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	n.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serve(c)
		}
	}()
	return n
}

func (n *NVR) Addr() string { return n.ln.Addr().String() }
func (n *NVR) Close()       { n.ln.Close(); n.HTTP.Close() }

// Push sends records (fields separated by tabs, records by "\n\n\n" as the device
// writes them) in one command frame to every logged-in connection; it returns how
// many got it.
func (n *NVR) Push(records ...string) int {
	var b strings.Builder
	for _, r := range records {
		b.WriteString(r + "\n\n\n")
	}
	n.Mu.Lock()
	defer n.Mu.Unlock()
	for c := range n.conns {
		c.Write(cmdFrame(b.String()))
	}
	return len(n.conns)
}

// Logged: how many connections are logged in now.
func (n *NVR) Logged() int { n.Mu.Lock(); defer n.Mu.Unlock(); return len(n.conns) }

func cmdFrame(text string) []byte {
	hdr := make([]byte, 20)
	copy(hdr, magicCmd)
	binary.LittleEndian.PutUint16(hdr[6:8], uint16(len(text)+20))
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(text)))
	return append(hdr, text...)
}

func dataFrame(chunk []byte) []byte {
	body := append([]byte("NVS\x00\x7f\x00\x00\x01"), chunk...)
	hdr := make([]byte, 20)
	copy(hdr, magicData)
	binary.LittleEndian.PutUint16(hdr[14:16], uint16(len(body)+20))
	return append(hdr, body...)
}

func md5b64(s string) string {
	h := md5.Sum([]byte(s))
	return base64.StdEncoding.EncodeToString(h[:])
}

func (n *NVR) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	host := "127.0.0.1"
	send := func(text string) { c.Write(cmdFrame(text + "\n\n\n")) }
	for {
		hdr := make([]byte, 20)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return
		}
		body := make([]byte, int(binary.LittleEndian.Uint16(hdr[6:8]))-20)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		if len(body) == 0 {
			continue // keep-alive
		}
		f := strings.Split(strings.TrimSpace(string(body)), "\t")
		switch {
		case len(f) > 5 && f[3] == "LOGON" && f[9] == "805306367":
			// the first step: refused, with the key
			send(host + "\tIP\tUSER\tLOGON\tLOGONFAILED\t3\t0\t0\t" + base64.StdEncoding.EncodeToString([]byte(key)))
		case len(f) > 5 && f[3] == "LOGON":
			if f[4] != md5b64(strings.ToLower(User)+key) || f[5] != md5b64(Pass+key) {
				send(host + "\tIP\tUSER\tLOGON\tLOGONFAILED\t1")
				return
			}
			send(host + "\tIP\tUSER\tLOGON\tLOGONSUCCESS")
			n.Mu.Lock()
			n.Logins++
			burst := append([]string{"IP\tINNER\tCMDID\t7777"}, n.Burst...)
			if n.conns == nil {
				n.conns = map[net.Conn]bool{}
			}
			n.conns[c] = true
			n.Mu.Unlock()
			defer func() { n.Mu.Lock(); delete(n.conns, c); n.Mu.Unlock() }()
			var b bytes.Buffer
			for _, rec := range burst {
				b.WriteString(rec + "\n\n\n")
			}
			c.Write(cmdFrame(b.String()))
		case len(f) > 6 && f[3] == "CONNECT":
			var from, to int64
			var ch int
			fmt.Sscanf(f[7], "TM:%d:%d:%d", &from, &to, &ch)
			n.Mu.Lock()
			n.Connects = append(n.Connects, f[7])
			n.StreamIDs = append(n.StreamIDs, f[4])
			n.CmdIDs = append(n.CmdIDs, f[6])
			silent := n.SilentConnects > 0
			if silent {
				n.SilentConnects--
			}
			video, ok := n.Recorded[ch+1]
			stamp, hold, lead := n.Stamp, n.Hold, n.StampLead
			if !silent && ok {
				n.Active++
				n.MaxActive = max(n.MaxActive, n.Active)
			}
			n.Mu.Unlock()
			if silent || !ok {
				continue // say nothing, like the real thing
			}
			time.Sleep(hold)
			sdv := SDV(video)
			if stamp > 0 {
				sdv = SDVStamped(video, from-lead, stamp)
			}
			for i := 0; i < len(sdv); i += 30000 { // chunks cut through the frames
				c.Write(dataFrame(sdv[i:min(i+30000, len(sdv))]))
			}
			if stamp == 0 {
				send(host + "\tIP\tINNER\tDISCONNECT\t" + f[4])
			}
			n.Mu.Lock()
			n.Active--
			n.Mu.Unlock()
		case len(f) > 4 && f[3] == "DISCONNECT":
			n.Mu.Lock()
			n.Disconnects = append(n.Disconnects, f[4])
			n.Mu.Unlock()
		}
	}
}

// SDVStamped is SDV with the stamps of a Tiandy recorder: before every picture an SEI
// (TDST) with its time, perSecond pictures a second from first (wall clock seconds as
// UTC, the TM of a CONNECT).
func SDVStamped(h264 []byte, first int64, perSecond int) []byte {
	pics := 0
	return sdv(h264, func() []byte {
		t := first + int64(pics/perSecond)
		pics++
		p := []byte("TDST\x01\x00\x08\x00\xd3\xfe\x04\x00")
		p = binary.LittleEndian.AppendUint32(p, uint32(t))
		return append([]byte{0, 0, 0, 1, 6, 0xf0, 0x10}, append(Escape(p), 0x80)...)
	})
}

// Escape adds the emulation prevention bytes to the payload of a NAL unit.
func Escape(p []byte) []byte {
	var out []byte
	zeros := 0
	for _, c := range p {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func (n *NVR) search(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	body := string(b)
	tag := func(name string) string {
		_, v, _ := strings.Cut(body, "<"+name+">")
		v, _, _ = strings.Cut(v, "</"+name+">")
		return strings.TrimSpace(v)
	}
	refuse := func() {
		w.WriteHeader(400)
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><ResponseStatus version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">`+
			"\r\n<requestURL>"+r.URL.Path+"</requestURL>\r\n<statusCode>3</statusCode>\r\n<statusString>Device Error</statusString>\r\n<subStatusCode>deviceError</subStatusCode>\r\n</ResponseStatus>\r\n")
	}
	if n.SearchBusy > 0 {
		n.SearchBusy--
		refuse()
		return
	}
	parse := func(v string) time.Time { t, _ := time.ParseInLocation("2006-01-02 15:04:05", v, time.Local); return t }
	w.Header().Set("Content-Type", `application/xml; charset="UTF-8"`)
	if strings.HasSuffix(r.URL.Path, "/dailyDistribution") {
		var ch, year, month int
		fmt.Sscanf(r.URL.Path, "/ISAPI/ContentMgmt/record/tracks/%d/type/1/dailyDistribution", &ch)
		fmt.Sscan(tag("year"), &year)
		fmt.Sscan(tag("monthOfYear"), &month)
		n.Searches = append(n.Searches, fmt.Sprintf("track %d %d-%02d", ch, year, month))
		first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
		var out strings.Builder
		out.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<trackDailyDistribution>\r\n<dayList>\r\n")
		for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
			rec := false
			for _, sp := range n.Searched[ch] {
				if parse(sp[0]).Before(d.AddDate(0, 0, 1)) && parse(sp[1]).After(d) {
					rec = true
				}
			}
			fmt.Fprintf(&out, "<day>\r\n<id>%d</id>\r\n<dayOfMonth>%d</dayOfMonth>\r\n<record>%v</record>\r\n</day>\r\n", d.Day(), d.Day(), rec)
		}
		out.WriteString("</dayList>\r\n</trackDailyDistribution>\r\n")
		io.WriteString(w, out.String())
		return
	}
	var chanNo int
	fmt.Sscan(tag("chanNo"), &chanNo)
	recType := tag("recType")
	n.Searches = append(n.Searches, fmt.Sprintf("day %s chanNo %d", tag("day"), chanNo))
	if recType != "ALL" {
		n.Searches[len(n.Searches)-1] += " " + recType
	}
	day, _ := time.ParseInLocation("2006-01-02", tag("day"), time.Local)
	end := day.AddDate(0, 0, 1)
	var list []string
	for _, sp := range n.Searched[chanNo+1] {
		a, b := parse(sp[0]), parse(sp[1])
		if !a.Before(end) || !b.After(day) {
			continue
		}
		typ := sp[2]
		if typ == "" {
			typ = "Alarm"
		}
		if n.TypeFilter && recType != "ALL" && recType != typ {
			continue
		}
		a, b = max64(a, day), min64(b, end.Add(-time.Second))
		list = append(list, fmt.Sprintf("<matchElement>\r\n<chanNo>%d</chanNo>\r\n<streamType>main</streamType>\r\n<type>%s</type>\r\n"+
			"<timeSpan>\r\n<startTime>%s</startTime>\r\n<endTime>%s</endTime>\r\n</timeSpan>\r\n</matchElement>\r\n",
			chanNo, typ, a.Format("2006-01-02T15:04:05Z"), b.Format("2006-01-02T15:04:05Z")))
	}
	for i := len(list) - 1; i > 0; i -= 2 { // not in order, like the real thing
		list[i], list[i-1] = list[i-1], list[i]
	}
	answer := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<CDailySearchResult>\r\n<numOfMatches>%d</numOfMatches>\r\n<matchList>\r\n%s</matchList>\r\n</CDailySearchResult>\r\n",
		len(list), strings.Join(list, ""))
	if n.SearchLimit > 0 && len(answer) > n.SearchLimit {
		refuse()
		return
	}
	io.WriteString(w, answer)
}

func max64(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func min64(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// SDV wraps an H.264 stream in the .sdv container: a stream description, then every
// picture behind a 32 byte header that starts with 00 00 00 02 and holds a false start
// code.
func SDV(h264 []byte) []byte { return sdv(h264, nil) }

func sdv(h264 []byte, sei func() []byte) []byte {
	out := []byte("SDV stream description 1.0\x00")
	var au []byte
	flush := func() {
		if len(au) == 0 {
			return
		}
		hdr := []byte{0, 0, 0, 2}
		hdr = append(hdr, bytes.Repeat([]byte{0xaa}, 20)...)
		hdr = append(hdr, 0, 0, 0, 1, 2, 0xff, 0xff, 0) // the false NAL unit
		if sei != nil {
			hdr = append(hdr, sei()...)
		}
		out = append(append(out, hdr...), au...)
		au = nil
	}
	for _, nal := range bytes.Split(h264, []byte{0, 0, 1}) {
		nal = bytes.TrimRight(nal, "\x00")
		if len(nal) == 0 {
			continue
		}
		au = append(append(au, 0, 0, 0, 1), nal...)
		if t := nal[0] & 0x1f; t == 1 || t == 5 { // a picture ends the record
			flush()
		}
	}
	flush()
	return out
}
