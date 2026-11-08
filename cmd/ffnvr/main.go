// ffnvr - network video recorder configured with a RouterOS-style syntax.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ffnvr/internal/alarm"
	"ffnvr/internal/config"
	"ffnvr/internal/engine"
	"ffnvr/internal/jobs"
	"ffnvr/internal/modules"
	"ffnvr/internal/profile"
	"ffnvr/internal/shell"
	"ffnvr/internal/sysd"
	"ffnvr/internal/web"
)

const defaultConfig = "/etc/ffnvr/config.rsc"

// version is set when building: -ldflags "-X main.version=1.2.3"
var version = "dev"

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

const usage = `usage: ffnvr <command> [flags]     (flags may also come before the command)

commands:
  shell    interactive shell
  apply    write the systemd units of the configuration and start/stop/restart them
  diff     print what apply would change, modify nothing
  export   print the normalised configuration
  worker   run the /nvr/download jobs and check remote recorders (ffnvr-worker.service)
  alarm    the alarm server: events of the recorders (ffnvr-alarm.service, /ip/service alarm-*, /event)
  web      the web panel (ffnvr-www.service, /ip/service www)
  help     this text

Without a command ffnvr only prints this help: apply has to be asked for explicitly.

examples:
  ffnvr shell
  ffnvr diff  -c /etc/ffnvr/config.rsc
  ffnvr apply -c /etc/ffnvr/config.rsc
  ffnvr export -c /etc/ffnvr/config.rsc

flags:
`

func main() {
	var cfgs multi
	flag.Var(&cfgs, "c", "configuration file or directory of *.rsc files (repeatable; default "+defaultConfig+")")
	root := flag.String("root", "/", "operate on this filesystem root (systemctl is not run unless it is /)")
	noSvc := flag.Bool("no-services", false, "write the unit files but do not run systemctl")
	ffmpeg := flag.String("ffmpeg", "ffmpeg", "ffmpeg binary used by the worker")
	listen := flag.String("listen", ":80", "web: address to listen on")
	allow := flag.String("allow", "", "web: addresses/networks that may connect, e.g. 192.168.0.0/16,10.0.0.5 (empty: all)")
	proxy := flag.String("proxy", "", "web: reverse proxies whose X-Forwarded-For is believed, e.g. 10.0.0.5")
	statusEvery := flag.Duration("status-every", time.Minute, "worker: how often it looks for due work (remote recorders are read every poll-interval of /system/settings)")
	sessionKeep := flag.Duration("session-keep", profile.SessionsKeep, "worker: sign ins of the web panel unused this long are removed from the users' profiles")
	alarmDVRIP := flag.String("dvrip", "", "alarm: address of the DVRIP alarm server, e.g. :15002 (empty: off)")
	alarmDVRIPAllow := flag.String("dvrip-allow", "", "alarm: who may push DVRIP alarms (empty: all)")
	alarmSMTP := flag.String("smtp", "", "alarm: address of the SMTP server, e.g. :2525 (empty: off)")
	alarmSMTPAllow := flag.String("smtp-allow", "", "alarm: who may send e-mails (empty: all)")
	alarmLAPI := flag.String("lapi", "", "alarm: address of the HTTP server Uniview devices push their events to (LAPI), e.g. :8091 (empty: off)")
	alarmLAPIAllow := flag.String("lapi-allow", "", "alarm: who may push LAPI events (empty: all)")
	histFile := flag.String("history", defaultHistory(), "shell command history file (empty: in memory only)")
	flag.Usage = func() { fmt.Fprint(flag.CommandLine.Output(), usage); flag.PrintDefaults() }
	flag.Parse()

	// the command may stand before or after the flags: "ffnvr -c x export" and "ffnvr export -c x"
	cmd := flag.Arg(0)
	if cmd != "" {
		if err := flag.CommandLine.Parse(flag.Args()[1:]); err != nil {
			os.Exit(2)
		}
		if flag.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "ffnvr: unexpected argument %q after the command %q\n", flag.Arg(0), cmd)
			os.Exit(2)
		}
	}
	switch cmd {
	case "help":
		flag.CommandLine.SetOutput(os.Stdout)
		flag.Usage()
		return
	case "":
		fmt.Fprintln(os.Stderr, "ffnvr: no command given")
		flag.Usage()
		os.Exit(2)
	case "web":
		if len(cfgs) == 0 {
			cfgs = multi{defaultConfig}
		}
		allow, err := modules.ParseAllow(*allow)
		if err != nil {
			fatal(err)
		}
		proxies, err := modules.ParseAllow(*proxy)
		if err != nil {
			fatal(err)
		}
		load := func() (*config.Store, error) {
			e, err := engine.New(engine.Options{Root: *root})
			if err != nil {
				return nil, err
			}
			if err := e.LoadFiles(cfgs); err != nil {
				return nil, err
			}
			return e.Store, nil
		}
		if _, err := load(); err != nil {
			fatal(err)
		}
		var edit func(func(*config.Store) error) error
		if len(cfgs) == 1 { // the panel writes saved views and camera positions into it
			if st, err := os.Stat(cfgs[0]); err == nil && !st.IsDir() {
				edit = func(fn func(*config.Store) error) error {
					return engine.EditConfig(cfgs[0], engine.Options{Root: *root}, fn)
				}
			}
		}
		var systemd web.Systemd
		if filepath.Clean(*root) == "/" { // the camera services exist on the real system only
			systemd = &sysd.Systemd{}
		}
		srv := web.HTTPServer(*listen, web.New(web.Options{Load: load, Root: *root, Version: version, Allow: allow, Proxies: proxies, Systemd: systemd, Edit: edit}))
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(sctx)
		}()
		log.Printf("web panel on %s", *listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal(err)
		}
		return
	case "worker":
		if len(cfgs) == 0 {
			cfgs = multi{defaultConfig}
		}
		// the configuration is read again whenever needed: changes saved by the shell
		// apply without restarting the worker
		load := func() (*config.Store, error) {
			e, err := engine.New(engine.Options{Root: *root})
			if err != nil {
				return nil, err
			}
			if err := e.LoadFiles(cfgs); err != nil {
				return nil, err
			}
			e.Store.TakeNotices()
			return e.Store, nil
		}
		logger := log.New(os.Stderr, "", 0)
		cfgFile := "" // one file: the worker may add the cameras appearing on remote recorders
		if len(cfgs) == 1 {
			if st, err := os.Stat(cfgs[0]); err == nil && !st.IsDir() {
				cfgFile = cfgs[0]
			}
		}
		w := &jobs.Worker{
			Spool: jobs.Spool{Dir: filepath.Join(*root, jobs.Dir)}, FFmpeg: *ffmpeg, Log: logger,
			Remote: modules.RemoteSource(load),
			Every:  *statusEvery,
			Periodic: func(ctx context.Context) {
				s, err := load()
				if err != nil {
					logger.Printf("configuration: %v", err)
					return
				}
				modules.RefreshStatus(ctx, s, *root, logger.Printf)
				engine.AutoAddCameras(s, cfgFile, engine.Options{Root: *root}, logger.Printf)
				modules.ApplySchedules(ctx, s, *root, *ffmpeg, time.Now(), logger.Printf) // before the index is made
				modules.MaintainLocalLog(s, *root, time.Now(), logger.Printf)
				modules.ScheduleSnapshots(ctx, s, *root, *ffmpeg, time.Now(), logger.Printf)
				var users []string
				for _, u := range s.Items("/user") {
					users = append(users, u.Val("user"))
				}
				prof := profile.New(*root)
				prof.Prune(users, logger.Printf)                            // the profiles of removed users
				prof.PruneSessions(time.Now(), *sessionKeep, logger.Printf) // the sign ins of the panel unused for long
			},
			Background: func(ctx context.Context) {
				if !modules.PendingThumbs(*root) {
					return
				}
				if s, err := load(); err == nil {
					modules.MakeThumbs(ctx, s, *root, *ffmpeg, 20, logger.Printf)
				}
			},
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := w.Run(ctx); err != nil {
			fatal(err)
		}
		return
	case "alarm":
		if len(cfgs) == 0 {
			cfgs = multi{defaultConfig}
		}
		load := func() (*config.Store, error) {
			e, err := engine.New(engine.Options{Root: *root})
			if err != nil {
				return nil, err
			}
			if err := e.LoadFiles(cfgs); err != nil {
				return nil, err
			}
			e.Store.TakeNotices()
			return e.Store, nil
		}
		if _, err := load(); err != nil {
			fatal(err)
		}
		dAllow, err := modules.ParseAllow(*alarmDVRIPAllow)
		if err != nil {
			fatal(err)
		}
		sAllow, err := modules.ParseAllow(*alarmSMTPAllow)
		if err != nil {
			fatal(err)
		}
		lAllow, err := modules.ParseAllow(*alarmLAPIAllow)
		if err != nil {
			fatal(err)
		}
		logger := log.New(os.Stderr, "", 0)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := alarm.Run(ctx, alarm.Options{Load: load, Root: *root, DVRIP: *alarmDVRIP, DVRIPAllow: dAllow,
			SMTP: *alarmSMTP, SMTPAllow: sAllow, LAPI: *alarmLAPI, LAPIAllow: lAllow, Logf: logger.Printf}); err != nil {
			fatal(err)
		}
		return
	case "shell", "export", "apply", "diff":
	default:
		fmt.Fprintf(os.Stderr, "ffnvr: unknown command %q\n", cmd)
		flag.Usage()
		os.Exit(2)
	}
	if len(cfgs) == 0 {
		// the shell may start from scratch, export needs a configuration
		if _, err := os.Stat(defaultConfig); err == nil || cmd != "shell" {
			cfgs = multi{defaultConfig}
		}
	}
	e, err := engine.New(engine.Options{Root: *root, NoServices: *noSvc})
	if err != nil {
		fatal(err)
	}
	if err := e.LoadFiles(cfgs); err != nil {
		fatal(err)
	}
	for _, n := range e.Store.TakeNotices() {
		fmt.Fprintln(os.Stderr, "ffnvr: warning:", n)
	}
	switch cmd {
	case "export":
		fmt.Print(e.Store.Export("/"))
	case "apply", "diff":
		p, err := e.Plan()
		if err != nil {
			fatal(err)
		}
		if cmd == "diff" {
			p.WriteDiff(os.Stdout)
			return
		}
		if err := e.Apply(p, os.Stdout); err != nil {
			fatal(err)
		}
	case "shell":
		save := ""
		if len(cfgs) == 1 {
			save = cfgs[0]
		}
		if err := shell.Run(e, os.Stdin, os.Stdout, shell.Options{SaveFile: save, HistoryFile: *histFile}); err != nil {
			fatal(err)
		}
	}
}

func defaultHistory() string {
	if h := os.Getenv("HOME"); h != "" {
		return h + "/.ffnvr_history"
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ffnvr:", err)
	os.Exit(1)
}
