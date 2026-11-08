package alarm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The SMTP server takes the e-mails of recorders and cameras that can do nothing else:
// it accepts every login and every recipient, keeps nothing but what the event needs
// (subject, the start of the text, the first picture) and delivers nothing further.
// Who may connect is limited by address= of /ip/service alarm-smtp.
//
// STARTTLS is offered with a certificate made at start (recorders do not check it);
// a few insist on TLS from the first byte (port 465) - not supported.

// Mail is what a recorder sent.
type Mail struct {
	Remote  string // its address
	Helo    string
	User    string // AUTH user ("" without login)
	From    string
	To      []string
	Subject string
	Text    string // the start of the text
	Picture []byte // the first picture
	Raw     string // the headers (diagnostics)
}

// SMTPServer serves the connections of a listener.
type SMTPServer struct {
	Hostname string
	MaxSize  int64 // the largest message (0: 16 MB)
	TLS      *tls.Config
	Accept   func(remote net.Addr) bool // nil: everyone
	Handle   func(Mail)
	Logf     func(string, ...any)
}

// Serve accepts connections until ctx ends.
func (s *SMTPServer) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if s.Accept != nil && !s.Accept(c.RemoteAddr()) {
			c.Close()
			continue
		}
		go s.session(c)
	}
}

func (s *SMTPServer) maxSize() int64 {
	if s.MaxSize > 0 {
		return s.MaxSize
	}
	return 16 << 20
}

type smtpConn struct {
	c   net.Conn
	r   *bufio.Reader
	w   *bufio.Writer
	tls bool
}

func (sc *smtpConn) reply(code int, lines ...string) error {
	for i, l := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		fmt.Fprintf(sc.w, "%d%s%s\r\n", code, sep, l)
	}
	return sc.w.Flush()
}

// readLine reads one command line (at most 4 KB).
func (sc *smtpConn) readLine() (string, error) {
	sc.c.SetReadDeadline(time.Now().Add(5 * time.Minute))
	var sb strings.Builder
	for {
		frag, isPrefix, err := sc.r.ReadLine()
		if err != nil {
			return "", err
		}
		if sb.Len()+len(frag) > 4096 {
			return "", errors.New("line too long")
		}
		sb.Write(frag)
		if !isPrefix {
			return sb.String(), nil
		}
	}
}

func (s *SMTPServer) session(c net.Conn) {
	defer c.Close()
	sc := &smtpConn{c: c, r: bufio.NewReader(c), w: bufio.NewWriter(c)}
	host := s.Hostname
	if host == "" {
		host = "ffnvr"
	}
	m := Mail{Remote: hostOf(c.RemoteAddr())}
	sc.reply(220, host+" ESMTP ffnvr alarm server")
	for {
		line, err := sc.readLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		arg = strings.TrimSpace(arg)
		switch strings.ToUpper(verb) {
		case "EHLO":
			m.Helo = arg
			ext := []string{host, "8BITMIME", fmt.Sprintf("SIZE %d", s.maxSize()), "AUTH PLAIN LOGIN", "AUTH=PLAIN LOGIN"}
			if s.TLS != nil && !sc.tls {
				ext = append(ext, "STARTTLS")
			}
			sc.reply(250, ext...)
		case "HELO":
			m.Helo = arg
			sc.reply(250, host)
		case "STARTTLS":
			if s.TLS == nil || sc.tls {
				sc.reply(502, "not available")
				continue
			}
			sc.reply(220, "go ahead")
			tc := tls.Server(c, s.TLS)
			tc.SetDeadline(time.Now().Add(30 * time.Second))
			if err := tc.Handshake(); err != nil {
				s.logf("smtp %s: TLS: %v", m.Remote, err)
				return
			}
			tc.SetDeadline(time.Time{})
			c = tc
			sc = &smtpConn{c: tc, r: bufio.NewReader(tc), w: bufio.NewWriter(tc), tls: true}
			m = Mail{Remote: m.Remote} // RFC 3207: forget everything said before
		case "AUTH":
			user, ok := s.auth(sc, arg)
			if !ok {
				sc.reply(501, "authentication cancelled")
				continue
			}
			m.User = user
			sc.reply(235, "accepted")
		case "MAIL":
			if !strings.HasPrefix(strings.ToUpper(arg), "FROM:") {
				sc.reply(501, "MAIL FROM:<address>")
				continue
			}
			m.From, m.To = address(arg[5:]), nil
			sc.reply(250, "ok")
		case "RCPT":
			if !strings.HasPrefix(strings.ToUpper(arg), "TO:") {
				sc.reply(501, "RCPT TO:<address>")
				continue
			}
			if len(m.To) < 100 {
				m.To = append(m.To, address(arg[3:]))
			}
			sc.reply(250, "ok")
		case "DATA":
			if len(m.To) == 0 {
				sc.reply(503, "RCPT first")
				continue
			}
			sc.reply(354, "end with <CRLF>.<CRLF>")
			data, err := readData(sc, s.maxSize())
			if errors.Is(err, errTooBig) {
				sc.reply(552, "message too big")
				continue
			}
			if err != nil {
				return
			}
			got := m
			if err := parseMessage(data, &got); err != nil {
				s.logf("smtp %s: %v", m.Remote, err)
			}
			if s.Handle != nil {
				s.Handle(got)
			}
			sc.reply(250, "queued")
			m.From, m.To = "", nil
		case "RSET":
			m.From, m.To = "", nil
			sc.reply(250, "ok")
		case "NOOP":
			sc.reply(250, "ok")
		case "VRFY":
			sc.reply(252, "send some mail, I'll try my best")
		case "QUIT":
			sc.reply(221, "bye")
			return
		default:
			sc.reply(502, "command not implemented")
		}
	}
}

// auth runs AUTH PLAIN or LOGIN and returns the user; any password is accepted.
func (s *SMTPServer) auth(sc *smtpConn, arg string) (string, bool) {
	mech, initial, _ := strings.Cut(arg, " ")
	ask := func(prompt string) (string, bool) {
		sc.reply(334, base64.StdEncoding.EncodeToString([]byte(prompt)))
		l, err := sc.readLine()
		if err != nil || l == "*" {
			return "", false
		}
		return l, true
	}
	dec := func(v string) string {
		b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		return string(b)
	}
	switch strings.ToUpper(mech) {
	case "PLAIN":
		if initial == "" {
			var ok bool
			if initial, ok = ask(""); !ok {
				return "", false
			}
		}
		parts := strings.Split(dec(initial), "\x00") // authzid, authcid, password
		if len(parts) != 3 {
			return "", false
		}
		return parts[1], true
	case "LOGIN":
		user := initial
		if user == "" {
			var ok bool
			if user, ok = ask("Username:"); !ok {
				return "", false
			}
		}
		if _, ok := ask("Password:"); !ok {
			return "", false
		}
		return dec(user), true
	}
	return "", false
}

var errTooBig = errors.New("too big")

// readData reads the message up to the line with a single dot, undoing dot-stuffing.
func readData(sc *smtpConn, max int64) ([]byte, error) {
	var buf bytes.Buffer
	tooBig := false
	lineStart := true
	for {
		sc.c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := sc.r.ReadSlice('\n')
		whole := true
		if err == bufio.ErrBufferFull {
			err, whole = nil, false // a long line: the rest follows
		} else if err != nil {
			return nil, err
		}
		if lineStart && whole && (bytes.Equal(line, []byte(".\r\n")) || bytes.Equal(line, []byte(".\n"))) {
			break
		}
		if lineStart && len(line) > 0 && line[0] == '.' {
			line = line[1:] // the sender doubled a leading dot
		}
		lineStart = whole
		if int64(buf.Len()+len(line)) > max {
			tooBig = true
			continue // read on to the end of the message, keep nothing
		}
		buf.Write(line)
	}
	if tooBig {
		return nil, errTooBig
	}
	return buf.Bytes(), nil
}

func address(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, '<'); i >= 0 {
		if j := strings.IndexByte(v[i:], '>'); j > 0 {
			return strings.ToLower(v[i+1 : i+j])
		}
	}
	addr, _, _ := strings.Cut(v, " ")
	return strings.ToLower(addr)
}

func hostOf(a net.Addr) string {
	if h, _, err := net.SplitHostPort(a.String()); err == nil {
		return h
	}
	return a.String()
}

func (s *SMTPServer) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// ---------------------------------------------------------------- the message

const maxText = 4 << 10

var wordDecoder = mime.WordDecoder{CharsetReader: charsetReader}

// charsetReader knows UTF-8 and the single-byte Latin charsets; anything else is
// passed on as it is (better a few odd characters than no subject).
func charsetReader(charset string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "us-ascii", "ascii", "windows-1252", "cp1252":
		b, err := io.ReadAll(in)
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return strings.NewReader(string(r)), err
	}
	return in, nil
}

func decodeHeader(v string) string {
	if d, err := wordDecoder.DecodeHeader(v); err == nil {
		v = d
	}
	return strings.TrimSpace(strings.ToValidUTF8(v, "?"))
}

// parseMessage fills subject, text and picture of m.
func parseMessage(data []byte, m *Mail) error {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		m.Text = clip(string(data))
		return fmt.Errorf("not a mail message: %w", err)
	}
	m.Subject = decodeHeader(msg.Header.Get("Subject"))
	var hb strings.Builder
	for _, k := range []string{"From", "To", "Subject", "Date", "X-Mailer", "Content-Type"} {
		if v := msg.Header.Get(k); v != "" {
			fmt.Fprintf(&hb, "%s: %s\n", k, v)
		}
	}
	m.Raw = hb.String()
	var html string
	walkPart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), "", msg.Body, m, &html, 0)
	if m.Text == "" && html != "" {
		m.Text = clip(stripTags(html))
	}
	return nil
}

func walkPart(ctype, cte, disposition string, body io.Reader, m *Mail, html *string, depth int) {
	if depth > 8 {
		return
	}
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil {
		mt = "text/plain"
	}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			p, err := mr.NextRawPart() // raw: the encoding is decoded below, once
			if err != nil {
				return
			}
			walkPart(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p.Header.Get("Content-Disposition"), p, m, html, depth+1)
		}
	}
	r := decodeBody(cte, body)
	_, dparams, _ := mime.ParseMediaType(disposition)
	name := strings.ToLower(dparams["filename"] + params["name"])
	switch {
	case strings.HasPrefix(mt, "image/") || (mt == "application/octet-stream" && (strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg"))):
		if m.Picture == nil {
			b, err := io.ReadAll(io.LimitReader(r, 4<<20+1))
			if err == nil && len(b) <= 4<<20 && isJPEG(b) {
				m.Picture = b
			}
		}
	case mt == "text/plain" && m.Text == "" && !strings.HasPrefix(strings.ToLower(disposition), "attachment"):
		b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
		m.Text = clip(toUTF8(b, params["charset"]))
	case mt == "text/html" && *html == "":
		b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
		*html = toUTF8(b, params["charset"])
	}
}

func decodeBody(cte string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &stripSpace{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// stripSpace drops line breaks inside base64 (the decoder does not take them everywhere).
type stripSpace struct{ r io.Reader }

func (s *stripSpace) Read(p []byte) (int, error) {
	for {
		n, err := s.r.Read(p)
		j := 0
		for _, c := range p[:n] {
			if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
				p[j] = c
				j++
			}
		}
		if j > 0 || err != nil {
			return j, err
		}
	}
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 }

func toUTF8(b []byte, charset string) string {
	if !utf8.Valid(b) {
		if r, err := charsetReader(charset, bytes.NewReader(b)); err == nil {
			if d, err := io.ReadAll(r); err == nil {
				b = d
			}
		}
	}
	return strings.ToValidUTF8(string(b), "?")
}

var tagRe = regexp.MustCompile(`(?s)<(style|script)[^>]*>.*?</(style|script)>|<[^>]*>`)

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	for _, r := range [][2]string{{"&nbsp;", " "}, {"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return s
}

// clip: the text with its white space tidied, at most maxText bytes.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxText {
		s = s[:maxText]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// selfSigned makes a certificate for STARTTLS. RSA: the TLS of some cameras knows nothing else.
func selfSigned(host string) (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS10, // old cameras
	}, nil
}

// ---------------------------------------------------------------- what the mail says

// mailKinds: words in subject or text and the kind of event they mean, the more
// specific first. English, Polish and German, as recorders write them.
var mailKinds = []struct {
	kind  string
	words []string
}{
	{"VideoLoss", []string{"video loss", "videoloss", "loss detect", "utrata wideo", "utrata sygnału", "brak sygnału", "videoverlust"}},
	{"VideoBlind", []string{"tamper", "blind", "cover", "sabotaż", "zasłonięcie", "abdeckung"}},
	{"StorageFailure", []string{"hdd", "disk", "storage", "dysk", "festplatte"}},
	{"CrossLine", []string{"line crossing", "cross line", "crossline", "tripwire", "przekroczenie linii", "linienüberschreitung"}},
	{"Intrusion", []string{"intrusion", "region entr", "region exit", "perimeter", "wtargnięcie", "einbruch"}},
	{"Human", []string{"human", "person", "pedestrian", "człowiek", "osoba", "mensch", "personen"}},
	{"Vehicle", []string{"vehicle", "car ", "pojazd", "fahrzeug"}},
	{"AlarmInput", []string{"alarm input", "alarm in", "i/o", "external alarm", "wejście alarmowe", "alarmeingang"}},
	{"VideoMotion", []string{"motion", "ruch", "bewegung"}},
}

// MailKind tells the kind of event from what a mail says ("Mail" when nothing fits).
func MailKind(subject, text string) string {
	s := strings.ToLower(subject + " " + text)
	for _, k := range mailKinds {
		for _, w := range k.words {
			if strings.Contains(s, w) {
				return k.kind
			}
		}
	}
	return "Mail"
}

var channelRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:ch|channel|kana[łl]|kanal|camera|kamera|cam|ipc|d)[ :._#-]*0*(\d{1,3})\b`),
}

// MailChannel finds the channel a mail is about (0: none).
func MailChannel(subject, text string) int {
	for _, s := range []string{subject, text} {
		for _, re := range channelRes {
			if m := re.FindStringSubmatch(s); m != nil {
				var n int
				fmt.Sscan(m[1], &n)
				if n > 0 && n <= 256 {
					return n
				}
			}
		}
	}
	return 0
}
