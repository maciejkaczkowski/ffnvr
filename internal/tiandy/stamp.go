package tiandy

import (
	"bytes"
	"encoding/binary"
	"time"
)

// The recorder counts time on its own wall clock: the times of the search come with a
// "Z" that is not UTC, and the range of a CONNECT (TM:<from>:<to>:<channel>) is in
// seconds of the wall clock taken as UTC. A captured session of the web client asked for
// a day from its midnight with TM:1792540800 (2026-10-21 00:00:00 as UTC) and got the
// recording stamped 23:59:59 the day before, on both channels it played.

// wallUnix: the seconds of t on the wall clock (time.Local, the recorder's), as UTC.
func wallUnix(t time.Time) int64 {
	_, off := t.In(time.Local).Zone()
	return t.Unix() + int64(off)
}

// fromWall: the time of seconds of the wall clock taken as UTC.
func fromWall(sec int64) time.Time {
	u := time.Unix(sec, 0).UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(), 0, time.Local)
}

// Every picture of a recording is preceded by an SEI message of Tiandy's own (payload
// type 240, 16 bytes): "TDST", 4 and 4 bytes of something else, then the time of the
// picture in whole seconds of the wall clock (little endian), as captured:
//
//	00 00 00 01 4e 01 f0 10 54 44 53 54 01 00 08 00 d3 fe 04 00 7f 00 d8 6a 80
var tdstMagic = []byte("TDST")

// tdst finds the first stamp in b: the wall clock seconds (as UTC).
func tdst(b []byte) (int64, bool) {
	i := bytes.Index(b, tdstMagic)
	if i < 0 {
		return 0, false
	}
	p := unescape(b[i+len(tdstMagic):], 12)
	if len(p) < 12 {
		return 0, false
	}
	sec := int64(binary.LittleEndian.Uint32(p[8:12]))
	if y := time.Unix(sec, 0).UTC().Year(); y < 2000 || y > 2100 {
		return 0, false
	}
	return sec, true
}

// unescape takes up to n bytes of a NAL unit without its emulation prevention bytes
// (the 03 of 00 00 03).
func unescape(b []byte, n int) []byte {
	out := make([]byte, 0, n)
	zeros := 0
	for _, c := range b {
		if len(out) == n {
			break
		}
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

// StreamStart: the time of the first picture of an elementary stream of a recording. The
// stamps are of whole seconds; where the stream holds the pictures of the next second
// whole, the first picture's place in its second is told by how many come before it:
// k pictures of second T, then m of T+1 make the first one T+1-k/m. Otherwise the
// whole second.
func StreamStart(es []byte) (time.Time, bool) {
	var stamps []int64
	for i := 0; ; {
		j := bytes.Index(es[i:], tdstMagic)
		if j < 0 {
			break
		}
		if t, ok := tdst(es[i+j:]); ok && (len(stamps) == 0 || t >= stamps[0] && t <= stamps[0]+3) {
			stamps = append(stamps, t)
		}
		i += j + len(tdstMagic)
	}
	if len(stamps) == 0 {
		return time.Time{}, false
	}
	t0 := fromWall(stamps[0])
	k := 0
	for k < len(stamps) && stamps[k] == stamps[0] {
		k++
	}
	m := 0
	for k+m < len(stamps) && stamps[k+m] == stamps[0]+1 {
		m++
	}
	if m == 0 || k+m == len(stamps) || k > m { // the next second not whole (or not next)
		return t0, true
	}
	return t0.Add(time.Second - time.Duration(k)*time.Second/time.Duration(m)), true
}
