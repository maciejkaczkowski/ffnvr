package dvrip

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Alarms of XMeye recorders reach ffnvr in two ways:
//
// Alarm server (push): the recorder connects to TCP port 15002 of a server set in its
// menu (Network services, ALARM SERVER, protocol GENERAL) and sends one message - the
// usual 20 byte header and JSON - per alarm:
//
//	{ "Address" : "0x0B01A8C0", "Channel" : 0, "Descrip" : "", "Event" : "MotionDetect",
//	  "SerialID" : "1234567890abcdef", "StartTime" : "2026-11-02 12:00:00",
//	  "Status" : "Start", "Type" : "Alarm" }
//
// Address is the recorder's own IPv4 address, little endian. With "send logs" it also
// sends its log (Type Log). Firmware are not alike: some close the connection after
// one message, some send several, some leave out the header.
//
// Guard (pull): on a logged in session, message 1500 asks for the alarms; they come as
// message 1504 on the same connection ({ "AlarmInfo" : { "Channel" : 0, "Event" :
// "VideoMotion", "StartTime" : ..., "Status" : "Start" }, "Name" : "AlarmInfo" }), as
// the XMeye client does it (and python-dvr). Nothing needs to be set on the recorder.

const (
	cmdGuard     = 1500
	cmdAlarmInfo = 1504
)

// Alarm is one alarm message.
type Alarm struct {
	Event   string // MotionDetect, VideoMotion, LossDetect, BlindDetect, StorageFailure, HumanDetect...
	Status  string // Start, Stop ("" for log entries)
	Channel int    // from 1; 0: the recorder itself
	Type    string // Alarm, Log
	Serial  string // SerialID (push only)
	Address string // the recorder's IPv4 address (push only)
	Time    time.Time
	Descrip string
	Raw     string
}

// ParseAlarm reads the JSON of a pushed alarm or of an AlarmInfo message.
func ParseAlarm(payload []byte) (Alarm, error) {
	raw := strings.TrimSpace(string(trimJSON(payload)))
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Alarm{Raw: raw}, fmt.Errorf("not JSON: %.80q", raw)
	}
	if inner, ok := m["AlarmInfo"].(map[string]any); ok {
		m = inner
	}
	str := func(k string) string {
		switch v := m[k].(type) {
		case string:
			return strings.TrimSpace(v)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		return ""
	}
	a := Alarm{Event: str("Event"), Status: str("Status"), Type: str("Type"), Serial: str("SerialID"), Descrip: str("Descrip"), Raw: raw}
	if a.Event == "" {
		return a, errors.New("no Event")
	}
	if ch, err := strconv.Atoi(str("Channel")); err == nil && ch >= 0 {
		a.Channel = ch + 1
	}
	if a.Type == "Log" {
		a.Channel = 0 // log entries are about the recorder
	}
	if t, err := time.ParseInLocation(TimeLayout, str("StartTime"), time.Local); err == nil {
		a.Time = t
	}
	if ip, ok := hexAddress(str("Address")); ok {
		a.Address = ip
	}
	return a, nil
}

// hexAddress turns "0x0B01A8C0" into "192.168.1.11".
func hexAddress(s string) (string, bool) {
	v, err := parseHexU32(s)
	if err != nil || v == 0 {
		return "", false
	}
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return net.IP(b[:]).String(), true
}

// ReadPushed reads the alarms of one connection of a recorder to the alarm server and
// calls fn for every message (with the error when one cannot be read). idle ends a
// connection that stays silent.
func ReadPushed(c net.Conn, idle time.Duration, fn func(Alarm, error)) {
	br := bufio.NewReaderSize(c, 16<<10)
	for {
		c.SetReadDeadline(time.Now().Add(idle))
		b, err := br.Peek(1)
		if err != nil {
			return
		}
		if b[0] == 0xFF {
			hdr := make([]byte, 20)
			if _, err := io.ReadFull(br, hdr); err != nil {
				return
			}
			n := binary.LittleEndian.Uint32(hdr[16:20])
			if n > 1<<20 {
				fn(Alarm{}, fmt.Errorf("message of %d bytes", n))
				return
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(br, payload); err != nil {
				return
			}
			fn(ParseAlarm(payload))
			continue
		}
		// no header: JSON objects one after another (or anything else: reported)
		obj, err := readObject(br)
		if len(strings.TrimSpace(obj)) > 0 {
			fn(ParseAlarm([]byte(obj)))
		}
		if err != nil {
			return
		}
	}
}

// readObject reads up to the end of one JSON object (or up to 64 KB of garbage).
func readObject(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	depth, started, inStr, esc := 0, false, false, false
	for sb.Len() < 64<<10 {
		c, err := br.ReadByte()
		if err != nil {
			return sb.String(), err
		}
		if !started {
			if c != '{' {
				if c > ' ' && c != 0 {
					sb.WriteByte(c) // garbage before the object: reported as not JSON
				}
				continue
			}
			sb.Reset()
			started = true
		}
		sb.WriteByte(c)
		switch {
		case inStr && esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case inStr && c == '"':
			inStr = false
		case inStr:
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return sb.String(), nil
			}
		}
	}
	return sb.String(), errors.New("too long")
}

// Alarms logs in, asks for the alarms (guard) and calls fn for every one until ctx
// ends or the connection breaks. connected (may be nil) is called once the recorder
// accepted the guard.
func (c *Client) Alarms(ctx context.Context, connected func(), fn func(Alarm)) error {
	cn, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return err
	}
	defer cn.Close()
	stop := context.AfterFunc(ctx, func() { cn.Close() })
	defer stop()
	if _, _, err := cn.call(cmdGuard, map[string]any{"Name": "", "SessionID": cn.sessionHex()}, 15*time.Second); err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	if connected != nil {
		connected()
	}
	kctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cn.keepAlive(kctx)
	for {
		// keep-alive answers come every 15 s: a minute of silence is a dead connection
		cn.c.SetReadDeadline(time.Now().Add(time.Minute))
		id, payload, err := cn.recv()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if id != cmdAlarmInfo {
			continue
		}
		if a, err := ParseAlarm(payload); err == nil {
			fn(a)
		}
	}
}
