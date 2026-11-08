package uniview

import (
	"encoding/base64"
	"encoding/binary"
	"io"
	"strings"
)

// rtpPacket is the part of an RTP packet (RFC 3550) the depacketizer needs.
type rtpPacket struct {
	Seq     uint16
	TS      uint32
	PT      uint8
	Marker  bool
	Payload []byte
	Stamp   uint32 // the time of the picture (UTC seconds) of Uniview's extension, 0: none
}

// stampProfile: the RTP header extension in which a Uniview recorder sends with every
// packet of a recording the time of its picture, in whole UTC seconds (the first 4 of
// its 12 bytes, big endian), as captured from a recorder's web client:
//
//	90 6c 00 01 | ba 88 0e 6a | cf 5b 00 02 | ab ac 00 03 | 6a e9 4f 1c 00 00 00 00 00 05 00 00
//
// (6ae94f1c: 2026-11-03 03:03:56 UTC, the key frame 7 s before the range asked for)
const stampProfile = 0xabac

func parseRTP(b []byte) (rtpPacket, bool) {
	if len(b) < 12 || b[0]>>6 != 2 {
		return rtpPacket{}, false
	}
	p := rtpPacket{
		Marker: b[1]&0x80 != 0,
		PT:     b[1] & 0x7f,
		Seq:    binary.BigEndian.Uint16(b[2:]),
		TS:     binary.BigEndian.Uint32(b[4:]),
	}
	off := 12 + 4*int(b[0]&0x0f) // CSRCs
	if b[0]&0x10 != 0 {          // header extension
		if len(b) < off+4 {
			return p, false
		}
		n := 4 * int(binary.BigEndian.Uint16(b[off+2:]))
		if binary.BigEndian.Uint16(b[off:]) == stampProfile && n >= 4 && len(b) >= off+8 {
			p.Stamp = binary.BigEndian.Uint32(b[off+4:])
		}
		off += 4 + n
	}
	end := len(b)
	if b[0]&0x20 != 0 && end > 0 { // padding
		end -= int(b[end-1])
	}
	if off > end {
		return p, false
	}
	p.Payload = b[off:end]
	return p, true
}

// depacketizer turns RTP payloads of H.264 (RFC 6184) or H.265 (RFC 7798) into an
// Annex-B byte stream (NAL units with 00 00 00 01 start codes).
type depacketizer struct {
	codec   string // H264 or H265
	w       io.Writer
	written int64
	fu      []byte // NAL unit being reassembled from fragments
	lastSeq uint16
	haveSeq bool
}

func newDepacketizer(codec string, w io.Writer) *depacketizer {
	return &depacketizer{codec: codec, w: w}
}

var startCode = []byte{0, 0, 0, 1}

func (d *depacketizer) writeNAL(nal []byte) error {
	if len(nal) == 0 {
		return nil
	}
	if _, err := d.w.Write(startCode); err != nil {
		return err
	}
	n, err := d.w.Write(nal)
	d.written += int64(len(startCode) + n)
	return err
}

func (d *depacketizer) push(p rtpPacket) error {
	lost := d.haveSeq && p.Seq != d.lastSeq+1
	d.lastSeq, d.haveSeq = p.Seq, true
	if lost {
		d.fu = nil // a fragment is missing: the NAL unit is broken
	}
	b := p.Payload
	if len(b) == 0 {
		return nil
	}
	if d.codec == "H265" {
		return d.pushH265(b)
	}
	return d.pushH264(b)
}

func (d *depacketizer) pushH264(b []byte) error {
	switch t := b[0] & 0x1f; {
	case t >= 1 && t <= 23:
		return d.writeNAL(b)
	case t == 24: // STAP-A
		return d.aggregate(b[1:])
	case t == 28: // FU-A
		if len(b) < 2 {
			return nil
		}
		start, end := b[1]&0x80 != 0, b[1]&0x40 != 0
		if start {
			d.fu = append([]byte{b[0]&0xe0 | b[1]&0x1f}, b[2:]...)
		} else if d.fu != nil {
			d.fu = append(d.fu, b[2:]...)
		}
		if end && d.fu != nil {
			nal := d.fu
			d.fu = nil
			return d.writeNAL(nal)
		}
	}
	return nil
}

func (d *depacketizer) pushH265(b []byte) error {
	if len(b) < 2 {
		return nil
	}
	switch t := (b[0] >> 1) & 0x3f; {
	case t < 48:
		return d.writeNAL(b)
	case t == 48: // aggregation packet
		return d.aggregate(b[2:])
	case t == 49: // fragmentation unit
		if len(b) < 3 {
			return nil
		}
		start, end, typ := b[2]&0x80 != 0, b[2]&0x40 != 0, b[2]&0x3f
		if start {
			d.fu = append([]byte{b[0]&0x81 | typ<<1, b[1]}, b[3:]...)
		} else if d.fu != nil {
			d.fu = append(d.fu, b[3:]...)
		}
		if end && d.fu != nil {
			nal := d.fu
			d.fu = nil
			return d.writeNAL(nal)
		}
	}
	return nil
}

// aggregate writes the NAL units of an aggregation packet: [size16][NAL]...
func (d *depacketizer) aggregate(b []byte) error {
	for len(b) >= 2 {
		n := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if n > len(b) {
			return nil
		}
		if err := d.writeNAL(b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// decodeB64 decodes base64 with or without padding (sprop-parameter-sets).
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}
