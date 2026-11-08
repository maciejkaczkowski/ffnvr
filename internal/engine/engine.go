// Package engine ties everything together: it registers the menus of all modules
// in the config store, loads scripts into it and brings the system in line with it.
package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"ffnvr/internal/config"
	"ffnvr/internal/module"
	"ffnvr/internal/modules"
	"ffnvr/internal/script"
	"ffnvr/internal/units"
)

// Options of the engine.
type Options struct {
	Root    string          // filesystem root, "/" for the real system
	Modules []module.Module // nil: modules.All()
	// NoServices: write files but never run systemctl. Commands are also skipped when
	// Root is not "/".
	NoServices bool
	Run        units.Runner // runs systemctl; nil: os/exec (tests replace it)
	// Exe returns the path of the running binary (nil: os.Executable); see ensureExe
	Exe func() (string, error)
}

// Engine holds the loaded configuration.
type Engine struct {
	Opts  Options
	Mods  []module.Module
	Store *config.Store
	Ctx   *module.Context
}

// New creates an engine with all modules registered.
func New(o Options) (*Engine, error) {
	if o.Root == "" {
		o.Root = "/"
	}
	mods := o.Modules
	if mods == nil {
		mods = modules.All()
	}
	e := &Engine{Opts: o, Mods: mods, Store: config.NewStore()}
	for _, m := range e.Mods {
		for _, menu := range m.Menus() {
			if e.Store.Menu(menu.Path) != nil {
				return nil, fmt.Errorf("module %s: menu %s registered twice", m.Name(), menu.Path)
			}
			e.Store.Register(menu)
		}
	}
	e.Ctx = &module.Context{Store: e.Store, Root: o.Root, Live: filepath.Clean(o.Root) == "/"}
	for _, m := range e.Mods {
		if b, ok := m.(module.Binder); ok {
			b.Bind(e.Ctx)
		}
	}
	e.Store.Describe = func(menu string) []string {
		for _, m := range e.Mods {
			if d, ok := m.(module.Describer); ok {
				if info := d.Describe(e.Ctx, menu); info != nil {
					return info
				}
			}
		}
		return nil
	}
	return e, nil
}

func collectFiles(paths []string) ([]string, error) {
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, en := range ents {
			if !en.IsDir() && strings.HasSuffix(en.Name(), ".rsc") {
				names = append(names, filepath.Join(p, en.Name()))
			}
		}
		sort.Strings(names)
		files = append(files, names...)
	}
	return files, nil
}

// LoadFiles parses files/directories (*.rsc) and applies all commands.
// Commands are applied menu by menu in module registration order (relative
// order inside one menu is kept), so file names do not matter. Messages for the
// user stay in the store (Store.TakeNotices).
func (e *Engine) LoadFiles(paths []string) error {
	files, err := collectFiles(paths)
	if err != nil {
		return err
	}
	var cmds []script.Command
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		cs, err := script.Parse(fh, f)
		fh.Close()
		if err != nil {
			return err
		}
		cmds = append(cmds, cs...)
	}
	for _, c := range cmds {
		if e.Store.MenuIndex(c.Path) < 0 {
			return fmt.Errorf("%s:%d: unknown menu %s", c.File, c.Line, c.Path)
		}
		if m := e.Store.Menu(c.Path); m.Live != nil {
			return fmt.Errorf("%s:%d: %s is not configuration, it cannot be used in a configuration file", c.File, c.Line, m.Path)
		} else if _, ok := m.Commands[c.Verb]; ok || c.Verb == "scan" || c.Verb == "probe" {
			return fmt.Errorf("%s:%d: %s %s is not configuration, it cannot be used in a configuration file", c.File, c.Line, m.Path, c.Verb)
		}
	}
	sort.SliceStable(cmds, func(i, j int) bool {
		return e.Store.MenuIndex(cmds[i].Path) < e.Store.MenuIndex(cmds[j].Path)
	})
	for _, c := range cmds {
		if c.Verb == "print" || c.Verb == "export" {
			continue
		}
		if _, err := e.Store.Exec(c); err != nil {
			return fmt.Errorf("%s:%d: %w", c.File, c.Line, err)
		}
	}
	return nil
}

// AfterCommand gives modules a chance to follow up an interactive command that
// succeeded (e.g. reading the cameras of a recorder that was just added); it returns
// what they want to tell the user. Scripts loaded from files do not trigger it.
func (e *Engine) AfterCommand(c script.Command) string {
	var b strings.Builder
	for _, m := range e.Mods {
		if h, ok := m.(module.CommandHook); ok {
			b.WriteString(h.AfterCommand(e.Ctx, c))
		}
	}
	return b.String()
}

// Plan returns the changes of the systemd units that the configuration wants.
func (e *Engine) Plan() (*units.Plan, error) {
	e.Ctx.Binary = e.binaryID()
	var want []units.Unit
	for _, m := range e.Mods {
		b, ok := m.(module.UnitBuilder)
		if !ok {
			continue
		}
		us, err := b.Units(e.Ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Name(), err)
		}
		want = append(want, us...)
	}
	return units.NewPlan(e.Opts.Root, want)
}

// Apply carries out a plan, writing a log to w.
func (e *Engine) Apply(p *units.Plan, w io.Writer) error {
	run := e.Opts.Run
	if run == nil {
		run = func(args ...string) error {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Stdout, cmd.Stderr = w, w
			return cmd.Run()
		}
	}
	if e.Opts.NoServices || (e.Opts.Run == nil && !e.Ctx.Live) {
		run = nil
	}
	if len(p.Changes) > 0 {
		e.ensureExe(w)
	}
	return p.Apply(w, run)
}

// ensureExe makes sure the services find ffnvr where their units run it from
// (/usr/local/bin/ffnvr): when nothing is there, a symbolic link to the running binary
// is made. A link rather than a path of the moment in the units: the units stay the same
// wherever the binary is, and a new version is installed by replacing one file.
func (e *Engine) ensureExe(w io.Writer) {
	link := filepath.Join(e.Opts.Root, modules.WorkerExe)
	if _, err := os.Stat(link); err == nil {
		return // a binary, or a link that works
	}
	exe, err := e.exe()
	if err != nil {
		fmt.Fprintf(w, "warning: %s is missing and the running binary is unknown: %v\n", modules.WorkerExe, err)
		return
	}
	if exe == link {
		return
	}
	os.Remove(link) // a link to a binary that is gone
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err == nil {
		err = os.Symlink(exe, link)
	}
	if err != nil {
		fmt.Fprintf(w, "warning: cannot link %s to %s: %v\n", modules.WorkerExe, exe, err)
		return
	}
	fmt.Fprintf(w, "linked     %s -> %s\n", modules.WorkerExe, exe)
	for _, dir := range []string{"/home/", "/root/", "/tmp/", "/run/user/"} {
		if strings.HasPrefix(exe, dir) {
			fmt.Fprintf(w, "warning: %s is under %s, which ffnvr-www.service cannot see (ProtectHome, PrivateTmp);\n"+
				"         install the binary in /usr/local/bin (make install)\n", exe, strings.TrimSuffix(dir, "/"))
		}
	}
}

// binaryID hashes the binary the services will run: /usr/local/bin/ffnvr, or the
// running one when that is missing (ensureExe links it there).
func (e *Engine) binaryID() string {
	path := filepath.Join(e.Opts.Root, modules.WorkerExe)
	if _, err := os.Stat(path); err != nil {
		if path, err = e.exe(); err != nil {
			return ""
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

func (e *Engine) exe() (string, error) {
	if e.Opts.Exe != nil {
		return e.Opts.Exe()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
