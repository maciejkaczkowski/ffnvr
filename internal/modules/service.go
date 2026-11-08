package modules

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"ffnvr/internal/config"
	"ffnvr/internal/module"
	"ffnvr/internal/units"
)

// serviceModule: /ip/service - the services ffnvr offers, as in RouterOS: fixed items
// that are only set.
//
//	/ip/service
//	set [ find name=www ] enabled=yes port=80 address=192.168.0.0/16
//
// www is the web panel, a service of its own (ffnvr-www.service): it does not share a
// process with the recording or the worker, so a crash or an attack on it touches
// neither, and it can be switched off on recorders that do not need it.
type serviceModule struct{}

func (serviceModule) Name() string { return "service" }

func (serviceModule) Menus() []*config.Menu {
	return []*config.Menu{{
		Path:    "/ip/service",
		Kind:    config.Multi,
		NameKey: "name",
		Help:    "services: www (the web panel), alarm-dvrip (XMeye alarm server), alarm-smtp (e-mails of recorders and cameras), alarm-lapi (pushes of Uniview cameras subscribed to, events=subscribe of /camera)",
		Fields: []config.Field{
			{Name: "name", Help: "read-only"},
			{Name: "port", Type: config.TInt},
			{Name: "address", Type: config.TList, Help: "who may connect: addresses or networks, e.g. 192.168.0.0/16,10.1.2.3 (empty: everyone)"},
			{Name: "proxy", Type: config.TList, Help: "reverse proxies in front of the panel (address or network, e.g. 10.0.0.5): from them X-Forwarded-For gives the browser's address (sign in limits, logs)"},
			{Name: "enabled", Type: config.TBool},
			{Name: "comment"},
		},
		Fixed: []map[string]string{
			{"name": "www", "port": "80", "enabled": "no"},
			{"name": "alarm-dvrip", "port": "15002", "enabled": "no"},
			{"name": "alarm-smtp", "port": "2525", "enabled": "no"},
			{"name": "alarm-lapi", "port": "8091", "enabled": "no"},
		},
		Validate: validateService,
	}}
}

func validateService(_ *config.Store, it *config.Item) error {
	if p, err := strconv.Atoi(it.Val("port")); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("port must be 1..65535")
	}
	if _, err := ParseAllow(it.Val("address")); err != nil {
		return err
	}
	if _, err := ParseAllow(it.Val("proxy")); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	if it.Val("proxy") != "" && it.Val("name") != "www" {
		return fmt.Errorf("proxy: only for www")
	}
	return nil
}

// ParseAllow reads an address= list: addresses and networks.
func ParseAllow(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, a := range splitList(v) {
		a = strings.TrimSpace(a)
		if p, err := netip.ParsePrefix(a); err == nil {
			out = append(out, p.Masked())
			continue
		}
		ip, err := netip.ParseAddr(a)
		if err != nil {
			return nil, fmt.Errorf("address %q: expected an address or a network (192.168.0.0/16)", a)
		}
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

// Units: an enabled www runs as ffnvr-www.service; port and addresses go into its
// command line, so changing them restarts it on commit.
func (serviceModule) Units(c *module.Context) ([]units.Unit, error) {
	var out []units.Unit
	if u := alarmUnit(c); u != nil {
		out = append(out, *u)
	}
	www := find(c.Store, "/ip/service", "name", "www")
	if www == nil || www.Val("enabled") != "yes" {
		return out, nil
	}
	cmd := WorkerExe + " web -listen :" + www.Val("port")
	if a := www.Val("address"); a != "" {
		cmd += " -allow " + a
	}
	if p := www.Val("proxy"); p != "" {
		cmd += " -proxy " + p
	}
	return append(out, units.Unit{Name: units.Prefix + "www" + units.Suffix, Content: withBinary(`[Unit]
Description=ffnvr: web panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=`+cmd+`
Restart=always
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/var/lib/ffnvr /etc/ffnvr`+backupPaths(c.Store)+`

[Install]
WantedBy=multi-user.target
`, c)}), nil
}

// alarmUnit: ffnvr-alarm.service, when a port of the alarm server is on or a recorder
// or a camera is subscribed to. Ports and addresses are on its command line (a change restarts it on
// commit); the recorders it reads from the configuration while running.
func alarmUnit(c *module.Context) *units.Unit {
	cmd := WorkerExe + " alarm"
	on := false
	for _, svc := range []struct{ name, flag string }{{"alarm-dvrip", "dvrip"}, {"alarm-smtp", "smtp"}, {"alarm-lapi", "lapi"}} {
		it := find(c.Store, "/ip/service", "name", svc.name)
		if it == nil || it.Val("enabled") != "yes" {
			continue
		}
		on = true
		cmd += " -" + svc.flag + " :" + it.Val("port")
		if a := it.Val("address"); a != "" {
			cmd += " -" + svc.flag + "-allow " + a
		}
	}
	for _, n := range c.Store.Items("/nvr") {
		if n.Val("events") == "subscribe" && n.Val("disabled") != "yes" {
			on = true
		}
	}
	if len(SubscribedCameras(c.Store)) > 0 { // events=subscribe of /camera
		on = true
	}
	if !on {
		return nil
	}
	return &units.Unit{Name: units.Prefix + "alarm" + units.Suffix, Content: withBinary(`[Unit]
Description=ffnvr: alarm server (events of the recorders)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=`+cmd+`
Restart=always
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/var/lib/ffnvr

[Install]
WantedBy=multi-user.target
`, c)}
}

// StatusLine is what /nvr print shows about a recorder ("" for local ones).
func StatusLine(s *config.Store, root, nvr string) string {
	n := find(s, "/nvr", "name", nvr)
	if n == nil || !remote(n) {
		return ""
	}
	return strings.ReplaceAll(describeStatus(readStatus(root, nvr)), "\n       ", "; ")
}

// backupPaths: the panel writes configuration backups into the storages of types=backup.
func backupPaths(s *config.Store) string {
	out := ""
	for _, name := range StoragesOf(s, "backup") {
		if st := find(s, "/storage", "name", name); st != nil {
			out += " -" + st.Val("path") // "-": a missing directory does not stop the service
		}
	}
	return out
}
