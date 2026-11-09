// Package profile keeps what users arrange for themselves in the panel, beside the
// state ffnvr keeps of the recorders (/var/lib/ffnvr), not in the configuration: one
// directory per user, one JSON file per kind of thing.
//
//	/var/lib/ffnvr/profile/<user>/dashboards.json   saved views of the live view
//	/var/lib/ffnvr/profile/<user>/sessions.json     sign ins of the panel (see Sessions)
//	/var/lib/ffnvr/profile/<user>/settings.json     own preferences: the start page, HQ on zoom, the theme (see Settings)
//
// Every file is an object with its own "version", so that a kind can change its shape
// later and read the files of the older one. A file is written whole and atomically;
// the panel is its only writer, the writes of one process are serialized here.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// Dir of the profiles, below the root.
const Dir = "/var/lib/ffnvr/profile"

// Store reads and writes the profiles below a root ("" or "/": the system).
type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Store { return &Store{root: root} }

// the names of users and of files: the characters of /user (no dots first: no ".." nor
// hidden files)
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]*$`)

func (s *Store) path(user, name string) (string, error) {
	if !nameRe.MatchString(user) || len(user) > 64 {
		return "", fmt.Errorf("profile: bad user name %q", user)
	}
	if !nameRe.MatchString(name) || len(name) > 64 {
		return "", fmt.Errorf("profile: bad file name %q", name)
	}
	return filepath.Join(s.root, Dir, user, name+".json"), nil
}

// Read decodes the file name of a user into v; a file that is not there leaves v as
// it is (found false).
func (s *Store) Read(user, name string, v any) (found bool, err error) {
	p, err := s.path(user, name)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return true, fmt.Errorf("profile %s/%s: %w", user, name, err)
	}
	return true, nil
}

// Update reads the file name of a user into v (left as it is when there is none),
// lets fn change it and writes it back; fn's error leaves the file alone. Updates are
// serialized: a read and its write see no other write in between.
func (s *Store) Update(user, name string, v any, fn func() error) error {
	p, err := s.path(user, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Read(user, name, v); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Remove deletes the whole profile of a user (the user is gone).
func (s *Store) Remove(user string) error {
	if !nameRe.MatchString(user) {
		return fmt.Errorf("profile: bad user name %q", user)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(filepath.Join(s.root, Dir, user))
}

// Users that have a profile.
func (s *Store) Users() []string {
	ents, _ := os.ReadDir(filepath.Join(s.root, Dir))
	var out []string
	for _, e := range ents {
		if e.IsDir() && nameRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// Prune removes the profiles of users that are no longer in keep (the users of the
// configuration): the worker calls it now and then.
func (s *Store) Prune(keep []string, logf func(string, ...any)) {
	k := map[string]bool{}
	for _, u := range keep {
		k[u] = true
	}
	for _, u := range s.Users() {
		if !k[u] {
			if err := s.Remove(u); err != nil {
				logf("profile %s: %v", u, err)
			} else {
				logf("profile %s removed: no such user", u)
			}
		}
	}
}
