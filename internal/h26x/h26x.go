// Package h26x looks into elementary H.264/H.265 streams: which of the two a stream is
// and its frame rate (from the timing information of an H.264 SPS). Raw streams from
// recorders have no timestamps; ffmpeg needs the frame rate.
package h26x

import (
	"bytes"
	"errors"
)

type bitReader struct {
	b   []byte
	pos int // in bits
}

func (r *bitReader) bit() (uint, error) {
	if r.pos >= len(r.b)*8 {
		return 0, errors.New("SPS: too short")
	}
	v := uint(r.b[r.pos/8]>>(7-r.pos%8)) & 1
	r.pos++
	return v, nil
}

func (r *bitReader) bits(n int) (uint, error) {
	var v uint
	for i := 0; i < n; i++ {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		v = v<<1 | b
	}
	return v, nil
}

func (r *bitReader) ue() (uint, error) {
	zeros := 0
	for {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		if zeros++; zeros > 31 {
			return 0, errors.New("SPS: bad exp-Golomb code")
		}
	}
	v, err := r.bits(zeros)
	return (1<<zeros - 1) + v, err
}

func (r *bitReader) se() (int, error) {
	v, err := r.ue()
	if v&1 == 1 {
		return int(v+1) / 2, err
	}
	return -int(v / 2), err
}

// unescape removes the emulation prevention bytes (00 00 03 -> 00 00).
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// SPSFrameRate reads the frame rate of an H.264 SPS NAL unit (with its header byte);
// ok is false when the SPS has no timing information.
func SPSFrameRate(nal []byte) (fps float64, ok bool) {
	if len(nal) < 4 || nal[0]&0x1f != 7 {
		return 0, false
	}
	r := &bitReader{b: unescape(nal[1:])}
	var err error
	must := func(_ uint, e error) {
		if err == nil {
			err = e
		}
	}
	mustS := func(_ int, e error) {
		if err == nil {
			err = e
		}
	}
	profile, _ := r.bits(8)
	must(r.bits(16)) // constraints, level
	must(r.ue())     // sps id
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma, e := r.ue()
		must(chroma, e)
		if chroma == 3 {
			must(r.bit())
		}
		must(r.ue())                 // bit depth luma
		must(r.ue())                 // bit depth chroma
		must(r.bit())                // qpprime
		if m, _ := r.bit(); m == 1 { // scaling matrices
			n := 8
			if chroma == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				if present, _ := r.bit(); present == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					last, next := 8, 8
					for j := 0; j < size; j++ {
						if next != 0 {
							d, e := r.se()
							mustS(d, e)
							next = (last + d + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	must(r.ue()) // log2 max frame num
	poc, e := r.ue()
	must(poc, e)
	switch poc {
	case 0:
		must(r.ue())
	case 1:
		must(r.bit())
		mustS(r.se())
		mustS(r.se())
		n, e := r.ue()
		must(n, e)
		for i := uint(0); i < n && err == nil; i++ {
			mustS(r.se())
		}
	}
	must(r.ue())  // max ref frames
	must(r.bit()) // gaps
	must(r.ue())  // width
	must(r.ue())  // height
	if frameMbsOnly, _ := r.bit(); frameMbsOnly == 0 {
		must(r.bit())
	}
	must(r.bit()) // direct 8x8
	if crop, _ := r.bit(); crop == 1 {
		for i := 0; i < 4; i++ {
			must(r.ue())
		}
	}
	if vui, _ := r.bit(); vui == 0 || err != nil {
		return 0, false
	}
	if ar, _ := r.bit(); ar == 1 {
		if idc, _ := r.bits(8); idc == 255 {
			must(r.bits(32))
		}
	}
	if ov, _ := r.bit(); ov == 1 {
		must(r.bit())
	}
	if vs, _ := r.bit(); vs == 1 {
		must(r.bits(4))
		if cd, _ := r.bit(); cd == 1 {
			must(r.bits(24))
		}
	}
	if cl, _ := r.bit(); cl == 1 {
		must(r.ue())
		must(r.ue())
	}
	timing, _ := r.bit()
	if timing == 0 || err != nil {
		return 0, false
	}
	units, e1 := r.bits(32)
	scale, e2 := r.bits(32)
	if e1 != nil || e2 != nil || units == 0 || scale == 0 {
		return 0, false
	}
	fps = float64(scale) / float64(2*units)
	if fps < 1 || fps > 240 {
		return 0, false
	}
	return fps, true
}

// Detect looks at the NAL units of an Annex-B stream: an H.264 SPS gives the ffmpeg
// format "h264" and maybe the frame rate (0: unknown); H.265 parameter sets "hevc".
// ok is false while neither was seen.
func Detect(es []byte) (format string, fps float64, ok bool) {
	for _, nal := range bytes.Split(es, []byte{0, 0, 1}) {
		nal = bytes.TrimRight(nal, "\x00")
		if len(nal) < 2 {
			continue
		}
		if t := (nal[0] >> 1) & 0x3f; nal[0]&0x81 == 0 && nal[1] == 1 && (t == 32 || t == 33) {
			return "hevc", 0, true // VPS/SPS of H.265
		}
		if nal[0]&0x1f == 7 && nal[0]&0x80 == 0 {
			fps, _ := SPSFrameRate(nal)
			return "h264", fps, true
		}
	}
	return "", 0, false
}
