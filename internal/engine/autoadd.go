package engine

import (
	"strings"

	"ffnvr/internal/config"
	"ffnvr/internal/modules"
	"ffnvr/internal/units"
)

// AutoAddCameras: the worker adds the cameras that appeared on remote recorders to the
// configuration file (see modules.NewChannels) and brings go2rtc up to date, so the
// live view shows them. Only go2rtc: other differences between the file and the
// system (an edit nobody applied yet) are left for "ffnvr apply". s is the
// configuration as loaded for this round; file "" (several files, a directory): the
// worker cannot write, it only tells.
func AutoAddCameras(s *config.Store, file string, opts Options, logf func(string, ...any)) {
	todo := modules.NewChannels(s, opts.Root)
	if len(todo) == 0 {
		return
	}
	if file == "" {
		for nvr, chs := range todo {
			logf("nvr %s: %d new camera(s) on the recorder; the configuration is not one file, run: /nvr scan %s", nvr, len(chs), nvr)
		}
		return
	}
	var lines []string
	err := EditConfig(file, opts, func(st *config.Store) error {
		var err error
		lines, err = modules.AddNewCameras(st, opts.Root, modules.NewChannels(st, opts.Root)) // again: the file may differ from s
		return err
	})
	for _, l := range lines {
		logf("auto-add: %s", l)
	}
	if err != nil {
		logf("auto-add: %v", err)
		return
	}
	if len(lines) == 0 {
		return
	}
	if err := applyGo2rtc(opts, file, logf); err != nil {
		logf("auto-add: go2rtc: %v", err)
	}
}

// applyGo2rtc applies only the changes of go2rtc's unit and configuration.
func applyGo2rtc(opts Options, file string, logf func(string, ...any)) error {
	e, err := New(opts)
	if err != nil {
		return err
	}
	if err := e.LoadFiles([]string{file}); err != nil {
		return err
	}
	p, err := e.Plan()
	if err != nil {
		return err
	}
	var only []units.Change
	var other []string
	for _, c := range p.Changes {
		switch {
		case c.Kind == units.Same:
		case c.Name == modules.Go2rtcUnit || c.Name == modules.GoConfig:
			only = append(only, c)
		default:
			other = append(other, c.Name)
		}
	}
	if len(other) > 0 {
		logf("auto-add: not applied (waiting for ffnvr apply): %s", strings.Join(other, ", "))
	}
	if len(only) == 0 {
		return nil
	}
	var out strings.Builder
	err = e.Apply(&units.Plan{Root: p.Root, Changes: only}, &out)
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l != "" {
			logf("auto-add: %s", l)
		}
	}
	return err
}
