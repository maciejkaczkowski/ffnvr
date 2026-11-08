// Package dvriptest is a fake DVRIP (XMeye) recorder for tests: login with the Sofia
// hash, SystemInfo/StorageInfo/ChannelTitle/ChnStatus, the file search, downloads of
// files over a second connection claimed by the session and playing them (Start,
// Pause, Locate, Continue, Stop) at a pace, as the XMeye client was captured doing.
package dvriptest

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	User = "admin"
	Pass = "secret"
)

// Recording of the fake: a file of a channel, played from Video (H.264) with times
// counted from Begin.
type Recording struct {
	Channel    int // from 0
	Begin, End time.Time
	Video      []byte
	FPS        int
}

// NVR is the state of the fake; change it under Mu.
type NVR struct {
	Mu         sync.Mutex
	Titles     []string
	Status     []string // NetWork.ChnStatus Status per channel; nil: not answered
	Recordings []Recording
	PageSize   int // files per OPFileQuery answer
	ExtraEmpty int // empty entries after the channels in NetWork.ChnStatus (32 in all on an NBD6808T-PL)
	Rebooted   bool
	// the clock: Skew is the recorder's clock against time.Now (OPTimeSetting moves it),
	// TimeSets the times set; NTP is NetWork.NetNTP; TimeMin System.TimeZone
	Skew     time.Duration
	TimeSets []string
	NTP      map[string]any
	TimeMin  int
	Zip      []byte   // the configuration export
	Requests []string // names of requests seen
	// PlaySpeed: how many times faster than real time a file is played at Fast 0 (0: 20)
	PlaySpeed float64
	// CameraSkew: the camera's clock against the recorder's, in the frames' stamps (in
	// a captured session the camera was 1:55:22 behind)
	CameraSkew time.Duration
	Actions    []string              // OPPlayBack actions seen: "Start", "Locate 2026-11-03 05:28:08", ...
	Playing    int                   // files being played now…
	MaxPlaying int                   // …and the most at once
	claimed    map[uint32]*claimConn // the data connection of a session waiting for DownloadStart or Start
	sessions   uint32
	ln         net.Listener
}

type claimConn struct {
	c   net.Conn
	wmu sync.Mutex
}

func New() *NVR {
	n := &NVR{
		Titles:   []string{"Brama", "D02", "D03", "Magazyn"},
		Status:   []string{"Connected", "NoConfig", "NoConfig", "Disconnected"},
		PageSize: 2,
		NTP: map[string]any{"Enable": true, "TimeZone": 13, "UpdatePeriod": 60, "Server": map[string]any{
			"Address": "0x00000000", "Anonymity": false, "Name": "192.168.0.254", "Password": "", "Port": 123, "UserName": ""}},
		TimeMin: -60,
		claimed: map[uint32]*claimConn{},
		Zip:     append([]byte("PK\x03\x04\x14\x00ppp/pppoe-enable3"), []byte("PK\x05\x06\x00\x00\x00\x00")...),
	}
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
func (n *NVR) Close()       { n.ln.Close() }

func sofiaHash(p string) string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	sum := md5.Sum([]byte(p))
	var out []byte
	for i := 0; i < 16; i += 2 {
		out = append(out, chars[(uint16(sum[i])+uint16(sum[i+1]))%62])
	}
	return string(out)
}

func packet(msgid uint16, payload []byte) []byte {
	return packetOf(0x1A, msgid, payload)
}

func packetOf(session uint32, msgid uint16, payload []byte) []byte {
	hdr := make([]byte, 20)
	hdr[0] = 0xFF
	binary.LittleEndian.PutUint32(hdr[4:8], session)
	binary.LittleEndian.PutUint16(hdr[14:16], msgid)
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(payload)))
	return append(hdr, payload...)
}

func reply(msgid uint16, v any) []byte {
	b, _ := json.Marshal(v)
	return packet(msgid, append(b, '\n', 0))
}

const layout = "2006-01-02 15:04:05"

func (n *NVR) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	var pl *player // the file this control connection plays
	defer func() {
		if pl != nil {
			pl.stop()
		}
	}()
	for {
		hdr := make([]byte, 20)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return
		}
		body := make([]byte, binary.LittleEndian.Uint32(hdr[16:20]))
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		msgid := binary.LittleEndian.Uint16(hdr[14:16])
		session := binary.LittleEndian.Uint32(hdr[4:8])
		raw := strings.TrimRight(string(body), "\x00\n")
		if !strings.Contains(raw, `" : `) {
			c.Write(reply(msgid+1, map[string]any{"Ret": 101})) // the spaced format is required
			continue
		}
		var req map[string]any
		json.Unmarshal([]byte(raw), &req)
		name, _ := req["Name"].(string)
		n.Mu.Lock()
		n.Requests = append(n.Requests, fmt.Sprintf("%d %s", msgid, name))
		n.Mu.Unlock()
		switch msgid {
		case 1000:
			if req["UserName"] != User || req["PassWord"] != sofiaHash(Pass) {
				c.Write(reply(1001, map[string]any{"Ret": 205}))
				return
			}
			n.Mu.Lock()
			id := 0x1A + n.sessions // a session of its own for every login
			n.sessions++
			n.Mu.Unlock()
			c.Write(reply(1001, map[string]any{"Ret": 100, "SessionID": fmt.Sprintf("0x%08X", id), "AliveInterval": 21}))
		case 1450:
			if op, _ := req["OPMachine"].(map[string]any); op["Action"] == "Reboot" {
				n.Mu.Lock()
				n.Rebooted = true
				n.Mu.Unlock()
			}
			if v, ok := req["OPTimeSetting"].(string); ok {
				t, err := time.ParseInLocation(layout, v, time.Local)
				if err != nil {
					c.Write(reply(1451, map[string]any{"Name": "", "Ret": 101}))
					continue
				}
				n.Mu.Lock()
				n.Skew, n.TimeSets = time.Until(t), append(n.TimeSets, v)
				n.Mu.Unlock()
			}
			c.Write(reply(1451, map[string]any{"Name": "", "Ret": 100, "SessionID": "0x0000001A"}))
		case 1542:
			n.Mu.Lock()
			z := n.Zip
			n.Mu.Unlock()
			c.Write(packet(1543, z))
		case 1006:
			c.Write(reply(1007, map[string]any{"Ret": 100, "Name": "KeepAlive"}))
		case 1020:
			switch name {
			case "SystemInfo":
				c.Write(reply(1021, map[string]any{"Ret": 100, "Name": "SystemInfo", "SystemInfo": map[string]any{
					"HardWare": "NBD6808T-PL", "SoftWareVersion": "V4.02.R11.34531119.12001.130000.00000",
					"SerialNo": "a1b2c3d4e5f6a7b8", "BuildTime": "2015-12-04 10:36:00", "DigChannel": len(n.Titles),
					"DeviceRunTime": "0x00000A3C"}}))
			case "StorageInfo":
				c.Write(reply(1021, map[string]any{"Ret": 100, "Name": "StorageInfo", "StorageInfo": []any{
					map[string]any{"PartNumber": 2, "Partition": []any{
						map[string]any{"TotalSpace": "0x000E8DE8", "RemainSpace": "0x00081B4C", "Status": 0},
						map[string]any{"TotalSpace": "0x00000000", "RemainSpace": "0x00000000"},
					}}}}))
			}
		case 1040:
			n.Mu.Lock()
			if v, ok := req["NetWork.NetNTP"].(map[string]any); ok && name == "NetWork.NetNTP" {
				n.NTP = v
			}
			n.Mu.Unlock()
			c.Write(reply(1041, map[string]any{"Name": "", "Ret": 100}))
		case 1042:
			n.Mu.Lock()
			status := n.Status
			ntp, tmin := n.NTP, n.TimeMin
			n.Mu.Unlock()
			switch name {
			case "NetWork.NetNTP":
				c.Write(reply(1043, map[string]any{"Ret": 100, "Name": name, name: ntp}))
				continue
			case "System.TimeZone":
				c.Write(reply(1043, map[string]any{"Ret": 100, "Name": name, name: map[string]any{"FirstUserTimeZone": 0, "timeMin": tmin}}))
				continue
			case "General.Location":
				c.Write(reply(1043, map[string]any{"Ret": 100, "Name": name, name: map[string]any{"DSTRule": "On", "DateFormat": "YYMMDD"}}))
				continue
			}
			if status == nil {
				c.Write(reply(1043, map[string]any{"Ret": 607}))
				continue
			}
			var arr []any
			for i, s := range status {
				arr = append(arr, map[string]any{"ChnName": fmt.Sprintf("D%02d", i+1), "Status": s, "CurRes": "720P", "MaxRes": "1080P"})
			}
			for i := 0; i < n.ExtraEmpty; i++ {
				arr = append(arr, map[string]any{"ChnName": "", "CurRes": "", "MaxRes": "", "Status": ""})
			}
			c.Write(reply(1043, map[string]any{"Ret": 100, "Name": "NetWork.ChnStatus", "NetWork.ChnStatus": arr}))
		case 1048:
			c.Write(reply(1049, map[string]any{"Ret": 100, "Name": "ChannelTitle", "ChannelTitle": n.Titles}))
		case 1452:
			n.Mu.Lock()
			skew := n.Skew
			n.Mu.Unlock()
			c.Write(reply(1453, map[string]any{"Ret": 100, "Name": "OPTimeQuery", "OPTimeQuery": time.Now().Add(skew).Format(layout)}))
		case 1440:
			q, _ := req["OPFileQuery"].(map[string]any)
			from, _ := time.ParseInLocation(layout, q["BeginTime"].(string), time.Local)
			to, _ := time.ParseInLocation(layout, q["EndTime"].(string), time.Local)
			ch := int(q["Channel"].(float64))
			var files []any
			n.Mu.Lock()
			for _, rec := range n.Recordings {
				if rec.Channel == ch && rec.Begin.Before(to) && rec.End.After(from) && len(files) < n.PageSize {
					files = append(files, fileJSON(rec))
				}
			}
			n.Mu.Unlock()
			if len(files) == 0 {
				c.Write(reply(1441, map[string]any{"Ret": 119, "Name": "OPFileQuery"})) // nothing found
				continue
			}
			c.Write(reply(1441, map[string]any{"Ret": 100, "Name": "OPFileQuery", "OPFileQuery": files}))
		case 1424: // Claim: this connection receives the session's next download or playing
			cc := &claimConn{c: c}
			n.Mu.Lock()
			n.claimed[session] = cc
			n.Mu.Unlock()
			c.Write(reply(1425, map[string]any{"Ret": 100}))
		case 1420:
			op, _ := req["OPPlayBack"].(map[string]any)
			action, _ := op["Action"].(string)
			start, _ := op["StartTime"].(string)
			n.Mu.Lock()
			par, _ := op["Parameter"].(map[string]any)
			value, _ := par["Value"].(float64)
			if action == "Locate" {
				n.Actions = append(n.Actions, action+" "+start)
			} else if action == "Fast" {
				n.Actions = append(n.Actions, fmt.Sprintf("Fast %d", int(value)))
			} else if action != "DownloadStart" && action != "DownloadStop" {
				n.Actions = append(n.Actions, action)
			}
			n.Mu.Unlock()
			c.Write(reply(1421, map[string]any{"Ret": 100}))
			switch action {
			case "Pause":
				if pl != nil {
					pl.pause(true)
				}
				continue
			case "Continue":
				if pl != nil {
					pl.pause(false)
				}
				continue
			case "Locate":
				if pl != nil {
					t, _ := time.ParseInLocation(layout, start, time.Local)
					pl.locate(t)
				}
				continue
			case "Fast":
				if pl != nil {
					pl.fast(int(value))
				}
				continue
			case "Stop":
				if pl != nil {
					pl.stop()
					pl = nil
				}
				continue
			case "Start", "DownloadStart":
			default:
				continue
			}
			n.Mu.Lock()
			cc := n.claimed[session]
			delete(n.claimed, session)
			var rec *Recording
			for i := range n.Recordings {
				if fileName(n.Recordings[i]) == par["FileName"] {
					rec = &n.Recordings[i]
				}
			}
			n.Mu.Unlock()
			if cc == nil || rec == nil {
				continue // without Claim first, nothing is sent
			}
			if action == "DownloadStart" {
				go stream(cc.c, *rec)
				continue
			}
			if pl != nil {
				pl.stop()
			}
			pl = n.play(cc.c, *rec)
		}
	}
}

func fileName(r Recording) string {
	return fmt.Sprintf("/idea0/%s/%03d/%s-%s[R][@40][0].h264", r.Begin.Format("2006-01-02"), r.Channel+1,
		r.Begin.Format("15.04.05"), r.End.Format("15.04.05"))
}

func fileJSON(r Recording) map[string]any {
	return map[string]any{"BeginTime": r.Begin.Format(layout), "EndTime": r.End.Format(layout), "FileName": fileName(r),
		"DiskNo": 0, "SerialNo": 0, "FileLength": "0x00002795"}
}

func packTime(t time.Time) uint32 {
	return uint32(t.Second()) | uint32(t.Minute())<<6 | uint32(t.Hour())<<12 | uint32(t.Day())<<17 |
		uint32(t.Month())<<22 | uint32(t.Year()-2000)<<26
}

// Sofia frames the H.264 of a recording: an I frame header with fps and time before
// every picture with parameter sets, a P frame header before the others, some audio.
func Sofia(video []byte, fps int, begin time.Time) []byte {
	var out []byte
	for _, f := range sofiaFrames(video, fps, begin) {
		out = append(out, f.data...)
	}
	return out
}

// sofiaFrame: a picture with its Sofia header (and the audio after it).
type sofiaFrame struct {
	data []byte
	key  bool
	at   time.Time // when it was recorded
}

func sofiaFrames(video []byte, fps int, begin time.Time) []sofiaFrame {
	var out []sofiaFrame
	var au []byte
	frameNo := 0
	idr := false
	flush := func() {
		if len(au) == 0 {
			return
		}
		at := begin.Add(time.Duration(frameNo) * time.Second / time.Duration(fps))
		var b []byte
		if idr {
			h := []byte{0, 0, 1, 0xFC, 2, byte(fps), 20, 15, 0, 0, 0, 0, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(h[8:], packTime(at))
			binary.LittleEndian.PutUint32(h[12:], uint32(len(au)))
			b = append(append(b, h...), au...)
		} else {
			h := []byte{0, 0, 1, 0xFD, 0, 0, 0, 0}
			binary.LittleEndian.PutUint32(h[4:], uint32(len(au)))
			b = append(append(b, h...), au...)
		}
		if frameNo%5 == 0 { // a little audio between the pictures
			b = append(b, 0, 0, 1, 0xFA, 0x0e, 0x02, 4, 0, 0xd5, 0xd5, 0xd5, 0xd5)
		}
		out = append(out, sofiaFrame{data: b, key: idr, at: at})
		frameNo++
		au, idr = nil, false
	}
	for _, nal := range bytes.Split(video, []byte{0, 0, 1}) {
		nal = bytes.TrimRight(nal, "\x00")
		if len(nal) == 0 {
			continue
		}
		t := nal[0] & 0x1f
		idr = idr || t == 7 || t == 5
		au = append(append(au, 0, 0, 0, 1), nal...)
		if t == 1 || t == 5 {
			flush()
		}
	}
	flush()
	return out
}

func stream(c net.Conn, rec Recording) {
	data := Sofia(rec.Video, rec.FPS, rec.Begin)
	for i := 0; i < len(data); i += 32 << 10 {
		if _, err := c.Write(packet(1426, data[i:min(i+32<<10, len(data))])); err != nil {
			return
		}
	}
	// then silence: the client takes it as the end of the file
}
