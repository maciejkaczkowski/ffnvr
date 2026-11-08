package profile

import (
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	s := New(t.TempDir())
	now := time.Now()
	id := SessionID("cookie")
	if id == "cookie" || len(id) != 64 {
		t.Fatal(id)
	}
	s.SaveSession("jan", Session{ID: id, CSRF: "x", Created: now, Active: now})
	s.SaveSession("jan", Session{ID: "old", Created: now.Add(-48 * time.Hour), Active: now.Add(-25 * time.Hour)})
	s.SaveSession("ola", Session{ID: "o", Created: now, Active: now})
	// the same id again: its activity, not a second one
	s.SaveSession("jan", Session{ID: id, CSRF: "x", Created: now, Active: now.Add(time.Minute)})
	all := s.AllSessions(t.Logf)
	if len(all["jan"]) != 2 || len(all["ola"]) != 1 || !all["jan"][0].Active.Equal(now.Add(time.Minute)) {
		t.Fatalf("%+v", all)
	}
	// the worker: unused for a day
	s.PruneSessions(now, SessionsKeep, t.Logf)
	all = s.AllSessions(t.Logf)
	if len(all["jan"]) != 1 || all["jan"][0].ID != id || len(all["ola"]) != 1 {
		t.Errorf("%+v", all)
	}
	if err := s.DeleteSession("jan", id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession("jan", "none"); err != nil {
		t.Error(err)
	}
	if all = s.AllSessions(t.Logf); len(all["jan"]) != 0 {
		t.Errorf("%+v", all)
	}
	// a user without a profile gets none from the worker
	s.PruneSessions(now, SessionsKeep, t.Logf)
	if u := s.Users(); len(u) != 2 {
		t.Errorf("%v", u)
	}
}
