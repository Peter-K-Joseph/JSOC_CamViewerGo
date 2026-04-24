package streaming

import (
	"encoding/binary"
	"fmt"
)

// RTPPacket represents a parsed RTP packet.
type RTPPacket struct {
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	Marker         bool
	Payload        []byte
}

// AccessUnit is a fully reconstructed codec access unit in AnnexB format.
type AccessUnit struct {
	Codec     string // "h264" or "h265"
	Timestamp uint32
	Data      []byte // AnnexB: 00 00 00 01 <NAL> ...
	Keyframe  bool
}

var annexBStart = []byte{0x00, 0x00, 0x00, 0x01}

// ParseRTPPacket parses the fixed RTP header from raw bytes.
func ParseRTPPacket(data []byte) (*RTPPacket, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("rtp: packet too short (%d bytes)", len(data))
	}
	if data[0]>>6 != 2 {
		return nil, fmt.Errorf("rtp: unsupported version %d", data[0]>>6)
	}

	padding := (data[0] & 0x20) != 0
	ext := (data[0] & 0x10) != 0
	cc := int(data[0] & 0x0f)
	marker := (data[1] & 0x80) != 0
	pt := data[1] & 0x7f
	seq := binary.BigEndian.Uint16(data[2:4])
	ts := binary.BigEndian.Uint32(data[4:8])
	ssrc := binary.BigEndian.Uint32(data[8:12])

	offset := 12 + cc*4
	if offset > len(data) {
		return nil, fmt.Errorf("rtp: csrc overflow")
	}

	if ext && offset+4 <= len(data) {
		extLen := int(binary.BigEndian.Uint16(data[offset+2:offset+4])) * 4
		offset += 4 + extLen
	}

	if padding && len(data) > 0 {
		padLen := int(data[len(data)-1])
		data = data[:len(data)-padLen]
	}

	if offset > len(data) {
		return nil, fmt.Errorf("rtp: payload offset overflow")
	}

	return &RTPPacket{
		PayloadType:    pt,
		SequenceNumber: seq,
		Timestamp:      ts,
		SSRC:           ssrc,
		Marker:         marker,
		Payload:        data[offset:],
	}, nil
}

// ─── H.264 Depacketizer ───────────────────────────────────────────────────────

type H264Depacketizer struct {
	SPS       []byte
	PPS       []byte
	fragments []byte
	au        []byte
	keyframe  bool
	lastSeq   uint16
	started   bool
}

func (d *H264Depacketizer) Push(pkt *RTPPacket) []AccessUnit {
	if len(pkt.Payload) == 0 {
		return nil
	}

	nalType := pkt.Payload[0] & 0x1f
	var aus []AccessUnit

	switch {
	case nalType >= 1 && nalType <= 23:
		// Single NAL unit
		au := d.emitSingle(pkt.Payload, pkt.Timestamp)
		if au != nil {
			aus = append(aus, *au)
		}

	case nalType == 24:
		// STAP-A: multiple NALs in one packet
		data := pkt.Payload[1:]
		for len(data) >= 3 {
			size := int(binary.BigEndian.Uint16(data[0:2]))
			data = data[2:]
			if size > len(data) {
				break
			}
			nal := data[:size]
			data = data[size:]
			au := d.emitSingle(nal, pkt.Timestamp)
			if au != nil {
				aus = append(aus, *au)
			}
		}

	case nalType == 28:
		// FU-A fragmented unit
		if len(pkt.Payload) < 2 {
			return nil
		}
		fuHeader := pkt.Payload[1]
		start := (fuHeader & 0x80) != 0
		end := (fuHeader & 0x40) != 0
		fuNalType := fuHeader & 0x1f

		if start {
			d.fragments = d.fragments[:0]
			nalHdr := (pkt.Payload[0] & 0xe0) | fuNalType
			d.fragments = append(d.fragments, nalHdr)
			d.keyframe = fuNalType == 5
		}
		if len(d.fragments) > 0 {
			d.fragments = append(d.fragments, pkt.Payload[2:]...)
		}
		if end && len(d.fragments) > 0 {
			au := d.emitSingle(d.fragments, pkt.Timestamp)
			d.fragments = d.fragments[:0]
			if au != nil {
				aus = append(aus, *au)
			}
		}
	}

	return aus
}

func (d *H264Depacketizer) emitSingle(nal []byte, ts uint32) *AccessUnit {
	if len(nal) == 0 {
		return nil
	}
	nalType := nal[0] & 0x1f

	if nalType == 7 {
		d.SPS = clone(nal)
	} else if nalType == 8 {
		d.PPS = clone(nal)
	}

	// Suppress standalone parameter set NALs — they are stored above and
	// prepended to the next IDR.  Emitting them as individual AccessUnits
	// causes decoders to produce green/corrupt frames.
	if nalType == 7 || nalType == 8 {
		return nil
	}

	var buf []byte
	if nalType == 5 {
		// IDR: prepend SPS+PPS
		if d.SPS != nil {
			buf = append(buf, annexBStart...)
			buf = append(buf, d.SPS...)
		}
		if d.PPS != nil {
			buf = append(buf, annexBStart...)
			buf = append(buf, d.PPS...)
		}
	}
	buf = append(buf, annexBStart...)
	buf = append(buf, nal...)

	return &AccessUnit{
		Codec:     "h264",
		Timestamp: ts,
		Data:      buf,
		Keyframe:  nalType == 5,
	}
}

// ─── H.265 Depacketizer ───────────────────────────────────────────────────────

type H265Depacketizer struct {
	VPS       []byte
	SPS       []byte
	PPS       []byte
	fragments []byte
	keyframe  bool
}

func (d *H265Depacketizer) Push(pkt *RTPPacket) []AccessUnit {
	if len(pkt.Payload) < 2 {
		return nil
	}

	// H.265 RTP payload header: 2 bytes
	// nalType = (payload[0] >> 1) & 0x3f
	nalType := (pkt.Payload[0] >> 1) & 0x3f
	var aus []AccessUnit

	switch {
	case nalType <= 47:
		// Single NAL unit
		au := d.emitSingle(pkt.Payload, pkt.Timestamp)
		if au != nil {
			aus = append(aus, *au)
		}

	case nalType == 48:
		// AP (Aggregation Packet)
		data := pkt.Payload[2:]
		for len(data) >= 2 {
			size := int(binary.BigEndian.Uint16(data[0:2]))
			data = data[2:]
			if size > len(data) {
				break
			}
			au := d.emitSingle(data[:size], pkt.Timestamp)
			if au != nil {
				aus = append(aus, *au)
			}
			data = data[size:]
		}

	case nalType == 49:
		// FU (Fragmentation Unit)
		if len(pkt.Payload) < 3 {
			return nil
		}
		fuHeader := pkt.Payload[2]
		start := (fuHeader & 0x80) != 0
		end := (fuHeader & 0x40) != 0
		fuNalType := fuHeader & 0x3f

		if start {
			d.fragments = d.fragments[:0]
			// reconstruct NAL header
			hdr0 := (pkt.Payload[0] & 0x81) | (fuNalType << 1)
			hdr1 := pkt.Payload[1]
			d.fragments = append(d.fragments, hdr0, hdr1)
			d.keyframe = fuNalType == 19 || fuNalType == 20
		}
		if len(d.fragments) > 0 {
			d.fragments = append(d.fragments, pkt.Payload[3:]...)
		}
		if end && len(d.fragments) > 0 {
			au := d.emitSingle(d.fragments, pkt.Timestamp)
			d.fragments = d.fragments[:0]
			if au != nil {
				aus = append(aus, *au)
			}
		}
	}

	return aus
}

func (d *H265Depacketizer) emitSingle(nal []byte, ts uint32) *AccessUnit {
	if len(nal) < 2 {
		return nil
	}
	nalType := (nal[0] >> 1) & 0x3f

	switch nalType {
	case 32:
		d.VPS = clone(nal)
	case 33:
		d.SPS = clone(nal)
	case 34:
		d.PPS = clone(nal)
	}

	// Suppress standalone parameter set NALs (VPS/SPS/PPS).  They are stored
	// above and prepended to the next IDR.  Emitting them individually causes
	// decoders to produce green/corrupt frames.
	if nalType == 32 || nalType == 33 || nalType == 34 {
		return nil
	}

	var buf []byte
	isIDR := nalType == 19 || nalType == 20
	if isIDR {
		if d.VPS != nil {
			buf = append(buf, annexBStart...)
			buf = append(buf, d.VPS...)
		}
		if d.SPS != nil {
			buf = append(buf, annexBStart...)
			buf = append(buf, d.SPS...)
		}
		if d.PPS != nil {
			buf = append(buf, annexBStart...)
			buf = append(buf, d.PPS...)
		}
	}
	buf = append(buf, annexBStart...)
	buf = append(buf, nal...)

	return &AccessUnit{
		Codec:     "h265",
		Timestamp: ts,
		Data:      buf,
		Keyframe:  isIDR,
	}
}

func clone(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
