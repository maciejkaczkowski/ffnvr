package dvrip

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"
)

func msgFrame(msgid uint16, session uint32, body string) []byte {
	payload := append([]byte(body), '\n', 0)
	hdr := make([]byte, 20)
	hdr[0] = 0xFF
	binary.LittleEndian.PutUint32(hdr[4:8], session)
	binary.LittleEndian.PutUint16(hdr[14:16], msgid)
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(payload)))
	return append(hdr, payload...)
}

const pushed = `{ "Address" : "0x0B01A8C0", "Channel" : 2, "Descrip" : "", "Event" : "MotionDetect", "SerialID" : "abc123", "StartTime" : "2026-11-02 12:00:05", "Status" : "Start", "Type" : "Alarm" }`

func TestParseAlarm(t *testing.T) {
	a, err := ParseAlarm([]byte(pushed + "\n\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Event != "MotionDetect" || a.Channel != 3 || a.Serial != "abc123" || a.Address != "192.168.1.11" || a.Status != "Start" {
		t.Errorf("%+v", a)
	}
	if want := time.Date(2026, 11, 2, 12, 0, 5, 0, time.Local); !a.Time.Equal(want) {
		t.Errorf("time %v", a.Time)
	}
	g, err := ParseAlarm([]byte(`{ "AlarmInfo" : { "Channel" : 0, "Event" : "VideoMotion", "StartTime" : "2026-11-02 12:00:00", "Status" : "Stop" }, "Name" : "AlarmInfo", "SessionID" : "0x00000002" }`))
	if err != nil || g.Event != "VideoMotion" || g.Channel != 1 || g.Status != "Stop" {
		t.Errorf("%+v %v", g, err)
	}
	// alarmserver issue #26: anything that is not JSON must not bring the server down
	for _, bad := range []string{"", "GET / HTTP/1.0", "{", "{}", "[1,2]"} {
		if _, err := ParseAlarm([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func pushRead(t *testing.T, data []byte) (out []Alarm, errs int) {
	t.Helper()
	srv, cli := net.Pipe()
	go func() { cli.Write(data); cli.Close() }()
	ReadPushed(srv, 2*time.Second, func(a Alarm, err error) {
		if err != nil {
			errs++
			return
		}
		out = append(out, a)
	})
	return
}

func TestReadPushed(t *testing.T) {
	// two framed messages on one connection
	data := append(msgFrame(1504, 0, pushed), msgFrame(1504, 0, `{"Event":"LossDetect","Channel":0,"Status":"Stop","Type":"Alarm"}`)...)
	if as, errs := pushRead(t, data); len(as) != 2 || errs != 0 || as[1].Event != "LossDetect" {
		t.Errorf("framed: %+v errs %d", as, errs)
	}
	// without a header
	if as, errs := pushRead(t, []byte(pushed+"\n"+pushed)); len(as) != 2 || errs != 0 {
		t.Errorf("bare JSON: %+v errs %d", as, errs)
	}
	// a load balancer's health check (alarmserver issue #26)
	if as, errs := pushRead(t, []byte("GET / HTTP/1.0\r\n\r\n")); len(as) != 0 || errs != 1 {
		t.Errorf("garbage: %+v errs %d", as, errs)
	}
}

// a recorder that answers login and guard, then sends alarms
func fakeGuard(t *testing.T, alarms []string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		read := func() (uint16, map[string]any) {
			hdr := make([]byte, 20)
			if _, err := io.ReadFull(c, hdr); err != nil {
				return 0, nil
			}
			body := make([]byte, binary.LittleEndian.Uint32(hdr[16:20]))
			io.ReadFull(c, body)
			var m map[string]any
			json.Unmarshal(trimJSON(body), &m)
			return binary.LittleEndian.Uint16(hdr[14:16]), m
		}
		if id, _ := read(); id != cmdLogin {
			return
		}
		c.Write(msgFrame(cmdLogin+1, 7, `{"Ret":100,"SessionID":"0x00000007"}`))
		if id, m := read(); id != cmdGuard || m["SessionID"] != "0x00000007" {
			t.Errorf("guard request: %d %v", id, m)
			return
		}
		c.Write(msgFrame(cmdGuard+1, 7, `{"Name":"","Ret":100,"SessionID":"0x00000007"}`))
		for _, a := range alarms {
			c.Write(msgFrame(cmdAlarmInfo, 7, a))
		}
		time.Sleep(time.Second)
	}()
	return ln.Addr().String()
}

func TestAlarmsGuard(t *testing.T) {
	addr := fakeGuard(t, []string{
		`{"AlarmInfo":{"Channel":1,"Event":"VideoMotion","StartTime":"2026-11-02 12:00:00","Status":"Start"},"Name":"AlarmInfo","SessionID":"0x00000007"}`,
		`{"AlarmInfo":{"Channel":1,"Event":"VideoMotion","StartTime":"2026-11-02 12:00:09","Status":"Stop"},"Name":"AlarmInfo","SessionID":"0x00000007"}`,
	})
	c, _ := NewClient(addr, "admin", "")
	var got []Alarm
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.Alarms(ctx, nil, func(a Alarm) { got = append(got, a) })
	if err == nil {
		t.Error("the closed connection must end Alarms with an error")
	}
	if len(got) != 2 || got[0].Channel != 2 || got[1].Status != "Stop" {
		t.Fatalf("%+v", got)
	}
}
