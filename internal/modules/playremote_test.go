package modules

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestDHAVStart(t *testing.T) {
	// a key frame of 2026-11-01 00:00:02 (as the recorders write it), after some noise
	frame := make([]byte, 32)
	copy(frame, "DHAV")
	frame[4] = 0xfd
	d := uint32(2026-2000)<<26 | 11<<22 | 1<<17 | 0<<12 | 0<<6 | 2
	binary.LittleEndian.PutUint32(frame[16:20], d)
	head := append([]byte{0, 1, 2, 'D', 'H'}, frame...)
	got, ok := DHAVStart(head)
	if !ok || !got.Equal(time.Date(2026, 11, 1, 0, 0, 2, 0, time.Local)) {
		t.Errorf("%v %v", got, ok)
	}
	if _, ok := DHAVStart([]byte("not a DHAV stream")); ok {
		t.Error("no header")
	}
	frame[4] = 0xf0 // audio only: no time of a video frame
	if _, ok := DHAVStart(frame); ok {
		t.Error("audio")
	}
}

func TestDHAVStartFine(t *testing.T) {
	// frames of 40 ms: 15 of second 2, then second 3; the counter wraps between them
	var head []byte
	for i := 0; i < 30; i++ {
		frame := make([]byte, 32)
		copy(frame, "DHAV")
		frame[4] = 0xfc
		sec := uint32(2)
		if i >= 15 {
			sec = 3
		}
		d := uint32(2026-2000)<<26 | 11<<22 | 1<<17 | 0<<12 | 0<<6 | sec
		binary.LittleEndian.PutUint32(frame[16:20], d)
		binary.LittleEndian.PutUint16(frame[20:22], uint16(65000+i*40)) // wraps at 65536
		head = append(head, frame...)
	}
	got, ok := DHAVStart(head)
	if want := time.Date(2026, 11, 1, 0, 0, 2, 400e6, time.Local); !ok || !got.Equal(want) {
		t.Errorf("%v, want %v", got, want)
	}
}
