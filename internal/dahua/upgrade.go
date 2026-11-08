package dahua

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The firmware upgrade of the web interface (captured on DHI-NVR2X-4KS3, firmware
// 4.005.0000000.6.R):
//
//  1. the JSON-RPC login (/RPC2_Login);
//  2. the notification channel (/webappoverwebsocket) subscribes to the upgrader's
//     state: upgrader.attachState {"proc": N};
//  3. POST /RPC2_Upgrade, multipart/form-data with one part "file" (the .bin), the
//     session as the cookie WebClientHttpSessionID;
//  4. the recorder tells client.notifyUpgraderStream: Preparing, Downloading
//     (receiving the file, Progress 0-100), Upgrading (installing, 0-100), then
//     Succeeded - or a failure such as FeatureAPIUnmatch (a file of another model);
//  5. only then (minutes after the upload) /RPC2_Upgrade answers: an HTML page with
//     "upgrade successfully"; the recorder restarts by itself.
//
// global.keepAlive keeps the session alive meanwhile.

// Upgrade states the recorder reports.
const (
	UpgradePreparing   = "Preparing"
	UpgradeDownloading = "Downloading" // the recorder receives the file
	UpgradeUpgrading   = "Upgrading"   // the recorder installs it
	UpgradeSucceeded   = "Succeeded"
)

// UpgradeState is one report during an upgrade: what the recorder told last
// (State, Progress in percent of that state; "" before it tells anything) and how
// much of the file went out.
type UpgradeState struct {
	State       string
	Progress    int
	Sent, Total int64
}

// Percent is the progress of the whole upgrade as the web interface shows it: the
// transfer is the first half, the installation the second.
func (s UpgradeState) Percent() int {
	sent := 0
	if s.Total > 0 {
		sent = int(min(s.Sent, s.Total) * 50 / s.Total)
	}
	switch s.State {
	case UpgradeSucceeded:
		return 100
	case UpgradeUpgrading:
		return 50 + clamp(s.Progress)/2
	case UpgradeDownloading: // the recorder's count lags behind what went out
		return max(clamp(s.Progress)/2, sent)
	}
	return sent
}

func clamp(p int) int { return min(max(p, 0), 100) }

// failedState: a state that ends the upgrade without success.
func failedState(st string) bool {
	switch st {
	case "", UpgradePreparing, UpgradeDownloading, UpgradeUpgrading, UpgradeSucceeded:
		return false
	}
	return true
}

// ErrUpgrade is a refusal of the upgrade by the recorder (a wrong file, a failure
// while installing), as opposed to a network or login problem.
var ErrUpgrade = errors.New("the recorder refused the firmware")

func upgradeError(state string) error {
	switch state {
	case "FeatureAPIUnmatch":
		return fmt.Errorf("%w: the file is not meant for this model (%s)", ErrUpgrade, state)
	case "NotEnoughMemory", "NoEnoughMemory":
		return fmt.Errorf("%w: not enough memory on the recorder (%s)", ErrUpgrade, state)
	}
	return fmt.Errorf("%w: %s", ErrUpgrade, state)
}

// UpgradeTimeout: how long the recorder may take after the upload (the installation
// took 90 s on the captured recorder; larger ones take longer).
var UpgradeTimeout = 20 * time.Minute

// keepAliveEvery: how often the session is kept alive during an upgrade.
var keepAliveEvery = 20 * time.Second

// notifyProc: the callback number the notifications of the upgrader carry.
const notifyProc = 101

// Upgrade installs a firmware file (.bin) of size bytes on the recorder. report, if not
// nil, is called with every change (from other goroutines, never at once). After a
// success the recorder restarts by itself: it is unreachable for a few minutes.
func (c *Client) Upgrade(ctx context.Context, file io.Reader, name string, size int64, report func(UpgradeState)) error {
	if size <= 0 {
		return errors.New("the firmware file is empty")
	}
	rpc, err := c.LoginRPC(ctx)
	if err != nil {
		return err
	}
	sid := rpc.SessionID()

	var mu sync.Mutex
	st := UpgradeState{Total: size}
	update := func(change func(*UpgradeState)) {
		mu.Lock()
		defer mu.Unlock()
		old := st
		change(&st)
		if report != nil && st != old {
			report(st)
		}
	}
	current := func() UpgradeState { mu.Lock(); defer mu.Unlock(); return st }
	if report != nil {
		report(st)
	}

	bg, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { stop(); wg.Wait() }()

	// the recorder's own progress; without it the upgrade still goes on
	failed := make(chan struct{}) // closed when the recorder reports a failure
	var failOnce sync.Once
	if n, err := c.dialNotify(bg, sid); err == nil {
		if err := n.subscribe("upgrader.attachState", map[string]any{"proc": notifyProc}); err == nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				n.listen(func(method string, params map[string]any) {
					if method != "client.notifyUpgraderStream" {
						return
					}
					state, _ := params["State"].(string)
					p, _ := params["Progress"].(float64)
					update(func(s *UpgradeState) { s.State, s.Progress = state, int(p) })
					if failedState(state) {
						failOnce.Do(func() { close(failed) })
					}
				})
			}()
		}
		go func() { <-bg.Done(); n.Close() }()
	}
	wg.Add(1)
	go func() { // the session must outlive the installation
		defer wg.Done()
		t := time.NewTicker(keepAliveEvery)
		defer t.Stop()
		for {
			select {
			case <-bg.Done():
				return
			case <-t.C:
				kctx, cancel := context.WithTimeout(bg, 10*time.Second)
				rpc.Call(kctx, "global.keepAlive", map[string]any{"timeout": 300, "active": false}, nil)
				cancel()
			}
		}
	}()

	answer, err := c.postUpgrade(bg, sid, file, name, size, func(sent int64) {
		update(func(s *UpgradeState) { s.Sent = sent })
	}, failed)
	if err != nil || !strings.Contains(strings.ToLower(answer), "success") {
		// the reason may still be on its way over the notification channel
		select {
		case <-failed:
		case <-time.After(2 * time.Second):
		}
	}
	last := current()
	switch {
	case failedState(last.State):
		err = upgradeError(last.State)
	case err == nil && !strings.Contains(strings.ToLower(answer), "success") && last.State != UpgradeSucceeded:
		err = fmt.Errorf("%w: %s", ErrUpgrade, or(answer, "no answer"))
	case err != nil && last.State == UpgradeSucceeded:
		err = nil // the recorder went down to restart before answering
	}
	if err != nil {
		stop()
		wg.Wait() // the keep-alive is done with the session
		rpc.Close()
		return err
	}
	update(func(s *UpgradeState) { s.State, s.Progress = UpgradeSucceeded, 100 })
	return nil // no logout: the recorder restarts
}

var tags = regexp.MustCompile(`<[^>]*>`)

// postUpgrade sends the file and waits for the answer: its text without HTML. A
// failure the notifications tell ends the wait.
func (c *Client) postUpgrade(ctx context.Context, sid string, file io.Reader, name string, size int64, sent func(int64), failed <-chan struct{}) (string, error) {
	var rnd [12]byte
	rand.Read(rnd[:])
	boundary := "---------------------------" + hex.EncodeToString(rnd[:])
	name = strings.NewReplacer(`"`, "", "\r", "", "\n", "", "\\", "").Replace(path.Base(strings.ReplaceAll(name, "\\", "/")))
	head := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + name + "\"\r\n" +
		"Content-Type: application/octet-stream\r\n\r\n"
	tail := "\r\n--" + boundary + "--\r\n"
	body := io.MultiReader(strings.NewReader(head), &counter{r: io.LimitReader(file, size), sent: sent}, strings.NewReader(tail))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-failed:
			cancel() // the recorder gave up: no need to wait for its answer
		case <-ctx.Done():
		}
	}()
	u := *c.base
	u.Path, u.RawQuery = "/RPC2_Upgrade", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return "", err
	}
	// a known length: the recorder's server does not take chunked uploads
	req.ContentLength = int64(len(head)) + size + int64(len(tail))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.AddCookie(&http.Cookie{Name: "WebClientHttpSessionID", Value: sid})
	// the answer comes after the installation: no header timeout of the normal calls
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if t, ok := c.plain.(*http.Transport); ok {
		tr = t.Clone()
	}
	tr.ResponseHeaderTimeout = UpgradeTimeout
	defer tr.CloseIdleConnections()
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return "", fmt.Errorf("RPC2_Upgrade: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	text := strings.Join(strings.Fields(tags.ReplaceAllString(string(raw), " ")), " ")
	if resp.StatusCode != http.StatusOK {
		return text, fmt.Errorf("RPC2_Upgrade: HTTP %d %s", resp.StatusCode, text)
	}
	return text, nil
}

type counter struct {
	r    io.Reader
	n    int64
	sent func(int64)
	last time.Time
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if now := time.Now(); err != nil || now.Sub(c.last) > 200*time.Millisecond {
		c.last = now
		c.sent(c.n)
	}
	return n, err
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
