// Package script parses the MikroTik RouterOS-like configuration syntax.
//
//	/camera
//	add name=front url="rtsp://10.0.0.20/stream1" \
//	    comment="front door"
//	set front disabled=yes
//	/system clock set time-zone-name=Europe/Warsaw
package script

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Param is a key=value pair.
type Param struct{ Key, Value string }

// Command is one parsed command.
type Command struct {
	File    string
	Line    int
	Path    string   // absolute menu path, e.g. "/camera/stream"
	Verb    string   // add, set, remove, ...
	Args    []string // positional arguments (indexes, names)
	Find    []Param  // selector from [find k=v ...]; Key "" means the menu's item name (NameKey)
	HasFind bool
	Params  []Param
}

// Verbs recognised by the parser.
var Verbs = map[string]bool{
	"add": true, "set": true, "remove": true, "unset": true,
	"print": true, "export": true, "enable": true, "disable": true,
	"cancel": true,                // action menus (/nvr/download)
	"scan":   true, "probe": true, // menu commands (/nvr scan, /nvr probe)
	"rights": true, "status": true, // /user rights, /user/policy rights
	"reboot": true, "backup": true, "upgrade": true, "datetime": true, // /nvr/tools
}

type token struct {
	key, val string
	eq       bool // key=value
	bracket  bool // [ ... ]
}

func unescape(c rune) rune {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	}
	return c
}

func tokenize(s string) ([]token, error) {
	var toks []token
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		for i < len(rs) && unicode.IsSpace(rs[i]) {
			i++
		}
		if i >= len(rs) || rs[i] == '#' {
			break
		}
		if rs[i] == '[' {
			depth, j, inq := 0, i, false
			for ; j < len(rs); j++ {
				c := rs[j]
				if inq {
					if c == '\\' {
						j++
					} else if c == '"' {
						inq = false
					}
					continue
				}
				if c == '"' {
					inq = true
				} else if c == '[' {
					depth++
				} else if c == ']' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated '['")
			}
			toks = append(toks, token{val: string(rs[i+1 : j]), bracket: true})
			i = j + 1
			continue
		}
		var cur []rune
		eqAt, inq := -1, false
		for i < len(rs) {
			c := rs[i]
			if inq {
				if c == '\\' && i+1 < len(rs) {
					i++
					cur = append(cur, unescape(rs[i]))
				} else if c == '"' {
					inq = false
				} else {
					cur = append(cur, c)
				}
				i++
				continue
			}
			if unicode.IsSpace(c) {
				break
			}
			if c == '"' {
				inq = true
				i++
				continue
			}
			if c == '=' && eqAt < 0 {
				eqAt = len(cur)
			}
			cur = append(cur, c)
			i++
		}
		if inq {
			return nil, fmt.Errorf("unterminated quote")
		}
		t := token{val: string(cur)}
		if eqAt > 0 {
			t.eq, t.key, t.val = true, string(cur[:eqAt]), string(cur[eqAt+1:])
		}
		toks = append(toks, t)
	}
	return toks, nil
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ParseLine parses one logical line relative to the current menu path cur.
// It returns the command (Verb=="" when the line only changes the path or is
// empty) and the new current path.
func ParseLine(cur, line string) (Command, string, error) {
	toks, err := tokenize(line)
	if err != nil {
		return Command{}, cur, err
	}
	if len(toks) == 0 {
		return Command{}, cur, nil
	}
	segs := splitPath(cur)
	var cmd Command
	i := 0
	for ; i < len(toks); i++ {
		t := toks[i]
		if t.eq || t.bracket {
			break
		}
		w := t.val
		if Verbs[w] {
			cmd.Verb = w
			i++
			break
		}
		if w == ".." {
			if len(segs) > 0 {
				segs = segs[:len(segs)-1]
			}
			continue
		}
		if strings.HasPrefix(w, "/") {
			segs = nil
		}
		parts := splitPath(w)
		if strings.Contains(w, "/") && len(parts) > 0 && Verbs[parts[len(parts)-1]] {
			// "/camera/set": the command may be the last segment of the path
			segs = append(segs, parts[:len(parts)-1]...)
			cmd.Verb = parts[len(parts)-1]
			i++
			break
		}
		segs = append(segs, parts...)
	}
	path := "/" + strings.Join(segs, "/")
	if cmd.Verb == "" {
		if i < len(toks) {
			return Command{}, cur, fmt.Errorf("missing command (add/set/remove/...) before %q", toks[i].key+toks[i].val)
		}
		return Command{}, path, nil
	}
	cmd.Path = path
	for ; i < len(toks); i++ {
		t := toks[i]
		switch {
		case t.bracket:
			inner, err := tokenize(t.val)
			if err != nil {
				return Command{}, cur, err
			}
			cmd.HasFind = true
			for _, it := range inner {
				switch {
				case it.eq && it.key == "find":
					// [ find=front ]: shorthand for the item name (menu NameKey)
					cmd.Find = append(cmd.Find, Param{"", it.val})
				case it.eq:
					cmd.Find = append(cmd.Find, Param{it.key, it.val})
				case it.val == "find" || it.val == "where":
				case len(cmd.Find) == 0 && !strings.ContainsAny(it.val, "()<>!~"):
					// [ find front ]: a bare word selects by item name (menu NameKey)
					cmd.Find = append(cmd.Find, Param{"", it.val})
				default:
					return Command{}, cur, fmt.Errorf("unsupported expression in [ %s ]", t.val)
				}
			}
		case t.eq:
			cmd.Params = append(cmd.Params, Param{t.key, t.val})
		default:
			cmd.Args = append(cmd.Args, t.val)
		}
	}
	return cmd, cur, nil // one-liners do not change the current path
}

// Parse reads a whole script.
func Parse(r io.Reader, file string) ([]Command, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var cmds []Command
	cur, pending := "", ""
	lineNo, start := 0, 0
	flush := func(full string) error {
		cmd, np, err := ParseLine(cur, full)
		if err != nil {
			return fmt.Errorf("%s:%d: %w", file, start, err)
		}
		cur = np
		if cmd.Verb != "" {
			cmd.File, cmd.Line = file, start
			cmds = append(cmds, cmd)
		}
		return nil
	}
	for sc.Scan() {
		lineNo++
		if pending == "" {
			start = lineNo
		}
		line := sc.Text()
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.HasSuffix(trimmed, "\\") && !strings.HasSuffix(trimmed, "\\\\") {
			pending += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		full := pending + line
		pending = ""
		if err := flush(full); err != nil {
			return nil, err
		}
	}
	if pending != "" {
		if err := flush(pending); err != nil {
			return nil, err
		}
	}
	return cmds, sc.Err()
}
