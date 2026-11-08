package tiandy

import (
	"bytes"
	"errors"
	"io"
)

// The .sdv container of Tiandy recordings: after a stream description, every frame is
// preceded by a header of fixed length (32 bytes) starting with 00 00 00 02 - a
// sequence valid H.264/H.265 never contains. The header itself holds a false start
// code (00 00 00 01), so it must be skipped whole, not searched for a start code.

var (
	sdvFrameMagic = []byte{0, 0, 0, 2}
	startCode     = []byte{0, 0, 0, 1}
)

const sdvHeaderRemainder = 28 // the 32 byte header without its 4 byte magic

// sdvRecord returns the elementary stream of one record (the bytes after a magic).
func sdvRecord(rec []byte) []byte {
	if len(rec) < sdvHeaderRemainder {
		return nil // too short, e.g. the stream description
	}
	body := rec[sdvHeaderRemainder:]
	if bytes.HasPrefix(body, startCode) {
		return body
	}
	if k := bytes.Index(body, startCode); k >= 0 { // a header of another length
		return body[k:]
	}
	return nil
}

// DemuxSDV turns a whole .sdv stream into an elementary H.264/H.265 stream.
func DemuxSDV(sdv []byte) []byte {
	var out bytes.Buffer
	for _, rec := range bytes.Split(sdv, sdvFrameMagic) {
		out.Write(sdvRecord(rec))
	}
	return out.Bytes()
}

// sdvDemuxer does the same while the stream arrives: Write takes the .sdv bytes in any
// pieces, the elementary stream goes to w record by record.
//
// With until set it also ends the stream at the first picture stamped at or after it
// (errRangeEnd): the recorder goes on past the end of a range it was asked for, and
// stops only when it has been silent for a while. It ends there only when the first
// stamp fell where the range begins (from), so stamps of another clock end nothing.
type sdvDemuxer struct {
	w           io.Writer
	buf         []byte // the current record (after its magic), or the bytes before the first
	from, until int64  // the range in wall clock seconds (as UTC); until 0: no end
	seen, armed bool   // a stamp was seen; the first one fell in the range
	ended       bool
}

// errRangeEnd: the stream passed the end of the range.
var errRangeEnd = errors.New("the end of the range")

// record passes the elementary stream of a record on, unless it is past the range.
func (d *sdvDemuxer) record(body []byte) error {
	if len(body) == 0 || d.ended {
		return nil
	}
	if d.until > 0 {
		if t, ok := tdst(body[:min(len(body), 96)]); ok {
			if !d.seen {
				d.seen = true
				d.armed = t >= d.from-120 && t < d.until
			}
			if d.armed && t >= d.until {
				d.ended = true
				return errRangeEnd
			}
		}
	}
	_, err := d.w.Write(body)
	return err
}

func (d *sdvDemuxer) Write(p []byte) (int, error) {
	d.buf = append(d.buf, p...)
	for {
		// a magic may be split between pieces: search from the start of the record
		i := bytes.Index(d.buf, sdvFrameMagic)
		if i < 0 {
			return len(p), nil
		}
		if err := d.record(sdvRecord(d.buf[:i])); err != nil {
			return 0, err
		}
		d.buf = append(d.buf[:0], d.buf[i+len(sdvFrameMagic):]...)
	}
}

// Close writes the last record.
func (d *sdvDemuxer) Close() error {
	err := d.record(sdvRecord(d.buf))
	d.buf = nil
	if errors.Is(err, errRangeEnd) {
		return nil
	}
	return err
}
