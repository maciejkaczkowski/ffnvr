package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"ffnvr/internal/modules"
)

// Backups of several recorders into a storage of types=backup, run in the background;
// the page follows them.

type backupItem struct {
	Nvr, State, Kind, Path, Size, Err string
}

type backupRun struct {
	ID, User, Storage, Dir string
	Started                time.Time
	Items                  []*backupItem
	Done                   bool
}

type backupRuns struct {
	mu   sync.Mutex
	runs map[string]*backupRun
}

func (b *backupRuns) get(id string) (backupRun, []backupItem, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[id]
	if !ok {
		return backupRun{}, nil, false
	}
	items := make([]backupItem, len(r.Items))
	for i, it := range r.Items {
		items[i] = *it
	}
	return *r, items, true
}

// POST /tools/backup-to: nvr (several), storage, dir.
func (s *Server) backupTo(w http.ResponseWriter, r *http.Request) {
	se := s.current(r)
	if se == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.PostFormValue("csrf") != se.csrf {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		http.Error(w, "configuration error", http.StatusInternalServerError)
		return
	}
	if !can(store, se, modules.PermSystemBackup) {
		s.denied(w, se, modules.PermSystemBackup)
		return
	}
	t := texts[lang(r)]
	storage, dir := r.PostFormValue("storage"), strings.Trim(r.PostFormValue("dir"), "/ ")
	fail := func(msg string) { http.Redirect(w, r, "/?p=backup&err="+url.QueryEscape(msg), http.StatusSeeOther) }
	if !slices.Contains(modules.StoragesOf(store, "backup"), storage) {
		fail(t["bk_no_storage"])
		return
	}
	run := &backupRun{ID: token()[:12], User: se.user, Storage: storage, Dir: dir, Started: time.Now()}
	for _, n := range r.PostForm["nvr"] {
		if modules.CanAccess(store, se.user, n) {
			run.Items = append(run.Items, &backupItem{Nvr: n, State: t["bk_waiting"], Kind: kMute})
		}
	}
	if len(run.Items) == 0 {
		fail(t["bk_none"])
		return
	}
	s.backups.mu.Lock()
	if s.backups.runs == nil {
		s.backups.runs = map[string]*backupRun{}
	}
	for id, old := range s.backups.runs { // finished runs are kept for a day
		if old.Done && time.Since(old.Started) > 24*time.Hour {
			delete(s.backups.runs, id)
		}
	}
	s.backups.runs[run.ID] = run
	s.backups.mu.Unlock()
	s.opt.Log.Printf("web: %s backs up %d recorders into %s", se.user, len(run.Items), storage)
	go func() {
		for _, it := range run.Items {
			s.backups.mu.Lock()
			it.State, it.Kind = t["bk_running"], kWarn
			s.backups.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			path, size, err := modules.BackupTo(ctx, store, it.Nvr, storage, dir)
			cancel()
			s.backups.mu.Lock()
			if err != nil {
				it.State, it.Kind, it.Err = t["error"], kErr, err.Error()
				s.opt.Log.Printf("web: backup of %s: %v", it.Nvr, err)
			} else {
				it.State, it.Kind, it.Path, it.Size = "OK", kOK, path, human(uint64(size))
			}
			s.backups.mu.Unlock()
		}
		s.backups.mu.Lock()
		run.Done = true
		s.backups.mu.Unlock()
	}()
	http.Redirect(w, r, "/?p=backup&run="+run.ID, http.StatusSeeOther)
}

// backupPage adds the storages and a run of the user to the backup page.
func (s *Server) backupPage(p *systemPage, storages []string, runID string) {
	p.BackupStorages = storages
	if runID == "" {
		return
	}
	run, items, ok := s.backups.get(runID)
	if !ok || run.User != p.User {
		return
	}
	p.BackupRun = &run
	p.BackupItems = items
	p.BackupNote = fmt.Sprintf(p.T["bk_run"], run.Storage, or(run.Dir, "backup"), run.Started.Format("15:04:05"))
}
