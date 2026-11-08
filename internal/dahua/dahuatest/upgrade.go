package dahuatest

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// wsConn is the server side of the notification WebSocket.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex
	proc int
}

// send writes one message: the 2-byte header length, the header, the body.
func (c *wsConn) send(typ string, body any) {
	b, _ := json.Marshal(body)
	h, _ := json.Marshal(map[string]any{"TotalSize": len(b), "Type": typ})
	msg := binary.LittleEndian.AppendUint16(nil, uint16(len(h)))
	msg = append(append(msg, h...), b...)
	frame := []byte{0x82}
	if len(msg) < 126 {
		frame = append(frame, byte(len(msg)))
	} else {
		frame = append(frame, 126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(msg)))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn.Write(append(frame, msg...))
}

// read returns the payload of the next (masked) client frame.
func (c *wsConn) read() (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return 0, nil, err
	}
	l := int(h[1] & 0x7f)
	if l == 126 {
		var b [2]byte
		io.ReadFull(c.br, b[:])
		l = int(binary.BigEndian.Uint16(b[:]))
	}
	var mask [4]byte
	io.ReadFull(c.br, mask[:])
	p := make([]byte, l)
	if _, err := io.ReadFull(c.br, p); err != nil {
		return 0, nil, err
	}
	for i := range p {
		p[i] ^= mask[i%4]
	}
	return h[0] & 0x0f, p, nil
}

// serveNotify: /webappoverwebsocket, the subscriptions of the JSON-RPC session S1.
func (n *NVR) serveNotify(w http.ResponseWriter, r *http.Request) {
	n.Mu.Lock()
	n.Requests = append(n.Requests, "WS "+r.URL.Path)
	refuse := n.NoNotify
	n.Mu.Unlock()
	if c, err := r.Cookie("WebClientHttpSessionID"); err != nil || c.Value != "S1" || refuse {
		w.WriteHeader(401)
		return
	}
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: WebSocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	c := &wsConn{conn: conn, br: brw.Reader}
	go func() {
		defer conn.Close()
		for {
			op, p, err := c.read()
			if err != nil || op == 0x8 {
				n.Mu.Lock()
				for i, s := range n.subs {
					if s == c {
						n.subs = append(n.subs[:i], n.subs[i+1:]...)
						break
					}
				}
				n.Mu.Unlock()
				return
			}
			if op != 0x2 || len(p) < 2 {
				continue
			}
			hl := int(binary.LittleEndian.Uint16(p))
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
				ID     int            `json:"id"`
			}
			if 2+hl > len(p) || json.Unmarshal(p[2+hl:], &req) != nil {
				continue
			}
			n.Mu.Lock()
			n.Requests = append(n.Requests, "WS "+req.Method)
			if req.Method == "upgrader.attachState" {
				proc, _ := req.Params["proc"].(float64)
				c.proc = int(proc)
				n.subs = append(n.subs, c)
			}
			n.Mu.Unlock()
			c.send("Response", map[string]any{"id": req.ID, "params": map[string]any{"SID": 262146}, "result": true, "session": "S1"})
		}
	}()
}

// notify tells the subscribers the upgrader's state.
func (n *NVR) notify(state string, progress int) {
	n.Mu.Lock()
	subs := append([]*wsConn(nil), n.subs...)
	step := n.UpgradeStep
	n.Mu.Unlock()
	for _, c := range subs {
		c.send("Notification", map[string]any{"callback": c.proc, "id": 38797, "method": "client.notifyUpgraderStream",
			"params": map[string]any{"ChipType": "Main", "File": "", "Progress": progress, "SID": 262146, "State": state}, "session": "S1"})
	}
	time.Sleep(step)
}

// serveUpgrade: /RPC2_Upgrade, the file in the multipart part "file"; the states go
// out over the WebSocket, the answer comes at the end, as the recorders do.
func (n *NVR) serveUpgrade(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("WebClientHttpSessionID"); err != nil || c.Value != "S1" {
		w.WriteHeader(401)
		return
	}
	if r.ContentLength <= 0 {
		w.WriteHeader(411) // no chunked uploads
		return
	}
	n.notify("Preparing", 0)
	mr, err := r.MultipartReader()
	if err != nil {
		w.WriteHeader(400)
		return
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "file" {
		w.WriteHeader(400)
		return
	}
	data, _ := io.ReadAll(part)
	n.notify("Downloading", 50)
	n.notify("Downloading", 100)
	n.Mu.Lock()
	n.Upgraded, n.UpgradeName = data, part.FileName()
	n.Requests = append(n.Requests, "UPGRADE "+part.FileName())
	fail := n.UpgradeFail
	n.Mu.Unlock()
	if fail != "" {
		n.notify(fail, 0)
		fmt.Fprint(w, "<html><body><h1>upgrade failed!</h1></body></html>")
		return
	}
	n.notify("Upgrading", 50)
	n.notify("Upgrading", 100)
	n.notify("Succeeded", 100)
	fmt.Fprint(w, `<html><body><script>if(parent.downSucceed) parent.downSucceed();</script><h1>sonia upgrade successfully!<h1>`)
}
