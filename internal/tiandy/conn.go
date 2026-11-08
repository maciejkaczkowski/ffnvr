// Package tiandy is a client of the native protocol of Tiandy recorders (port 3001,
// e.g. TC-R3120): login, the information the recorder sends after it (model, firmware,
// disks, channels) and downloading recordings by time range. Based on a prototype
// reconstructed from captured traffic of the original client.
package tiandy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// The protocol: frames of a 20 byte header and a body. Command frames (f1f5eaf5) carry
// text: records of tab separated fields ending with "\n\n\n". Data frames (f1f5eaf9)
// carry the recording, behind an 8 byte "NVS\0" + IP header.
var (
	magicCmd  = []byte{0xf1, 0xf5, 0xea, 0xf5}
	magicData = []byte{0xf1, 0xf5, 0xea, 0xf9}
)

// DefaultPort of the protocol.
const DefaultPort = "3001"

// ErrLogin: the recorder refused the username or password.
var ErrLogin = errors.New("login refused: wrong username or password")

type conn struct {
	c     net.Conn
	r     *bufio.Reader
	ip    string // the recorder's address as it goes into every message
	seq   uint16
	queue [][]string
}

func dial(ctx context.Context, addr string) (*conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	return &conn{c: c, r: bufio.NewReaderSize(c, 256<<10), ip: host}, nil
}

func (c *conn) Close() error { return c.c.Close() }

func (c *conn) sendCmd(payload []byte) error {
	hdr := make([]byte, 20)
	copy(hdr, magicCmd)
	binary.LittleEndian.PutUint16(hdr[4:6], c.seq)
	binary.LittleEndian.PutUint16(hdr[6:8], uint16(len(payload)+20))
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(payload)))
	c.seq++
	c.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := c.c.Write(append(hdr, payload...))
	return err
}

func (c *conn) send(parts ...string) error {
	return c.sendCmd([]byte(c.ip + "\t" + strings.Join(parts, "\t") + "\n\n\n"))
}

// frame reads one frame: the text of a command frame, or the data of a data frame.
func (c *conn) frame() (isData bool, body []byte, err error) {
	hdr := make([]byte, 20)
	if _, err = io.ReadFull(c.r, hdr); err != nil {
		return
	}
	var size int
	switch {
	case bytes.Equal(hdr[:4], magicCmd):
		size = int(binary.LittleEndian.Uint16(hdr[6:8]))
	case bytes.Equal(hdr[:4], magicData):
		size, isData = int(binary.LittleEndian.Uint16(hdr[14:16])), true
	default:
		return false, nil, fmt.Errorf("bad frame signature % x", hdr[:4])
	}
	body = make([]byte, max(size-20, 0))
	if _, err = io.ReadFull(c.r, body); err != nil {
		return
	}
	if isData && len(body) >= 8 && bytes.Equal(body[:4], []byte("NVS\x00")) {
		body = body[8:]
	}
	return isData, body, nil
}

// recv returns the next text record (data frames are skipped). Records without the
// address in front get it, so field positions are the same for all.
func (c *conn) recv() ([]string, error) {
	for len(c.queue) == 0 {
		isData, body, err := c.frame()
		if err != nil {
			return nil, err
		}
		if isData {
			continue
		}
		for _, block := range strings.Split(strings.TrimSpace(string(body)), "\n\n\n") {
			if block = strings.Trim(block, "\x00\r\n"); block == "" {
				continue
			}
			parts := strings.Split(block, "\t")
			if !strings.Contains(parts[0], ".") {
				parts = append([]string{c.ip}, parts...)
			}
			c.queue = append(c.queue, parts)
		}
	}
	m := c.queue[0]
	c.queue = c.queue[1:]
	return m, nil
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// login: the first LOGON is answered with LOGONFAILED 3 and a key; then, on a new
// connection, the user name (lower case) and the password are sent as MD5 with the key.
func login(ctx context.Context, addr, user, pass string) (*conn, error) {
	c, err := dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	c.c.SetReadDeadline(time.Now().Add(15 * time.Second))
	c.send("IP", "USER", "LOGON", b64(user), b64(pass), "", "1", "UTF-8", "805306367", "1")
	resp, err := c.recv()
	c.Close()
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	if len(resp) < 9 || resp[4] != "LOGONFAILED" || resp[5] != "3" {
		return nil, fmt.Errorf("login: unexpected answer %q", strings.Join(resp, " | "))
	}
	key, err := base64.StdEncoding.DecodeString(resp[8])
	if err != nil {
		return nil, fmt.Errorf("login: bad key: %w", err)
	}
	if c, err = dial(ctx, addr); err != nil {
		return nil, err
	}
	hu := md5.Sum(append([]byte(strings.ToLower(user)), key...))
	hp := md5.Sum(append([]byte(pass), key...))
	c.c.SetReadDeadline(time.Now().Add(15 * time.Second))
	c.send("IP", "USER", "LOGON", base64.StdEncoding.EncodeToString(hu[:]),
		base64.StdEncoding.EncodeToString(hp[:]), "", "1", "UTF-8", "1", "1")
	resp, err = c.recv()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("login: %w", err)
	}
	if len(resp) > 4 && resp[4] == "LOGONFAILED" {
		c.Close()
		return nil, ErrLogin
	}
	c.queue = append([][]string{resp}, c.queue...)
	c.c.SetReadDeadline(time.Time{})
	return c, nil
}

func idxOf(msg []string, tok string) int {
	for i, f := range msg {
		if f == tok {
			return i
		}
	}
	return -1
}
