package eventdb

import (
	"context"
	"testing"
)

// The cameras of a local recorder have no channels: the camera tells their events
// apart, and a camera whose connection broke ends only its own.
func TestCamerasOfLocalRecorder(t *testing.T) {
	db, w, _ := newTest(t)
	const kind = `INNER\ALARM\MOTIONDETECT`
	add := func(cam string, s float64, st State) {
		w.Add(Notice{Nvr: "nvr01", Channel: 1, Camera: cam, Kind: kind, Source: "tiandy", Time: at(s), State: st})
	}
	add("a", 0, Start)
	add("b", 1, Start)
	add("a", 5, Stop)
	add("b", 3, Start) // repeated: joins b
	w.Add(Notice{Nvr: "nvr01", Channel: 1, Camera: "c", Kind: `INNER\CMD\X`, Source: "tiandy", Time: at(2), State: Pulse})
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	evs := all(t, db)
	if len(evs) != 3 {
		t.Fatalf("%d events: %+v", len(evs), evs)
	}
	byCam := map[string]Event{}
	for _, e := range evs {
		byCam[e.Camera] = e
	}
	if a := byCam["a"]; a.Open() || !a.Ended.Equal(at(5)) {
		t.Errorf("a: %+v", a)
	}
	if b := byCam["b"]; !b.Open() || b.Count != 2 {
		t.Errorf("b: %+v", b)
	}
	// another camera of the recorder loses its connection: b stays open
	add("x", 4, Start)
	w.LostCamera("nvr01", "x")
	w.Flush()
	if b := byCamera(t, db, "b"); !b.Open() {
		t.Errorf("b ended by the loss of x: %+v", b)
	}
	if x := byCamera(t, db, "x"); x.Open() {
		t.Errorf("x stays open: %+v", x)
	}
	w.LostCamera("nvr01", "b")
	w.Flush()
	if b := byCamera(t, db, "b"); b.Open() || !b.Ended.Equal(at(3)) {
		t.Errorf("b after its loss: %+v", b)
	}

	// kind= with a pattern: * any text, / for \
	for pat, want := range map[string]int{`*\ALARM\*`: 3, `*/alarm/*`: 3, `*CMD*`: 1, kind: 3, `INNER\ALARM`: 0, `*_*`: 0} {
		n, err := db.Count(context.Background(), Filter{Kind: pat})
		if err != nil || n != want {
			t.Errorf("kind=%s: %d %v, want %d", pat, n, err, want)
		}
	}
}

func byCamera(t *testing.T, db *DB, cam string) Event {
	t.Helper()
	evs, err := db.Query(context.Background(), Filter{Camera: cam})
	if err != nil || len(evs) != 1 {
		t.Fatalf("camera %s: %v %+v", cam, err, evs)
	}
	return evs[0]
}
