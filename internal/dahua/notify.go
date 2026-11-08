package dahua

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// The notification channel of the web interface: a WebSocket (/webappoverwebsocket,
// the JSON-RPC session as the cookie WebClientHttpSessionID). Every message is a
// binary frame:
//
//	uint16 little endian: the length of the header
//	header: {"TotalSize":<length of the body>,"Type":"SubScribe"|"Request"|"Response"|"Notification",...}
//	body:   JSON-RPC, e.g. {"method":"client.notifyUpgraderStream","params":{...},"callback":101}

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type notifier struct {
	conn net.Conn
	br   *bufio.Reader
	sid  string
	wmu  sync.Mutex
	id   int
}

// dialNotify opens the notification channel of a JSON-RPC session.
func (c *Client) dialNotify(ctx context.Context, sid string) (*notifier, error) {
	host := c.base.Host
	tlsOn := c.base.Scheme == "https"
	if c.base.Port() == "" {
		if tlsOn {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if tlsOn {
		tc := tls.Client(conn, &tls.Config{ServerName: c.base.Hostname(), InsecureSkipVerify: true})
		if err := tc.HandshakeContext(dctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	var key [16]byte
	rand.Read(key[:])
	k := base64.StdEncoding.EncodeToString(key[:])
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(conn, "GET /webappoverwebsocket HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nOrigin: %s://%s\r\nCookie: WebClientHttpSessionID=%s\r\n\r\n",
		c.base.Host, k, c.base.Scheme, c.base.Host, sid)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("webappoverwebsocket: %w", err)
	}
	resp.Body.Close()
	sum := sha1.Sum([]byte(k + wsGUID))
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, fmt.Errorf("webappoverwebsocket: HTTP %d", resp.StatusCode)
	}
	conn.SetDeadline(time.Time{})
	return &notifier{conn: conn, br: br, sid: sid}, nil
}

func (n *notifier) Close() error { return n.conn.Close() }

// subscribe sends a subscription (e.g. upgrader.attachState); the answer comes
// through listen.
func (n *notifier) subscribe(method string, params any) error {
	n.wmu.Lock()
	n.id++
	id := n.id
	n.wmu.Unlock()
	body, _ := json.Marshal(map[string]any{"method": method, "params": params, "id": id, "session": n.sid})
	head, _ := json.Marshal(map[string]any{"TotalSize": len(body), "Type": "SubScribe", "SessionID": n.sid, "URL": "SubscribeNotify"})
	msg := binary.LittleEndian.AppendUint16(nil, uint16(len(head)))
	msg = append(append(msg, head...), body...)
	return n.write(0x2, msg)
}

// write sends one masked frame (a client must mask).
func (n *notifier) write(op byte, payload []byte) error {
	n.wmu.Lock()
	defer n.wmu.Unlock()
	frame := []byte{0x80 | op}
	switch l := len(payload); {
	case l < 126:
		frame = append(frame, 0x80|byte(l))
	case l <= 0xffff:
		frame = append(frame, 0x80|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(l))
	default:
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(l))
	}
	var mask [4]byte
	rand.Read(mask[:])
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	n.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := n.conn.Write(frame)
	return err
}

// readMessage returns the payload of the next data message (answering pings).
func (n *notifier) readMessage() ([]byte, error) {
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(n.br, h[:]); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		l := uint64(h[1] & 0x7f)
		switch l {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(n.br, b[:]); err != nil {
				return nil, err
			}
			l = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(n.br, b[:]); err != nil {
				return nil, err
			}
			l = binary.BigEndian.Uint64(b[:])
		}
		if l > 16<<20 {
			return nil, errors.New("webappoverwebsocket: frame too large")
		}
		var mask []byte
		if h[1]&0x80 != 0 {
			mask = make([]byte, 4)
			if _, err := io.ReadFull(n.br, mask); err != nil {
				return nil, err
			}
		}
		p := make([]byte, l)
		if _, err := io.ReadFull(n.br, p); err != nil {
			return nil, err
		}
		if mask != nil {
			for i := range p {
				p[i] ^= mask[i%4]
			}
		}
		switch op {
		case 0x8: // close
			return nil, io.EOF
		case 0x9: // ping
			n.write(0xA, p)
			continue
		case 0xA: // pong
			continue
		}
		msg = append(msg, p...)
		if fin {
			return msg, nil
		}
	}
}

// listen reads the channel until it closes, giving every notification to fn.
func (n *notifier) listen(fn func(method string, params map[string]any)) {
	for {
		msg, err := n.readMessage()
		if err != nil {
			return
		}
		for len(msg) >= 2 { // a message may carry several
			hl := int(binary.LittleEndian.Uint16(msg))
			if 2+hl > len(msg) {
				break
			}
			var head struct{ TotalSize int }
			json.Unmarshal(msg[2:2+hl], &head)
			rest := msg[2+hl:]
			size := head.TotalSize
			if size <= 0 || size > len(rest) {
				size = len(rest)
			}
			var body struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(rest[:size], &body) == nil && body.Method != "" {
				fn(body.Method, body.Params)
			}
			msg = rest[size:]
		}
	}
}
