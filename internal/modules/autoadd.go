package modules

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
)

// Cameras that appear on a remote recorder are added by the worker (like /nvr scan
// does), so the live view and the archive see them without anyone scanning. Only
// channels never seen before: the worker remembers the channels it has seen with a
// camera (state directory, <nvr>.seen.json), so a camera removed from the
// configuration on purpose does not come back; /nvr scan still adds every missing one.
// The first time (no such file) the channels without a camera in the configuration
// count as new. auto-add=no on the recorder turns it off.

func seenFile(root, nvr string) string { return filepath.Join(root, StatusDir, nvr+".seen.json") }

func readSeen(root, nvr string) (map[int]bool, bool) {
	b, err := os.ReadFile(seenFile(root, nvr))
	if err != nil {
		return map[int]bool{}, false
	}
	var list []int
	if json.Unmarshal(b, &list) != nil {
		return map[int]bool{}, false
	}
	m := map[int]bool{}
	for _, n := range list {
		m[n] = true
	}
	return m, true
}

func writeSeen(root, nvr string, seen map[int]bool) error {
	var list []int
	for n := range seen {
		list = append(list, n)
	}
	sort.Ints(list)
	b, _ := json.Marshal(list)
	dir := filepath.Join(root, StatusDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".seen-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), seenFile(root, nvr))
}

// NewChannels are the channels to add, per recorder: with a camera now (as last read),
// none in the configuration, never seen before. Recorders with nothing new are
// marked as seen at once.
func NewChannels(s *config.Store, root string) map[string][]Channel {
	out := map[string][]Channel{}
	for _, nvr := range s.Items("/nvr") {
		name := nvr.Val("name")
		if !remote(nvr) || nvr.Val("disabled") == "yes" || nvr.Val("auto-add") == "no" {
			continue
		}
		st := readStatus(root, name)
		if st == nil || st.Error != "" || st.StatesError != "" {
			continue // unknown, or every channel looks taken: only /nvr scan (by hand)
		}
		configured := map[int]bool{}
		for _, c := range s.Items("/camera") {
			if c.Val("nvr") == name {
				n, _ := strconv.Atoi(c.Val("channel"))
				configured[n] = true
			}
		}
		seen, known := readSeen(root, name)
		if !known {
			seen = configured // the first time: what has no camera yet is new
		}
		var add []Channel
		changed := !known
		for _, ch := range st.ChannelList {
			if !ch.Active() {
				continue
			}
			if !seen[ch.Number] && !configured[ch.Number] {
				add = append(add, ch)
				continue
			}
			if !seen[ch.Number] {
				seen[ch.Number], changed = true, true
			}
		}
		if len(add) > 0 {
			out[name] = add
		} else if changed {
			writeSeen(root, name, seen)
		}
	}
	return out
}

// AddNewCameras adds the cameras of NewChannels to s (the worker then saves s) and
// marks them seen; the lines tell what was done.
func AddNewCameras(s *config.Store, root string, todo map[string][]Channel) ([]string, error) {
	var log []string
	names := make([]string, 0, len(todo))
	for n := range todo {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		nvr := find(s, "/nvr", "name", name)
		if nvr == nil {
			continue
		}
		c, err := newRecorder(nvr)
		if err != nil {
			return log, err
		}
		st := readStatus(root, name)
		width := 2
		if st != nil {
			width = max(2, len(strconv.Itoa(len(st.ChannelList))))
		}
		seen, _ := readSeen(root, name)
		for _, cam := range s.Items("/camera") {
			if cam.Val("nvr") == name {
				n, _ := strconv.Atoi(cam.Val("channel"))
				seen[n] = true
			}
		}
		for _, ch := range todo[name] {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			msg, err := addChannelCamera(ctx, s, c, name, ch, width)
			cancel()
			seen[ch.Number] = true // also when it fails: said once, not every poll
			if err != nil {
				log = append(log, name+": channel "+strconv.Itoa(ch.Number)+" not added: "+err.Error()+" (add it by hand, or /nvr scan "+name+")")
				continue
			}
			log = append(log, name+":"+strings.TrimPrefix(msg, " "))
		}
		writeSeen(root, name, seen)
	}
	return log, nil
}
