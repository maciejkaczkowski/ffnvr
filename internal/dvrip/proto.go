// Package dvrip is a client of DVRIP ("Sofia", NETsurveillance), the protocol of
// HiSilicon/Xiongmai recorders (XMeye, iCSee) on TCP port 34567: login, device
// information, channels, disks, searching recordings and downloading them. Based on a
// prototype verified on captured traffic of an NBD6808T-PL (firmware V4.02.R11).
package dvrip

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Message ids (a request's answer has id+1).
const (
	cmdLogin        = 1000
	cmdKeepAlive    = 1006
	cmdSysInfo      = 1020 // SystemInfo, StorageInfo
	cmdConfigGet    = 1042 // dotted paths: NetWork.ChnStatus
	cmdChannelTitle = 1048
	cmdTimeQuery    = 1452 // OPTimeQuery
	cmdFileQuery    = 1440
	cmdPlayDownload = 1420 // DownloadStart / DownloadStop
	cmdPlayClaim    = 1424 // Claim (on the data connection)
	cmdDownloadData = 1426 // the file, in blocks of 32 KB
)

// DefaultPort of the protocol.
const DefaultPort = "34567"

// TimeLayout of times in the protocol (the recorder's local time).
const TimeLayout = "2006-01-02 15:04:05"

// ErrLogin: the recorder refused the login.
var ErrLogin = errors.New("login refused")

// sofiaHash: MD5 of the password folded into 8 alphanumeric characters.
func sofiaHash(password string) string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	sum := md5.Sum([]byte(password))
	out := make([]byte, 0, 8)
	for i := 0; i < len(sum); i += 2 {
		out = append(out, chars[(uint16(sum[i])+uint16(sum[i+1]))%62])
	}
	return string(out)
}

// spaceJSON writes JSON the way the XMeye application does - { "k" : v, "k2" : 1 } -
// because some firmware pick fields by literal patterns (`"FileName" : "`) and lose
// them silently otherwise. Strings are left alone (colons in times).
func spaceJSON(in []byte) []byte {
	out := make([]byte, 0, len(in)+len(in)/4)
	inStr, esc := false, false
	for _, c := range in {
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			out = append(out, c)
		case '{':
			out = append(out, '{', ' ')
		case '}':
			out = append(out, ' ', '}')
		case ':':
			out = append(out, ' ', ':', ' ')
		case ',':
			out = append(out, ',', ' ')
		default:
			out = append(out, c)
		}
	}
	return out
}

type conn struct {
	c       net.Conn
	r       *bufio.Reader
	wmu     sync.Mutex
	session uint32
	seq     uint32
}

func dial(ctx context.Context, addr string) (*conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return &conn{c: c, r: bufio.NewReaderSize(c, 128<<10)}, nil
}

func (c *conn) Close() error { return c.c.Close() }

func (c *conn) sessionHex() string { return fmt.Sprintf("0x%08X", c.session) }

// send: a 20 byte header (little endian: 0xFF, version, session, sequence, message
// id, length) and the JSON body ended by "\n\0".
func (c *conn) send(msgid uint16, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	payload := append(spaceJSON(b), '\n', 0)
	hdr := make([]byte, 20)
	hdr[0] = 0xFF
	c.wmu.Lock()
	defer c.wmu.Unlock()
	binary.LittleEndian.PutUint32(hdr[4:8], c.session)
	binary.LittleEndian.PutUint32(hdr[8:12], c.seq)
	binary.LittleEndian.PutUint16(hdr[14:16], msgid)
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(payload)))
	c.seq++
	c.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = c.c.Write(append(hdr, payload...))
	return err
}

func (c *conn) recv() (msgid uint16, payload []byte, err error) {
	hdr := make([]byte, 20)
	if _, err = io.ReadFull(c.r, hdr); err != nil {
		return
	}
	if hdr[0] != 0xFF {
		return 0, nil, fmt.Errorf("bad header (byte 0 is 0x%02X)", hdr[0])
	}
	msgid = binary.LittleEndian.Uint16(hdr[14:16])
	n := binary.LittleEndian.Uint32(hdr[16:20])
	if n > 64<<20 {
		return 0, nil, fmt.Errorf("message of %d bytes", n)
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(c.r, payload)
	return
}

func trimJSON(b []byte) []byte { return []byte(strings.TrimRight(string(b), "\x00\r\n ")) }

// call sends a request and decodes the answer with the matching id (others, e.g. late
// keep-alive answers, are skipped). Ret other than 100 is an error.
func (c *conn) call(msgid uint16, body any, timeout time.Duration) (map[string]any, []byte, error) {
	if err := c.send(msgid, body); err != nil {
		return nil, nil, err
	}
	c.c.SetReadDeadline(time.Now().Add(timeout))
	defer c.c.SetReadDeadline(time.Time{})
	for {
		id, payload, err := c.recv()
		if err != nil {
			return nil, nil, err
		}
		if id != msgid+1 {
			continue
		}
		payload = trimJSON(payload)
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil, payload, fmt.Errorf("answer %d is not JSON: %.80q", id, payload)
		}
		if r, ok := m["Ret"].(float64); ok && int(r) != 100 && int(r) != 0 {
			return m, payload, fmt.Errorf("Ret=%d%s", int(r), retMeaning(int(r)))
		}
		return m, payload, nil
	}
}

func retMeaning(r int) string {
	switch r {
	case 203:
		return " (account locked after too many attempts)"
	case 205:
		return " (wrong username or password)"
	case 106, 107:
		return " (no permission)"
	}
	return ""
}

// get asks for one named object.
func (c *conn) get(msgid uint16, name string) (map[string]any, []byte, error) {
	return c.call(msgid, map[string]any{"Name": name, "SessionID": c.sessionHex()}, 15*time.Second)
}

func parseHexU32(s string) (uint32, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return 0, errors.New("empty")
	}
	v, err := strconv.ParseUint(s, 16, 64)
	return uint32(v), err
}

// login opens the control connection of a session.
func login(ctx context.Context, addr, user, pass string) (*conn, error) {
	c, err := dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	m, _, err := c.call(cmdLogin, map[string]any{
		"EncryptType": "MD5", "LoginType": "DVRIP-Web", "UserName": user, "PassWord": sofiaHash(pass),
	}, 15*time.Second)
	if err != nil {
		c.Close()
		if strings.HasPrefix(err.Error(), "Ret=") {
			return nil, fmt.Errorf("%w: %v", ErrLogin, err)
		}
		return nil, fmt.Errorf("login: %w", err)
	}
	sid, _ := m["SessionID"].(string)
	if c.session, err = parseHexU32(sid); err != nil {
		c.Close()
		return nil, fmt.Errorf("login: bad SessionID %q", sid)
	}
	return c, nil
}

// keepAlive keeps the session until ctx ends (the recorder drops it after ~20 s).
func (c *conn) keepAlive(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.send(cmdKeepAlive, map[string]any{"Name": "KeepAlive", "SessionID": c.sessionHex()})
		}
	}
}
