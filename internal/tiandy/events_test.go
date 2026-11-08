package tiandy_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"ffnvr/internal/tiandy"
	"ffnvr/internal/tiandy/tiandytest"
)

// A frame of a TC camera, as captured (192.168.0.53): the second record leaves out the
// address and the system.
const capturedFrame = "192.168.0.53\tPROXY\tINNER\tPARAGET\tLOCALSTORE\tMAPDEVICE\t0\t/nfs\t\t\t0\n\n\n" +
	"INNER\tPARAGET\tDISK\tINFO\t12\t0\t0\t0\t0\t0\n\n\n\n\n\n"

func TestRecordTree(t *testing.T) {
	recs := tiandy.ParseRecords([]byte(capturedFrame), "10.0.0.1")
	if len(recs) != 2 {
		t.Fatalf("%d records: %+v", len(recs), recs)
	}
	if got := recs[0].String(); got != `192.168.0.53\PROXY\INNER\PARAGET\LOCALSTORE\MAPDEVICE\0\/nfs\\\0` {
		t.Errorf("first: %s", got)
	}
	// the second one is of the first one's address and system
	if got := recs[1].Tree(); got != `192.168.0.53\PROXY\INNER\PARAGET\DISK\INFO` || recs[1].Kind() != `INNER\PARAGET\DISK\INFO` {
		t.Errorf("second: %s (%s)", got, recs[1].Kind())
	}
	if !recs[1].Param() {
		t.Error("PARAGET is a parameter readout")
	}

	al := tiandy.ParseRecords([]byte("192.168.0.53\tPROXY\tINNER\tALARM\tMOTIONDETECT\t0\t1\n\n\n\n\n\n"), "")[0]
	ch, on, ok := al.AlarmState()
	if !ok || ch != 1 || !on || al.Kind() != `INNER\ALARM\MOTIONDETECT` {
		t.Fatalf("alarm: %d %v %v %q", ch, on, ok, al.Kind())
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(al.JSON()), &j); err != nil {
		t.Fatal(err)
	}
	f, _ := j["fields"].(map[string]any)
	if j["tree"] != `192.168.0.53\PROXY\INNER\ALARM\MOTIONDETECT` || j["system"] != "PROXY" || f["channel"] != 1.0 || f["active"] != true {
		t.Errorf("json: %s", al.JSON())
	}
	// values in capitals after a value stay values; a value is not a word
	r := tiandy.ParseRecords([]byte("10.1.1.1\tIP\tINNER\tPARAGET\tDEVMODEL\tTC-R3120 SPEC:I/B/V3.0"), "")[0]
	if r.Kind() != `INNER\PARAGET\DEVMODEL` || len(r.Args) != 1 {
		t.Errorf("devmodel: %q %q", r.Kind(), r.Args)
	}
	// a record of the system without the address takes the connection's
	r = tiandy.ParseRecords([]byte("IP\tINNER\tCMDID\t7777"), "10.2.2.2")[0]
	if r.Tree() != `10.2.2.2\IP\INNER\CMDID` {
		t.Errorf("no address: %s", r.Tree())
	}
}

type collected struct {
	mu  sync.Mutex
	evs []tiandy.Event
}

func (c *collected) add(e tiandy.Event) { c.mu.Lock(); c.evs = append(c.evs, e); c.mu.Unlock() }
func (c *collected) get() []tiandy.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tiandy.Event(nil), c.evs...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("waiting for " + what)
}

func TestEvents(t *testing.T) {
	defer tiandy.SetEventTimings(300*time.Millisecond, 100*time.Millisecond)()
	dev := tiandytest.New()
	defer dev.Close()
	dev.Burst = []string{"PROXY\tINNER\tPARAGET\tDISK\tINFO\t0\t0\t0\t0\t0\t0"}
	c, _ := tiandy.NewClient(dev.Addr(), tiandytest.User, tiandytest.Pass)
	ctx, cancel := context.WithCancel(context.Background())
	var got collected
	done := make(chan error, 1)
	go func() { done <- c.Events(ctx, nil, got.add) }()
	waitFor(t, "the login", func() bool { return dev.Logged() == 1 })
	time.Sleep(250 * time.Millisecond) // the burst is over

	const motion = "127.0.0.1\tPROXY\tINNER\tALARM\tMOTIONDETECT\t0\t1"
	for i := 0; i < 5; i++ { // repeated while the motion lasts: one start
		dev.Push(motion)
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, "the held end", func() bool { return len(got.get()) >= 5 })
	// a second alarm, ended by its record; a record of other news
	dev.Push("127.0.0.1\tPROXY\tINNER\tALARM\tVIDEOLOST\t1\t1")
	dev.Push("127.0.0.1\tPROXY\tINNER\tALARM\tVIDEOLOST\t1\t0", "INNER\tCMD\tSOMETHING\t5")
	waitFor(t, "the end of VIDEOLOST", func() bool { return len(got.get()) >= 8 })
	cancel()
	<-done

	var lines []string
	for _, e := range got.get() {
		l := e.Kind()
		switch {
		case e.Alarm && e.Active:
			l += " start"
		case e.Alarm && e.Held:
			l += " held"
		case e.Alarm:
			l += " stop"
		case e.Burst:
			l += " burst"
		}
		if e.Alarm {
			l += " ch" + string(rune('0'+e.Channel))
		}
		lines = append(lines, l)
	}
	want := []string{`USER\LOGON\LOGONSUCCESS burst`, `INNER\CMDID burst`, `INNER\PARAGET\DISK\INFO burst`,
		`INNER\ALARM\MOTIONDETECT start ch1`, `INNER\ALARM\MOTIONDETECT held ch1`,
		`INNER\ALARM\VIDEOLOST start ch2`, `INNER\ALARM\VIDEOLOST stop ch2`, `INNER\CMD\SOMETHING`}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	// the held end is at the last repetition, not when it was noticed
	for _, e := range got.get() {
		if e.Held && time.Since(e.Time) < 300*time.Millisecond {
			t.Errorf("held end at %v", e.Time)
		}
	}
}

func TestEventsLoginRefused(t *testing.T) {
	dev := tiandytest.New()
	defer dev.Close()
	c, _ := tiandy.NewClient(dev.Addr(), tiandytest.User, "wrong")
	if err := c.Events(context.Background(), func() { t.Error("connected") }, func(tiandy.Event) {}); err != tiandy.ErrLogin {
		t.Fatalf("err %v", err)
	}
}
