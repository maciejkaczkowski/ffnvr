package web

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGo2rtc answers the API the panel proxies and records what it was asked.
type fakeGo2rtc struct {
	mu   sync.Mutex
	seen []string // path?query, and the headers of note
	srv  *httptest.Server
}

func newFakeGo2rtc() *fakeGo2rtc {
	f := &fakeGo2rtc{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.URL.Path+"?"+r.URL.RawQuery+" cookie="+r.Header.Get("Cookie")+" origin="+r.Header.Get("Origin"))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/stream.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=192000\nhls/playlist.m3u8?id=abc\n")
		case "/api/hls/playlist.m3u8":
			io.WriteString(w, "#EXTM3U\n#EXTINF:0.500,\nsegment.ts?id=abc&n=1\n")
		case "/api/frame.jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
		case "/api/ws":
			key := r.Header.Get("Sec-Websocket-Key")
			h := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			conn, rw, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
				base64.StdEncoding.EncodeToString(h[:]) + "\r\n\r\n")
			rw.Flush()
			msg := readFrame(rw.Reader) // the client's {"type":"mse",...}
			if !strings.Contains(string(msg), `"mse"`) {
				return
			}
			writeFrame(conn, 1, []byte(`{"type":"mse","value":"video/mp4; codecs=\"avc1.640029\""}`), false)
			writeFrame(conn, 2, []byte("\x00\x00\x00\x18ftypiso5"), false)
			io.Copy(io.Discard, rw) // a stream lasts until the viewer goes
		default:
			w.WriteHeader(404)
		}
	}))
	return f
}

func writeFrame(w io.Writer, op byte, data []byte, masked bool) {
	hdr := []byte{0x80 | op, byte(len(data))}
	if masked {
		hdr[1] |= 0x80
		key := []byte{1, 2, 3, 4}
		hdr = append(hdr, key...)
		m := make([]byte, len(data))
		for i := range data {
			m[i] = data[i] ^ key[i%4]
		}
		data = m
	}
	w.Write(append(hdr, data...))
}

func readFrame(r *bufio.Reader) []byte {
	b0, _ := r.ReadByte()
	_ = b0
	b1, _ := r.ReadByte()
	n := int(b1 & 0x7f)
	if n == 126 {
		var l [2]byte
		io.ReadFull(r, l[:])
		n = int(binary.BigEndian.Uint16(l[:]))
	}
	var key [4]byte
	if b1&0x80 != 0 {
		io.ReadFull(r, key[:])
	}
	data := make([]byte, n)
	io.ReadFull(r, data)
	if b1&0x80 != 0 {
		for i := range data {
			data[i] ^= key[i%4]
		}
	}
	return data
}

func TestLiveProxy(t *testing.T) {
	g := newFakeGo2rtc()
	defer g.srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(g.srv.URL, "http://"))
	srv, st := setup(t)
	st.exec(t, `/camera set [find name=cam1 nvr=nvr01] stream-lq="rtsp://192.168.10.21/s1"`, `/system/live set enabled=yes port=`+port)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// the page: the tree of the user's cameras that have live streams
	_, page := get(t, c, srv.URL+"/?p=live")
	contains(t, "live page", page, `class="srcs lside"`, `data-n="nvr01" data-c="cam1" data-label="brama"`,
		`data-n="nvr01" data-c="cam3"`, `data-layout="25"`, `<script src="/static/live.js" nonce="`, `id="lgrid"`, `id="lmap"`,
		`"n":"nvr01","c":"cam1","label":"brama"`, `"hq":true,"lq":true`, `<dialog id="ddel">`)
	contains(t, "zoom to HQ: the default", page, `"zoomhq":true`)
	_, js := get(t, c, srv.URL+"/static/live.js")
	contains(t, "script", js, "MediaSource", "/live/ws?", "dragstart", "/live/view/save", "/live/positions")
	if code, _ := get(t, client(), srv.URL+"/static/live.js"); code != 401 {
		t.Error("the script for signed in users")
	}
	if strings.Contains(page, `data-c="cam2"`) || strings.Contains(page, "nvr09") {
		t.Error("disabled cameras and recorders without access are not offered")
	}

	// HLS: the stream by camera and quality, then go2rtc's own session links
	code, body := get(t, c, srv.URL+"/live/api/stream.m3u8?n=nvr01&c=cam1&q=lq")
	if code != 200 || !strings.Contains(body, "hls/playlist.m3u8?id=abc") {
		t.Errorf("%d %s", code, body)
	}
	code, body = get(t, c, srv.URL+"/live/api/hls/playlist.m3u8?id=abc")
	if code != 200 || !strings.Contains(body, "segment.ts") {
		t.Errorf("%d %s", code, body)
	}
	code, body = get(t, c, srv.URL+"/live/frame.jpeg?n=nvr01&c=cam1&q=hq")
	if code != 200 || body != "\xff\xd8\xff\xd9" {
		t.Errorf("snapshot: %d", code)
	}
	g.mu.Lock()
	seen := strings.Join(g.seen, "\n")
	g.mu.Unlock()
	for _, want := range []string{"/api/stream.m3u8?src=nvr01%2Fcam1%2Flq cookie= origin=", "/api/hls/playlist.m3u8?id=abc cookie=", "/api/frame.jpeg?src=nvr01%2Fcam1%2Fhq"} {
		if !strings.Contains(seen, want) {
			t.Errorf("go2rtc was not asked %q:\n%s", want, seen)
		}
	}
	// refusals: not signed in, no access, no such stream, live off
	if code, _ := get(t, client(), srv.URL+"/live/frame.jpeg?n=nvr01&c=cam1&q=hq"); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	if code, _ := get(t, c, srv.URL+"/live/frame.jpeg?n=nvr09&c=x&q=hq"); code != 403 {
		t.Errorf("no access: %d", code)
	}
	if code, _ := get(t, c, srv.URL+"/live/frame.jpeg?n=nvr01&c=cam2&q=hq"); code != 404 {
		t.Errorf("disabled camera: %d", code)
	}
	if code, _ := get(t, c, srv.URL+"/live/api/../../etc"); code == 200 {
		t.Error("only the proxied paths")
	}

	// MSE over WebSocket, through the proxy
	u, _ := url.Parse(srv.URL)
	ws := func(origin string) (string, *bufio.Reader, net.Conn) {
		conn, err := net.Dial("tcp", u.Host)
		if err != nil {
			t.Fatal(err)
		}
		cookies := c.Jar.Cookies(u)
		var ck []string
		for _, x := range cookies {
			ck = append(ck, x.Name+"="+x.Value)
		}
		io.WriteString(conn, "GET /live/ws?n=nvr01&c=cam1&q=hq HTTP/1.1\r\nHost: "+u.Host+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: "+origin+"\r\nCookie: "+strings.Join(ck, "; ")+"\r\n\r\n")
		r := bufio.NewReader(conn)
		status, _ := r.ReadString('\n')
		for {
			l, err := r.ReadString('\n')
			if err != nil || l == "\r\n" {
				break
			}
		}
		return status, r, conn
	}
	status, r, conn := ws(srv.URL)
	if !strings.Contains(status, "101") {
		t.Fatalf("upgrade: %q", status)
	}
	writeFrame(conn, 1, []byte(`{"type":"mse","value":"avc1.640029"}`), true)
	if m := readFrame(r); !strings.Contains(string(m), `video/mp4; codecs=\"avc1.640029\"`) {
		t.Errorf("%q", m)
	}
	if m := readFrame(r); !strings.Contains(string(m), "ftyp") {
		t.Errorf("%q", m)
	}
	conn.Close()
	status, _, conn = ws("http://evil.example")
	conn.Close()
	if !strings.Contains(status, "403") {
		t.Errorf("a page of another site: %q", status)
	}
	g.mu.Lock()
	for _, l := range g.seen {
		if strings.HasPrefix(l, "/api/ws") && (!strings.Contains(l, "src=nvr01%2Fcam1%2Fhq") || !strings.HasSuffix(l, "origin=")) {
			t.Errorf("ws to go2rtc: %s", l)
		}
	}
	g.mu.Unlock()
}

func TestGo2rtcVersion(t *testing.T) {
	g := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			io.WriteString(w, `{"config_path":"/x","host":"127.0.0.1:1984","rtsp":{},"version":"1.9.9"}`)
		}
	}))
	defer g.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(g.URL, "http://"))
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes port=`+port)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")
	_, page := get(t, c, srv.URL+"/?p=firmware")
	contains(t, "firmware", page, "<dt>go2rtc</dt><dd>1.9.9 (/usr/local/bin/go2rtc, 127.0.0.1:"+port+")</dd>")
	g.Close()
	_, page = get(t, c, srv.URL+"/?p=firmware")
	contains(t, "firmware", page, "<dt>go2rtc</dt><dd>not running")
}

func TestTranscodeSlots(t *testing.T) {
	var s tcSlots
	if !s.acquire("n/a/tc-hq", 2) || !s.acquire("n/b/tc-hq", 2) {
		t.Fatal("two streams")
	}
	if s.acquire("n/c/tc-hq", 2) {
		t.Error("a third stream over the limit")
	}
	if !s.acquire("n/a/tc-lq", 1) {
		t.Error("LQ has its own limit")
	}
	if s.acquire("n/b/tc-lq", 1) {
		t.Error("over the LQ limit")
	}
	if !s.acquire("n/a/tc-hq", 2) {
		t.Error("another viewer of a running stream")
	}
	if ok, used := s.free("n/c/tc-hq", 2); ok || used != 2 {
		t.Errorf("free: %v %d", ok, used)
	}
	s.release("n/a/tc-hq")
	if s.acquire("n/c/tc-hq", 2) {
		t.Error("a still has a viewer")
	}
	s.release("n/a/tc-hq")
	if !s.acquire("n/c/tc-hq", 2) {
		t.Error("a is gone: c fits")
	}
}
func TestTranscodeProxy(t *testing.T) {
	g := newFakeGo2rtc()
	defer g.srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(g.srv.URL, "http://"))
	srv, st := setup(t)
	st.exec(t, `/system/live set enabled=yes port=`+port)
	c := client()
	signIn(t, c, srv.URL, "jan", "secret")

	// off: the page does not offer it, the panel refuses it
	_, page := get(t, c, srv.URL+"/?p=live")
	contains(t, "live page", page, `"tchq":false,"tclq":false`)
	if code, _ := get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam1"); code != 403 {
		t.Errorf("transcoding off: %d", code)
	}
	if code, _ := get(t, c, srv.URL+"/live/ws?n=nvr01&c=cam1&q=hq&tc=1"); code != 403 {
		t.Errorf("transcoding off, ws: %d", code)
	}

	// on for @firma2 (jan), at most one stream
	st.exec(t, `/camera set [find name=cam1 nvr=nvr01] stream-lq="rtsp://192.168.10.21/s1"`,
		`/camera set [find name=cam3 nvr=nvr01] stream-lq="rtsp://192.168.10.23/s1"`,
		`/system/live set transcode=yes transcode-users=@firma2 transcode-max=1 transcode-height=480 transcode-lq-max=0`)
	_, page = get(t, c, srv.URL+"/?p=live")
	contains(t, "live page", page, `"tchq":true,"tclq":false`)
	if code, _ := get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam1&q=lq"); code != 403 {
		t.Errorf("LQ off (max 0): %d", code)
	}
	_, js := get(t, c, srv.URL+"/static/live.js")
	contains(t, "script", js, "/live/transcode?", "&tc=1", "codecs not matched", "D.tclq", "value: videoCodecs()", "hasVideo(m.value)")
	code, body := get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam1")
	if code != 200 || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"height":480`) {
		t.Errorf("check: %d %s", code, body)
	}
	if code, _ := get(t, c, srv.URL+"/live/frame.jpeg?n=nvr01&c=cam1&q=hq&tc=1"); code != 400 {
		t.Errorf("only over /live/ws: %d", code)
	}
	if code, _ := get(t, c, srv.URL+"/live/transcode?n=nvr09&c=x"); code != 403 {
		t.Errorf("no access: %d", code)
	}

	// a transcoded stream holds the slot while it is watched
	u, _ := url.Parse(srv.URL)
	ws := func(path string) (string, *bufio.Reader, net.Conn) {
		conn, err := net.Dial("tcp", u.Host)
		if err != nil {
			t.Fatal(err)
		}
		var ck []string
		for _, x := range c.Jar.Cookies(u) {
			ck = append(ck, x.Name+"="+x.Value)
		}
		io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: "+u.Host+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: "+srv.URL+"\r\nCookie: "+strings.Join(ck, "; ")+"\r\n\r\n")
		r := bufio.NewReader(conn)
		status, _ := r.ReadString('\n')
		for {
			l, err := r.ReadString('\n')
			if err != nil || l == "\r\n" {
				break
			}
		}
		return status, r, conn
	}
	status, r, conn := ws("/live/ws?n=nvr01&c=cam1&q=hq&tc=1")
	if !strings.Contains(status, "101") {
		t.Fatalf("upgrade: %q", status)
	}
	writeFrame(conn, 1, []byte(`{"type":"mse","value":"avc1.640029"}`), true)
	readFrame(r)
	g.mu.Lock()
	seen := strings.Join(g.seen, "\n")
	g.mu.Unlock()
	if !strings.Contains(seen, "/api/ws?src=nvr01%2Fcam1%2Ftc-hq") {
		t.Errorf("go2rtc was not asked for the transcoded stream:\n%s", seen)
	}
	_, body = get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam3")
	if !strings.Contains(body, `"busy":true`) || !strings.Contains(body, `"used":1`) || !strings.Contains(body, `"max":1`) {
		t.Errorf("the limit: %s", body)
	}
	if _, body = get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam1"); !strings.Contains(body, `"ok":true`) {
		t.Errorf("another viewer of the running stream: %s", body)
	}
	status2, _, conn2 := ws("/live/ws?n=nvr01&c=cam3&q=hq&tc=1")
	conn2.Close()
	if !strings.Contains(status2, "429") {
		t.Errorf("over the limit: %q", status2)
	}
	conn.Close()
	// the slot is free again once the viewer is gone (the proxy notices the close)
	var ok bool
	for i := 0; i < 50 && !ok; i++ {
		_, body = get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam3")
		ok = strings.Contains(body, `"ok":true`)
		if !ok {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !ok {
		t.Errorf("the slot stays taken: %s", body)
	}

	// LQ: its own limit, its own size; the HQ stream does not count against it
	st.exec(t, `/system/live set transcode-lq-max=1`)
	status, r, conn = ws("/live/ws?n=nvr01&c=cam1&q=hq&tc=1")
	writeFrame(conn, 1, []byte(`{"type":"mse","value":"avc1.640029"}`), true)
	readFrame(r)
	code, body = get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam1&q=lq")
	if code != 200 || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"height":0`) {
		t.Errorf("LQ: %d %s", code, body)
	}
	status2, r2, conn2 := ws("/live/ws?n=nvr01&c=cam1&q=lq&tc=1")
	if !strings.Contains(status2, "101") {
		t.Fatalf("LQ upgrade: %q", status2)
	}
	writeFrame(conn2, 1, []byte(`{"type":"mse","value":"avc1.640029"}`), true)
	readFrame(r2)
	if _, body = get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam3&q=lq"); !strings.Contains(body, `"busy":true`) {
		t.Errorf("the LQ limit: %s", body)
	}
	if code, _ := get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam3&q=lq"); code != 200 {
		t.Errorf("busy is not an error: %d", code)
	}
	g.mu.Lock()
	seen = strings.Join(g.seen, "\n")
	g.mu.Unlock()
	if !strings.Contains(seen, "/api/ws?src=nvr01%2Fcam1%2Ftc-lq") {
		t.Errorf("go2rtc was not asked for the transcoded LQ:\n%s", seen)
	}
	conn.Close()
	conn2.Close()
	_ = status

	// a user outside transcode-users
	st.exec(t, `/system/live set transcode-users=ola`)
	if code, _ := get(t, c, srv.URL+"/live/transcode?n=nvr01&c=cam1"); code != 403 {
		t.Errorf("not in transcode-users: %d", code)
	}
}
