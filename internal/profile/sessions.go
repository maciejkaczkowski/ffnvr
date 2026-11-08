package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Sessions: the user's sign ins of the panel (sessions.json), kept so that a restart
// of the panel signs nobody out.
//
//	{"version": 1, "sessions": [{"id": "9f2c…", "csrf": "…",
//	  "created": "2026-11-05T08:00:00+01:00", "active": "2026-11-05T09:12:00+01:00"}]}
//
// The id is the SHA-256 of the session's cookie, never the cookie itself: whoever reads
// the file cannot sign in with it. The panel writes a session when it is made, again
// now and then while it is used (active), and removes it on sign out; the worker
// removes the ones unused for long (PruneSessions). The two are separate processes,
// so a write of one may now and then undo one of the other; the panel writes a session
// in use again within a minute (SaveSession adds it back), and a session the worker
// misses is removed on its next pass.
type Sessions struct {
	Version  int       `json:"version"`
	Sessions []Session `json:"sessions"`
}

type Session struct {
	ID      string    `json:"id"` // SessionID of the cookie
	CSRF    string    `json:"csrf"`
	Created time.Time `json:"created"`
	Active  time.Time `json:"active"` // last use (written at most once a minute or so)
}

// SessionsFile is the name of the file in a profile.
const SessionsFile = "sessions"

// SessionsVersion is the shape written now.
const SessionsVersion = 1

// SessionsKeep: the worker removes the sessions unused this long (by default).
const SessionsKeep = 24 * time.Hour

// SessionID is what the profile keeps of a session's cookie.
func SessionID(cookie string) string {
	h := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(h[:])
}

// errUnchanged: an Update with nothing to write.
var errUnchanged = errors.New("unchanged")

// SaveSession writes a session of a user: a new one, or the same id again (its
// activity).
func (s *Store) SaveSession(user string, se Session) error {
	var ss Sessions
	return s.Update(user, SessionsFile, &ss, func() error {
		ss.Version = SessionsVersion
		for i := range ss.Sessions {
			if ss.Sessions[i].ID == se.ID {
				ss.Sessions[i] = se
				return nil
			}
		}
		ss.Sessions = append(ss.Sessions, se)
		return nil
	})
}

// DeleteSession removes a session of a user (signed out); none is no error.
func (s *Store) DeleteSession(user, id string) error {
	var ss Sessions
	err := s.Update(user, SessionsFile, &ss, func() error {
		for i := range ss.Sessions {
			if ss.Sessions[i].ID == id {
				ss.Sessions = append(ss.Sessions[:i], ss.Sessions[i+1:]...)
				return nil
			}
		}
		return errUnchanged
	})
	if errors.Is(err, errUnchanged) {
		return nil
	}
	return err
}

// AllSessions returns the sessions of every profile, by user (the panel reads them
// when it starts).
func (s *Store) AllSessions(logf func(string, ...any)) map[string][]Session {
	out := map[string][]Session{}
	for _, u := range s.Users() {
		var ss Sessions
		if _, err := s.Read(u, SessionsFile, &ss); err != nil {
			logf("profile %s: %v", u, err)
			continue
		}
		if len(ss.Sessions) > 0 {
			out[u] = ss.Sessions
		}
	}
	return out
}

// PruneSessions removes the sessions unused for longer than keep (their last activity
// before now-keep): the worker calls it now and then.
func (s *Store) PruneSessions(now time.Time, keep time.Duration, logf func(string, ...any)) {
	for _, u := range s.Users() {
		var ss Sessions
		n := 0
		err := s.Update(u, SessionsFile, &ss, func() error {
			kept := ss.Sessions[:0]
			for _, se := range ss.Sessions {
				if now.Sub(se.Active) > keep {
					n++
					continue
				}
				kept = append(kept, se)
			}
			if n == 0 {
				return errUnchanged
			}
			ss.Sessions = kept
			return nil
		})
		switch {
		case errors.Is(err, errUnchanged):
		case err != nil:
			logf("profile %s: sessions: %v", u, err)
		default:
			logf("profile %s: %d session(s) unused for %s removed", u, n, keep)
		}
	}
}
