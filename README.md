<p align="center">
  <img src="docs/logo.svg" width="64" alt="ffnvr logo">
</p>

# ffnvr

**English** | [Polski](README.pl.md)

**ffnvr** is a network video recorder (NVR) for Linux, configured with a syntax modelled on MikroTik RouterOS. It records IP cameras with `ffmpeg`, runs every camera as its own `systemd` service, and can also manage remote hardware recorders (Dahua, Uniview, Tiandy, XMeye/DVRIP): reading their cameras and state, downloading their recordings, rebooting them, backing them up, setting their clocks and receiving their events.

The whole configuration lives in one plain-text script (`/etc/ffnvr/config.rsc` by default) and is edited in an interactive shell with Tab completion and context help, just like a RouterOS console.

![ffnvr](docs/ffnvr-main.png)

---

## Table of contents

- [Features](#features)
- [How it works](#how-it-works)
- [Requirements](#requirements)
- [Installation](#installation)
- [Getting started](#getting-started)
- [Web panel](#web-panel)
- [CLI reference](#cli-reference)
  - [The `ffnvr` command](#the-ffnvr-command)
  - [The interactive shell](#the-interactive-shell)
  - [Configuration script syntax](#configuration-script-syntax)
  - [Menu reference](#menu-reference)
- [Files and directories](#files-and-directories)
- [Development](#development)

---

## Features

- **Local recording** of IP cameras over RTSP with `ffmpeg -c copy` (no re-encoding), split into 15-minute `.mkv` segments.
- **One systemd service per camera** (`ffnvr-<nvr>-<camera>.service`), with optional sandboxing (`/system/systemd security=basic|high|custom`) and a memory limit.
- **Remote recorders**: Dahua (JSON-RPC or CGI), Uniview, Tiandy and XMeye/DVRIP. Cameras appearing on a recorder can be added automatically.
- **Retention and disk reserve**: old recordings are deleted after a set time or when free space runs short.
- **Recording schedules**: day templates and weekly schedules with `regular`, `alarm` (keep only what surrounds alarms, with pre/post-alarm time) and `none` modes.
- **Event / alarm server**: DVRIP alarm server, SMTP server for recorder e-mails, Uniview LAPI receiver, and subscriptions to Dahua, Hikvision, Tiandy, Uniview and DVRIP devices. Events are kept in SQLite, filtered and acknowledged.
- **Archive jobs**: export recordings (local or from remote recorders) to `.mp4`, optionally split into blocks.
- **Motion bars** for the archive, computed from local recordings.
- **PTZ control** of cameras and of cameras behind recorders.
- **Live view** through [go2rtc](https://github.com/AlexxIT/go2rtc) (optional), with on-demand H.264 transcoding for browsers without H.265.
- **Users, groups and policies** with fine-grained rights for the web panel.
- **Safe changes**: `diff` shows exactly which unit files and `systemctl` calls a change would cause before `commit`/`apply` does it.
- A single static Go binary (`CGO_ENABLED=0`).

## How it works

ffnvr does not run as one big daemon. The configuration is turned into a set of `systemd` units, and `apply` (or `commit` in the shell) writes them and starts, stops or restarts only what changed.

| Unit | When it exists | What it does |
|---|---|---|
| `ffnvr-<nvr>-<camera>.service` | each enabled camera of a `type=local` recorder | `ffmpeg` recording the camera into `<storage>/<nvr>/<camera>/` |
| `ffnvr-worker.service` | always | `ffnvr worker`: archive jobs, retention, recording schedules, polling remote recorders, auto-adding cameras, thumbnails, snapshots |
| `ffnvr-www.service` | `/ip/service www` enabled | `ffnvr web`: the web panel |
| `ffnvr-alarm.service` | an `alarm-*` service enabled or any `events=subscribe` | `ffnvr alarm`: the event server |
| `ffnvr-go2rtc.service` | `/system/live enabled=yes` | go2rtc for live view, listening on localhost only |

The services read the same configuration file, so most changes saved by the shell take effect without restarting them.

![How ffnvr works](docs/how-it-works-en.png)

## Requirements

- Linux with **systemd**.
- **ffmpeg** at `/usr/bin/ffmpeg` (the camera services call it by this path).
- **Go 1.22+** to build.
- Optional: **go2rtc** (default path `/usr/local/bin/go2rtc`) for live view and thumbnails.
- Root privileges to install units and run `apply`.

## Installation

```bash
git clone https://github.com/maciejkaczkowski/ffnvr.git ffnvr
cd ffnvr

go get modernc.org/sqlite@v1.34.3

make build          # builds a static ./ffnvr binary
make test           # optional: go vet + tests
sudo make install   # see below
```

`make install`:

- installs the binary to **`/usr/local/bin/ffnvr`** (the generated units run this path, so keep it there),
- creates `/etc/ffnvr`,
- creates the system account `ffnvr`, used by the camera services when `/system/systemd security=high`.

Install ffmpeg (and optionally go2rtc), for example on Debian/Ubuntu:

```bash
sudo apt install ffmpeg
# optional, for live view:
sudo install -m 0755 go2rtc_linux_amd64 /usr/local/bin/go2rtc
```

To embed a version number in the binary:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=1.0.0" -o ffnvr ./cmd/ffnvr
```

## Getting started

### 1. Create the configuration file and open the shell

```bash
sudo touch /etc/ffnvr/config.rsc
sudo ffnvr shell
```

```
ffnvr shell - Tab completes, ? shows help, Ctrl-D leaves
[ffnvr] />
```

Press `Tab` at any time to complete menus, parameters and values, or type `?` to see what is accepted at that point.

### 2. Add a storage for recordings

```
/storage add name=disk1 path=/srv/disk1 types=archive,backup
```

### 3. Add a local recorder

```
/nvr add name=nvr01 type=local storage=disk1 retention=30d
```

### 4. Add cameras

Directly with an RTSP address:

```
/camera add name=yard nvr=nvr01 stream="rtsp://user:pass@192.168.1.21:554/stream1" alias=Yard
```

Or through a shared profile (login and address scheme for many cameras of the same make):

```
/camera/profile add name=dahua type=dahua username=admin password=secret \
    stream="rtsp://<user>:<password>@<host>/cam/realmonitor?channel=1&subtype=0"
/camera add name=front nvr=nvr01 profile=dahua host=192.168.1.20
```

`print detail` shows the values a camera takes from its profile, including the low-quality stream derived automatically:

```
[ffnvr*] /> /camera print detail
  0  name=front nvr=nvr01 profile=dahua host=192.168.1.20 enabled=yes
       username=admin  (profile dahua)
       password=*****  (profile dahua)
       stream=rtsp://admin:*****@192.168.1.20/cam/realmonitor?channel=1&subtype=0  (profile dahua)
       stream-lq=rtsp://admin:*****@192.168.1.20/cam/realmonitor?channel=1&subtype=1  (made from stream)
       engine=internal  (default)
```

### 5. (Optional) A user and the web panel

```
/user/policy add name=admin allow=*
/user add user=admin policy=admin password=ChangeMe
/ip/service set www enabled=yes port=8080
```

The password is stored only as a SHA-512 crypt hash (`password-hash=`).

### 6. Review and apply

```
diff      # shows the unit files and systemctl calls that commit would make
commit    # writes the units, starts/restarts services, saves the configuration
```

The `*` in the prompt (`[ffnvr*]`) means there are uncommitted changes. Check the result:

```bash
systemctl status 'ffnvr-*'
ls /srv/disk1/nvr01/front/
```

### Adding a remote recorder

```
/nvr add name=nvr02 type=dahua host=192.168.1.2 username=admin password=secret
/nvr scan nvr02     # reads the recorder and adds a camera for every active channel
/nvr probe nvr02    # raw answers of the recorder, for diagnostics
```

Cameras of a remote recorder are named `chNN` (channel number), with an alias taken from the channel title. With `auto-add=yes` (the default) the worker adds new channels by itself.

### Non-interactive use

The same configuration can be edited in a text editor and applied from the command line:

```bash
sudo ffnvr diff               # what would change
sudo ffnvr apply              # do it
ffnvr export                  # print the normalised configuration
```

## Web panel

ffnvr includes a web panel (`ffnvr web`, enabled with `/ip/service set www enabled=yes`). It can be started and used, but **it was built mainly for viewing, not for configuration**: live view of the cameras, browsing and playing back the archive, the event log and an overview of recorders, disks and jobs. Access is controlled by `/user`, `/user/group` and `/user/policy`.

The configuration of ffnvr is done in the CLI described below. The panel is available in English, Polish and German.

---

## CLI reference

### The `ffnvr` command

```
ffnvr <command> [flags]     (flags may also come before the command)
```

Without a command ffnvr only prints help: `apply` has to be asked for explicitly.

#### Commands

| Command | Description |
|---|---|
| `shell` | Interactive shell (see [below](#the-interactive-shell)). |
| `apply` | Write the systemd units of the configuration and start/stop/restart them. |
| `diff` | Print what `apply` would change; modify nothing. |
| `export` | Print the normalised configuration. |
| `worker` | Run the archive jobs and periodic work, check remote recorders (`ffnvr-worker.service`). |
| `alarm` | The alarm server: events of recorders and cameras (`ffnvr-alarm.service`). |
| `web` | The web panel (`ffnvr-www.service`). |
| `help` | Print the help text. |

`worker`, `alarm` and `web` are normally started by the generated systemd units, not by hand.

#### Flags

| Flag | Default | Applies to | Description |
|---|---|---|---|
| `-c <path>` | `/etc/ffnvr/config.rsc` | all | Configuration file, or a directory of `*.rsc` files. Repeatable. |
| `-root <dir>` | `/` | all | Operate on this filesystem root. `systemctl` is not run unless it is `/` (useful for testing). |
| `-no-services` | `false` | apply, shell | Write the unit files but do not run `systemctl`. |
| `-history <file>` | `~/.ffnvr_history` | shell | Command history file (empty: in memory only). |
| `-ffmpeg <bin>` | `ffmpeg` | worker | ffmpeg binary used by the worker. |
| `-status-every <dur>` | `1m` | worker | How often the worker looks for due work. |
| `-session-keep <dur>` | `24h` | worker | Web panel sign-ins unused this long are removed. |
| `-listen <addr>` | `:80` | web | Address to listen on. |
| `-allow <list>` | *(all)* | web | Addresses/networks that may connect, e.g. `192.168.0.0/16,10.0.0.5`. |
| `-proxy <list>` | | web | Reverse proxies whose `X-Forwarded-For` is trusted. |
| `-dvrip <addr>` | *(off)* | alarm | Address of the DVRIP alarm server, e.g. `:15002`. |
| `-dvrip-allow <list>` | *(all)* | alarm | Who may push DVRIP alarms. |
| `-smtp <addr>` | *(off)* | alarm | Address of the SMTP server, e.g. `:2525`. |
| `-smtp-allow <list>` | *(all)* | alarm | Who may send e-mails. |
| `-lapi <addr>` | *(off)* | alarm | Address of the HTTP server Uniview devices push events to (LAPI), e.g. `:8091`. |
| `-lapi-allow <list>` | *(all)* | alarm | Who may push LAPI events. |

Examples:

```bash
ffnvr shell
ffnvr diff   -c /etc/ffnvr/config.rsc
ffnvr apply  -c /etc/ffnvr/config.rsc
ffnvr export -c /etc/ffnvr/conf.d/          # a directory of *.rsc files
ffnvr diff   -root /tmp/test -c ./test.rsc  # try a configuration without touching the system
```

The `shell` and `web` commands can save changes back to the configuration only when exactly one file is given (not a directory).

### The interactive shell

`ffnvr shell` opens a RouterOS-like console. Changes are kept in memory until `commit`.

On a terminal it offers line editing, persistent history, `Tab` completion (menus, parameters, values, item names) and `?` context help. When input comes from a pipe or a file it reads plain lines, so it can be scripted:

```bash
printf '/camera print\n' | ffnvr shell
```

#### Navigation

| Input | Effect |
|---|---|
| `/camera` | Go to the menu `/camera`. |
| `/camera profile` or `/camera/profile` | Go to a submenu. |
| `..` | One level up. |
| `/` | Back to the root. |
| `/camera print` | Run a command in another menu without going there. |

The prompt shows the current menu: `[ffnvr] /camera>`.

#### Shell commands

| Command | Description |
|---|---|
| `diff` | Show what `commit` would change (systemd units, systemctl calls). |
| `commit` | Write the units, start/stop/restart services, save the configuration. |
| `save [file]` | Write the configuration as a script (default: the loaded file). |
| `load <file>` | Apply a script or directory on top of the current state. |
| `history` | Show the command history. |
| `help` | Short help. |
| `quit`, `exit`, `Ctrl-D` | Leave the shell. |
| `<text>?` | Context help for what has been typed so far. |

Lines containing passwords are never stored in the history.

#### Keys

| Key | Action |
|---|---|
| `Tab` | Complete menus, parameters, values and item names. |
| `?` | Help for the current position. |
| `Up` / `Down` | Browse the history. |
| `Ctrl-R` | Search the history. |
| `Ctrl-A` / `Ctrl-E` | Start / end of line. |
| `Ctrl-K` / `Ctrl-U` | Delete to end / start of line. |
| `Ctrl-W` | Delete the previous word. |
| `Ctrl-Y` | Paste the deleted text. |
| `Alt-B` / `Alt-F` | Word back / forward. |

#### Menu commands (verbs)

| Verb | Description |
|---|---|
| `print` | Show the items. `print detail` also shows inherited and live values (state of cameras, recorders...). |
| `add k=v ...` | Create an item (list menus). |
| `set <item> k=v ...` | Change items. In single menus (e.g. `/system/settings`): `set k=v`. |
| `unset <item> k ...` | Remove values (back to default). |
| `remove <item>` | Delete items. Items still referenced elsewhere cannot be removed. |
| `enable <item>` / `disable <item>` | Shortcut for `disabled=no/yes` or `enabled=yes/no`. |
| `export` | Print the configuration of this menu, or of everything below the current level. |

Menu-specific verbs (`scan`, `probe`, `rights`, `status`, `cancel`, `ack`, `reboot`, `backup`, `upgrade`, `datetime`) are described in the [menu reference](#menu-reference).

#### Selecting items

| Selector | Example |
|---|---|
| Number (as shown by `print`) | `set 0 enabled=no` |
| Several numbers | `/event/log ack 12,13` |
| Name | `set front enabled=no` |
| Alias | `set Yard enabled=no` |
| `[find k=v ...]` | `remove [find comment="old"]`, `set [find name=front nvr=nvr01] ptz=yes` |

Camera names are unique per recorder only. When the same name exists on several recorders, use the number or `[find name=... nvr=...]`.

### Configuration script syntax

The configuration file is a script in the same syntax as the shell, and `export` / `save` produce it:

```
# comment
/storage
add name=disk1 path=/srv/disk1 types=archive,backup

/camera
add name=front nvr=nvr01 profile=dahua host=192.168.1.20 \
    comment="front door"

/ip/service
set [ find name=www ] port=8080 enabled=yes
```

- A line with only a menu path changes the current menu; a path may also precede a command on the same line (`/system/live set enabled=yes`).
- `#` starts a comment, `\` at the end of a line continues it.
- Values containing spaces or any of `" \ $ [ ] ( ) ; # ?` must be quoted; inside quotes `\"`, `\\`, `\$` and `\n` are escapes.
- Booleans: `yes`/`no` (also `true`/`false`). Lists: comma-separated `a,b,c`.
- Durations: `10s`, `5m`, `1h`, `30d`. Sizes: `200M`, `2G`.
- A directory given with `-c` is loaded as all its `*.rsc` files.

### Menu reference

Legend: **req** = required, **secret** = masked by `print`, **ref** = must name an existing item of another menu.

#### `/storage`

Places where recordings and archives are kept. List menu, item name: `name`.

| Parameter | Type | Description |
|---|---|---|
| `name` | string, req | Name used by `/nvr storage=...`. |
| `path` | string, req, unique | Absolute directory, e.g. `/srv/disk1`. |
| `types` | list | What else it is for: `archive` (the Archive page), `backup` (configuration backups). |
| `disabled` | yes\|no | |
| `comment` | string | |

#### `/user/group`

User groups (e.g. one per company). List menu, item name: `group`.

| Parameter | Type | Description |
|---|---|---|
| `group` | string, req | Group name, referenced as `@group`. |
| `comment` | string | |

#### `/user/policy`

What users may do in the web panel. List menu, item name: `name`.

| Parameter | Type | Description |
|---|---|---|
| `name` | string, req | |
| `allow` | list of rights | Granted rights: a right, `branch.*` or `*`. |
| `deny` | list of rights | Exceptions to `allow` (the more specific entry wins). |
| `comment` | string | |

Extra verb: `rights <policy>` shows the rights the policy grants.

Rights:

| Right | Meaning |
|---|---|
| `live` | Live module: watching the cameras. |
| `live.views` | Saving and removing own views (tile layouts). |
| `live.map`, `live.map.edit` | The map of the cameras; placing and turning cameras on it. |
| `live.thumbs` | Thumbnails of all cameras. |
| `live.ptz`, `live.ptz.edit` | PTZ control; saving a camera's PTZ direction swap. |
| `archive` | Archive module: searching recordings, thumbnails, motion. |
| `archive.export` | Copying recordings into archive storages (jobs). |
| `archive.play` | Playing local recordings in the panel. |
| `archive.download` | Downloading recorded files and finished jobs. |
| `archive.jobs` | Cancelling and retrying jobs. |
| `system` | System module: overview, recorders, cameras, disks, jobs, alerts. |
| `system.logs` | Logs of the camera services. |
| `system.record` | Recording schedules of local recorders. |
| `system.nvr.reboot` / `.backup` / `.firmware` / `.clock` | Reboot, configuration backup, firmware update, clock of remote recorders. |
| `system.events`, `system.events.ack` | The event log; acknowledging events. |
| `system.events.system` | The system log (sign-ins, configuration changes, deletions). |

A sub-right has no effect without its parent: `archive.play` needs `archive` too (or use `archive.*`). Wildcards: `live.*`, `live.map.*`, `live.ptz.*`, `archive.*`, `system.*`, `system.nvr.*`, `system.events.*`, `*`.

```
/user/policy add name=viewer allow=live.*,archive,archive.play
/user/policy add name=operator allow=* deny=system.nvr.firmware
```

#### `/user`

Users of the web panel. List menu, item name: `user`.

| Parameter | Type | Description |
|---|---|---|
| `user` | string, req | Login. |
| `groups` | list, ref `/user/group` | Groups of the user. |
| `policy` | ref `/user/policy` | Rights in the web panel; none: no rights. |
| `password` | string, secret | Stored only as `password-hash`. |
| `password-hash` | string, secret | crypt hash: `$6$...` (SHA-512) or `$5$...` (SHA-256). |
| `disabled` | yes\|no | |
| `comment` | string | |

Extra verb: `rights <user>` shows what the user may do in the web panel.

#### `/nvr`

Recorders. List menu, item name: `name`.

| Parameter | Type | Description |
|---|---|---|
| `type` | `local\|dahua\|uniview\|tiandy\|dvrip`, req | `local`: ffnvr records itself to a `/storage`; others: a hardware recorder read over its API. |
| `name` | string, req | |
| `storage` | ref `/storage` | `type=local`: where it records. |
| `retention` | duration | `type=local`: how long recordings are kept, e.g. `30d` (empty: forever). |
| `record-schedule` | ref `/record/schedule` | `type=local`: schedule of all its cameras (the camera's own one wins; empty: recorded all the time). |
| `host` | string | Remote: address, e.g. `192.168.1.2`, `192.168.1.2:8080`, `https://nvr.lan` (Tiandy port 3001, DVRIP 34567). |
| `username` | string | Remote. |
| `password` | string, secret | Remote. |
| `auto-add` | yes\|no | Remote: new cameras on the recorder are added by the worker (default `yes`). |
| `http-port` | integer | `type=tiandy`: port of the web interface (default 80). |
| `api` | `rpc\|cgi` | `type=dahua`: JSON-RPC (default) or CGI (older firmware). |
| `speed` | integer | `type=uniview`: download/playback speed multiplier (RTSP Speed; player default 8, `1` = real time). |
| `fps` | integer | `type=tiandy`: frame rate of downloads (default: from the stream, else 20). |
| `events` | `no\|subscribe` | `subscribe`: the alarm server connects to the recorder for its events (Uniview: pushes to `alarm-lapi`, which has to be enabled). Default `no`. |
| `event-filter` | ref `/event/filter` | Which kinds of its events are kept (unset: `default-filter` of `/event`). |
| `event-id` | string | How the recorder identifies itself to the alarm server when its address does not (NAT): serial number (DVRIP) or SMTP login / sender. |
| `valid-users` | list, ref `/user`, `@/user/group` | Who may use it in the web panel, e.g. `maciej,@company2`. |
| `disabled` | yes\|no | |
| `comment` | string | |

Extra verbs:

- `scan <nvr>` – read cameras and state of a remote recorder, add the missing cameras.
- `probe <nvr>` – show the raw answers of a remote recorder (diagnostics).

`print` adds the live state of each recorder (e.g. `UNREACHABLE: ...`).

#### `/camera/profile`

Shared settings of cameras of local recorders (login, address scheme). A camera's own values win. List menu, item name: `name`.

| Parameter | Type | Description |
|---|---|---|
| `name` | string, req | |
| `username` | string | Login of the cameras. |
| `password` | string, secret | Password of the cameras. |
| `stream` | string | Address scheme, e.g. `rtsp://<host>/<ch>/1`. Placeholders: `<host>` `<ch>` `<ch0>` `<user>` `<password>`. |
| `stream-lq` | string | Scheme of the low-quality stream (empty: derived from `stream`; `none`: no LQ). |
| `engine` | `internal\|go2rtc` | As on `/camera`. |
| `type` | `dahua\|dvrip\|hikvision\|tiandy\|uniview` | Make of the cameras. |
| `ptz` | yes\|no | PTZ control (default `no`). |
| `comment` | string | |

#### `/camera`

Cameras. List menu, item name: `name` (unique per recorder), alias: `alias`.

| Parameter | Type | Description |
|---|---|---|
| `name` | string, req | Unique per nvr; recordings and the service use it. |
| `alias` | string | Optional friendly name, unique per nvr (also among names). |
| `nvr` | ref `/nvr`, req | |
| `stream` | string | `rtsp://host:port/path` (required on a `type=local` nvr unless given by a profile). With `engine=go2rtc`: any go2rtc source, e.g. `dvrip://user:pass@host?channel=0&subtype=0`. |
| `stream-lq` | string | Low-quality (sub) stream for live view. Empty: derived from `stream` for Uniview, Tiandy, Dahua, Hikvision, XMeye and `dvrip://` addresses; `none`: no LQ. |
| `engine` | `internal\|go2rtc` | `internal` (ffmpeg connects, RTSP only; default) or `go2rtc` (ffmpeg records go2rtc's local copy, live view shares the connection). |
| `channel` | integer | Channel on a remote recorder (required there); local: `<ch>` of the scheme. |
| `profile` | ref `/camera/profile` | Local: shared settings. |
| `host` | string | Local: `<host>` of the scheme, e.g. `192.168.1.2` or `192.168.1.2:8554`. |
| `username` | string | Local: overrides the profile's. |
| `password` | string, secret | Local: overrides the profile's (`""` overrides with none). |
| `group` | string | Free text, shown in the service description. |
| `enabled` | yes\|no | Default `yes`. |
| `comment` | string | |
| `type` | `dahua\|dvrip\|hikvision\|tiandy\|uniview` | Local: make / protocol of the camera. A camera of a remote recorder takes the recorder's type. |
| `ptz` | yes\|no | PTZ control (local: via `type`, default `no`; remote: through the recorder, default `yes` when it can). |
| `ptz-host` | string | Local: address (and port) for PTZ and `events=subscribe` (default: `host`, else the stream host). |
| `ptz-channel` | integer | Local: PTZ channel (default: `channel`, else 1). |
| `ptz-swap-horizontal` | yes\|no | Left and right swapped (camera mounted upside down or mirrored). |
| `ptz-swap-vertical` | yes\|no | Up and down swapped. |
| `events` | `no\|subscribe` | Local: the alarm server connects to the camera for its events. Tiandy: own protocol, port 3001; Dahua: eventManager over HTTP; Hikvision: ISAPI alertStream (port 443 = HTTPS); Uniview: LAPI push to `alarm-lapi`. Default `no`. |
| `record-schedule` | ref `/record/schedule` | Local: the camera's schedule (over the recorder's). |
| `event-filter` | ref `/event/filter` | Local: which kinds of its events are kept, e.g. `*/ALARM/*` (Tiandy), `VideoMotion,VideoBlind` (Dahua), `VMD,tamperdetection` (Hikvision), `MotionAlarm,MaskImageAlarm` (Uniview). |

`print` adds the live state of each camera; `print detail` shows values inherited from the profile and derived values.

#### `/camera/position`

Where cameras stand on the map of the web panel. List menu (also written by the panel when a user with `live.map.edit` places a camera).

| Parameter | Type | Description |
|---|---|---|
| `nvr` | string, req | Recorder. |
| `camera` | string, req | Camera. |
| `lat` | string, req | Latitude. |
| `long` | string, req | Longitude. |
| `direction` | string | Where it looks, degrees from north (0..359). |
| `angle` | string | Field of view in degrees (default 90). |
| `range` | string | How far it sees, in metres (default 30). |

#### `/record/day`

Recording schedule: what one day looks like. List menu, item name: `name`.

| Parameter | Type | Description |
|---|---|---|
| `name` | string, req | |
| `spans` | list | Spans and their mode, e.g. `"08:00-16:00=regular,16:00-22:00=alarm"`; `24:00` ends the day; spans may not overlap. |
| `default` | `regular\|alarm\|none` | Mode of the time no span covers (default `none`). |
| `comment` | string | |

Modes:

- `regular` – everything is kept.
- `alarm` – only what lies around the camera's alarms (pre-alarm before, post-alarm after). Works for cameras that send their alarms: `type=dvrip` (via `alarm-dvrip`), and `type=tiandy`, `dahua`, `hikvision`, `uniview` with `events=subscribe`.
- `none` – nothing is kept.

Cameras always record continuously; the worker afterwards keeps, cuts (without re-encoding) or deletes each finished 15-minute segment according to the schedule.

#### `/record/schedule`

Weekly schedule, assigned with `record-schedule=` of `/nvr` (all its cameras) or `/camera` (wins). List menu, item name: `name`.

| Parameter | Type | Description |
|---|---|---|
| `name` | string, req | |
| `mon` … `sun` | ref `/record/day` | Template of the day; unset: recorded all the time. |
| `week` | string | Shorthand for `mon=`…`sun=`: `"mon-fri:office,sat-sun:weekend"` (stored as single days). |
| `pre-alarm` | duration | Alarm mode: kept before an alarm, 0s–5m (default `10s`). |
| `post-alarm` | duration | Alarm mode: kept after an alarm, 0s–10m (default `60s`). |
| `alarm-filters` | list, ref `/event/filter` | Besides motion detection, the kinds of these filters (`action=accept`) count as alarms. |
| `alarm-silence` | duration | Cameras pushing alarms (DVRIP): no event at all for this long means alarms may not arrive – the whole time is kept and an alert raised (default `1d`; `0`: never). |
| `comment` | string | |

Extra verb: `status <schedule>` – where the schedule is used, whether the alarms of its cameras arrive, what the worker did.

```
/record/day
add name=office default=none spans="08:00-16:00=regular"
add name=night default=alarm spans="06:00-22:00=regular"
/record/schedule
add name=week week="mon-fri:office,sat-sun:night" pre-alarm=10s post-alarm=60s
/nvr
set nvr01 record-schedule=week
```

#### `/event`

Settings of the event store (single menu: `set k=v`).

| Parameter | Type | Description |
|---|---|---|
| `retention` | duration | How long events are kept, e.g. `90d` (default `30d`, at least `1h`). |
| `gap` | duration | Notices of one camera and kind closer than this are one event (default `5s`; `0s`: never joined). |
| `default-filter` | ref `/event/filter` | Filter of recorders without `event-filter=` and of unknown senders. Unset: `NewFile`, `TimeChange`, `NTPAdjustTime`, `RtspSessionDisconnect`, `SnapManual` are dropped. |

#### `/event/filter`

Which kinds of events are kept. List menu, item name: `name`.

| Parameter | Type | Description |
|---|---|---|
| `name` | string, req | |
| `action` | `accept\|drop`, req | `accept`: only the listed kinds are kept; `drop`: the listed kinds are discarded, all others kept. |
| `kinds` | list | Event kinds as recorders send them, e.g. `VideoMotion,VideoLoss`. `*` matches any text, `/` may be written for `\` (e.g. `*/ALARM/*` for Tiandy alarms). Matching is case-insensitive. |
| `comment` | string | |

A filter replaces the built-in list, it does not add to it.

```
/event/filter
add name=dahua-nvr action=drop kinds=VideoMotion,RtspSessionDisconnect
add name=tiandy-alarms action=accept kinds=*/ALARM/*
/event
set retention=90d gap=5s default-filter=dahua-nvr
```

#### `/event/log` (action menu)

The events received and the system log. Not configuration – nothing is saved.

| Verb | Description |
|---|---|
| `print [filters]` | List events (newest first). |
| `ack <numbers>` or `ack [filters]` | Acknowledge events. |

| Parameter | Type | Description |
|---|---|---|
| `nvr` | string | The recorder. |
| `camera` | string | The camera (its name). |
| `kind` | string | e.g. `VideoMotion`, `VideoLoss`, `Mail`, `VMD`, `MotionAlarm`; system log: `Login`, `LoginFailed`, `ConfigSaved`, `RecordingsDeleted`, `NvrOffline`... `*` and `/` as in filters. |
| `origin` | `recorders\|system` | Events from devices, or the system log. |
| `user` | string | System log: who did it. |
| `text` | string | Contained in the details or the sender's address. |
| `from` / `to` | `"YYYY-MM-DD HH:MM[:SS]"` | Time range. |
| `unacked` | yes\|no | `print`: only unacknowledged events. |
| `count` | integer | `print`: at most this many, newest (default 50). |
| `raw` | yes\|no | `print`: show the first raw notice under each event. |

```
/event/log print nvr=nvr02 kind=VideoMotion
/event/log print origin=system count=20
/event/log ack 12,13
/event/log ack nvr=nvr02
```

#### `/nvr/download` (action menu)

Archive recordings to `.mp4` files as background jobs run by the worker.

| Verb | Description |
|---|---|
| `add ...` | Create a job. |
| `print` | Show the jobs. |
| `cancel <job>` | Stop a job. |
| `remove <job>` | Delete a job. |

| Parameter | Type | Description |
|---|---|---|
| `camera` | string, req | Camera name or alias. |
| `nvr` | string | Needed when the camera name exists on more than one nvr. |
| `storage` | ref `/storage`, req | Where the archive goes. |
| `path` | string | Subdirectory of the storage, e.g. `case-12/door`. |
| `layout` | `nvr-camera\|flat` | `nvr-camera` (default): files in `<path>/<nvr>/<camera>/`; `flat`: all in `<path>/`. |
| `file` | string | File name (default `<nvr>_<camera>_<start>_<end>.mp4`; with `block`: the prefix). |
| `block` | duration | Split into files of this length: `15m`, `1h`, `1h30m`, `1d` (default: one file). |
| `block-align` | `clock\|start` | `clock` (default): 11:50–12:00, 12:00–13:00…; `start`: 11:50–12:50, 12:50–13:50… |
| `start` | `"YYYY-MM-DD HH:MM[:SS]"`, req | Local time. |
| `end` | `"YYYY-MM-DD HH:MM[:SS]"`, req | Local time. |
| `comment` | string | |

```
/nvr/download add camera=front storage=disk1 path=case-12 \
    start="2026-11-07 08:00" end="2026-11-07 12:00" block=1h
/nvr/download print
```

Jobs are run by the worker (`ffnvr-worker.service`), so the configuration must have been applied with `commit`/`apply` first.

#### `/nvr/tools` (action menu)

Actions on remote recorders.

| Verb | Description |
|---|---|
| `reboot nvr=<nvr>` | Reboot the recorder. |
| `backup nvr=<nvr> storage=<storage> [directory=...]` | Save the recorder's configuration backup (default directory `backup`). |
| `upgrade nvr=<nvr> file=<path>` | Firmware upgrade from a `.bin` file on this server (Dahua). |
| `datetime nvr=<nvr> [...]` | Show or set the clock and NTP. |

| Parameter | Type | Description |
|---|---|---|
| `nvr` | ref `/nvr` | The recorder. |
| `storage` | ref `/storage` | backup: where the file goes. |
| `directory` | string | backup: subdirectory of the storage, e.g. `backup/nvr02`. |
| `file` | string | upgrade: the firmware file. |
| `time` | string | datetime: `now` (this server's time) or `YYYY-MM-DD HH:MM:SS`. |
| `ntp` | yes\|no | datetime: synchronise with NTP. |
| `ntp-server` | string | datetime: NTP server address. |
| `ntp-port` | string | datetime: NTP server port (default: unchanged). |
| `ntp-interval` | string | datetime: minutes between synchronisations (default: unchanged). |

```
/nvr/tools/reboot nvr=nvr02
/nvr/tools/backup nvr=nvr02 storage=disk1 directory=backup/nvr02
/nvr/tools/upgrade nvr=nvr02 file=/tmp/DH_NVR_V4.005.bin
/nvr/tools/datetime nvr=nvr02
/nvr/tools/datetime nvr=nvr02 time=now
/nvr/tools/datetime nvr=nvr02 ntp=yes ntp-server=pool.ntp.org ntp-interval=60
```

#### `/ip/service`

Network services. Fixed items: they cannot be added or removed, only `set`; `unset` restores the default.

| Item | Default port | Description |
|---|---|---|
| `www` | 80 | The web panel. |
| `alarm-dvrip` | 15002 | XMeye/DVRIP alarm server. |
| `alarm-smtp` | 2525 | SMTP server for e-mails of recorders and cameras. |
| `alarm-lapi` | 8091 | Receiver of Uniview LAPI pushes (`events=subscribe` of Uniview cameras). |

| Parameter | Type | Description |
|---|---|---|
| `name` | string | Read-only. |
| `port` | integer | |
| `address` | list | Who may connect: addresses or networks, e.g. `192.168.0.0/16,10.1.2.3` (empty: everyone). |
| `proxy` | list | Reverse proxies in front of the panel whose `X-Forwarded-For` is trusted. |
| `enabled` | yes\|no | All disabled by default. |
| `comment` | string | |

```
/ip/service set www enabled=yes port=8080 address=192.168.0.0/16
/ip/service set alarm-dvrip enabled=yes
```

#### `/system/settings`

| Parameter | Type | Description |
|---|---|---|
| `poll-interval` | duration | How often remote recorders are read (firmware, disks, cameras); default `5m`, at least `1m`. |
| `disk-reserve` | size | Free space kept on every storage of local recorders, e.g. `2G`: the oldest recordings go when it is short, whatever the retention (default: none). |

#### `/system/systemd`

Confinement of the recording services (ffmpeg of the cameras). `print detail` shows what the units get.

| Parameter | Type | Description |
|---|---|---|
| `security` | `none\|basic\|high\|custom` | `none` (default: root, no sandbox); `basic` (root in a sandbox: system read-only, only the camera's directory writable); `high` (basic + own account, no devices, kernel and syscalls locked); `custom` (basic changed by the fields below). |
| `memory-max` | size | Memory limit of one recording, e.g. `200M`, `1G`, `unlimited` (default `200M`). |
| `extra` | list | More `[Service]` directives, e.g. `"CPUQuota=50%,IOWeight=50"` (resources and sandboxing only). |
| `user` | string | `high`: the account (default `ffnvr`); `custom`: default `root`. |
| `no-new-privileges` | yes\|no | custom: `NoNewPrivileges` (default yes). |
| `protect-system` | `no\|yes\|full\|strict` | custom: `ProtectSystem` (default strict). |
| `protect-home` | `no\|yes\|read-only\|tmpfs` | custom: `ProtectHome` (default yes). |
| `private-tmp` | yes\|no | custom: `PrivateTmp` (default yes). |
| `private-devices` | yes\|no | custom: `PrivateDevices` (default no). |
| `protect-kernel` | yes\|no | custom: kernel tunables/modules/logs, cgroups, clock, hostname (default no). |
| `restrict` | yes\|no | custom: namespaces, realtime, SUID/SGID, personality, address families (default no). |
| `system-call-filter` | yes\|no | custom: `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute` (default no). |
| `drop-capabilities` | yes\|no | custom: empty `CapabilityBoundingSet` (default no). |

For `security=high` the account must exist (`make install` creates it):

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin ffnvr
```

#### `/system/motion-detector`

How the motion bars of the archive are made from local recordings.

| Parameter | Type | Description |
|---|---|---|
| `mode` | `auto\|heuristic\|manual` | `auto`: packets, or keyframes when packets do not follow motion; `heuristic`: the same choice among `methods`; `manual`: the one of `methods` (default `auto`). |
| `methods` | list of `packets\|keyframes\|pixels` | Methods for `heuristic` / `manual`. |
| `gap` | duration | A pause that ends a range (default `3s`). |
| `min-duration` | duration | Shortest event (default `1s`). |
| `padding` | duration | Added before and after every range (default `2s`). |
| `skip` | duration | Start of every segment not analysed (default `4s`; `0s`: none). |
| `window` | duration | packets: moving median window, each side (default `60s`). |
| `ratio-min` | number | packets: least signal (× background) that counts (default 1.12). |
| `noise-k` | number | packets: threshold = 1 + noise-k × noise (default 4). |
| `noise-k-low` | number | packets: lower hysteresis threshold (default 2). |
| `noise-max` | number | packets: above this noise packet sizes are not trusted (default 0.30). |
| `cbr-flat` | number | packets: noise ratio above which the stream is considered CBR (default 6). |
| `max-verify` | integer | packets: how many ranges are checked in the picture at most (default 30). |
| `pixel-diff` | 0–255 | pixels, keyframes: brightness change of a pixel that is motion (default 20). |
| `pixel-min-percent` | % | pixels, keyframes: share of the picture changed that is motion (default 0.10). |
| `pixel-fps` | number | pixels: frames per second analysed (default 4). |
| `pixel-width` | px | pixels, keyframes: width the picture is scaled to (default 320). |
| `osd-height` | px | pixels, keyframes: top band ignored (the clock), after scaling (default 24). |
| `global-percent` | % | pixels, keyframes: more of the picture changing at once is a light/IR switch, not motion (default 50). |
| `keyframe-max-gop` | duration | Longest GOP for the keyframes method (default `5s`). |

#### `/system/live`

Live view through go2rtc on localhost, proxied by the web panel.

| Parameter | Type | Description |
|---|---|---|
| `enabled` | yes\|no | Run go2rtc (default `no`). |
| `go2rtc` | string | go2rtc binary (default `/usr/local/bin/go2rtc`). |
| `port` | integer | API port on 127.0.0.1 (default 1984). |
| `rtsp-port` | integer | RTSP port on 127.0.0.1, for cameras with `engine=go2rtc` (default 8554). |
| `map-tiles` | string | Map tiles URL (default `https://tile.openstreetmap.org/{z}/{x}/{y}.png`). |
| `transcode` | yes\|no | H.264 for browsers without H.265, transcoded on demand (default `no`). |
| `transcode-users` | list, ref `/user`, `@/user/group` | Who may use transcoding; empty: nobody. |
| `transcode-height` | integer | Height of transcoded HQ, 144..2160 (default 720). |
| `transcode-max` | integer | Max. transcoded HQ streams at once, 0..32 (default 2). |
| `transcode-lq-height` | integer | Height of transcoded LQ, 144..2160, or 0: own size (default). |
| `transcode-lq-max` | integer | Max. transcoded LQ streams at once, 0..32 (default 4). |
| `transcode-hardware` | `none\|auto\|vaapi\|cuda\|v4l2m2m\|rkmpp` | Encoder: `none` (libx264, default), `auto`, VAAPI (Intel/AMD), CUDA (NVIDIA), V4L2 M2M, Rockchip MPP. |

#### `/system/snapshot`

Thumbnails of all cameras for the live view, taken by the worker through go2rtc (needs `/system/live enabled=yes`).

| Parameter | Type | Description |
|---|---|---|
| `enabled` | yes\|no | Take thumbnails (default `no`). |
| `interval` | `daily\|weekly` | Default `daily`. |
| `time` | `HH:MM` | Local time (default `03:00`). |
| `day` | `monday`…`sunday` | `interval=weekly`: the day (default `monday`). |
| `quality` | `lq\|hq` | Stream to take them from (default `lq`). |
| `attempts` | 1–10 | Attempts per camera (default 3). |

#### `/system/www`

Look of the web panel.

| Parameter | Type | Description |
|---|---|---|
| `name` | string | Name shown instead of "ffnvr". |
| `tagline` | string | Line under the name on the sign-in page. |
| `login-name` | yes\|no | Show the name on the sign-in page (default yes). |
| `login-logo` | yes\|no | Show the logo on the sign-in page (default yes). |

---

## Files and directories

| Path | Contents |
|---|---|
| `/usr/local/bin/ffnvr` | The binary (the units call it by this path). |
| `/etc/ffnvr/config.rsc` | Default configuration. |
| `/etc/ffnvr/go2rtc.yaml` | go2rtc configuration generated by ffnvr. |
| `/etc/systemd/system/ffnvr-*.service` | Generated units. |
| `/var/lib/ffnvr/jobs` | Archive jobs. |
| `/var/lib/ffnvr/events` | Event database (SQLite) and pictures. |
| `/var/lib/ffnvr/status` | Last known state of remote recorders. |
| `/var/lib/ffnvr/profile` | Web panel user profiles and sessions. |
| `<storage>/<nvr>/<camera>/` | Recordings: `YYYY-MM-DDTHH-MM-SS.mkv`, 15-minute segments. |
| `<storage>/<nvr>/.<camera>/` | Technical data of a camera: index, summary, schedule state. |
| `~/.ffnvr_history` | Shell history. |

## Development

```bash
make test     # go vet ./... && go test ./...
make build
```

Use `-root` and `-no-services` to try configurations against a scratch directory without touching the system:

```bash
mkdir -p /tmp/ffnvr-test
ffnvr diff  -root /tmp/ffnvr-test -c ./example.rsc
ffnvr apply -root /tmp/ffnvr-test -c ./example.rsc -no-services
```
