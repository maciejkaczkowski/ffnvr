// Package config holds the in-memory configuration tree (menus, items) and
// executes parsed commands against it, validating them against menu schemas.
package config

import (
	"fmt"
	"strconv"
	"strings"

	"ffnvr/internal/script"
)

// Type is the value type of a field.
type Type int

const (
	TString Type = iota
	TBool
	TInt
	TEnum
	TEnumList // comma separated, every element from Enum
	TList     // comma separated free strings
)

// Field describes one parameter of a menu.
type Field struct {
	Name    string
	Type    Type
	Enum    []string
	Virtual bool // handled by Menu.Virtual, never stored
	Secret  bool // value is masked by "print"
	// Required: every item of a list menu must have a value (checked after add/set/unset).
	// Conditional requirements (only for some type=...) belong in Menu.Validate.
	Required bool
	// Strict turns the menu sources of Ref into a real reference: the value (every element
	// of a list) must exist there, and the referenced item cannot be removed or renamed
	// while it is used.
	Strict bool
	// Unique: no two items of a list menu may have the same value (the NameKey always is).
	Unique bool
	// KeepEmpty: key="" stores an empty value instead of removing the key (an empty
	// password that overrides an inherited one); unset removes it.
	KeepEmpty bool
	Help      string
	// Ref lists where the shell finds candidate values (comma separated):
	// "<menu path>:<key>" takes that key of all items of the menu, "sys:<name>"
	// is a built-in source (interfaces, timezones, users, groups, shells).
	Ref string
}

var typeNames = map[Type]string{TString: "string", TBool: "yes|no", TInt: "integer", TList: "a,b,c"}

// TypeName describes the accepted values for help texts.
func (f Field) TypeName() string {
	if f.Type == TEnum || f.Type == TEnumList {
		t := strings.Join(f.Enum, "|")
		if f.Type == TEnumList {
			t = "list of " + t
		}
		return t
	}
	return typeNames[f.Type]
}

// Kind of menu.
type Kind int

const (
	Single Kind = iota // one set of properties (/system/clock)
	Multi              // list of items (/camera)
)

// names returns the name and the alias of an item (those that are set).
func (m *Menu) names(it *Item) []string {
	var out []string
	for _, k := range []string{m.NameKey, m.AliasKey} {
		if k != "" && it.Val(k) != "" {
			out = append(out, it.Val(k))
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Menu is a node of the configuration tree.
type Menu struct {
	Path    string
	Kind    Kind
	Fields  []Field
	NameKey string // optional key usable as item name in "set <name>"
	// NameScope makes the name unique only among items with the same value of this field
	// (cameras: unique per nvr). A name shared by several items cannot select them,
	// "set cam1" then asks for the number or [find name=cam1 nvr=...].
	NameScope string
	// AliasKey is a second, optional name of an item (cameras: alias=). It selects the item
	// like the name does, and within NameScope no name or alias may repeat.
	AliasKey string
	Help     string
	Virtual  func(s *Store, key, val string) error
	// Settings turns a list menu into a hybrid one: "set k=v" without an item
	// selector changes these menu-wide settings (e.g. /record set enabled=yes),
	// while add/set <n>/remove work on the items described by Fields.
	Settings []Field
	// Aliases are alternative paths for the same menu.
	Aliases []string
	// Validate checks (and may normalise) an item after add/set; an error rejects the
	// command and leaves the item unchanged.
	// Required fields and Strict references are checked by the store before Validate runs,
	// unique values (NameKey, Unique fields) after it, so Validate may normalise them.
	Validate func(s *Store, it *Item) error
	// Detail gives the extra lines "print detail" shows under an item (e.g. the values
	// a camera takes from its profile).
	Detail func(s *Store, it *Item) []string
	// CheckParams rejects contradicting parameters given in ONE command.
	CheckParams func(params []script.Param) error
	// Live turns the menu into an action menu: its commands are not configuration.
	// They are checked against Fields like any other (for add also Required, Strict refs
	// and Validate) and handed to Live; nothing is stored, exported or saved, and
	// configuration scripts may not use the menu.
	Live Live
	// Fixed items exist from the start and cannot be added or removed, only set (like
	// /ip/service of RouterOS): their default values, NameKey included. unset restores a
	// default; export writes only what differs, as set [ find name=... ].
	Fixed []map[string]string
	// Commands are extra verbs acting on items without being configuration themselves
	// (/nvr scan nvr02). Items are selected like for set/remove; configuration scripts
	// may not use them.
	Commands map[string]Command
}

// Command is an extra verb of a menu.
type Command struct {
	Help string
	Run  func(s *Store, c script.Command, items []*Item) (string, error)
}

// Live executes the commands of an action menu (e.g. jobs: add, cancel, remove, print).
type Live interface {
	Verbs() []string
	// IDs lists the items a command may select, with a short summary (for completion).
	IDs() [][2]string
	// Run executes one command. params holds the checked parameters of the command.
	Run(s *Store, c script.Command, params *Item) (string, error)
}

func fieldIn(fields []Field, name string) *Field {
	for i := range fields {
		if fields[i].Name == name {
			return &fields[i]
		}
	}
	return nil
}

func (m *Menu) field(name string) *Field { return fieldIn(m.Fields, name) }

// Item is an ordered set of key=value pairs.
type Item struct {
	keys []string
	vals map[string]string
}

// NewItem creates an empty item.
func NewItem() *Item { return &Item{vals: map[string]string{}} }

func (it *Item) clone() *Item {
	c := NewItem()
	for _, k := range it.keys {
		c.Set(k, it.vals[k])
	}
	return c
}

func (it *Item) assign(o *Item) { it.keys, it.vals = o.keys, o.vals }

// Get returns a value.
func (it *Item) Get(k string) (string, bool) { v, ok := it.vals[k]; return v, ok }

// Val returns a value or "".
func (it *Item) Val(k string) string { return it.vals[k] }

// Set stores a value keeping insertion order.
func (it *Item) Set(k, v string) {
	if _, ok := it.vals[k]; !ok {
		it.keys = append(it.keys, k)
	}
	it.vals[k] = v
}

// Unset removes a key.
func (it *Item) Unset(k string) {
	if _, ok := it.vals[k]; !ok {
		return
	}
	delete(it.vals, k)
	for i, kk := range it.keys {
		if kk == k {
			it.keys = append(it.keys[:i], it.keys[i+1:]...)
			break
		}
	}
}

// Keys returns keys in insertion order.
func (it *Item) Keys() []string { return it.keys }

// Store is the whole configuration.
type Store struct {
	// Describe returns extra lines for "print" of a list menu, one per item (see module.Describer).
	Describe func(menu string) []string
	notices  []string

	aliases map[string]string
	menus   []*Menu
	byPath  map[string]*Menu
	single  map[string]*Item
	lists   map[string][]*Item
	touched map[string]bool
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{aliases: map[string]string{}, byPath: map[string]*Menu{}, single: map[string]*Item{},
		lists: map[string][]*Item{}, touched: map[string]bool{}}
}

// Notify records a message for the user (shown by the shell, or as a warning after loading).
func (s *Store) Notify(msg string) { s.notices = append(s.notices, msg) }

// TakeNotices returns and clears the recorded messages.
func (s *Store) TakeNotices() []string {
	n := s.notices
	s.notices = nil
	return n
}

// Register adds a menu; registration order defines command apply order.
func (s *Store) Register(m *Menu) {
	s.menus = append(s.menus, m)
	s.byPath[m.Path] = m
	for _, a := range m.Aliases {
		s.aliases[a] = m.Path
	}
	if m.Kind == Single || len(m.Settings) > 0 {
		s.single[m.Path] = NewItem()
	}
	for _, def := range m.Fixed {
		s.lists[m.Path] = append(s.lists[m.Path], fixedItem(m, def))
	}
}

// fixedItem builds a fixed item from its defaults, in the order of the fields.
func fixedItem(m *Menu, def map[string]string) *Item {
	it := NewItem()
	for _, f := range m.Fields {
		if v, ok := def[f.Name]; ok {
			it.Set(f.Name, v)
		}
	}
	return it
}

// fixedDefaults returns the defaults of a fixed item.
func (m *Menu) fixedDefaults(it *Item) map[string]string {
	for _, def := range m.Fixed {
		if def[m.NameKey] == it.Val(m.NameKey) {
			return def
		}
	}
	return nil
}

// Canon resolves menu aliases to the canonical path.
func (s *Store) Canon(path string) string {
	if c, ok := s.aliases[path]; ok {
		return c
	}
	return path
}

// MenuIndex returns registration index or -1.
func (s *Store) MenuIndex(path string) int {
	path = s.Canon(path)
	for i, m := range s.menus {
		if m.Path == path {
			return i
		}
	}
	return -1
}

// Menu returns a menu by path.
func (s *Store) Menu(path string) *Menu { return s.byPath[s.Canon(path)] }

// Single returns the property set of a singleton menu (never nil for known menus).
func (s *Store) Single(path string) *Item { return s.single[path] }

// Items returns the items of a list menu.
func (s *Store) Items(path string) []*Item { return s.lists[path] }

// SetItems replaces all items of a list menu (used by virtual fields).
func (s *Store) SetItems(path string, items []*Item) {
	s.lists[path] = items
	s.touched[path] = true
}

// Touched tells whether any mutating command hit the menu.
func (s *Store) Touched(path string) bool { return s.touched[path] }

// HasPath reports whether path is a menu or a prefix of one.
func (s *Store) HasPath(path string) bool {
	path = s.Canon(path)
	if path == "/" {
		return true
	}
	for _, m := range s.menus {
		if m.Path == path || strings.HasPrefix(m.Path, path+"/") {
			return true
		}
	}
	return false
}

// Children lists next path segments below path (for the shell).
func (s *Store) Children(path string) []string {
	prefix := strings.TrimRight(path, "/") + "/"
	seen := map[string]bool{}
	var out []string
	for _, m := range s.menus {
		if strings.HasPrefix(m.Path, prefix) {
			seg := strings.Split(strings.TrimPrefix(m.Path, prefix), "/")[0]
			if !seen[seg] {
				seen[seg] = true
				out = append(out, seg)
			}
		}
	}
	return out
}

func normalize(f *Field, v string) (string, error) {
	switch f.Type {
	case TBool:
		switch strings.ToLower(v) {
		case "yes", "true":
			return "yes", nil
		case "no", "false":
			return "no", nil
		}
		return "", fmt.Errorf("%s: %q is not a boolean (yes/no)", f.Name, v)
	case TInt:
		if _, err := strconv.Atoi(v); err != nil {
			return "", fmt.Errorf("%s: %q is not an integer", f.Name, v)
		}
	case TEnum:
		for _, e := range f.Enum {
			if e == v {
				return v, nil
			}
		}
		return "", fmt.Errorf("%s: %q is not one of: %s", f.Name, v, strings.Join(f.Enum, ", "))
	case TEnumList, TList:
		var out []string
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if f.Type == TEnumList {
				ok := false
				for _, e := range f.Enum {
					ok = ok || e == p
				}
				if !ok {
					return "", fmt.Errorf("%s: %q is not one of: %s", f.Name, p, strings.Join(f.Enum, ", "))
				}
			}
			out = append(out, p)
		}
		return strings.Join(out, ","), nil
	}
	return v, nil
}

func (s *Store) applyParams(m *Menu, fields []Field, it *Item, params []script.Param) error {
	if m.CheckParams != nil {
		if err := m.CheckParams(params); err != nil {
			return fmt.Errorf("%s: %w", m.Path, err)
		}
	}
	for _, p := range params {
		f := fieldIn(fields, p.Key)
		if f == nil {
			var names []string
			for _, ff := range fields {
				names = append(names, ff.Name)
			}
			return fmt.Errorf("%s: unknown parameter %q (valid: %s)", m.Path, p.Key, strings.Join(names, ", "))
		}
		if f.Virtual {
			if m.Virtual == nil {
				return fmt.Errorf("%s: parameter %q not supported", m.Path, p.Key)
			}
			if err := m.Virtual(s, f.Name, p.Value); err != nil {
				return err
			}
			continue
		}
		if p.Value == "" {
			if f.KeepEmpty {
				it.Set(p.Key, "")
			} else {
				it.Unset(p.Key)
			}
			continue
		}
		v, err := normalize(f, p.Value)
		if err != nil {
			return fmt.Errorf("%s: %w", m.Path, err)
		}
		if err := s.checkRef(f, v); err != nil {
			return fmt.Errorf("%s: %w", m.Path, err)
		}
		it.Set(p.Key, v)
	}
	return nil
}

// resolve returns the indexes of targeted items and remaining positional args.
func (s *Store) resolve(m *Menu, c script.Command) ([]int, []string, error) {
	items := s.lists[m.Path]
	var idx []int
	if c.HasFind {
		find := make([]script.Param, 0, len(c.Find))
		var desc []string
		for _, p := range c.Find {
			if p.Key == "" {
				if m.NameKey == "" {
					return nil, nil, fmt.Errorf("%s: [find %s] needs key=value here (this menu has no item name)", m.Path, p.Value)
				}
				p.Key = m.NameKey
			}
			if m.field(p.Key) == nil {
				return nil, nil, fmt.Errorf("%s: unknown parameter %q in [find ...]", m.Path, p.Key)
			}
			find = append(find, p)
			desc = append(desc, p.Key+"="+quoteVal(p.Value))
		}
		for i, it := range items {
			ok := true
			for _, p := range find {
				if v, has := it.Get(p.Key); !has || v != p.Value {
					ok = false
				}
			}
			if ok {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			// RouterOS silently does nothing; say it, a typo here otherwise goes unnoticed
			s.Notify(fmt.Sprintf("%s: %s [find %s] matched no item, nothing changed", m.Path, c.Verb, strings.Join(desc, " ")))
		}
		return idx, c.Args, nil
	}
	if len(c.Args) == 0 {
		return nil, nil, fmt.Errorf("%s: %s needs an item number or [find ...]", m.Path, c.Verb)
	}
	for _, a := range strings.Split(c.Args[0], ",") {
		if n, err := strconv.Atoi(a); err == nil {
			if n < 0 || n >= len(items) {
				return nil, nil, fmt.Errorf("%s: no such item: %d", m.Path, n)
			}
			idx = append(idx, n)
			continue
		}
		var hits []int
		for i, it := range items {
			if contains(m.names(it), a) {
				hits = append(hits, i)
			}
		}
		switch {
		case len(hits) == 0:
			return nil, nil, fmt.Errorf("%s: no such item: %s", m.Path, a)
		case len(hits) > 1:
			var where []string
			for _, i := range hits {
				w := strconv.Itoa(i)
				if m.NameScope != "" {
					w += fmt.Sprintf(" (%s=%s)", m.NameScope, items[i].Val(m.NameScope))
				}
				where = append(where, w)
			}
			return nil, nil, fmt.Errorf("%s: %s is not unique: %s; use the number or [find %s=%s %s=...]",
				m.Path, a, strings.Join(where, ", "), m.NameKey, quoteVal(a), m.NameScope)
		}
		idx = append(idx, hits[0])
	}
	return idx, c.Args[1:], nil
}

// settingsTarget tells whether fields are the menu-wide settings of a hybrid menu.
func settingsTarget(m *Menu, fields []Field) bool {
	return len(m.Settings) > 0 && len(fields) == len(m.Settings) && &fields[0] == &m.Settings[0]
}

// isSettingsCmd tells whether a set/unset on a hybrid menu addresses the
// menu-wide settings (no item selector) rather than an item.
func (s *Store) isSettingsCmd(m *Menu, c script.Command) bool {
	if m.Kind != Multi || len(m.Settings) == 0 || c.HasFind {
		return false
	}
	switch c.Verb {
	case "set":
		return len(c.Args) == 0
	case "unset":
		if len(c.Args) == 0 {
			return false
		}
		for _, a := range c.Args {
			if fieldIn(m.Settings, a) == nil {
				return false
			}
		}
		return true
	}
	return false
}

// Exec runs one command. print/export return text, mutating verbs return "".
func (s *Store) Exec(c script.Command) (string, error) {
	c.Path = s.Canon(c.Path)
	m := s.byPath[c.Path]
	if m == nil {
		// "/" and levels that only group menus (/system): export everything below them
		if c.Verb == "export" && s.HasPath(c.Path) && len(c.Params) == 0 && !c.HasFind && len(c.Args) == 0 {
			return s.Export(c.Path), nil
		}
		if s.HasPath(c.Path) {
			return "", fmt.Errorf("%s is not a menu, only 'export' works here (or go to one of: %s)",
				c.Path, strings.Join(s.Children(c.Path), ", "))
		}
		return "", fmt.Errorf("unknown menu %s", c.Path)
	}
	if m.Live != nil {
		return s.execLive(m, c)
	}
	if cmd, ok := m.Commands[c.Verb]; ok {
		idx, rest, err := s.resolve(m, c)
		if err != nil {
			return "", err
		}
		if len(rest) > 0 || len(c.Params) > 0 {
			return "", fmt.Errorf("%s: %s takes only items", m.Path, c.Verb)
		}
		items := make([]*Item, len(idx))
		for i, n := range idx {
			items[i] = s.lists[m.Path][n]
		}
		return cmd.Run(s, c, items)
	}
	if c.Verb == "scan" || c.Verb == "probe" || c.Verb == "rights" {
		return "", fmt.Errorf("%s: %s is not supported", m.Path, c.Verb)
	}
	if c.Verb == "cancel" {
		return "", fmt.Errorf("%s: cancel is not supported", m.Path)
	}
	switch c.Verb {
	case "print":
		detail := false
		for _, a := range c.Args {
			if a == "detail" {
				detail = true
			}
		}
		return s.print(m, detail), nil
	case "export":
		return s.Export(c.Path), nil
	case "add":
		if m.Kind != Multi || m.Fixed != nil {
			return "", fmt.Errorf("%s: 'add' is not available here, use 'set'", m.Path)
		}
		it := NewItem()
		if err := s.applyParams(m, m.Fields, it, c.Params); err != nil {
			return "", err
		}
		if err := s.checkItem(m, it, nil); err != nil {
			return "", err
		}
		s.lists[m.Path] = append(s.lists[m.Path], it)
	case "set", "unset", "enable", "disable":
		var targets []*Item
		var rest []string
		fields := m.Fields
		switch {
		case m.Kind == Single:
			targets, rest = []*Item{s.single[m.Path]}, c.Args
		case s.isSettingsCmd(m, c):
			targets, rest, fields = []*Item{s.single[m.Path]}, c.Args, m.Settings
		default:
			idx, r, err := s.resolve(m, c)
			if err != nil {
				return "", err
			}
			rest = r
			for _, i := range idx {
				targets = append(targets, s.lists[m.Path][i])
			}
		}
		for _, it := range targets {
			switch c.Verb {
			case "set":
				if len(rest) > 0 {
					return "", fmt.Errorf("%s: unexpected argument %q", m.Path, rest[0])
				}
				work := it.clone()
				if err := s.applyParams(m, fields, work, c.Params); err != nil {
					return "", err
				}
				if err := s.checkChange(m, fields, it, work); err != nil {
					return "", err
				}
				it.assign(work)
			case "unset":
				work := it.clone()
				for _, k := range rest {
					if fieldIn(fields, k) == nil {
						return "", fmt.Errorf("%s: unknown parameter %q", m.Path, k)
					}
					work.Unset(k)
					if def := m.fixedDefaults(it); def != nil {
						if v, ok := def[k]; ok {
							work.Set(k, v) // a fixed item goes back to its default
						}
					}
				}
				if err := s.checkChange(m, fields, it, work); err != nil {
					return "", err
				}
				it.assign(work)
			default:
				switch {
				case m.field("disabled") != nil:
					it.Set("disabled", map[string]string{"enable": "no", "disable": "yes"}[c.Verb])
				case m.field("enabled") != nil:
					it.Set("enabled", map[string]string{"enable": "yes", "disable": "no"}[c.Verb])
				default:
					return "", fmt.Errorf("%s: %s is not supported", m.Path, c.Verb)
				}
			}
		}
	case "remove":
		if m.Kind != Multi || m.Fixed != nil {
			return "", fmt.Errorf("%s: cannot remove", m.Path)
		}
		idx, _, err := s.resolve(m, c)
		if err != nil {
			return "", err
		}
		drop := map[int]bool{}
		for _, i := range idx {
			if err := s.checkInUse(m, s.lists[m.Path][i], nil); err != nil {
				return "", err
			}
			drop[i] = true
		}
		var keep []*Item
		for i, it := range s.lists[m.Path] {
			if !drop[i] {
				keep = append(keep, it)
			}
		}
		s.lists[m.Path] = keep
	default:
		return "", fmt.Errorf("unsupported command %q", c.Verb)
	}
	s.touched[m.Path] = true
	return "", nil
}

func quoteVal(v string) string {
	// "?" asks for help in the shell unless quoted, so an exported line must quote it
	if v != "" && !strings.ContainsAny(v, " \t\"\\$[]();#?") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`)
	return `"` + r.Replace(v) + `"`
}

func (s *Store) secretKey(m *Menu, k string) bool {
	f := m.field(k)
	return f != nil && f.Secret
}

func line(it *Item) string {
	var parts []string
	for _, k := range it.keys {
		parts = append(parts, k+"="+quoteVal(it.vals[k]))
	}
	return strings.Join(parts, " ")
}

func (s *Store) print(m *Menu, detail bool) string {
	var b strings.Builder
	if m.Kind == Single {
		it := s.single[m.Path]
		for _, k := range it.keys {
			v := it.vals[k]
			if s.secretKey(m, k) {
				v = "*****"
			}
			fmt.Fprintf(&b, "%22s: %s\n", k, v)
		}
		if detail && m.Detail != nil {
			for _, l := range m.Detail(s, it) {
				fmt.Fprintf(&b, "  %s\n", l)
			}
		}
		return b.String()
	}
	if len(m.Settings) > 0 {
		for _, k := range s.single[m.Path].keys {
			fmt.Fprintf(&b, "%22s: %s\n", k, s.single[m.Path].vals[k])
		}
	}
	var info []string
	if s.Describe != nil {
		info = s.Describe(m.Path)
	}
	for i, it := range s.lists[m.Path] {
		masked := NewItem()
		for _, k := range it.keys {
			v := it.vals[k]
			if s.secretKey(m, k) && v != "" { // an empty one is shown as such
				v = "*****"
			}
			masked.Set(k, v)
		}
		fmt.Fprintf(&b, "%3d  %s\n", i, line(masked))
		if i < len(info) && info[i] != "" {
			fmt.Fprintf(&b, "       %s\n", info[i])
		}
		if detail && m.Detail != nil {
			for _, l := range m.Detail(s, it) {
				fmt.Fprintf(&b, "       %s\n", l)
			}
		}
	}
	return b.String()
}

// Export renders the configuration below prefix as a re-importable script.
func (s *Store) Export(prefix string) string {
	prefix = strings.TrimRight(prefix, "/")
	var b strings.Builder
	// a blank line between menus, for readability (the script parser ignores it)
	section := func() {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
	}
	for _, m := range s.menus {
		if prefix != "" && m.Path != prefix && !strings.HasPrefix(m.Path, prefix+"/") {
			continue
		}
		if m.Fixed != nil {
			var lines []string
			for _, it := range s.lists[m.Path] {
				def := m.fixedDefaults(it)
				var parts []string
				for _, f := range m.Fields { // in the order of the fields, like the defaults
					if v, ok := it.vals[f.Name]; ok && f.Name != m.NameKey && v != def[f.Name] {
						parts = append(parts, f.Name+"="+quoteVal(v))
					}
				}
				if len(parts) > 0 {
					lines = append(lines, fmt.Sprintf("set [ find %s=%s ] %s", m.NameKey, quoteVal(it.Val(m.NameKey)), strings.Join(parts, " ")))
				}
			}
			if len(lines) > 0 {
				section()
				fmt.Fprintf(&b, "%s\n%s\n", m.Path, strings.Join(lines, "\n"))
			}
			continue
		}
		it := s.single[m.Path]
		hasSingle := it != nil && len(it.keys) > 0
		if m.Kind == Single {
			if hasSingle {
				section()
				fmt.Fprintf(&b, "%s\nset %s\n", m.Path, line(it))
			}
			continue
		}
		if !hasSingle && len(s.lists[m.Path]) == 0 {
			continue
		}
		section()
		fmt.Fprintf(&b, "%s\n", m.Path)
		if hasSingle {
			fmt.Fprintf(&b, "set %s\n", line(it))
		}
		for _, it := range s.lists[m.Path] {
			fmt.Fprintf(&b, "add %s\n", line(it))
		}
	}
	return b.String()
}

// HelpText describes the fields of a menu (for the shell).
func (m *Menu) HelpText() string {
	var b strings.Builder
	if len(m.Settings) > 0 {
		b.WriteString("settings (set k=v):\n")
		for _, f := range m.Settings {
			fmt.Fprintf(&b, "  %-22s %s\n", f.Name, f.TypeName())
		}
		b.WriteString("items (add / set <n> k=v):\n")
	}
	for _, f := range m.Fields {
		fmt.Fprintf(&b, "  %-22s %s\n", f.Name, f.TypeName())
	}
	return b.String()
}

// SecretKeys returns the names of all secret fields (never stored in the shell history).
func (s *Store) SecretKeys() map[string]bool {
	out := map[string]bool{}
	for _, m := range s.menus {
		for _, f := range append(append([]Field{}, m.Fields...), m.Settings...) {
			if f.Secret {
				out[f.Name] = true
			}
		}
	}
	return out
}

// checkChange validates an item (or the settings of a hybrid menu) after set/unset: old is the
// stored version, work the changed copy.
func (s *Store) checkChange(m *Menu, fields []Field, old, work *Item) error {
	if m.Kind == Single {
		if m.Validate != nil {
			if err := m.Validate(s, work); err != nil {
				return fmt.Errorf("%s: %w", m.Path, err)
			}
		}
		return nil
	}
	if settingsTarget(m, fields) {
		return nil
	}
	if m.Fixed != nil && work.Val(m.NameKey) != old.Val(m.NameKey) {
		return fmt.Errorf("%s: %s cannot be changed", m.Path, m.NameKey)
	}
	if err := s.checkItem(m, work, old); err != nil {
		return err
	}
	return s.checkInUse(m, old, work)
}

// checkItem runs the checks of a list item: required fields, Menu.Validate, unique values. self is the stored item being changed, nil for a new one.
func (s *Store) checkItem(m *Menu, it, self *Item) error {
	var missing []string
	for _, f := range m.Fields {
		if f.Required && it.Val(f.Name) == "" {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: missing required %s", m.Path, plural("parameter", missing))
	}
	if m.Validate != nil {
		if err := m.Validate(s, it); err != nil {
			return fmt.Errorf("%s: %w", m.Path, err)
		}
	}
	// names and aliases: unique together, within the scope
	for _, k := range []string{m.NameKey, m.AliasKey} {
		v := it.Val(k)
		if k == "" || v == "" {
			continue
		}
		where := ""
		if m.NameScope != "" {
			where = fmt.Sprintf(" on %s=%s", m.NameScope, quoteVal(it.Val(m.NameScope)))
		}
		for _, o := range s.lists[m.Path] {
			if o == self || (m.NameScope != "" && o.Val(m.NameScope) != it.Val(m.NameScope)) {
				continue
			}
			if o.Val(k) == v {
				return fmt.Errorf("%s: %s=%s already exists%s", m.Path, k, quoteVal(v), where)
			}
			if contains(m.names(o), v) {
				other := m.NameKey
				if k == m.NameKey {
					other = m.AliasKey
				}
				return fmt.Errorf("%s: %s=%s is already the %s of %s%s", m.Path, k, quoteVal(v), other, o.Val(m.NameKey), where)
			}
		}
	}
	for _, f := range m.Fields {
		v := it.Val(f.Name)
		if v == "" || !f.Unique || f.Name == m.NameKey || f.Name == m.AliasKey {
			continue
		}
		for n, o := range s.lists[m.Path] {
			if o == self || o.Val(f.Name) != v {
				continue
			}
			id := strconv.Itoa(n)
			if m.NameKey != "" && o.Val(m.NameKey) != "" {
				id = o.Val(m.NameKey)
			}
			return fmt.Errorf("%s: %s=%s is already used by %s", m.Path, f.Name, quoteVal(v), id)
		}
	}
	return nil
}

func plural(word string, names []string) string {
	if len(names) == 1 {
		return word + " " + names[0]
	}
	return word + "s " + strings.Join(names, ", ")
}

// menuRefs returns the "<menu path>:<key>" sources of a field (sys: sources are skipped).
// A source may start with a value prefix: "@/user/group:group" refers with "@firma1"
// to the group firma1.
func menuRefs(f *Field) []ref {
	var out []ref
	for _, src := range strings.Split(f.Ref, ",") {
		if src == "" || strings.HasPrefix(src, "sys:") {
			continue
		}
		if r, ok := parseRef(src); ok {
			out = append(out, r)
		}
	}
	return out
}

// ref is one menu source of a Field.Ref: values prefix+<key of an item of path>.
type ref struct{ prefix, path, key string }

// parseRef reads "[prefix]/menu/path:key".
func parseRef(src string) (ref, bool) {
	i := strings.IndexByte(src, '/')
	if i < 0 {
		return ref{}, false
	}
	path, key, ok := strings.Cut(src[i:], ":")
	return ref{src[:i], path, key}, ok
}

// ParseRef exposes parseRef (completion): prefix, menu path, key.
func ParseRef(src string) (prefix, path, key string, ok bool) {
	r, ok := parseRef(src)
	return r.prefix, r.path, r.key, ok
}

func values(f *Field, v string) []string {
	if f.Type == TList || f.Type == TEnumList {
		return strings.Split(v, ",")
	}
	return []string{v}
}

// checkRef makes sure a value of a Strict field exists in one of the referenced menus.
func (s *Store) checkRef(f *Field, v string) error {
	refs := menuRefs(f)
	if !f.Strict || len(refs) == 0 {
		return nil
	}
	for _, x := range values(f, v) {
		found := false
		var where []string
		prefixed := false // a source with a prefix matches: it is the one meant
		for _, r := range refs {
			prefixed = prefixed || (r.prefix != "" && strings.HasPrefix(x, r.prefix))
		}
		for _, r := range refs {
			name, ok := strings.CutPrefix(x, r.prefix)
			if !ok || (prefixed && r.prefix == "") {
				continue
			}
			where = append(where, s.Canon(r.path))
			for _, it := range s.lists[s.Canon(r.path)] {
				found = found || it.Val(r.key) == name
			}
		}
		if !found {
			if len(where) == 0 {
				for _, r := range refs {
					where = append(where, r.prefix+s.Canon(r.path))
				}
			}
			return fmt.Errorf("%s: %q does not exist in %s", f.Name, x, strings.Join(where, ", "))
		}
	}
	return nil
}

// usedBy tells which item refers (through a Strict field) to value val of key in menu path.
func (s *Store) usedBy(path, key, val string) (string, bool) {
	for _, m := range s.menus {
		for _, set := range [][]Field{m.Fields, m.Settings} {
			for i := range set {
				f := &set[i]
				if !f.Strict {
					continue
				}
				var prefixes []string
				for _, r := range menuRefs(f) {
					if s.Canon(r.path) == path && r.key == key {
						prefixes = append(prefixes, r.prefix)
					}
				}
				if len(prefixes) == 0 {
					continue
				}
				uses := func(it *Item) bool {
					for _, x := range values(f, it.Val(f.Name)) {
						for _, p := range prefixes {
							if x == p+val {
								return true
							}
						}
					}
					return false
				}
				if len(m.Settings) > 0 && &set[0] == &m.Settings[0] {
					if it := s.single[m.Path]; it != nil && uses(it) {
						return m.Path, true
					}
					continue
				}
				if m.Kind == Single {
					if it := s.single[m.Path]; it != nil && uses(it) {
						return m.Path, true
					}
					continue
				}
				for n, it := range s.lists[m.Path] {
					if uses(it) {
						id := strconv.Itoa(n)
						if m.NameKey != "" && it.Val(m.NameKey) != "" {
							id = it.Val(m.NameKey)
							if m.NameScope != "" {
								id += " (" + m.NameScope + "=" + it.Val(m.NameScope) + ")"
							}
						}
						return m.Path + " " + id, true
					}
				}
			}
		}
	}
	return "", false
}

// checkInUse refuses to remove (now == nil) or change referenced values of an item.
func (s *Store) checkInUse(m *Menu, old, now *Item) error {
	for _, k := range old.keys {
		v := old.vals[k]
		if v == "" || (now != nil && now.Val(k) == v) {
			continue
		}
		if user, ok := s.usedBy(m.Path, k, v); ok {
			return fmt.Errorf("%s: %s=%s is used by %s", m.Path, k, quoteVal(v), user)
		}
	}
	return nil
}

func (s *Store) execLive(m *Menu, c script.Command) (string, error) {
	ok := false
	for _, v := range m.Live.Verbs() {
		ok = ok || v == c.Verb
	}
	if !ok {
		return "", fmt.Errorf("%s: %s is not supported here (use: %s)", m.Path, c.Verb, strings.Join(m.Live.Verbs(), ", "))
	}
	if c.HasFind {
		return "", fmt.Errorf("%s: [find] is not supported here, give the numbers", m.Path)
	}
	it := NewItem()
	if err := s.applyParams(m, m.Fields, it, c.Params); err != nil {
		return "", err
	}
	if c.Verb == "add" {
		if err := s.checkItem(m, it, nil); err != nil {
			return "", err
		}
	}
	return m.Live.Run(s, c, it)
}

// ReplaceItems makes the items of a list menu those of another store (copies).
func (s *Store) ReplaceItems(path string, from *Store) {
	var items []*Item
	for _, it := range from.lists[path] {
		c := NewItem()
		for _, k := range it.keys {
			c.Set(k, it.vals[k])
		}
		items = append(items, c)
	}
	s.lists[path] = items
}
