package dvrip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Client talks to one recorder; every call logs in anew.
type Client struct {
	addr       string
	user, pass string
	// Idle: silence on the data connection that ends a file (0: 8 s).
	Idle time.Duration
}

// NewClient accepts "192.168.1.10" or "192.168.1.10:34567".
func NewClient(addr, user, pass string) (*Client, error) {
	if addr == "" || strings.ContainsAny(addr, "/ ") {
		return nil, fmt.Errorf("invalid address %q", addr)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, DefaultPort)
	}
	return &Client{addr: addr, user: user, pass: pass}, nil
}

func (c *Client) idle() time.Duration {
	if c.Idle > 0 {
		return c.Idle
	}
	return 8 * time.Second
}

// Session is a logged in control connection for several questions.
type Session struct{ c *conn }

// Login opens a session; Close it.
func (c *Client) Login(ctx context.Context) (*Session, error) {
	cn, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return nil, err
	}
	return &Session{cn}, nil
}

func (s *Session) Close() error { return s.c.Close() }

// Raw asks for a named object and returns the answer as it is (diagnostics).
func (s *Session) Raw(msgid uint16, name string) (string, error) {
	_, raw, err := s.c.get(msgid, name)
	return string(raw), err
}

// Message ids for Raw.
const (
	SysInfo      = cmdSysInfo
	ConfigGet    = cmdConfigGet
	ChannelTitle = cmdChannelTitle
	TimeQuery    = cmdTimeQuery
)

// Device describes the recorder (SystemInfo).
type Device struct {
	HardWare, SoftWareVersion, SerialNo, BuildTime string
	DigChannel                                     int
	Channels                                       int // digital + analog (VideoInChannel)
	RunMinutes                                     int // DeviceRunTime
}

func (s *Session) Device() (Device, error) {
	m, _, err := s.c.get(cmdSysInfo, "SystemInfo")
	if err != nil {
		return Device{}, err
	}
	si, _ := m["SystemInfo"].(map[string]any)
	str := func(k string) string { v, _ := si[k].(string); return strings.TrimSpace(v) }
	d := Device{HardWare: str("HardWare"), SoftWareVersion: str("SoftWareVersion"), SerialNo: str("SerialNo"), BuildTime: str("BuildTime")}
	if n, ok := si["DigChannel"].(float64); ok {
		d.DigChannel = int(n)
	}
	d.Channels = d.DigChannel
	if n, ok := si["VideoInChannel"].(float64); ok {
		d.Channels += int(n)
	}
	if rt, ok := si["DeviceRunTime"].(string); ok {
		if n, err := parseHexU32(rt); err == nil {
			d.RunMinutes = int(n)
		}
	}
	return d, nil
}

// Channel of the recorder.
type Channel struct {
	Number int    // from 1
	Title  string // ChannelTitle, else ChnName
	// Status of NetWork.ChnStatus: Connected, NoConfig, ...; "" when unknown
	Status         string
	CurRes, MaxRes string // resolution now and at most, e.g. 720P, 1080P
}

// Channels lists the channels with their titles and state. statesErr: the state is
// unknown (NetWork.ChnStatus not answered), the titles still count.
func (s *Session) Channels() (chs []Channel, statesErr error, err error) {
	var titles []string
	if m, _, e := s.c.get(cmdChannelTitle, "ChannelTitle"); e == nil {
		if arr, ok := m["ChannelTitle"].([]any); ok {
			for _, t := range arr {
				titles = append(titles, strings.TrimSpace(fmt.Sprint(t)))
			}
		}
	} else {
		err = e
	}
	m, _, e := s.c.get(cmdConfigGet, "NetWork.ChnStatus")
	arr, _ := m["NetWork.ChnStatus"].([]any)
	// the answer has room for 32 channels; the recorder has as many as SystemInfo
	// tells (digital + analog), the rest are empty entries
	if d, err := s.Device(); err == nil && d.Channels > 0 {
		if len(arr) > d.Channels {
			arr = arr[:d.Channels]
		}
		if len(titles) > d.Channels {
			titles = titles[:d.Channels]
		}
	}
	for len(arr) > 0 { // and without SystemInfo: the empty entries at the end
		last, _ := arr[len(arr)-1].(map[string]any)
		if st, _ := last["Status"].(string); st != "" || fmt.Sprint(last["ChnName"]) != "" {
			break
		}
		arr = arr[:len(arr)-1]
	}
	if e != nil || len(arr) == 0 {
		if len(titles) == 0 {
			if e == nil {
				e = err
			}
			return nil, nil, fmt.Errorf("no channels: %v", e)
		}
		if e == nil {
			e = errors.New("NetWork.ChnStatus is empty")
		}
		for i, t := range titles {
			chs = append(chs, Channel{Number: i + 1, Title: t})
		}
		return chs, fmt.Errorf("NetWork.ChnStatus: %w", e), nil
	}
	for i, it := range arr {
		mm, _ := it.(map[string]any)
		ch := Channel{Number: i + 1}
		ch.Status, _ = mm["Status"].(string)
		ch.CurRes, _ = mm["CurRes"].(string)
		ch.MaxRes, _ = mm["MaxRes"].(string)
		if i < len(titles) {
			ch.Title = titles[i]
		}
		if ch.Title == "" {
			ch.Title, _ = mm["ChnName"].(string)
		}
		chs = append(chs, ch)
	}
	return chs, nil, nil
}

// Partition of a disk, in MB.
type Partition struct {
	Disk, Part        int
	TotalMB, RemainMB int64
}

// Storage lists the mounted partitions (StorageInfo).
func (s *Session) Storage() ([]Partition, error) {
	m, _, err := s.c.get(cmdSysInfo, "StorageInfo")
	if err != nil {
		return nil, err
	}
	disks, _ := m["StorageInfo"].([]any)
	var out []Partition
	for di, d := range disks {
		dm, _ := d.(map[string]any)
		parts, _ := dm["Partition"].([]any)
		for pi, p := range parts {
			pm, _ := p.(map[string]any)
			tot, _ := parseHexU32(fmt.Sprint(pm["TotalSpace"]))
			rem, _ := parseHexU32(fmt.Sprint(pm["RemainSpace"]))
			if tot == 0 && rem == 0 {
				continue // not mounted
			}
			out = append(out, Partition{di, pi, int64(tot), int64(rem)})
		}
	}
	return out, nil
}

// Time reads the recorder's clock (OPTimeQuery).
func (s *Session) Time() (time.Time, error) {
	m, _, err := s.c.get(cmdTimeQuery, "OPTimeQuery")
	if err != nil {
		return time.Time{}, err
	}
	v, _ := m["OPTimeQuery"].(string)
	return time.ParseInLocation(TimeLayout, v, time.Local)
}

// File is one recording found.
type File struct {
	BeginTime  string `json:"BeginTime"`
	EndTime    string `json:"EndTime"`
	FileName   string `json:"FileName"`
	DiskNo     int    `json:"DiskNo"`
	SerialNo   int    `json:"SerialNo"`
	FileLength string `json:"FileLength"`
}

// Files lists the recordings of a channel (from 0) between from and to; the answers
// come in pages, the next page starts at the last file's beginning.
func (s *Session) Files(channel int, from, to time.Time, fileType string) ([]File, error) {
	seen := map[string]bool{}
	var all []File
	cursor := from.Format(TimeLayout)
	for page := 0; page < 1000; page++ {
		m, _, err := s.c.call(cmdFileQuery, map[string]any{
			"Name": "OPFileQuery", "SessionID": s.c.sessionHex(),
			"OPFileQuery": map[string]any{
				"BeginTime": cursor, "EndTime": to.Format(TimeLayout), "Channel": channel,
				"DriverTypeMask": "0x0000FFFF", "Event": "*", "Type": fileType, "StreamType": "0x00000000",
			},
		}, 20*time.Second)
		if err != nil {
			if strings.HasPrefix(err.Error(), "Ret=") && len(all) == 0 {
				return nil, nil // the usual answer when nothing is recorded
			}
			return all, err
		}
		raw, _ := json.Marshal(m["OPFileQuery"])
		var batch []File
		json.Unmarshal(raw, &batch)
		added := 0
		for _, f := range batch {
			key := f.FileName + "|" + f.BeginTime
			if f.FileName == "" || seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, f)
			added++
			cursor = f.BeginTime
		}
		if added == 0 {
			break
		}
	}
	return all, nil
}
