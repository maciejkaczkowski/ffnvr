package shell

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ffnvr/internal/config"
	"ffnvr/internal/script"
)

// Candidate is one completion. Text replaces the word being completed, Suffix is
// appended only when the candidate is chosen uniquely.
type Candidate struct {
	Text, Suffix, Help string
}

// Completer produces context sensitive completions from the menu schema and
// from the data already entered (item names, references to other menus).
type Completer struct {
	Store *config.Store
	Root  string // filesystem root for sys: sources (interfaces, shells, time zones)
}

var shellCommands = []Candidate{
	{"diff", " ", "show what commit would change"},
	{"commit", " ", "apply: write units, (re)start services, save"},
	{"save", " ", "write the configuration as a script"},
	{"load", " ", "apply a script or directory on top of the current state"},
	{"history", " ", "show the command history"},
	{"help", " ", "show the shell help"},
	{"quit", " ", "leave the shell"},
}

var verbHelp = map[string]string{
	"add": "create an item", "set": "change values", "remove": "delete items",
	"unset": "clear values", "enable": "enable items", "disable": "disable items",
	"print": "show the items", "export": "show as a script", "cancel": "stop a job",
}

// splitLine splits the text before the cursor into finished words, the word
// being typed, its byte offset and whether the cursor is inside an open quote.
func splitLine(line string) (words []string, partial string, start int, inQuote bool) {
	var cur strings.Builder
	tok, q, esc := -1, false, false
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRuneInString(line[i:])
		switch {
		case esc:
			esc = false
			cur.WriteRune(r)
		case q && r == '\\':
			esc = true
			cur.WriteRune(r)
		case r == '"':
			q = !q
			if tok < 0 {
				tok = i
			}
			cur.WriteRune(r)
		case !q && (r == ' ' || r == '\t'):
			if tok >= 0 {
				words = append(words, cur.String())
				cur.Reset()
				tok = -1
			}
		default:
			if tok < 0 {
				tok = i
			}
			cur.WriteRune(r)
		}
		i += size
	}
	if tok >= 0 {
		return words, cur.String(), tok, q
	}
	return words, "", len(line), false
}

func inOpenQuote(line string) bool {
	_, _, _, q := splitLine(line)
	return q
}

type result struct {
	cands []Candidate
	delta int    // rune offset inside the partial word where the replacement starts
	hint  string // shown by "?" when there is nothing to complete
}

// Complete returns the rune index (in line) where replacement starts and the candidates.
func (c *Completer) Complete(cur, line string) (int, []Candidate) {
	start, r := c.run(cur, line)
	return start, r.cands
}

// Help returns what "?" shows: the candidates, or a hint about the expected value.
func (c *Completer) Help(cur, line string) []Candidate {
	_, r := c.run(cur, line)
	if len(r.cands) == 0 && r.hint != "" {
		return []Candidate{{Text: r.hint}}
	}
	return r.cands
}

func (c *Completer) run(cur, line string) (int, result) {
	words, partial, bstart, inQ := splitLine(line)
	start := utf8.RuneCountInString(line[:bstart])
	if inQ {
		return start, result{}
	}
	r := c.complete(cur, words, partial)
	return start + r.delta, r
}

func prefixed(cands []Candidate, p string) []Candidate {
	var out []Candidate
	for _, x := range cands {
		if strings.HasPrefix(x.Text, p) {
			out = append(out, x)
		}
	}
	return out
}

func (c *Completer) children(path string) []Candidate {
	var out []Candidate
	for _, ch := range c.Store.Children(path) {
		help := ""
		if m := c.Store.Menu(strings.TrimRight(path, "/") + "/" + ch); m != nil {
			help = m.Help
		}
		out = append(out, Candidate{ch, " ", help})
	}
	return out
}

func menuVerbs(m *config.Menu) []string {
	if m.Live != nil {
		return m.Live.Verbs()
	}
	if m.Kind == config.Single {
		return []string{"set", "unset", "print", "export"}
	}
	v := []string{"add", "set", "remove", "unset"}
	for name := range m.Commands {
		v = append(v, name)
	}
	for _, f := range m.Fields {
		if f.Name == "disabled" || f.Name == "enabled" {
			v = append(v, "enable", "disable")
			break
		}
	}
	return append(v, "print", "export")
}

func fieldCands(fields []config.Field, used map[string]bool, suffix string) []Candidate {
	var out []Candidate
	for _, f := range fields {
		if used[f.Name] {
			continue
		}
		h := f.TypeName()
		if f.Required {
			h += " (required)"
		}
		if f.Help != "" {
			h += "  " + f.Help
		}
		out = append(out, Candidate{f.Name, suffix, h})
	}
	return out
}

func summary(it *config.Item) string {
	var parts []string
	for _, k := range it.Keys() {
		parts = append(parts, k+"="+it.Val(k))
		if len(parts) == 4 {
			break
		}
	}
	s := strings.Join(parts, " ")
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

func (c *Completer) selectors(m *config.Menu) []Candidate {
	var out []Candidate
	count := map[string]int{}
	for _, it := range c.Store.Items(m.Path) {
		for _, k := range []string{m.NameKey, m.AliasKey} {
			if k != "" && it.Val(k) != "" {
				count[it.Val(k)]++
			}
		}
	}
	for i, it := range c.Store.Items(m.Path) {
		text := strconv.Itoa(i)
		// a name shared by several items (NameScope) cannot select one: offer the number
		if n := it.Val(m.NameKey); m.NameKey != "" && n != "" && count[n] == 1 {
			text = n
		}
		out = append(out, Candidate{text, " ", summary(it)})
		if a := it.Val(m.AliasKey); m.AliasKey != "" && a != "" && count[a] == 1 && a != text {
			out = append(out, Candidate{a, " ", summary(it)})
		}
	}
	return out
}

func (c *Completer) complete(cur string, words []string, partial string) result {
	if len(words) == 1 && (words[0] == "load" || words[0] == "save") {
		return result{cands: c.files(partial)}
	}
	cmd, np, err := script.ParseLine(cur, strings.Join(words, " "))
	if err != nil {
		return result{}
	}
	if cmd.Verb == "" { // still choosing the menu / command
		if strings.Contains(partial, "=") {
			return result{}
		}
		if i := strings.LastIndex(partial, "/"); i >= 0 {
			base := partial[:i+1]
			_, bp, err := script.ParseLine(np, base)
			if err != nil || !c.Store.HasPath(bp) {
				return result{}
			}
			return result{cands: prefixed(c.children(bp), partial[i+1:]), delta: utf8.RuneCountInString(base)}
		}
		var pool []Candidate
		pool = append(pool, c.children(np)...)
		if m := c.Store.Menu(np); m != nil {
			for _, v := range menuVerbs(m) {
				h := verbHelp[v]
				if cmd, ok := m.Commands[v]; ok {
					h = cmd.Help
				}
				pool = append(pool, Candidate{v, " ", h})
			}
		} else if c.Store.HasPath(np) {
			// "/" or a level that only groups menus: export everything below
			pool = append(pool, Candidate{"export", " ", "show everything below as a script"})
		}
		if len(words) == 0 {
			pool = append(pool, shellCommands...)
			if np != "/" {
				pool = append(pool, Candidate{"..", " ", "up one level"})
			}
		}
		out := prefixed(pool, partial)
		sort.SliceStable(out, func(i, j int) bool { return out[i].Text < out[j].Text })
		return result{cands: out}
	}

	m := c.Store.Menu(cmd.Path)
	if m == nil {
		return result{}
	}
	selected := cmd.HasFind || len(cmd.Args) > 0
	hybrid := m.Kind == config.Multi && len(m.Settings) > 0
	settingsCtx := hybrid && (cmd.Verb == "set" || cmd.Verb == "unset") && !selected
	fields := m.Fields
	if settingsCtx {
		fields = m.Settings
	}
	used := map[string]bool{}
	for _, p := range cmd.Params {
		used[p.Key] = true
	}

	if eq := strings.Index(partial, "="); eq >= 0 {
		switch {
		case cmd.Verb == "add", cmd.Verb == "set":
		case m.Live != nil && cmd.Verb != "print" && cmd.Verb != "cancel" && cmd.Verb != "remove": // actions
		default:
			return result{}
		}
		key := partial[:eq]
		for _, f := range fields {
			if f.Name == key {
				return c.values(f, partial[eq+1:], utf8.RuneCountInString(partial[:eq+1]))
			}
		}
		return result{}
	}

	var pool []Candidate
	if m.Live != nil {
		if cmd.Verb != "cancel" && cmd.Verb != "remove" && cmd.Verb != "print" {
			pool = fieldCands(m.Fields, used, "=") // add, and actions (/nvr/tools reboot ...)
		} else if !selected && cmd.Verb != "print" {
			for _, id := range m.Live.IDs() {
				pool = append(pool, Candidate{id[0], " ", id[1]})
			}
		}
		return result{cands: prefixed(pool, partial)}
	}
	switch cmd.Verb {
	case "print":
		if m.Detail != nil && len(cmd.Args) == 0 {
			pool = []Candidate{{"detail", " ", "also the values in effect and where they come from"}}
		}
	case "add":
		pool = fieldCands(m.Fields, used, "=")
	case "set":
		switch {
		case m.Kind == config.Single || selected:
			pool = fieldCands(fields, used, "=")
		default:
			pool = append(c.selectors(m), fieldCands(m.Settings, used, "=")...)
		}
	case "unset":
		switch {
		case m.Kind == config.Single || selected:
			pool = fieldCands(fields, used, " ")
		default:
			pool = append(c.selectors(m), fieldCands(m.Settings, used, " ")...)
		}
	default:
		if _, ok := m.Commands[cmd.Verb]; !ok && cmd.Verb != "remove" && cmd.Verb != "enable" && cmd.Verb != "disable" {
			break
		}
		if m.Kind == config.Multi && !selected {
			pool = c.selectors(m)
		}
	}
	return result{cands: prefixed(pool, partial)}
}

// values completes the value of a parameter.
func (c *Completer) values(f config.Field, prefix string, delta int) result {
	list := f.Type == config.TList || f.Type == config.TEnumList
	have := map[string]bool{}
	p := prefix
	if list {
		if i := strings.LastIndex(prefix, ","); i >= 0 {
			delta += utf8.RuneCountInString(prefix[:i+1])
			for _, x := range strings.Split(prefix[:i], ",") {
				have[x] = true
			}
			p = prefix[i+1:]
		}
	}
	suffix := " "
	if list {
		suffix = ""
	}
	var vals []string
	switch f.Type {
	case config.TBool:
		vals = []string{"yes", "no"}
	case config.TEnum, config.TEnumList:
		vals = f.Enum
	}
	for _, src := range strings.Split(f.Ref, ",") {
		if src != "" {
			vals = append(vals, c.refValues(src)...)
		}
	}
	var out []Candidate
	seen := map[string]bool{}
	for _, v := range vals {
		if strings.HasPrefix(v, p) && !have[v] && !seen[v] {
			seen[v] = true
			out = append(out, Candidate{Text: v, Suffix: suffix})
		}
	}
	hint := "<" + f.TypeName() + ">"
	if f.Help != "" {
		hint += "  " + f.Help
	}
	return result{cands: out, delta: delta, hint: hint}
}

func (c *Completer) refValues(src string) []string {
	if name, ok := strings.CutPrefix(src, "sys:"); ok {
		return c.sysValues(name)
	}
	prefix, path, key, ok := config.ParseRef(src)
	if !ok || c.Store.Menu(path) == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, it := range c.Store.Items(path) {
		if v := it.Val(key); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, prefix+v)
		}
	}
	return out
}

func (c *Completer) sysValues(name string) []string {
	var out []string
	switch name {
	case "interfaces":
		ents, _ := os.ReadDir(filepath.Join(c.Root, "/sys/class/net"))
		for _, e := range ents {
			out = append(out, e.Name())
		}
	case "shells":
		b, _ := os.ReadFile(filepath.Join(c.Root, "/etc/shells"))
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "/") {
				out = append(out, l)
			}
		}
	case "users", "groups":
		file := map[string]string{"users": "/etc/passwd", "groups": "/etc/group"}[name]
		b, _ := os.ReadFile(filepath.Join(c.Root, file))
		for _, l := range strings.Split(string(b), "\n") {
			if n, _, ok := strings.Cut(l, ":"); ok && n != "" && n[0] != '#' {
				out = append(out, n)
			}
		}
	case "timezones":
		base := filepath.Join(c.Root, "/usr/share/zoneinfo")
		filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(base, p)
			if d.IsDir() {
				if rel == "posix" || rel == "right" {
					return filepath.SkipDir
				}
				return nil
			}
			if b := filepath.Base(rel); b != "" && b[0] >= 'A' && b[0] <= 'Z' && !strings.Contains(b, ".") {
				out = append(out, rel)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

func (c *Completer) files(prefix string) []Candidate {
	matches, _ := filepath.Glob(prefix + "*")
	var out []Candidate
	for _, m := range matches {
		suffix := " "
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			m, suffix = m+"/", ""
		}
		out = append(out, Candidate{m, suffix, ""})
		if len(out) >= 200 {
			break
		}
	}
	return out
}
