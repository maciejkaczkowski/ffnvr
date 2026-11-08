package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ffnvr/internal/config"
	"ffnvr/internal/modules"
	"ffnvr/internal/profile"
	"ffnvr/internal/script"
)

// Saved views are kept in the user's profile (dashboards.json, package profile); camera
// positions (/camera/position) are written into the configuration file by the panel
// (Options.Edit).

func run(s *config.Store, path, verb string, find, params []script.Param) error {
	c := script.Command{Path: path, Verb: verb, Find: find, HasFind: find != nil, Params: params}
	_, err := s.Exec(c)
	return err
}

func p(k, v string) script.Param { return script.Param{Key: k, Value: v} }

type jsonResult struct {
	OK    bool   `json:"ok"`
	ID    string `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// editRequest: a signed in user with the right perm, the CSRF token, the configuration
// writable by the panel.
func (s *Server) editRequest(w http.ResponseWriter, r *http.Request, perm string) (*session, bool) {
	if s.opt.Edit == nil {
		writeJSON(w, http.StatusServiceUnavailable, jsonResult{Error: "the configuration cannot be written by the panel"})
		return nil, false
	}
	return s.userRequest(w, r, perm)
}

// userRequest: a signed in user with the right perm and the CSRF token (a change of
// the user's own profile).
func (s *Server) userRequest(w http.ResponseWriter, r *http.Request, perm string) (*session, bool) {
	se := s.current(r)
	if se == nil {
		writeJSON(w, http.StatusUnauthorized, jsonResult{Error: "not signed in"})
		return nil, false
	}
	if r.PostFormValue("csrf") != se.csrf {
		writeJSON(w, http.StatusForbidden, jsonResult{Error: "forbidden"})
		return nil, false
	}
	store, err := s.opt.Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResult{Error: "configuration error"})
		return nil, false
	}
	if !can(store, se, perm) {
		s.opt.Log.Printf("web: %s: no right %s", se.user, perm)
		writeJSON(w, http.StatusForbidden, jsonResult{Error: "the right " + perm + " is missing"})
		return nil, false
	}
	return se, true
}

type viewTile struct {
	Pos int    `json:"pos"`
	N   string `json:"n"`
	C   string `json:"c"`
	Q   string `json:"q"`
}

// POST /live/view/save: id (empty: a new view), name, size, tiles (JSON).
func (s *Server) viewSave(w http.ResponseWriter, r *http.Request) {
	se, ok := s.userRequest(w, r, modules.PermLiveViews)
	if !ok {
		return
	}
	var tiles []viewTile
	if err := json.Unmarshal([]byte(r.PostFormValue("tiles")), &tiles); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: "tiles: " + err.Error()})
		return
	}
	id, name := r.PostFormValue("id"), strings.TrimSpace(r.PostFormValue("name"))
	size, _ := strconv.Atoi(r.PostFormValue("size"))
	if size == 0 {
		size = 4
	}
	if name == "" || len([]rune(name)) > 60 || strings.ContainsAny(name, "\n\r\t") {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: "name: 1 to 60 characters on one line"})
		return
	}
	if !slices.Contains(profile.DashboardSizes, size) {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: "size: 1, 4, 9, 16 or 25 tiles"})
		return
	}
	store, err := s.opt.Load()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonResult{Error: "configuration error"})
		return
	}
	view := profile.Dashboard{Name: name, Size: size, Tiles: []profile.DashTile{}}
	taken := map[int]bool{}
	for _, t := range tiles {
		if !modules.CanAccess(store, se.user, t.N) {
			continue
		}
		switch {
		case t.Pos < 1 || t.Pos > size:
			err = fmt.Errorf("position %d: the view has %d tiles", t.Pos, size)
		case taken[t.Pos]:
			err = fmt.Errorf("position %d of the view is taken", t.Pos)
		case !nameOK(t.C):
			err = fmt.Errorf("camera %q: a name", t.C)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, jsonResult{Error: err.Error()})
			return
		}
		taken[t.Pos] = true
		q := t.Q
		if q != "hq" && q != "lq" {
			q = ""
		}
		view.Tiles = append(view.Tiles, profile.DashTile{Position: t.Pos, Nvr: t.N, Camera: t.C, Quality: q})
	}
	var ds profile.Dashboards
	err = s.prof.Update(se.user, profile.DashboardsFile, &ds, func() error {
		ds.Version = profile.DashboardsVersion
		if own := ds.Find(id); own != nil {
			view.ID = id
			*own = view
			return nil
		}
		if len(ds.Dashboards) >= 100 {
			return errors.New("at most 100 views")
		}
		view.ID = profile.NewID()
		ds.Dashboards = append(ds.Dashboards, view)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: err.Error()})
		return
	}
	s.opt.Log.Printf("web: %s saved the view %q", se.user, name)
	writeJSON(w, http.StatusOK, jsonResult{OK: true, ID: view.ID})
}

// nameOK: a name of a camera (the characters of names in the configuration).
func nameOK(v string) bool { return v != "" && len(v) <= 64 && camNameRe.MatchString(v) }

var camNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// POST /live/view/delete: id.
func (s *Server) viewDelete(w http.ResponseWriter, r *http.Request) {
	se, ok := s.userRequest(w, r, modules.PermLiveViews)
	if !ok {
		return
	}
	id := r.PostFormValue("id")
	var ds profile.Dashboards
	err := s.prof.Update(se.user, profile.DashboardsFile, &ds, func() error {
		if !ds.Delete(id) {
			return errors.New("no such view")
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: err.Error()})
		return
	}
	s.opt.Log.Printf("web: %s deleted the view %s", se.user, id)
	writeJSON(w, http.StatusOK, jsonResult{OK: true})
}

type posJSON struct {
	N      string  `json:"n"`
	C      string  `json:"c"`
	Lat    float64 `json:"lat"`
	Long   float64 `json:"long"`
	Dir    float64 `json:"dir"`
	Angle  float64 `json:"angle"`
	Range  float64 `json:"range"`
	Remove bool    `json:"remove"`
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// POST /live/positions: positions (JSON) of cameras the user may see.
func (s *Server) positionsSave(w http.ResponseWriter, r *http.Request) {
	se, ok := s.editRequest(w, r, modules.PermLiveMapEdit)
	if !ok {
		return
	}
	var ps []posJSON
	if err := json.Unmarshal([]byte(r.PostFormValue("positions")), &ps); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: "positions: " + err.Error()})
		return
	}
	err := s.opt.Edit(func(st *config.Store) error {
		for _, x := range ps {
			if !modules.CanAccess(st, se.user, x.N) {
				return fmt.Errorf("no access to %s", x.N)
			}
			if err := run(st, "/camera/position", "remove", []script.Param{p("nvr", x.N), p("camera", x.C)}, nil); err != nil {
				return err
			}
			if x.Remove {
				continue
			}
			dir := x.Dir
			for dir < 0 {
				dir += 360
			}
			for dir >= 360 {
				dir -= 360
			}
			if err := run(st, "/camera/position", "add", nil, []script.Param{p("nvr", x.N), p("camera", x.C),
				p("lat", strconv.FormatFloat(x.Lat, 'f', 7, 64)), p("long", strconv.FormatFloat(x.Long, 'f', 7, 64)),
				p("direction", strconv.FormatFloat(dir, 'f', 0, 64)), p("angle", strconv.FormatFloat(x.Angle, 'f', 0, 64)),
				p("range", strconv.FormatFloat(x.Range, 'f', 0, 64))}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, jsonResult{Error: err.Error()})
		return
	}
	s.opt.Log.Printf("web: %s placed %d cameras on the map", se.user, len(ps))
	writeJSON(w, http.StatusOK, jsonResult{OK: true})
}
