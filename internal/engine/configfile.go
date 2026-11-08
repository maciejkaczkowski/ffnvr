package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"ffnvr/internal/config"
)

// The configuration file is written by the shell (save, commit) and by the web panel
// (camera positions). Both take a lock beside it and write it whole and
// atomically. The menus the panel edits are taken from the file when the shell saves,
// unless the shell session changed them itself, so a save in the shell does not undo
// what the panel saved meanwhile.

// WebMenus are the menus the web panel writes.
var WebMenus = []string{"/camera/position"}

// lockFile holds an exclusive lock on <file>.lock until the returned func is called.
func lockFile(file string) (func(), error) {
	f, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s is locked", file)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// writeAtomic replaces a file with content (0600: it holds password hashes).
func writeAtomic(file, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// ModTime of a file (zero when it cannot be read).
func ModTime(file string) time.Time {
	st, err := os.Stat(file)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// EditConfig changes the configuration file: it is read (under the lock), fn changes
// the store, and the result is written back. Nothing is applied to services: the
// changes are of menus that need no commit.
func EditConfig(file string, opts Options, fn func(*config.Store) error) error {
	unlock, err := lockFile(file)
	if err != nil {
		return err
	}
	defer unlock()
	opts.NoServices = true
	e, err := New(opts)
	if err != nil {
		return err
	}
	if err := e.LoadFiles([]string{file}); err != nil {
		return err
	}
	if err := fn(e.Store); err != nil {
		return err
	}
	return writeAtomic(file, e.Store.Export("/"))
}

// SaveConfig writes the store to file for the shell. When the file changed since
// loaded (another writer: the panel), the web menus the session did not touch are
// taken from the file.
func SaveConfig(file string, s *config.Store, opts Options, loaded time.Time, touched map[string]bool) error {
	unlock, err := lockFile(file)
	if err != nil {
		return err
	}
	defer unlock()
	if mt := ModTime(file); !mt.IsZero() && !loaded.IsZero() && !mt.Equal(loaded) {
		opts.NoServices = true
		if cur, err := New(opts); err == nil && cur.LoadFiles([]string{file}) == nil {
			for _, path := range WebMenus {
				if !touched[path] {
					s.ReplaceItems(path, cur.Store)
				}
			}
		}
	}
	return writeAtomic(file, s.Export("/"))
}
