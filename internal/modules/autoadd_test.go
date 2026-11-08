package modules

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"ffnvr/internal/config"
)

func autoAddStore(t *testing.T) (*config.Store, string) {
	t.Helper()
	s, root := newStore(t), t.TempDir()
	for i, e := range run(t, s,
		`/nvr add type=dahua name=dh host=10.0.0.29 username=admin password=x`,
		`/camera add nvr=dh name=ch01 channel=1`,
	) {
		if e != "" {
			t.Fatalf("line %d: %s", i, e)
		}
	}
	return s, root
}

func setChannels(t *testing.T, root, nvr string, chs ...Channel) {
	t.Helper()
	if err := writeStatus(root, nvr, &NvrStatus{Checked: time.Now(), Channels: len(chs), ChannelList: chs}); err != nil {
		t.Fatal(err)
	}
}

func todoText(todo map[string][]Channel) string {
	var parts []string
	for nvr, chs := range todo {
		for _, ch := range chs {
			parts = append(parts, nvr+"/"+strconv.Itoa(ch.Number))
		}
	}
	return strings.Join(parts, " ")
}

func dhCameras(s *config.Store) string {
	var out []string
	for _, c := range s.Items("/camera") {
		if c.Val("nvr") == "dh" {
			out = append(out, c.Val("name")+"@"+c.Val("channel"))
		}
	}
	return strings.Join(out, " ")
}

func TestAutoAddNewChannels(t *testing.T) {
	s, root := autoAddStore(t)
	// the case of the report: the recorder now has 3 more cameras; an empty and a
	// disconnected channel are not cameras to add (yet)
	setChannels(t, root, "dh",
		Channel{Number: 1, State: "Connected"}, Channel{Number: 2, State: "Connected", Title: "Gate 2"},
		Channel{Number: 3, State: "Connected"}, Channel{Number: 4, State: "Connected"},
		Channel{Number: 5, State: "Empty"}, Channel{Number: 6, State: "Disconnected"})
	todo := NewChannels(s, root)
	if got := todoText(todo); got != "dh/2 dh/3 dh/4" {
		t.Fatalf("new: %q", got)
	}
	log, err := AddNewCameras(s, root, todo)
	if err != nil {
		t.Fatal(err)
	}
	if got := dhCameras(s); got != "ch01@1 ch02@2 ch03@3 ch04@4" {
		t.Errorf("cameras: %s\n%s", got, strings.Join(log, "\n"))
	}
	if !strings.Contains(strings.Join(log, "\n"), "dh: ch02: added, alias Gate_2") {
		t.Errorf("log: %q", log)
	}
	for _, c := range s.Items("/camera") {
		if c.Val("name") == "ch02" && c.Val("stream") != "rtsp://10.0.0.29:554/cam/realmonitor?channel=2&subtype=0" {
			t.Errorf("ch02 stream: %q", c.Val("stream"))
		}
	}
	if todo := NewChannels(s, root); len(todo) != 0 {
		t.Errorf("again: %v", todoText(todo))
	}

	// removed on purpose: does not come back (/nvr scan would)
	run(t, s, `/camera remove [ find name=ch03 nvr=dh ]`)
	if todo := NewChannels(s, root); len(todo) != 0 {
		t.Errorf("a removed camera comes back: %v", todoText(todo))
	}
	// the disconnected channel gets its camera: new
	setChannels(t, root, "dh", Channel{Number: 1, State: "Connected"}, Channel{Number: 2, State: "Connected"},
		Channel{Number: 3, State: "Connected"}, Channel{Number: 4, State: "Connected"}, Channel{Number: 6, State: "Connected"})
	if got := todoText(NewChannels(s, root)); got != "dh/6" {
		t.Errorf("connected later: %q", got)
	}
	// auto-add=no, a recorder that is offline, or tells no channel states: nothing
	run(t, s, `/nvr set [ find name=dh ] auto-add=no`)
	if todo := NewChannels(s, root); len(todo) != 0 {
		t.Errorf("auto-add=no: %v", todoText(todo))
	}
	run(t, s, `/nvr unset [ find name=dh ] auto-add`)
	for _, st := range []*NvrStatus{
		{Checked: time.Now(), Error: "timeout", ChannelList: []Channel{{Number: 7, State: "Connected"}}},
		{Checked: time.Now(), StatesError: "no states", ChannelList: []Channel{{Number: 7}}},
	} {
		writeStatus(root, "dh", st)
		if todo := NewChannels(s, root); len(todo) != 0 {
			t.Errorf("%+v: %v", st, todoText(todo))
		}
	}
}

// the first time (no memory yet) the channels without a camera are new: the report's
// recorder gets its cameras right after the update
func TestAutoAddFirstTime(t *testing.T) {
	s, root := autoAddStore(t)
	setChannels(t, root, "dh", Channel{Number: 1, State: "Connected"}, Channel{Number: 2, State: "Connected"})
	if got := todoText(NewChannels(s, root)); got != "dh/2" {
		t.Errorf("first time: %q", got)
	}
	// nothing new: the memory starts with what is there
	run(t, s, `/camera add nvr=dh name=ch02 channel=2`)
	if todo := NewChannels(s, root); len(todo) != 0 {
		t.Errorf("%v", todoText(todo))
	}
	if seen, ok := readSeen(root, "dh"); !ok || !seen[1] || !seen[2] {
		t.Errorf("seen: %v %v", seen, ok)
	}
}
