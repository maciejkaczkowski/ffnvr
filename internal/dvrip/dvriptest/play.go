package dvriptest

import (
	"net"
	"sync"
	"time"
)

// player plays a recording on a claimed data connection as messages 1422, at
// PlaySpeed times real time after a second sent at once (as the recorder did when
// captured), faster after Fast (as measured: Value 1 2x, 2 3x, 3 5x, 4 10x); the
// stamps are on the camera's clock (CameraSkew).
type player struct {
	mu     sync.Mutex
	wake   chan struct{} // closed on a change
	paused bool
	pos    int // the next frame
	jumped bool
	target time.Time // where Locate went
	done   bool
	rate   float64 // of Fast
}

var fastRates = map[int]float64{0: 1, 1: 2, 2: 3, 3: 5, 4: 10}

func (p *player) fast(v int) {
	p.change(func() {
		if r, ok := fastRates[v]; ok {
			p.rate = r
		}
	})
}

func (p *player) change(f func()) {
	p.mu.Lock()
	f()
	close(p.wake)
	p.wake = make(chan struct{})
	p.mu.Unlock()
}

func (p *player) pause(on bool) { p.change(func() { p.paused = on }) }
func (p *player) stop()         { p.change(func() { p.done = true }) }

// locate goes to the key frame at or after t (recorder's clock)
func (p *player) locate(t time.Time) {
	p.change(func() { p.jumped = true; p.target = t })
}

func (n *NVR) play(c net.Conn, rec Recording) *player {
	n.Mu.Lock()
	speed, skew := n.PlaySpeed, n.CameraSkew
	n.Playing++
	n.MaxPlaying = max(n.MaxPlaying, n.Playing)
	n.Mu.Unlock()
	if speed <= 0 {
		speed = 20
	}
	frames := sofiaFrames(rec.Video, rec.FPS, rec.Begin.Add(skew))
	p := &player{wake: make(chan struct{}), rate: 1}
	go func() {
		defer func() {
			n.Mu.Lock()
			n.Playing--
			n.Mu.Unlock()
		}()
		var seq uint32
		burst := rec.FPS // a second at once, after the start and every jump
		for {
			p.mu.Lock()
			if p.done {
				p.mu.Unlock()
				return
			}
			if p.jumped {
				p.jumped = false
				p.pos = len(frames)
				for i, f := range frames {
					if f.key && !f.at.Add(-skew).Before(p.target) {
						p.pos = i
						break
					}
				}
				burst = rec.FPS
			}
			if p.paused || p.pos >= len(frames) {
				wake := p.wake
				p.mu.Unlock()
				<-wake // paused, or the end of the file: silence
				continue
			}
			f := frames[p.pos]
			p.pos++
			rate := p.rate
			p.mu.Unlock()
			pk := packetOf(0x1A, 1422, f.data)
			pk[1] = 1
			pk[8], pk[9], pk[10], pk[11] = byte(seq), byte(seq>>8), byte(seq>>16), byte(seq>>24)
			seq++
			if _, err := c.Write(pk); err != nil {
				return
			}
			if burst > 0 {
				burst--
				continue
			}
			p.mu.Lock()
			wake := p.wake
			p.mu.Unlock()
			select {
			case <-time.After(time.Duration(float64(time.Second) / float64(rec.FPS) / speed / rate)):
			case <-wake:
			}
		}
	}()
	return p
}
