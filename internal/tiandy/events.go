package tiandy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Events of the native protocol. A logged-in control connection (of a camera or a
// recorder) gets the device's news by itself, no subscription command is needed: a
// captured session of a TC camera (192.168.0.53) carried, besides keep-alives,
//
//	192.168.0.53	PROXY	INNER	ALARM	MOTIONDETECT	0	1
//	192.168.0.53	PROXY	INNER	PARAGET	LOCALSTORE	MAPDEVICE	0	/nfs			0
//	INNER	PARAGET	DISK	INFO	0	0	0	0	0	0
//
// Every record is a branch of a tree, IP\SYSTEM\DESCRIPTION: the address of the device,
// the system (IP, PROXY) and the description - words in capitals (INNER\ALARM\
// MOTIONDETECT), then the values (channel 0, state 1). The records after the first one
// of a frame leave out the address and the system: they are the first one's.
//
// A motion alarm is repeated every quarter of a second or so while the motion lasts
// and no record says that it ended (the device's own log says "alarm disappeared"): an
// alarm not repeated for AlarmHold has ended. A record with state 0 ends it at once.

// Message is one record of the protocol as a branch of its tree.
type Message struct {
	IP     string   // the address of the device, as it writes it
	System string   // IP, PROXY ("" when not told)
	Path   []string // the description: INNER, ALARM, MOTIONDETECT
	Args   []string // the values after it
}

// Kind is the description, the words joined by backslashes: INNER\ALARM\MOTIONDETECT.
func (r Message) Kind() string { return strings.Join(r.Path, `\`) }

// Tree is the whole branch without the values: IP\SYSTEM\DESCRIPTION.
func (r Message) Tree() string {
	parts := []string{r.IP}
	if r.System != "" {
		parts = append(parts, r.System)
	}
	return strings.Join(append(parts, r.Path...), `\`)
}

// String is the record with the tabs as backslashes, values included:
// 192.168.0.53\PROXY\INNER\ALARM\MOTIONDETECT\0\1.
func (r Message) String() string {
	if len(r.Args) == 0 {
		return r.Tree()
	}
	return r.Tree() + `\` + strings.Join(r.Args, `\`)
}

// Param: a parameter readout (PARAGET), the device's state rather than news.
func (r Message) Param() bool { return r.has("PARAGET") }

func (r Message) has(word string) bool {
	for _, p := range r.Path {
		if p == word {
			return true
		}
	}
	return false
}

// AlarmState reads an alarm record (…\ALARM\<kind> <channel from 0> <state>): the channel
// from 1 and whether the alarm is on.
func (r Message) AlarmState() (channel int, on bool, ok bool) {
	if !r.has("ALARM") || len(r.Args) < 2 {
		return 0, false, false
	}
	ch, err := strconv.Atoi(r.Args[0])
	if err != nil || ch < 0 {
		return 0, false, false
	}
	return ch + 1, r.Args[1] != "0", true
}

// recordJSON: the record as JSON, for the event log (raw) and the alarm server's log.
type recordJSON struct {
	Tree   string         `json:"tree"`
	IP     string         `json:"ip"`
	System string         `json:"system,omitempty"`
	Kind   string         `json:"kind"`
	Args   []string       `json:"args"`
	Fields map[string]any `json:"fields,omitempty"`
}

// JSON is the record as one line of JSON; the values of known records are named in
// fields (an alarm: channel from 1, active).
func (r Message) JSON() string {
	j := recordJSON{Tree: r.Tree(), IP: r.IP, System: r.System, Kind: r.Kind(), Args: r.Args}
	if j.Args == nil {
		j.Args = []string{}
	}
	if ch, on, ok := r.AlarmState(); ok {
		j.Fields = map[string]any{"channel": ch, "active": on}
	}
	b, _ := json.Marshal(j)
	return string(b)
}

// systems: the second field of a record when it carries the system (an other word
// there stays in the description: nothing is lost, Tree shows it all).
var systems = map[string]bool{"IP": true, "PROXY": true}

func isAddress(f string) bool { return net.ParseIP(strings.Trim(f, "[]")) != nil }

// isWord: a word of the description - capitals, digits, _ - not a value.
func isWord(f string) bool {
	if f == "" || f[0] < 'A' || f[0] > 'Z' {
		return false
	}
	for i := 0; i < len(f); i++ {
		c := f[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// parseFields makes a record of the fields of one; prev is the record before it in the
// same frame (nil: none), ip the device's address when no record tells it.
func parseFields(fields []string, prev *Message, ip string) Message {
	r := Message{IP: ip}
	if prev != nil {
		r.IP, r.System = prev.IP, prev.System
	}
	i := 0
	if i < len(fields) && isAddress(fields[i]) {
		r.IP, r.System = fields[i], ""
		i++
		if i < len(fields) && systems[fields[i]] {
			r.System = fields[i]
			i++
		}
	} else if i < len(fields) && systems[fields[i]] {
		r.System = fields[i]
		i++
	}
	for ; i < len(fields) && isWord(fields[i]); i++ {
		r.Path = append(r.Path, fields[i])
	}
	r.Args = append([]string(nil), fields[i:]...)
	return r
}

// ParseRecords splits the text of a command frame into records; ip is the device's
// address for records that do not tell it.
func ParseRecords(body []byte, ip string) []Message {
	var out []Message
	var prev *Message
	for _, block := range strings.Split(string(body), "\n\n\n") {
		if block = strings.Trim(block, "\x00\r\n"); block == "" {
			continue
		}
		r := parseFields(strings.Split(block, "\t"), prev, ip)
		out = append(out, r)
		prev = &out[len(out)-1]
	}
	return out
}

// Event is a record of the device as it came, or an alarm beginning or ending.
type Event struct {
	Message
	Time time.Time
	// an alarm: Channel from 1, Active (began) or not (ended); Held: it ended because
	// it was not repeated (Time is then when it came last)
	Alarm   bool
	Channel int
	Active  bool
	Held    bool
	// Burst: a record of the burst after the login - the device's state, not news
	Burst bool
}

// Subscription timings (variables for the tests).
var (
	// AlarmHold: an alarm not repeated for this long has ended.
	AlarmHold = 3 * time.Second
	// eventSilence: a connection without a frame for this long is gone (the device
	// sends keep-alives every few seconds).
	eventSilence = 60 * time.Second
	// burstQuiet, burstMax: the burst after the login is over when the line is quiet
	// this long, at the latest after burstMax.
	burstQuiet = 1500 * time.Millisecond
	burstMax   = 8 * time.Second
	keepAlive  = 5 * time.Second
)

// ErrSilent: the device stopped sending.
var ErrSilent = errors.New("the device went silent")

// Events logs in and calls fn for every record and every alarm that begins or ends,
// until ctx ends or the connection breaks. connected is called once logged in.
// Repetitions of an alarm that is on are not passed on; its records are not either
// (an alarm is its beginning and its end).
func (c *Client) Events(ctx context.Context, connected func(), fn func(Event)) error {
	cn, err := login(ctx, c.addr, c.user, c.pass)
	if err != nil {
		return err
	}
	defer cn.Close()
	stop := context.AfterFunc(ctx, func() { cn.Close() })
	defer stop()
	if connected != nil {
		connected()
	}
	type batch struct {
		recs []Message
		at   time.Time
	}
	recs := make(chan batch, 64)
	errc := make(chan error, 1)
	quit := make(chan struct{})
	defer close(quit)
	// what the login brought with its answer
	var first []Message
	for _, f := range cn.queue {
		first = append(first, parseFields(f, nil, cn.ip))
	}
	cn.queue = nil
	go func() {
		for {
			cn.c.SetReadDeadline(time.Now().Add(eventSilence))
			isData, body, err := cn.frame()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					err = ErrSilent
				}
				errc <- err
				return
			}
			if isData || len(body) == 0 {
				continue // keep-alives
			}
			if rs := ParseRecords(body, cn.ip); len(rs) > 0 {
				select {
				case recs <- batch{rs, time.Now()}:
				case <-quit:
					return
				}
			}
		}
	}()

	type akey struct {
		kind string
		ch   int
	}
	type alarm struct {
		rec  Message
		last time.Time
	}
	on := map[akey]*alarm{}
	began := time.Now()
	lastRec := began
	burst := true
	handle := func(r Message, at time.Time) {
		if burst && (at.Sub(lastRec) >= burstQuiet || at.Sub(began) >= burstMax) {
			burst = false
		}
		lastRec = at
		ch, active, ok := r.AlarmState()
		if !ok {
			fn(Event{Message: r, Time: at, Burst: burst})
			return
		}
		k := akey{r.Kind(), ch}
		a := on[k]
		switch {
		case active && a == nil:
			on[k] = &alarm{rec: r, last: at}
			fn(Event{Message: r, Time: at, Alarm: true, Channel: ch, Active: true})
		case active:
			a.last = at // repeated: still on
		case a != nil:
			delete(on, k)
			fn(Event{Message: r, Time: at, Alarm: true, Channel: ch})
		}
	}
	for _, r := range first {
		handle(r, began)
	}
	tick := time.NewTicker(min(AlarmHold/4, 500*time.Millisecond))
	defer tick.Stop()
	ka := time.NewTicker(keepAlive)
	defer ka.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("events: %w", err)
		case b := <-recs:
			for _, r := range b.recs {
				handle(r, b.at)
			}
		case now := <-tick.C:
			if burst && (now.Sub(lastRec) >= burstQuiet || now.Sub(began) >= burstMax) {
				burst = false
			}
			for k, a := range on {
				if now.Sub(a.last) >= AlarmHold {
					delete(on, k)
					fn(Event{Message: a.rec, Time: a.last, Alarm: true, Channel: k.ch, Held: true})
				}
			}
		case <-ka.C:
			if err := cn.sendCmd(nil); err != nil {
				return fmt.Errorf("events: %w", err)
			}
		}
	}
}
