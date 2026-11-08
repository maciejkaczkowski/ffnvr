package alarm

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

var jpeg = []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3, 4, 0xFF, 0xD9}

func cameraMail() string {
	return "From: NVR <nvr@cam.local>\r\nTo: alarm@ffnvr\r\n" +
		"Subject: =?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("Alarm: Wykrycie ruchu CH3")) + "?=\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"XX\"\r\n\r\n" +
		"--XX\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Zdarzenie: ruch na kanale 3=0D=0A.kropka na pocz=C4=85tku\r\n" +
		"--XX\r\nContent-Type: image/jpeg; name=\"snap.jpg\"\r\nContent-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"snap.jpg\"\r\n\r\n" + base64.StdEncoding.EncodeToString(jpeg) + "\r\n--XX--\r\n"
}

func startSMTP(t *testing.T, accept func(net.Addr) bool) (string, chan Mail) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tc, err := selfSigned("ffnvr")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan Mail, 4)
	s := &SMTPServer{Hostname: "ffnvr", TLS: tc, MaxSize: 1 << 20, Accept: accept, Handle: func(m Mail) { got <- m }, Logf: t.Logf}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Serve(ctx, ln)
	return ln.Addr().String(), got
}

func TestSMTPWithTLSAndAuth(t *testing.T) {
	addr, got := startSMTP(t, nil)
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartTLS(&tls.Config{InsecureSkipVerify: true}); err != nil {
		t.Fatal(err)
	}
	if err := c.Auth(smtp.PlainAuth("", "nvr07", "whatever", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	c.Mail("nvr@cam.local")
	c.Rcpt("alarm@ffnvr")
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(cameraMail()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c.Quit()
	select {
	case m := <-got:
		if m.User != "nvr07" || m.From != "nvr@cam.local" || m.Subject != "Alarm: Wykrycie ruchu CH3" {
			t.Errorf("%+v", m)
		}
		if !strings.Contains(m.Text, ".kropka na początku") {
			t.Errorf("text %q", m.Text)
		}
		if string(m.Picture) != string(jpeg) {
			t.Errorf("picture %v", m.Picture)
		}
		if k, ch := MailKind(m.Subject, m.Text), MailChannel(m.Subject, m.Text); k != "VideoMotion" || ch != 3 {
			t.Errorf("kind %s channel %d", k, ch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no mail")
	}
}

func TestSMTPPlainAndLogin(t *testing.T) {
	addr, got := startSMTP(t, nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	script := "EHLO cam\r\nAUTH LOGIN\r\n" + b64("cam5") + "\r\n" + b64("pw") + "\r\n" +
		"MAIL FROM:<x@y>\r\nRCPT TO:<a@b>\r\nDATA\r\nSubject: Video Loss D2\r\n\r\nbody\r\n..\r\n.\r\nQUIT\r\n"
	conn.Write([]byte(script))
	select {
	case m := <-got:
		if m.User != "cam5" || MailKind(m.Subject, m.Text) != "VideoLoss" || MailChannel(m.Subject, m.Text) != 2 {
			t.Errorf("%+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no mail")
	}
}

func TestSMTPRefused(t *testing.T) {
	addr, _ := startSMTP(t, func(net.Addr) bool { return false })
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _ := conn.Read(make([]byte, 10)); n != 0 {
		t.Error("a refused address got a greeting")
	}
}

func TestMailKind(t *testing.T) {
	for s, want := range map[string]string{
		"Motion Detection Alarm": "VideoMotion", "HDD Error": "StorageFailure", "Human detected on IPC 4": "Human",
		"Hello": "Mail", "Przekroczenie linii": "CrossLine",
	} {
		if k := MailKind(s, ""); k != want {
			t.Errorf("%q: %s, want %s", s, k, want)
		}
	}
	if MailChannel("Motion on Channel 12", "") != 12 || MailChannel("record 2 done", "") != 0 {
		t.Error("channel")
	}
}
