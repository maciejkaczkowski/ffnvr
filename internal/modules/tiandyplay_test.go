package modules

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ffnvr/internal/tiandy"
	"ffnvr/internal/tiandy/tiandytest"
)

func TestTiandyFiles(t *testing.T) {
	fake := tiandytest.New()
	defer fake.Close()
	// as the recorder tells them: Alarm and Timer one after the other, cut at midnight
	fake.Searched = map[int][][3]string{1: {
		{"2026-10-20 22:29:17", "2026-10-20 22:29:20"},
		{"2026-10-20 22:29:20", "2026-10-21 00:46:42"},
		{"2026-10-21 00:46:42", "2026-10-21 00:46:49"},
		{"2026-10-21 03:00:00", "2026-10-21 03:10:00"},
		{"2026-11-02 08:00:00", "2026-11-02 09:00:00"},
	}}
	c, _ := tiandy.NewClient(fake.Addr(), tiandytest.User, tiandytest.Pass)
	c.HTTPPort = fake.HTTPPort()
	r := &tiandyRecorder{c: c}
	ctx := context.Background()
	at := func(v string) time.Time { t, _ := time.ParseInLocation("2006-01-02 15:04", v, time.Local); return t }
	files, err := r.Files(ctx, 1, at("2026-10-20 00:00"), at("2026-11-03 00:00"))
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, f := range files {
		got += fmt.Sprintf("%s-%s %s; ", f.Start.Format("01-02 15:04:05"), f.End.Format("01-02 15:04:05"), f.Name)
	}
	if want := "10-20 22:29:17-10-21 00:46:49 Alarm; 10-21 03:00:00-10-21 03:10:00 Alarm; 11-02 08:00:00-11-02 09:00:00 Alarm; "; got != want {
		t.Errorf("%s\nwant %s", got, want)
	}
	// only the days the calendar has are searched: 20, 21 and 2
	days := 0
	for _, s := range fake.Searches {
		if s[:3] == "day" {
			days++
		}
	}
	if days != 3 {
		t.Errorf("%v", fake.Searches)
	}
	// a range in the middle of a file
	files, _ = r.Files(ctx, 1, at("2026-10-21 00:10"), at("2026-10-21 00:20"))
	if len(files) != 1 || !files[0].Start.Equal(at("2026-10-21 00:00")) {
		t.Errorf("%+v", files)
	}
	if d, err := r.Days(ctx, 1, at("2026-11-01 00:00")); err != nil || fmt.Sprint(d) != "[2]" {
		t.Errorf("%v %v", d, err)
	}
	// no web search: the recorder cannot be searched
	c.HTTPPort = fake.HTTPPort()
	fake.HTTP.Close()
	if _, err := r.Files(ctx, 1, at("2026-10-20 00:00"), at("2026-10-21 00:00")); err == nil {
		t.Error("no web interface")
	}
}

func TestJoinRecords(t *testing.T) {
	at := func(v string) time.Time { t, _ := time.ParseInLocation("15:04:05", v, time.Local); return t }
	got := joinRecords([]tiandy.Record{
		{Start: at("00:46:49"), End: at("00:52:43"), Type: "Alarm"},
		{Start: at("00:00:00"), End: at("00:46:42"), Type: "Alarm"},
		{Start: at("00:46:42"), End: at("00:46:49"), Type: "Timer"},
		{Start: at("00:52:43"), End: at("00:52:43"), Type: "Timer"},
		{Start: at("01:00:00"), End: at("01:00:05"), Type: "Timer"},
		{Start: at("00:52:43"), End: at("00:53:00"), Type: "Alarm"}, // follows the one before
	})
	if len(got) != 2 || got[0].Name != "Alarm+Timer" || !got[0].End.Equal(at("00:53:00")) || got[1].Name != "Timer" {
		t.Errorf("%+v", got)
	}
	// the alarms: motion (the Timer between two of them parts them)
	if ev := got[0].Events; len(ev) != 2 || !ev[0].End.Equal(at("00:46:42")) || !ev[1].Start.Equal(at("00:46:49")) || !ev[1].End.Equal(at("00:53:00")) || got[1].Events != nil {
		t.Errorf("%+v", ev)
	}
}

func TestPlayableTiandy(t *testing.T) {
	s, _, _ := dahuaStore(t)
	run(t, s, `/nvr add type=tiandy name=nvr05 host=10.0.0.5 username=Admin password=x`, `/camera add name=ch01 nvr=nvr05 channel=1`)
	if !Playable(s, "nvr05") || !SearchedByDay(s, "nvr05") || SearchedByDay(s, "nvr02") {
		t.Error("tiandy plays, searched by day")
	}
	if _, err := PlayDays(context.Background(), s, "nvr05", "nope", time.Now()); err == nil {
		t.Error("no camera")
	}
	// a stamped stream tells its start, so does a DHAV one
	p := []byte("TDST\x01\x00\x08\x00\xd3\xfe\x04\x00\x7f\x00\xd8\x6a")
	if st, ok := PlayStart(nil, append([]byte{0, 0, 0, 1, 6, 0xf0, 0x10}, p...)); !ok || st.Format("2006-01-02 15:04:05") != "2026-10-20 23:59:59" {
		t.Errorf("%v %v", st, ok)
	}
	if _, ok := PlayStart(nil, []byte("nothing")); ok {
		t.Error("nothing")
	}
}

// busyDay: a day of much motion, as on a TC-R3105: Timer and Alarm one after the other,
// more than fits in an answer
func busyDay(day time.Time, spans int) [][3]string {
	var out [][3]string
	t := day
	f := "2006-01-02 15:04:05"
	for i := 0; i < spans; i++ {
		typ, d := "Timer", 40*time.Second
		if i%2 == 1 {
			typ, d = "Alarm", 15*time.Second
		}
		out = append(out, [3]string{t.Format(f), t.Add(d).Format(f), typ})
		t = t.Add(d)
	}
	return out
}

func TestTiandyRefusedDay(t *testing.T) {
	fake := tiandytest.New()
	defer fake.Close()
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)
	fake.Searched = map[int][][3]string{2: busyDay(day, 1600)} // about 340 kB told whole
	fake.SearchLimit = 300 << 10
	c, _ := tiandy.NewClient(fake.Addr(), tiandytest.User, tiandytest.Pass)
	c.HTTPPort = fake.HTTPPort()
	r := &tiandyRecorder{c: c}
	ctx := context.Background()

	// the type obeyed: the day in halves, as exact as told whole
	fake.TypeFilter = true
	files, err := r.Files(ctx, 2, day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	last := day.Add(800*55*time.Second - 0) // 800 pairs of 40 + 15 s
	if len(files) != 1 || !files[0].Start.Equal(day) || !files[0].End.Equal(last) || files[0].Name != "Timer+Alarm" ||
		len(files[0].Events) != 800 || !files[0].Events[0].Start.Equal(day.Add(40*time.Second)) {
		t.Fatalf("%d files %v %v %q %d", len(files), files[0].Start, files[0].End, files[0].Name, len(files[0].Events))
	}
	want := []string{"day 2026-11-01 chanNo 1", "day 2026-11-01 chanNo 1", "day 2026-11-01 chanNo 1 Alarm", "day 2026-11-01 chanNo 1 Timer"}
	if got := fake.Searches[1:]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%v", got)
	}

	// the type not obeyed (refused again): the whole day, as the calendar says
	fake.TypeFilter = false
	files, err = r.Files(ctx, 2, day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !files[0].Start.Equal(day) || files[0].End.Format("15:04:05") != "23:59:59" ||
		files[0].Name != "unsearched" || len(files[0].Events) != 0 {
		t.Errorf("%+v", files)
	}
}
