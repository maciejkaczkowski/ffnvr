// Package shell implements an interactive RouterOS-like shell. Changes are kept
// in memory until "commit" brings the services in line and saves the configuration;
// "diff" shows what commit would change.
//
// On a terminal it has line editing, history (Ctrl-R search), Tab completion and
// "?" context help; with a pipe or a script it reads plain lines.
package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"ffnvr/internal/engine"
	"ffnvr/internal/eventdb"
	"ffnvr/internal/modules"
	"ffnvr/internal/script"
)

// Options of the shell.
type Options struct {
	SaveFile    string // default file of "save"
	HistoryFile string // persistent history, "" keeps it in memory only
}

const helpText = `Navigation:   /camera/stream   |   /camera stream   |   ..   |   /
Commands:     add, set, remove, unset, enable, disable, print, export
              e.g.  add name=front url="rtsp://10.0.0.20/stream1"
                    set 0 disabled=yes        remove [find comment="x"]
Keys:         Tab completes (menus, parameters, values, item names), ? shows help for
              what you have typed so far, Up/Down and Ctrl-R browse the history,
              Ctrl-A/E/K/U/W/Y, Alt-B/F and the arrow keys edit the line.
diff          show what "commit" would change (systemd units, systemctl calls)
commit        write the units, start/stop/restart services, save the configuration
save [file]   write the configuration as a script (default: loaded file)
load <file>   apply a script/directory on top of the current state
history       show the command history
quit          leave the shell (Ctrl-D)
`

type session struct {
	e     *engine.Engine
	out   io.Writer
	cur   string
	opts  Options
	comp  *Completer
	dirty bool
	hist  func() []string
	// for saving beside the panel: when the file was loaded, which menus changed here
	loaded  time.Time
	touched map[string]bool
	skip    *regexp.Regexp // lines with secrets never enter the history
}

// Run starts the shell reading from in and writing to out.
func Run(e *engine.Engine, in io.Reader, out io.Writer, o Options) error {
	s := &session{e: e, out: out, cur: "/", opts: o, comp: &Completer{Store: e.Store, Root: e.Opts.Root}, touched: map[string]bool{}}
	if o.SaveFile != "" {
		s.loaded = engine.ModTime(o.SaveFile)
	}
	var keys []string
	for k := range e.Store.SecretKeys() {
		keys = append(keys, regexp.QuoteMeta(k))
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		s.skip = regexp.MustCompile(`(?:^|\s)(?:` + strings.Join(keys, "|") + `)=`)
	}
	if f, ok := in.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			if restore, err := makeRaw(f); err == nil { // a real terminal
				restore()
				return s.interactive(f)
			}
			return s.plain(in, true)
		}
	}
	return s.plain(in, false)
}

func (s *session) prompt() string {
	mark := ""
	if s.dirty {
		mark = "*"
	}
	return "[ffnvr" + mark + "] " + s.cur + "> "
}

func (s *session) plain(in io.Reader, prompt bool) error {
	var hist []string
	s.hist = func() []string { return hist }
	sc := bufio.NewScanner(in)
	for {
		if prompt {
			fmt.Fprint(s.out, s.prompt())
		}
		if !sc.Scan() {
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line != "" && (s.skip == nil || !s.skip.MatchString(line)) {
			hist = append(hist, line)
		}
		if s.handle(line) {
			return nil
		}
	}
}

func (s *session) interactive(f *os.File) error {
	ed := &Editor{
		In: bufio.NewReader(f), Out: s.out,
		Complete: func(line string) (int, []Candidate) { return s.comp.Complete(s.cur, line) },
		Help:     func(line string) []Candidate { return s.comp.Help(s.cur, line) },
		Raw:      func() (func(), error) { return makeRaw(f) },
		Width:    func() int { return termWidth(f) },
		Skip:     func(l string) bool { return s.skip != nil && s.skip.MatchString(l) },
	}
	ed.History = loadHistory(s.opts.HistoryFile)
	s.hist = func() []string { return ed.History }
	fmt.Fprintln(s.out, "ffnvr shell - Tab completes, ? shows help, Ctrl-D leaves")
	for {
		line, err := ed.ReadLine(s.prompt())
		switch {
		case errors.Is(err, ErrInterrupt):
			continue
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		}
		n := len(ed.History)
		ed.AddHistory(line)
		if len(ed.History) > n {
			appendHistory(s.opts.HistoryFile, line)
		}
		if s.handle(line) {
			return nil
		}
	}
}

func (s *session) printCands(c []Candidate) {
	w := 0
	for _, x := range c {
		if n := len([]rune(x.Text)); n > w {
			w = n
		}
	}
	for _, x := range c {
		if x.Help != "" {
			fmt.Fprintf(s.out, "  %-*s  %s\n", w, x.Text, x.Help)
		} else {
			fmt.Fprintf(s.out, "  %s\n", x.Text)
		}
	}
}

// handle executes one line. It returns true when the shell must end.
func (s *session) handle(line string) bool {
	defer func() {
		for _, n := range s.e.Store.TakeNotices() {
			fmt.Fprintln(s.out, "note:", n)
		}
	}()
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	out := s.out
	// RouterOS style context help: "<text>?" (the key binding does this on a terminal)
	if before, ok := strings.CutSuffix(line, "?"); ok && !inOpenQuote(before) {
		if h := s.comp.Help(s.cur, before); len(h) > 0 {
			s.printCands(h)
		}
		return false
	}
	fields := strings.Fields(line)
	switch fields[0] {
	case "quit", "exit":
		return true
	case "help":
		fmt.Fprint(out, helpText)
		return false
	case "history":
		for i, h := range s.hist() {
			fmt.Fprintf(out, "%4d  %s\n", i+1, h)
		}
		return false
	case "diff":
		p, err := s.e.Plan()
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return false
		}
		p.WriteDiff(out)
		return false
	case "commit":
		p, err := s.e.Plan()
		if err == nil {
			err = s.e.Apply(p, out)
		}
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return false
		}
		if s.opts.SaveFile == "" {
			fmt.Fprintln(out, "warning: the configuration was not saved, use save <file>")
		} else if err := s.saveTo(s.opts.SaveFile); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
		}
		return false
	case "save":
		file := s.opts.SaveFile
		if len(fields) > 1 {
			file = fields[1]
		}
		if file == "" {
			fmt.Fprintln(out, "error: no file given")
		} else if err := s.saveTo(file); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
		}
		return false
	case "load":
		if len(fields) < 2 {
			fmt.Fprintln(out, "error: usage: load <file>")
		} else if err := s.e.LoadFiles(fields[1:]); err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
		} else {
			s.dirty = true
		}
		return false
	}
	cmd, np, err := script.ParseLine(s.cur, line)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return false
	}
	if cmd.Verb == "" {
		if !s.e.Store.HasPath(np) {
			fmt.Fprintf(out, "error: no such menu: %s\n", np)
			return false
		}
		s.cur = np
		return false
	}
	res, err := s.e.Store.Exec(cmd)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return false
	}
	if m := s.e.Store.Menu(cmd.Path); cmd.Verb != "print" && cmd.Verb != "export" && (m == nil || m.Live == nil) {
		s.dirty = true // action menus (/nvr/download) do not change the configuration
		if s.touched == nil {
			s.touched = map[string]bool{}
		}
		s.touched[cmd.Path] = true
	}
	fmt.Fprint(out, res)
	fmt.Fprint(out, s.e.AfterCommand(cmd))
	return false
}

func (s *session) saveTo(file string) error {
	if err := engine.SaveConfig(file, s.e.Store, s.e.Opts, s.loaded, s.touched); err != nil {
		return err
	}
	menus := make([]string, 0, len(s.touched))
	for m := range s.touched {
		menus = append(menus, m)
	}
	sort.Strings(menus)
	text := "saved " + file
	if len(menus) > 0 {
		text += ": " + strings.Join(menus, ", ")
	}
	modules.SystemLog(s.e.Opts.Root, func(f string, a ...any) { fmt.Fprintf(s.out, "warning: "+f+"\n", a...) },
		eventdb.Entry{Kind: modules.SysConfigSaved, User: modules.ShellUser(), Address: modules.ShellAddress(), Text: text})
	s.loaded = engine.ModTime(file)
	fmt.Fprintf(s.out, "saved %s\n", file)
	s.dirty = false
	return nil
}

func loadHistory(path string) []string {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > 2000 { // keep the file small
		lines = lines[len(lines)-1000:]
		os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	}
	return lines
}

func appendHistory(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}
