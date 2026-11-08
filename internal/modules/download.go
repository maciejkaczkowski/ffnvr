package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ffnvr/internal/config"
	"ffnvr/internal/jobs"
	"ffnvr/internal/module"
	"ffnvr/internal/script"
	"ffnvr/internal/units"
)

// downloadModule: /nvr/download - archive recordings of a camera as one mp4 file.
// It is an action menu: "add" queues a job for the worker (ffnvr worker, run by
// ffnvr-worker.service), "print" shows the jobs and their progress.
//
//	/nvr/download add camera=cam01 storage=archive path=case-12 start="2026-10-24 08:00" end="2026-10-24 09:30"
//	/nvr/download print
//	/nvr/download cancel 3
//	/nvr/download remove 1,2
type downloadModule struct {
	ctx *module.Context
}

func (d *downloadModule) Name() string           { return "download" }
func (d *downloadModule) Bind(c *module.Context) { d.ctx = c }

func (d *downloadModule) spool() jobs.Spool {
	root := "/"
	if d.ctx != nil {
		root = d.ctx.Root
	}
	return jobs.Spool{Dir: filepath.Join(root, jobs.Dir)}
}

func (d *downloadModule) Menus() []*config.Menu {
	return []*config.Menu{{
		Path: "/nvr/download",
		Kind: config.Multi,
		Help: "archive recordings to mp4 (jobs)",
		Fields: []config.Field{
			{Name: "camera", Required: true, Ref: "/camera:name,/camera:alias", Strict: true, Help: "name or alias"},
			{Name: "nvr", Ref: "/nvr:name", Strict: true, Help: "needed when the camera name is used on more than one nvr"},
			{Name: "storage", Required: true, Ref: "/storage:name", Strict: true, Help: "where the archive goes"},
			{Name: "path", Help: "subdirectory of the storage, e.g. case-12/door"},
			{Name: "layout", Type: config.TEnum, Enum: []string{LayoutNvrCamera, LayoutFlat},
				Help: "nvr-camera (default): the files in <path>/<nvr>/<camera>/; flat: all in <path>/"},
			{Name: "file", Help: "file name, default <nvr>_<camera>_<start>_<end>.mp4 (with block: the prefix before _<start>_<end>)"},
			{Name: "block", Help: "split into files of this length: 15m, 1h, 1h30m, 1d (default: one file)"},
			{Name: "block-align", Type: config.TEnum, Enum: []string{jobs.AlignClock, jobs.AlignStart},
				Help: "clock (default): 11:50-12:00, 12:00-13:00 ...; start: 11:50-12:50, 12:50-13:50 ..."},
			{Name: "start", Required: true, Help: `"YYYY-MM-DD HH:MM[:SS]" local time`},
			{Name: "end", Required: true, Help: `"YYYY-MM-DD HH:MM[:SS]" local time`},
			{Name: "comment"},
		},
		Validate: validateDownload,
		Live:     d,
	}}
}

// Archive layouts (layout= of /nvr/download).
const (
	LayoutNvrCamera = "nvr-camera" // <path>/<nvr>/<camera>/<file>, the default
	LayoutFlat      = "flat"       // <path>/<file>
)

var timeLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02T15:04"}

func parseTime(field, v string) (time.Time, error) {
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%s %q: expected \"YYYY-MM-DD HH:MM[:SS]\"", field, v)
}

func checkRelPath(field, v string) error {
	if v == "" {
		return nil
	}
	for _, part := range strings.Split(v, "/") {
		if part == "" {
			return fmt.Errorf("%s %q: must be relative, without empty parts", field, v)
		}
		if err := checkName(field, part); err != nil {
			return err
		}
	}
	return nil
}

func validateDownload(s *config.Store, it *config.Item) error {
	from, err := parseTime("start", it.Val("start"))
	if err != nil {
		return err
	}
	to, err := parseTime("end", it.Val("end"))
	if err != nil {
		return err
	}
	if !to.After(from) {
		return fmt.Errorf("end must be after start")
	}
	if from.After(time.Now()) {
		return fmt.Errorf("start is in the future")
	}
	it.Set("start", from.Format(jobs.TimeFormat))
	it.Set("end", to.Format(jobs.TimeFormat))
	if b := it.Val("block"); b != "" {
		length, err := jobs.ParseLength(b)
		if err != nil {
			return fmt.Errorf("block %w", err)
		}
		it.Set("block", jobs.FormatLength(length))
		if it.Val("block-align") == "" {
			it.Set("block-align", jobs.AlignClock)
		}
		n := int(to.Sub(from) / length) // a lower bound, cheap for huge ranges
		if n <= jobs.MaxParts {
			n = len(jobs.Blocks(from, to, length, it.Val("block-align")))
		}
		if n > jobs.MaxParts {
			return fmt.Errorf("block=%s gives %d files, at most %d are allowed", it.Val("block"), n, jobs.MaxParts)
		}
	} else if it.Val("block-align") != "" {
		return fmt.Errorf("block-align needs block")
	}
	// camera names are unique per nvr only: nvr= picks the camera; it is required when
	// the name is used on several recorders and is stored resolved for Run
	var nvrs []string
	for _, c := range s.Items("/camera") {
		if isCamera(c, it.Val("camera")) {
			nvrs = append(nvrs, c.Val("nvr"))
		}
	}
	if n := it.Val("nvr"); n != "" {
		found := false
		for _, x := range nvrs {
			found = found || x == n
		}
		if !found {
			return fmt.Errorf("nvr %s has no camera %s (it is on: %s)", n, it.Val("camera"), strings.Join(nvrs, ", "))
		}
	} else if len(nvrs) > 1 {
		return fmt.Errorf("camera %s is on several nvrs (%s), give nvr=", it.Val("camera"), strings.Join(nvrs, ", "))
	} else if len(nvrs) == 1 {
		it.Set("nvr", nvrs[0])
	}
	if err := checkRelPath("path", it.Val("path")); err != nil {
		return err
	}
	if f := it.Val("file"); f != "" {
		if err := checkName("file", f); err != nil {
			return err
		}
		if it.Val("block") != "" {
			it.Set("file", strings.TrimSuffix(f, ".mp4"))
		} else if !strings.HasSuffix(f, ".mp4") {
			it.Set("file", f+".mp4")
		}
	}
	return nil
}

func (d *downloadModule) Verbs() []string { return []string{"add", "cancel", "remove", "print"} }

func (d *downloadModule) IDs() [][2]string {
	js, _ := d.spool().List()
	var out [][2]string
	for _, j := range js {
		out = append(out, [2]string{strconv.Itoa(j.ID), fmt.Sprintf("%s %s %s", j.State, camLabel(j), j.Start.Format(jobs.TimeFormat))})
	}
	return out
}

func find(s *config.Store, path, key, val string) *config.Item {
	for _, it := range s.Items(path) {
		if it.Val(key) == val {
			return it
		}
	}
	return nil
}

func (d *downloadModule) Run(s *config.Store, c script.Command, p *config.Item) (string, error) {
	sp := d.spool()
	switch c.Verb {
	case "add":
		if len(c.Args) > 0 {
			return "", fmt.Errorf("/nvr/download: unexpected %q", c.Args[0])
		}
		nvr := find(s, "/nvr", "name", p.Val("nvr"))
		var cam *config.Item
		for _, c := range s.Items("/camera") {
			if isCamera(c, p.Val("camera")) && c.Val("nvr") == p.Val("nvr") {
				cam = c
			}
		}
		dst := find(s, "/storage", "name", p.Val("storage"))
		from, _ := parseTime("start", p.Val("start"))
		to, _ := parseTime("end", p.Val("end"))
		dir := filepath.Join(dst.Val("path"), p.Val("path"))
		label := cam.Val("name") // the alias, when set, reads better in file names
		if cam.Val("alias") != "" {
			label = cam.Val("alias")
		}
		// the recorder and the camera are in the file names only where the directories
		// do not tell them: <nvr>/<camera>/<start>_<end>.mp4, flat <nvr>_<camera>_<start>_<end>.mp4
		prefix := nvr.Val("name") + "_" + label
		if p.Val("layout") != LayoutFlat { // a directory per recorder and camera: hundreds of files stay sorted
			dir = filepath.Join(dir, nvr.Val("name"), label)
			prefix = ""
		}
		name := func(r jobs.Range) string {
			span := r.Start.Format(jobs.SegmentLayout) + "_" + r.End.Format(jobs.SegmentLayout) + ".mp4"
			if prefix == "" {
				return span
			}
			return prefix + "_" + span
		}
		var parts []jobs.Part
		target := ""
		if p.Val("block") == "" { // one file
			file := p.Val("file")
			if file == "" {
				file = name(jobs.Range{Start: from, End: to})
			}
			target = filepath.Join(dir, file)
			parts = []jobs.Part{{Start: from, End: to, Target: target}}
		} else { // one file per block, in dir
			if f := p.Val("file"); f != "" {
				prefix = f
			}
			length, _ := jobs.ParseLength(p.Val("block"))
			for _, r := range jobs.Blocks(from, to, length, p.Val("block-align")) {
				parts = append(parts, jobs.Part{Start: r.Start, End: r.End, Target: filepath.Join(dir, name(r))})
			}
			target = dir + "/"
		}
		js, _ := sp.List()
		for _, part := range parts {
			if _, err := os.Stat(part.Target); err == nil {
				return "", fmt.Errorf("/nvr/download: %s already exists", part.Target)
			}
			for _, j := range js {
				for _, o := range j.Parts {
					if o.Target == part.Target && !j.State.Finished() {
						return "", fmt.Errorf("/nvr/download: job %d already writes %s", j.ID, part.Target)
					}
				}
			}
		}
		job := jobs.Job{
			Nvr: nvr.Val("name"), Camera: cam.Val("name"), Alias: cam.Val("alias"), Start: from, End: to,

			Target:  target,
			Block:   p.Val("block"),
			Align:   p.Val("block-align"),
			Parts:   parts,
			Comment: p.Val("comment"),
		}
		if remote(nvr) { // the recorder keeps the recordings, the worker asks it
			job.Remote = nvr.Val("type")
			job.Channel, _ = strconv.Atoi(cam.Val("channel"))
		} else {
			src := find(s, "/storage", "name", nvr.Val("storage"))
			job.Source = filepath.Join(src.Val("path"), nvr.Val("name"), cam.Val("name"))
		}
		j, err := sp.Submit(job)
		if err != nil {
			return "", fmt.Errorf("/nvr/download: %w", err)
		}
		msg := fmt.Sprintf("job %d queued: %s\n", j.ID, target)
		if len(parts) > 1 {
			msg = fmt.Sprintf("job %d queued: %d files (block %s, %s) in %s\n", j.ID, len(parts), j.Block, j.Align, target)
			for _, part := range parts {
				msg += fmt.Sprintf("  %s - %s  %s\n", part.Start.Format(jobs.TimeFormat), part.End.Format(jobs.TimeFormat), filepath.Base(part.Target))
			}
		}
		if !sp.WorkerAlive() {
			msg += "warning: " + jobs.ErrNoWorker.Error() + "\n"
		}
		return msg, nil
	case "cancel", "remove":
		ids, err := jobIDs(c.Args)
		if err != nil {
			return "", err
		}
		for _, id := range ids {
			if c.Verb == "cancel" {
				err = sp.Cancel(id)
			} else {
				err = sp.Remove(id)
			}
			if err != nil {
				return "", fmt.Errorf("/nvr/download: %w", err)
			}
		}
		return "", nil
	default: // print
		if len(c.Args) > 0 || len(c.Params) > 0 {
			return "", fmt.Errorf("/nvr/download: print takes no arguments")
		}
		return d.print(sp)
	}
}

func jobIDs(args []string) ([]int, error) {
	var ids []int
	for _, a := range args {
		for _, x := range strings.Split(a, ",") {
			id, err := strconv.Atoi(x)
			if err != nil || id < 1 {
				return nil, fmt.Errorf("/nvr/download: %q is not a job number", x)
			}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("/nvr/download: give the job numbers (see print)")
	}
	return ids, nil
}

func (d *downloadModule) print(sp jobs.Spool) (string, error) {
	js, err := sp.List()
	if err != nil {
		return "", err
	}
	type row struct{ id, nvr, cam, start, end, state, prog string }
	rows := []row{{" #", "NVR", "CAMERA", "START", "END", "STATE", "PROGRESS"}}
	w := [6]int{}
	waiting := false
	for _, j := range js {
		state := string(j.State)
		if !j.State.Finished() && sp.CancelRequested(j.ID) {
			state = "cancelling"
		}
		waiting = waiting || !j.State.Finished()
		prog := ""
		switch j.State {
		case jobs.Running:
			prog = fmt.Sprintf("%.0f%%", j.Progress*100)
			if len(j.Parts) > 1 {
				prog += fmt.Sprintf(" %d/%d", min(j.Count("done")+j.Count("empty")+1, len(j.Parts)), len(j.Parts))
			}
			if j.Speed != "" && j.Speed != "N/A" {
				prog += " " + j.Speed
			}
		case jobs.Done:
			prog = "100% "
			if len(j.Parts) > 1 {
				prog += fmt.Sprintf("%d files ", j.Count("done"))
			}
			prog += fmt.Sprintf("%s in %s", size(j.Size), j.Finished.Sub(j.Started).Round(time.Second))
		}
		rows = append(rows, row{fmt.Sprintf("%2d", j.ID), j.Nvr, camLabel(j),
			j.Start.Format(jobs.TimeFormat), j.End.Format(jobs.TimeFormat), state, prog})
	}
	for _, r := range rows {
		for i, c := range []string{r.id, r.nvr, r.cam, r.start, r.end, r.state} {
			w[i] = max(w[i], len(c))
		}
	}
	var b strings.Builder
	for i, r := range rows {
		line := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s  %s", w[0], r.id, w[1], r.nvr, w[2], r.cam,
			w[3], r.start, w[4], r.end, w[5], r.state, r.prog)
		b.WriteString(strings.TrimRight(line, " ") + "\n")
		if i == 0 {
			continue
		}
		pad := strings.Repeat(" ", w[0]+2)
		j := js[i-1]
		fmt.Fprintf(&b, "%s-> %s\n", pad, j.Target)
		if len(j.Parts) > 1 {
			fmt.Fprintf(&b, "%s%d files, block %s %s", pad, len(j.Parts), j.Block, j.Align)
			if n := j.Count("empty"); n > 0 {
				fmt.Fprintf(&b, ", %d without recordings", n)
			}
			b.WriteString("\n")
		}
		if j.Error != "" {
			fmt.Fprintf(&b, "%serror: %s\n", pad, j.Error)
		}
		if j.Comment != "" {
			fmt.Fprintf(&b, "%s;;; %s\n", pad, j.Comment)
		}
	}
	if len(js) == 0 {
		return "no jobs\n", nil
	}
	if waiting && !sp.WorkerAlive() {
		b.WriteString("warning: " + jobs.ErrNoWorker.Error() + "\n")
	}
	return b.String(), nil
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%dk", n>>10)
	}
}

// WorkerExe is where "make install" puts the binary run by the worker unit.
const WorkerExe = "/usr/local/bin/ffnvr"

const workerUnit = `[Unit]
Description=ffnvr: archive worker (/nvr/download jobs)
After=local-fs.target

[Service]
Type=simple
User=root
ExecStart=` + WorkerExe + ` worker
Nice=10
IOSchedulingClass=best-effort
IOSchedulingPriority=7
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`

// withBinary puts the identity of the ffnvr binary into a unit of ffnvr's own services
// (after "Type=simple"): a new version of ffnvr changes the unit, commit restarts it.
func withBinary(unit string, c *module.Context) string {
	if c == nil || c.Binary == "" {
		return unit
	}
	return strings.Replace(unit, "Type=simple\n", "Type=simple\nEnvironment=FFNVR_BINARY="+c.Binary+"\n", 1)
}

// Units: the worker always runs, it is idle without jobs.
func (d *downloadModule) Units(c *module.Context) ([]units.Unit, error) {
	return []units.Unit{{Name: units.Prefix + "worker" + units.Suffix, Content: withBinary(workerUnit, c)}}, nil
}

// isCamera: the camera is called v (by name or alias).
func isCamera(c *config.Item, v string) bool {
	return c.Val("name") == v || (c.Val("alias") != "" && c.Val("alias") == v)
}

// camLabel shows a job's camera as name(alias).
func camLabel(j *jobs.Job) string {
	if j.Alias != "" {
		return j.Camera + "(" + j.Alias + ")"
	}
	return j.Camera
}
