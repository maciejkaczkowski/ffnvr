package tiandy

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// The recorder's own web client keeps one logged-in control session and starts every
// stream of it on a connection of its own: CONNECT with the CMDID of the session and a
// virtual channel naming the stream (a captured session played two channels so, with
// 254 and then 250). Beginning a session costs: a login and the burst the recorder sends
// after it, which must be over before it starts a stream (seconds, longer while motion
// alarms keep coming). So the streams of a recorder share one session while they run and
// a little after, each with a virtual channel of its own, at most MaxStreams at once
// (downloads and the player together).

// MaxStreams: the most streams read from one recorder at once.
const MaxStreams = 4

// streamIDs: the virtual channels of the streams of a session, one each. The recorder's
// web client was seen using 254 and 250; going down by 4 keeps to its numbering.
var streamIDs = [MaxStreams]int{254, 250, 246, 242}

// sessionIdle: how long a session without streams is kept for the next one.
var sessionIdle = 30 * time.Second

type session struct {
	key   string
	ready chan struct{} // closed when the login is over; err tells how it went
	err   error
	ctrl  *conn
	cmdID string
	done  chan struct{} // closed when the session ends
	once  sync.Once
	// under sessions
	used [MaxStreams]bool
	n    int
	wake chan struct{} // closed when a stream is given back
	idle *time.Timer
}

var sessions = struct {
	sync.Mutex
	m map[string]*session
}{m: map[string]*session{}}

func (s *session) over() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *session) end() { s.once.Do(func() { close(s.done) }) }

// begin logs in, waits for the CMDID and lets the burst after the login pass; then the
// control line is kept alive and read (the recorder may stall when it is not read).
func (s *session) begin(addr, user, pass string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctrl, err := login(ctx, addr, user, pass)
	if err == nil {
		if s.cmdID, err = waitCmdID(ctrl, 15*time.Second); err != nil {
			ctrl.Close()
		}
	}
	if err != nil {
		s.err = err
		s.end()
		close(s.ready)
		return
	}
	drain(ctrl, 1500*time.Millisecond, 8*time.Second) // the recorder is not ready before
	if s.over() {                                     // closed meanwhile
		ctrl.Close()
		s.err = errors.New("the session was closed")
		close(s.ready)
		return
	}
	s.ctrl = ctrl
	go func() { <-s.done; ctrl.Close() }()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				if err := ctrl.sendCmd(nil); err != nil { // keep-alive
					s.end()
					return
				}
			}
		}
	}()
	go func() {
		for {
			ctrl.c.SetReadDeadline(time.Now().Add(2 * time.Minute))
			if _, _, err := ctrl.frame(); err != nil {
				var ne net.Error
				if !errors.As(err, &ne) || !ne.Timeout() {
					s.end()
					return
				}
			}
		}
	}()
	close(s.ready)
}

// stream takes a stream of the recorder: the session (begun when there is none) and a
// virtual channel of it, waited for while MaxStreams are in use. release gives it back.
func (c *Client) stream(ctx context.Context) (s *session, id int, release func(), err error) {
	key := c.addr + "\x00" + c.user + "\x00" + c.pass
	for {
		sessions.Lock()
		s = sessions.m[key]
		if s != nil && s.over() {
			s = nil
		}
		if s == nil {
			s = &session{key: key, ready: make(chan struct{}), done: make(chan struct{}), wake: make(chan struct{})}
			sessions.m[key] = s
			go s.begin(c.addr, c.user, c.pass)
		}
		sessions.Unlock()
		select {
		case <-s.ready:
		case <-ctx.Done():
			return nil, 0, nil, ctx.Err()
		}
		if s.err != nil {
			return nil, 0, nil, s.err // the next call begins anew
		}
		sessions.Lock()
		if s.over() { // ended meanwhile (the recorder dropped it): a new one
			sessions.Unlock()
			continue
		}
		for i := range s.used {
			if !s.used[i] {
				s.used[i] = true
				s.n++
				if s.idle != nil {
					s.idle.Stop()
					s.idle = nil
				}
				sessions.Unlock()
				var once sync.Once
				return s, streamIDs[i], func() { once.Do(func() { s.release(i) }) }, nil
			}
		}
		wake := s.wake
		sessions.Unlock()
		select {
		case <-wake:
		case <-s.done:
		case <-ctx.Done():
			return nil, 0, nil, ctx.Err()
		}
	}
}

func (s *session) release(i int) {
	sessions.Lock()
	defer sessions.Unlock()
	s.used[i] = false
	s.n--
	close(s.wake)
	s.wake = make(chan struct{})
	if s.n == 0 {
		s.idle = time.AfterFunc(sessionIdle, func() {
			sessions.Lock()
			idle := s.n == 0
			if idle && sessions.m[s.key] == s {
				delete(sessions.m, s.key)
			}
			sessions.Unlock()
			if idle {
				s.end()
			}
		})
	}
}

// CloseSessions ends the sessions without streams (tests; a recorder that changed).
func CloseSessions() {
	sessions.Lock()
	var idle []*session
	for k, s := range sessions.m {
		if s.n == 0 {
			idle = append(idle, s)
			delete(sessions.m, k)
		}
	}
	sessions.Unlock()
	for _, s := range idle {
		s.end()
	}
}
