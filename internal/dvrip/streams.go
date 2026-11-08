package dvrip

import (
	"context"
	"sync"
)

// A recorder sends only a few recordings at once (the XMeye client plays 4 channels
// side by side, each on a data connection of its own). ffnvr reads at most MaxStreams
// from one recorder at once: downloads and the player together. More wait for one to
// end.

// MaxStreams: the most streams read from one recorder at once.
const MaxStreams = 4

type streamPool struct {
	used int
	wake chan struct{} // closed when a stream is given back
}

var pools = struct {
	sync.Mutex
	m map[string]*streamPool
}{m: map[string]*streamPool{}}

// stream takes one of the recorder's streams, waiting while MaxStreams are in use (or
// until ctx ends). release gives it back; calling it again does nothing.
func (c *Client) stream(ctx context.Context) (release func(), err error) {
	for {
		pools.Lock()
		p := pools.m[c.addr]
		if p == nil {
			p = &streamPool{wake: make(chan struct{})}
			pools.m[c.addr] = p
		}
		if p.used < MaxStreams {
			p.used++
			pools.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					pools.Lock()
					p.used--
					close(p.wake)
					p.wake = make(chan struct{})
					pools.Unlock()
				})
			}, nil
		}
		wake := p.wake
		pools.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Streams: how many streams of the recorder are being read now.
func (c *Client) Streams() int {
	pools.Lock()
	defer pools.Unlock()
	if p := pools.m[c.addr]; p != nil {
		return p.used
	}
	return 0
}
