package alarm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/engine"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
)

func pushFrame(body string) []byte {
	payload := append([]byte(body), '\n', 0)
	hdr := make([]byte, 20)
	hdr[0] = 0xFF
	hdr[14], hdr[15] = 0xE0, 0x05 // 1504
	n := len(payload)
	hdr[16], hdr[17], hdr[18], hdr[19] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
	return append(hdr, payload...)
}

func TestServerEndToEnd(t *testing.T) {
	root := t.TempDir()
	// a Dahua recorder: three motion events on channel 1 (start, stop) and a disk error
	release := make(chan struct{})
	dh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=myboundary")
		for _, ev := range []string{"Code=VideoMotion;action=Start;index=0", "Code=NewFile;action=Pulse;index=0", "Code=RecordDelete;action=Pulse;index=0", "Code=VideoMotion;action=Stop;index=0", "Code=StorageFailure;action=Start;index=0"} {
			fmt.Fprintf(w, "--myboundary\r\nContent-Type: text/plain\r\n\r\n%s\r\n\r\n", ev)
		}
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer dh.Close()
	defer close(release)
	cfg := filepath.Join(root, "config.rsc")
	// nvrD drops RecordDelete (and, its filter replacing the built-in list, keeps
	// NewFile); nvrX keeps only motion; the unknown sender takes the built-in list
	os.WriteFile(cfg, []byte(`/event/filter
add name=nvr action=drop kinds=RecordDelete
add name=ipc action=accept kinds=MotionDetect
/nvr
add type=dahua name=nvrD host=`+strings.TrimPrefix(dh.URL, "http://")+` username=admin password=x events=subscribe event-filter=nvr
add type=dvrip name=nvrX host=10.9.9.9 username=admin event-id=SER123 event-filter=ipc
/camera
add nvr=nvrD name=door channel=1
add nvr=nvrX name=yard channel=2
/event
set gap=1s
`), 0o644)
	load := func() (*config.Store, error) {
		e, err := engine.New(engine.Options{Root: root})
		if err != nil {
			return nil, err
		}
		if err := e.LoadFiles([]string{cfg}); err != nil {
			return nil, err
		}
		return e.Store, nil
	}
	if _, err := load(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- Run(ctx, Options{Load: load, Root: root, DVRIP: "127.0.0.1:0", SMTP: "127.0.0.1:0", Logf: t.Logf, Reload: 200 * time.Millisecond})
	}()
	var st *modules.AlarmState
	for i := 0; i < 400 && (st == nil || st.Listeners["alarm-smtp"] == ""); i++ {
		time.Sleep(50 * time.Millisecond)
		st = modules.ReadAlarmState(root)
	}
	if st == nil {
		t.Fatal("no state")
	}
	// a DVRIP recorder behind NAT: known by its serial number (event-id)
	c, err := net.Dial("tcp", st.Listeners["alarm-dvrip"])
	if err != nil {
		t.Fatal(err)
	}
	c.Write(pushFrame(`{"Address":"0x0B01A8C0","Channel":1,"Event":"MotionDetect","SerialID":"ser123","Status":"Start","Type":"Alarm"}`))
	c.Write(pushFrame(`{"Address":"0x0B01A8C0","Channel":1,"Event":"VideoLoss","SerialID":"ser123","Status":"Start","Type":"Alarm"}`))
	c.Close()
	// an e-mail from somebody unknown
	sc, err := smtp.Dial(st.Listeners["alarm-smtp"]) // no STARTTLS: like most cameras
	if err != nil {
		t.Fatal(err)
	}
	sc.Mail("cam@x")
	sc.Rcpt("a@b")
	wr, _ := sc.Data()
	wr.Write([]byte("Subject: Video loss CH4\r\n\r\nlost\r\n"))
	if err := wr.Close(); err != nil {
		t.Fatal(err)
	}
	sc.Quit()

	db, err := eventdb.Open(eventdb.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var evs []eventdb.Event
	for i := 0; i < 100; i++ {
		evs, _ = db.Query(context.Background(), eventdb.Filter{Origin: "recorders"})
		if len(evs) >= 5 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	byKind := map[string]eventdb.Event{}
	for _, e := range evs {
		byKind[e.Kind] = e
	}
	if len(evs) != 5 {
		t.Fatalf("%d events: %+v", len(evs), evs)
	}
	if e := byKind["VideoMotion"]; e.Nvr != "nvrD" || e.Camera != "door" || e.Open() || e.Source != "dahua" {
		t.Errorf("dahua motion: %+v", e)
	}
	if e, ok := byKind["NewFile"]; !ok || e.Nvr != "nvrD" {
		t.Error("NewFile is kept by the filter of nvrD (it replaces the built-in list)")
	}
	if _, ok := byKind["RecordDelete"]; ok {
		t.Error("RecordDelete is dropped by the filter of nvrD")
	}
	if e, ok := byKind["VideoLoss"]; ok && e.Nvr == "nvrX" {
		t.Error("VideoLoss of nvrX: its filter accepts only MotionDetect")
	}
	if e := byKind["MotionDetect"]; e.Nvr != "nvrX" || e.Camera != "yard" || !e.Open() {
		t.Errorf("dvrip push: %+v", e)
	}
	// the mail comes from 127.0.0.1, the address of nvrD here: matched by address
	if e := byKind["VideoLoss"]; e.Nvr != "nvrD" || e.Channel != 4 || e.Source != "smtp" || e.Ended != e.Started {
		t.Errorf("mail: %+v", e)
	}
	if st := modules.ReadAlarmState(root); st == nil {
		t.Error("no state")
	}
	sys, _ := db.Query(context.Background(), eventdb.Filter{Origin: "system"})
	kinds := map[string]bool{}
	for _, e := range sys {
		kinds[e.Kind+" "+e.Nvr] = true
	}
	if !kinds["AlarmServer "] || !kinds["EventsConnection nvrD"] {
		t.Errorf("system log: %v", kinds)
	}

	// the recorder is no longer subscribed to: the subscription ends, its open events end
	cfgNo := strings.Replace(mustRead(t, cfg), "events=subscribe", "events=no", 1)
	os.WriteFile(cfg, []byte(cfgNo), 0o644)
	ok := false
	for i := 0; i < 60 && !ok; i++ {
		time.Sleep(100 * time.Millisecond)
		e, _ := db.Get(context.Background(), byKind["StorageFailure"].ID)
		ok = !e.Open()
	}
	if !ok {
		t.Error("the open event of an unsubscribed recorder stayed open")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end")
	}
	if text := modules.AlarmStateText(root); !strings.Contains(text, "alarm server") {
		t.Errorf("state text %q", text)
	}
}

func mustRead(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMatchOrder(t *testing.T) {
	d := &directory{nvrs: []*nvrEntry{
		{name: "a", ips: map[string]bool{"10.0.0.1": true}, ids: map[string]bool{}},
		{name: "b", ips: map[string]bool{"10.0.0.2": true}, ids: map[string]bool{"ser9": true}},
	}}
	if e := d.match([]string{"SER9"}, []string{"10.0.0.1"}); e == nil || e.name != "b" {
		t.Errorf("an id wins over an address: %v", e)
	}
	if e := d.match([]string{"x"}, []string{"", "10.0.0.1"}); e == nil || e.name != "a" {
		t.Errorf("by address: %v", e)
	}
	if e := d.match(nil, []string{"10.9.9.9"}); e != nil {
		t.Errorf("nobody: %v", e)
	}
	for in, want := range map[string]string{"192.168.1.2": "192.168.1.2", "192.168.1.2:8080": "192.168.1.2", "https://nvr.lan/": "nvr.lan", "[fe80::1]:80": "fe80::1"} {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q", in, got)
		}
	}
}
