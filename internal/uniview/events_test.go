package uniview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/uniview/univiewtest"
)

func TestParseNotification(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 0xff, 0xd9}
	cases := []struct {
		path, body        string
		kind, state, text string
		alarm             bool
		sub               int64
	}{
		{"/LAPI/V1.0/System/Event/Notification/Alarm",
			`{"Reference":"192.168.0.13:80/Subscription/Subscribers/7","AlarmInfo":{"AlarmType":"MotionAlarmOn","TimeStamp":1489040894,"AlarmSeq":327}}`,
			"MotionAlarm", "start", "MotionAlarmOn", true, 7},
		{"/LAPI/V1.0/System/Event/Notification/Alarm",
			`{"Reference":"192.168.0.13:80/Subscription/Subscribers/7","AlarmInfo":{"AlarmType":"MotionAlarmOff","TimeStamp":"1489040899"}}`,
			"MotionAlarm", "stop", "MotionAlarmOff", true, 7},
		{"/LAPI/V1.0/System/Event/Notification/Alarm",
			`{"Reference":"http://192.168.0.13:80/LAPI/V1.0/System/ALarmEvent/CommonAlarm/3","AlarmInfo":{"AlarmType":"LineDetectorCrossed","TimeStamp":1489040894}}`,
			"LineDetectorCrossed", "", "LineDetectorCrossed", true, 3},
		{"/LAPI/V1.0/System/Event/Notification/Alarm",
			`{"Reference":"x/1","AlarmInfo":{"AlarmType":"PeopleGathering"}}`, "PeopleGathering", "start", "PeopleGathering", true, 1},
		{"/LAPI/V1.0/System/Event/Notification/Alarm",
			`{"Reference":"x/1","AlarmInfo":{"AlarmType":"PeopleGatheringCleared"}}`, "PeopleGathering", "stop", "PeopleGatheringCleared", true, 1},
		{"/LAPI/V1.0/System/Event/Notification/FastMovingDetection",
			`{"Reference":"x/2","AlarmType":"FastMoving","TimeStamp":1,"SourceID":3,"SourceName":"Channel3","AlarmPicture":{"ImageNum":1,"ImageList":[{"Index":0,"Format":0,"Data":"` +
				base64.StdEncoding.EncodeToString(jpeg) + `"}]}}`,
			"FastMoving", "", "FastMoving (Channel3)", true, 2},
		{"/LAPI/V1.0/System/Event/Notification/PeopleCount/LineRuleData",
			`{"Reference":"x/2","TimeStamp":1,"ChannelID":0,"LineNum":0}`, `PeopleCount\LineRuleData`, "", "PeopleCount/LineRuleData", false, 2},
	}
	for _, c := range cases {
		n, err := ParseNotification(c.path, []byte(c.body))
		if err != nil {
			t.Errorf("%s: %v", c.body, err)
			continue
		}
		id, _ := ReferenceID(n.Reference)
		if n.Kind() != c.kind || n.State() != c.state || n.Text() != c.text || n.Alarm() != c.alarm || id != c.sub {
			t.Errorf("%s: kind %q state %q text %q alarm %v sub %d", c.body, n.Kind(), n.State(), n.Text(), n.Alarm(), id)
		}
	}
	n, _ := ParseNotification(cases[5].path, []byte(cases[5].body))
	if string(n.Picture) != string(jpeg) || n.SrcID != 3 || strings.Contains(n.Raw, "/9j/") && len(n.Raw) > 400 {
		t.Errorf("picture %x, src %d, raw %s", n.Picture, n.SrcID, n.Raw)
	}
	for body, want := range map[string]int{
		`{"AlarmInfo":{"AlarmType":"MotionAlarmOn","AlarmSrcType":8,"AlarmSrcID":3}}`:  3,
		`{"AlarmInfo":{"AlarmType":"InputAlarmOn","AlarmSrcType":9,"AlarmSrcID":201}}`: 2,
		`{"AlarmInfo":{"AlarmType":"InputAlarmOn","AlarmSrcType":9,"AlarmSrcID":1}}`:   0,
		`{"AlarmInfo":{"AlarmType":"DiskOffline","AlarmSrcType":0,"AlarmSrcID":1}}`:    0,
		`{"AlarmInfo":{"AlarmType":"MotionAlarmOn","AlarmSrcID":4}}`:                   4,
		`{"AlarmInfo":{"AlarmType":"MotionAlarmOn"}}`:                                  0,
	} {
		if n, _ := ParseNotification("/", []byte(body)); n.RecorderChannel() != want {
			t.Errorf("%s: channel %d, want %d", body, n.RecorderChannel(), want)
		}
	}
	big := `{"Reference":"x/1","AlarmInfo":{"AlarmType":"MotionAlarmOn"},"Extra":"` + strings.Repeat("A", 5000) + `"}`
	if n, _ := ParseNotification("/", []byte(big)); !strings.Contains(n.Raw, `"<5000 bytes>"`) || len(n.Raw) > 200 {
		t.Errorf("long strings are cut: %s", n.Raw)
	}
	if _, err := ParseNotification("/", []byte("<xml/>")); err == nil {
		t.Error("not JSON")
	}
	for ref, want := range map[string]int64{"a/Subscribers/12": 12, "12": 12, "a/b": -1, "": -1, "a/x12": -1} {
		id, ok := ReferenceID(ref)
		if ok != (want >= 0) || ok && id != want {
			t.Errorf("ReferenceID(%q) = %d %v", ref, id, ok)
		}
	}
}

// The subscription of the fake: made with this host's address and the port asked for,
// renewed, ended at the end; the pushes come there. A device that forgets the
// subscription (a restart) ends Events with an error.
func TestEvents(t *testing.T) {
	dev := univiewtest.New()
	defer dev.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		n, err := ParseNotification(r.URL.Path, b)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		got = append(got, n.Type+" "+n.Reference)
		mu.Unlock()
		w.Write(NotificationAnswer(r.URL.Path))
	})}
	go srv.Serve(ln)
	defer srv.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	c, err := NewClient(dev.Addr(), univiewtest.User, univiewtest.Pass, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	subs := make(chan Subscription, 1)
	done := make(chan error, 1)
	go func() { done <- c.Events(ctx, "", port, 50*time.Millisecond, func(s Subscription) { subs <- s }) }()
	var sub Subscription
	select {
	case sub = <-subs:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("not subscribed")
	}
	if sub.ID != 1 || sub.Address != "127.0.0.1:"+strconv.Itoa(port) || sub.Until.IsZero() {
		t.Errorf("subscription %+v", sub)
	}
	if n := dev.PushAlarm("MotionAlarmOn"); n != 1 {
		t.Errorf("%d took the push", n)
	}
	time.Sleep(200 * time.Millisecond)
	dev.Mu.Lock()
	renewals, d := dev.Renewals, dev.Subs[1].Duration
	dev.Mu.Unlock()
	if renewals < 2 || d != 30 { // three times the renewal, at least 30 s
		t.Errorf("%d renewals, duration %d", renewals, d)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("Events: %v", err)
	}
	if dev.Subscribed() != 0 || len(dev.Unsubscribed) != 1 {
		t.Errorf("not unsubscribed: %d left, %v", dev.Subscribed(), dev.Unsubscribed)
	}
	mu.Lock()
	if len(got) != 1 || got[0] != "MotionAlarmOn 127.0.0.1:"+strconv.Itoa(port)+"/Subscription/Subscribers/1" {
		t.Errorf("pushes %q", got)
	}
	mu.Unlock()

	// the device restarts: the renewal fails, Events ends
	go func() { done <- c.Events(context.Background(), "", port, 50*time.Millisecond, nil) }()
	time.Sleep(100 * time.Millisecond)
	dev.Mu.Lock()
	dev.Forget = true
	dev.Mu.Unlock()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "renewing the subscription") {
			t.Errorf("Events after a restart: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Events did not end when the device forgot the subscription")
	}
	// a wrong password
	bad, _ := NewClient(dev.Addr(), univiewtest.User, "wrong", false)
	if _, err := bad.Subscribe(context.Background(), "", port, time.Minute); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := c.Subscribe(context.Background(), "fe80::1", port, time.Minute); err == nil {
		t.Error("an IPv6 address was given to the device")
	}
}

// What an IPC sent on the wire (tcpdump): the answer to the subscription with a comma
// before }, the pushes as text/plain Json={...} naming Subscribers/0.
func TestWireQuirks(t *testing.T) {
	answer := "{\n\"Response\": {\n        \"ResponseURL\": \"/LAPI/V1.0/System/Event/Subscription/65280\",\n        \"CreatedID\": 65280, \n" +
		"        \"ResponseCode\": 0,\n        \"SubResponseCode\": 0,\n        \"ResponseString\": \"Succeed\",\n        \"StatusCode\": 0,\n" +
		"        \"StatusString\": \"Succeed\",\n        \"Data\": {\n            \"ID\": 65280,\n" +
		"            \"Reference\": \"192.168.0.149:80/Subscription/Subscribers/65280\",\n            \"CurrentTime\": 1794064482,\n" +
		"            \"TerminationTime\": 1794064542,\n\n        }\n}\n"
	var env struct {
		Response struct {
			Data struct{ ID, TerminationTime int64 }
		}
	}
	if err := json.Unmarshal(Lenient([]byte(answer)), &env); err != nil || env.Response.Data.ID != 65280 || env.Response.Data.TerminationTime != 1794064542 {
		t.Errorf("answer: %v %+v", err, env)
	}
	for in, want := range map[string]string{
		`{"a":"x,}","b":[1,2,],}`: `{"a":"x,}","b":[1,2]}`,
		`{"a":{"b":[1,{"c":"}"}`:  `{"a":{"b":[1,{"c":"}"}]}}`,
		`{"a":1}`:                 `{"a":1}`,
	} {
		if got := string(Lenient([]byte(in))); got != want {
			t.Errorf("Lenient(%s) = %s, want %s", in, got, want)
		}
	}
	push := "Json={\n        \"Reference\":    \"192.168.0.149:80/Subscription/Subscribers/0\",\n        \"AlarmInfo\":    {\n" +
		"                \"AlarmType\":    \"MotionAlarmOn\",\n                \"TimeStamp\":    1794064497,\n                \"AlarmSeq\":     0\n        }\n}"
	n, err := ParseNotification("/LAPI/V1.0/System/Event/Notification/Alarm", []byte(push))
	if err != nil || n.Kind() != "MotionAlarm" || n.State() != "start" || n.Time.Unix() != 1794064497 {
		t.Errorf("push: %v %+v", err, n)
	}
	enc := "Json=" + url.QueryEscape(`{"AlarmInfo":{"AlarmType":"MotionAlarmOff"}}`)
	if n, err := ParseNotification("/", []byte(enc)); err != nil || n.Type != "MotionAlarmOff" {
		t.Errorf("URL-encoded: %v %+v", err, n)
	}

	dev := univiewtest.New()
	defer dev.Close()
	dev.Quirky = true
	c, _ := NewClient(dev.Addr(), univiewtest.User, univiewtest.Pass, false)
	sub, err := c.Subscribe(context.Background(), "127.0.0.1", 8091, time.Minute)
	if err != nil || sub.ID != 65280 {
		t.Fatalf("subscribe: %v %+v", err, sub)
	}
	if _, err := c.Renew(context.Background(), sub.ID, time.Minute); err != nil {
		t.Errorf("renew: %v", err)
	}
}
