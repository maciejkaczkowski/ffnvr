package web

import (
	"fmt"
	"path/filepath"
	"strings"

	"ffnvr/internal/jobs"
)

// jobView is one /nvr/download job as the panel shows it.
type jobView struct {
	ID                        int
	Nvr, Camera, Range, Files string
	Kind, State               string
	Pct                       float64
	Progress, Target, When    string
	Error, Comment            string
	Active                    bool
	Retry, Download           bool // can be run again; its file can be downloaded
}

func (s *Server) spool() jobs.Spool { return jobs.Spool{Dir: filepath.Join(s.opt.Root, jobs.Dir)} }

// jobsOf returns the jobs of the given recorders, newest first.
func (s *Server) jobsOf(nvrs map[string]bool, t map[string]string) []jobView {
	sp := s.spool()
	list, _ := sp.List()
	var out []jobView
	for i := len(list) - 1; i >= 0; i-- {
		j := list[i]
		if !nvrs[j.Nvr] {
			continue
		}
		v := jobView{ID: j.ID, Nvr: j.Nvr, Camera: j.Camera, Target: j.Target, Error: j.Error, Comment: j.Comment, Active: !j.State.Finished()}
		if j.Alias != "" {
			v.Camera = j.Alias + " (" + j.Camera + ")"
		}
		v.Range = j.Start.Format("2006-01-02 15:04") + " – " + j.End.Format("15:04")
		if j.End.Format("2006-01-02") != j.Start.Format("2006-01-02") {
			v.Range = j.Start.Format("2006-01-02 15:04") + " – " + j.End.Format("2006-01-02 15:04")
		}
		done := j.Count("done")
		v.Files = fmt.Sprintf("%d / %d", done, len(j.Parts))
		if n := j.Count("empty"); n > 0 {
			v.Files += fmt.Sprintf(" (%d %s)", n, t["j_empty"])
		}
		v.State, v.Kind = t["j_"+string(j.State)], map[jobs.State]string{
			jobs.Queued: kMute, jobs.Running: kOK, jobs.Done: kOK, jobs.Failed: kErr, jobs.Cancelled: kMute}[j.State]
		if v.Active && sp.CancelRequested(j.ID) {
			v.State, v.Kind = t["j_cancelling"], kWarn
		}
		v.Pct = j.Progress * 100
		v.Retry = j.State == jobs.Failed || j.State == jobs.Cancelled
		v.Download = j.State == jobs.Done && len(j.Parts) == 1
		switch j.State {
		case jobs.Running:
			v.Progress = fmt.Sprintf("%.0f%%", v.Pct)
			if j.Speed != "" && j.Speed != "N/A" {
				v.Progress += " · " + j.Speed
			}
			v.When = t["started"] + " " + j.Started.Format("15:04")
		case jobs.Done:
			v.Progress = human(uint64(j.Size)) + " · " + since(j.Finished.Sub(j.Started), t)
			v.When = j.Finished.Format("2006-01-02 15:04")
		case jobs.Queued:
			v.When = t["j_queued_at"] + " " + j.Created.Format("2006-01-02 15:04")
		default:
			if !j.Finished.IsZero() {
				v.When = j.Finished.Format("2006-01-02 15:04")
			}
		}
		if len(j.Parts) > 1 {
			v.Target = strings.TrimSuffix(j.Target, "/") + "/ (" + j.Block + ")"
		}
		out = append(out, v)
	}
	return out
}

func splitJobs(all []jobView) (active, done []jobView) {
	for _, j := range all {
		if j.Active {
			active = append(active, j)
		} else {
			done = append(done, j)
		}
	}
	return
}
