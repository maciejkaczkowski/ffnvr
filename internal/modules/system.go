package modules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ffnvr/internal/config"
	"ffnvr/internal/jobs"
)

// systemModule: /system/settings, /system/www and /system/systemd - settings of ffnvr itself.
//
//	/system/settings
//	set poll-interval=15m
//	/system/www
//	set name="Monitoring ACME" login-name=no login-logo=no
type systemModule struct{}

func (systemModule) Name() string { return "system" }

// DefaultPollInterval: how often the worker reads remote recorders.
const DefaultPollInterval = 5 * time.Minute

func (systemModule) Menus() []*config.Menu {
	return []*config.Menu{
		{
			Path: "/system/settings",
			Kind: config.Single,
			Help: "settings of ffnvr",
			Fields: []config.Field{
				{Name: "poll-interval", Help: "how often remote recorders are read (firmware, disks, cameras); default 5m, at least 1m"},
				{Name: "disk-reserve", Help: "free space kept on every storage of local recorders, e.g. 2G: the oldest recordings go when it is short, whatever the retention (default: none)"},
			},
			Validate: func(_ *config.Store, it *config.Item) error {
				if v := it.Val("poll-interval"); v != "" {
					d, err := jobs.ParseLength(v)
					if err != nil {
						return fmt.Errorf("poll-interval %w", err)
					}
					it.Set("poll-interval", jobs.FormatLength(d))
				}
				if v := it.Val("disk-reserve"); v != "" {
					n, err := ParseSize(v)
					if err != nil {
						return err
					}
					it.Set("disk-reserve", FormatSize(n))
				}
				return nil
			},
		},
		motionDetectorMenu(),
		systemdMenu(),
		{
			Path: "/system/www",
			Kind: config.Single,
			Help: "look of the web panel",
			Fields: []config.Field{
				{Name: "name", Help: "the name shown instead of ffnvr"},
				{Name: "tagline", Help: "the line under the name on the sign in page (default: the translated one)"},
				{Name: "login-name", Type: config.TBool, Help: "show the name on the sign in page (default yes)"},
				{Name: "login-logo", Type: config.TBool, Help: "show the logo on the sign in page (default yes)"},
			},
			Validate: func(_ *config.Store, it *config.Item) error {
				for _, k := range []string{"name", "tagline"} {
					v := it.Val(k)
					if len([]rune(v)) > 60 {
						return fmt.Errorf("%s: at most 60 characters", k)
					}
					if strings.IndexFunc(v, unicode.IsControl) >= 0 {
						return errors.New(k + ": control characters are not allowed")
					}
				}
				return nil
			},
		},
	}
}

func single(s *config.Store, path string) *config.Item {
	if it := s.Single(path); it != nil {
		return it
	}
	return config.NewItem()
}

// PollInterval of remote recorders (/system/settings poll-interval).
func PollInterval(s *config.Store) time.Duration {
	if d, err := jobs.ParseLength(single(s, "/system/settings").Val("poll-interval")); err == nil {
		return d
	}
	return DefaultPollInterval
}

// Branding of the web panel (/system/www).
type Branding struct {
	Name, Tagline        string
	LoginName, LoginLogo bool
}

func WebBranding(s *config.Store) Branding {
	it := single(s, "/system/www")
	return Branding{Name: or(it.Val("name"), "ffnvr"), Tagline: it.Val("tagline"),
		LoginName: it.Val("login-name") != "no", LoginLogo: it.Val("login-logo") != "no"}
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ParseSize reads a size: 500M, 2G, 1.5T (powers of 1024; a trailing B is allowed).
func ParseSize(v string) (int64, error) {
	s := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(strings.ToUpper(v)), "B"))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult = 1 << 10
	case strings.HasSuffix(s, "M"):
		mult = 1 << 20
	case strings.HasSuffix(s, "G"):
		mult = 1 << 30
	case strings.HasSuffix(s, "T"):
		mult = 1 << 40
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || mult == 1 {
		return 0, fmt.Errorf("size %q: expected e.g. 500M, 2G, 1.5T", v)
	}
	return int64(f * float64(mult)), nil
}

// FormatSize writes a size the way ParseSize reads it.
func FormatSize(n int64) string {
	for _, u := range []struct {
		s string
		m int64
	}{{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}} {
		if n >= u.m {
			return strconv.FormatFloat(float64(n)/float64(u.m), 'f', -1, 64) + u.s
		}
	}
	return strconv.FormatInt(n, 10)
}

// HumanSize: a size to be read, rounded - one decimal below 100 of a unit (1.5G,
// 229M, 12.3K). FormatSize is exact (it writes values of the configuration back).
func HumanSize(n int64) string {
	for _, u := range []struct {
		s string
		m int64
	}{{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}} {
		if n >= u.m {
			v := float64(n) / float64(u.m)
			prec := 1
			if v >= 100 {
				prec = 0
			}
			return strings.TrimSuffix(strconv.FormatFloat(v, 'f', prec, 64), ".0") + u.s
		}
	}
	return strconv.FormatInt(n, 10)
}

// DiskReserve of /system/settings (0: none).
func DiskReserve(s *config.Store) int64 {
	n, _ := ParseSize(single(s, "/system/settings").Val("disk-reserve"))
	return n
}
