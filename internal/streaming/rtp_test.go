package streaming

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ── ParseRTPPacket ────────────────────────────────────────────────────────────

func makeRTPPacket(payloadType uint8, seq uint16, ts uint32, marker bool, payload []byte) []byte {
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80 // version=2, no padding, no ext, cc=0
	pkt[1] = payloadType
	if marker {
		pkt[1] |= 0x80
	}
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], ts)
	binary.BigEndian.PutUint32(pkt[8:12], 0xDEADBEEF) // SSRC
	copy(pkt[12:], payload)
	return pkt
}

func TestParseRTPPacket_Basic(t *testing.T) {
	payload := []byte{0x41, 0x42, 0x43}
	raw := makeRTPPacket(96, 1000, 9000, true, payload)

	pkt, err := ParseRTPPacket(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkt.PayloadType != 96 {
		t.Errorf("PayloadType = %d, want 96", pkt.PayloadType)
	}
	if pkt.SequenceNumber != 1000 {
		t.Errorf("SequenceNumber = %d, want 1000", pkt.SequenceNumber)
	}
	if pkt.Timestamp != 9000 {
		t.Errorf("Timestamp = %d, want 9000", pkt.Timestamp)
	}
	if !pkt.Marker {
		t.Error("Marker should be true")
	}
	if !bytes.Equal(pkt.Payload, payload) {
		t.Errorf("Payload = %v, want %v", pkt.Payload, payload)
	}
}

func TestParseRTPPacket_TooShort(t *testing.T) {
	_, err := ParseRTPPacket([]byte{0x80, 0x60})
	if err == nil {
		t.Fatal("expected error for short packet")
	}
}

func TestParseRTPPacket_BadVersion(t *testing.T) {
	raw := makeRTPPacket(96, 1, 0, false, []byte{0x01})
	raw[0] = (raw[0] & 0x3F) | 0x40 // set version = 1
	_, err := ParseRTPPacket(raw)
	if err == nil {
		t.Fatal("expected error for version != 2")
	}
}

func TestParseRTPPacket_Padding(t *testing.T) {
	// Build packet with padding flag set: last byte = pad count, pad bytes at end.
	payload := []byte{0x41, 0x42, 0x00, 0x00, 0x02} // 2 pad bytes
	raw := make([]byte, 12+len(payload))
	raw[0] = 0x80 | 0x20 // version=2, padding=1
	raw[1] = 96
	copy(raw[12:], payload)

	pkt, err := ParseRTPPacket(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Payload should be trimmed: original 5 bytes minus 2 padding = 3
	if len(pkt.Payload) != 3 {
		t.Errorf("Payload len = %d after padding removal, want 3", len(pkt.Payload))
	}
}

func TestParseRTPPacket_WithCSRC(t *testing.T) {
	// CC = 2 → 8 extra bytes of CSRC before payload
	raw := make([]byte, 12+8+3)
	raw[0] = 0x82 // version=2, CC=2
	raw[1] = 96
	copy(raw[20:], []byte{0xAA, 0xBB, 0xCC})

	pkt, err := ParseRTPPacket(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(pkt.Payload, []byte{0xAA, 0xBB, 0xCC}) {
		t.Errorf("Payload = %v with CSRC offset, want [AA BB CC]", pkt.Payload)
	}
}

// ── H264Depacketizer ──────────────────────────────────────────────────────────

func makeH264Pkt(payload []byte, marker bool) *RTPPacket {
	return &RTPPacket{
		PayloadType: 96,
		Timestamp:   90000,
		Marker:      marker,
		Payload:     payload,
	}
}

func TestH264_SingleNAL_NonIDR(t *testing.T) {
	d := &H264Depacketizer{}
	// NAL type 1 = non-IDR slice
	aus := d.Push(makeH264Pkt([]byte{0x41, 0x9A, 0x00, 0x01}, true))
	if len(aus) != 1 {
		t.Fatalf("want 1 AU, got %d", len(aus))
	}
	if aus[0].Keyframe {
		t.Error("non-IDR slice should not be keyframe")
	}
	if aus[0].Codec != "h264" {
		t.Errorf("Codec = %q, want h264", aus[0].Codec)
	}
	// Data should be annexB prefixed.
	if !bytes.HasPrefix(aus[0].Data, annexBStart) {
		t.Error("expected AnnexB start code")
	}
}

func TestH264_SPS_PPS_IDR_Sequence(t *testing.T) {
	d := &H264Depacketizer{}

	sps := []byte{0x67, 0x42, 0xC0, 0x28, 0xDA, 0x01}
	pps := []byte{0x68, 0xCE, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x84}

	// SPS (NAL type 7)
	d.Push(makeH264Pkt(sps, false))
	if d.SPS == nil {
		t.Fatal("SPS should be stored after NAL type 7")
	}

	// PPS (NAL type 8)
	d.Push(makeH264Pkt(pps, false))
	if d.PPS == nil {
		t.Fatal("PPS should be stored after NAL type 8")
	}

	// IDR (NAL type 5) — should produce keyframe AU with SPS+PPS prepended
	aus := d.Push(makeH264Pkt(idr, true))
	if len(aus) == 0 {
		t.Fatal("expected AU from IDR")
	}
	au := aus[len(aus)-1]
	if !au.Keyframe {
		t.Error("IDR should be keyframe")
	}
	// SPS and PPS should be prepended in AnnexB
	if !bytes.Contains(au.Data, sps) {
		t.Error("IDR AU should contain SPS")
	}
	if !bytes.Contains(au.Data, pps) {
		t.Error("IDR AU should contain PPS")
	}
}

func TestH264_STAPA(t *testing.T) {
	d := &H264Depacketizer{}

	nal1 := []byte{0x41, 0x01}
	nal2 := []byte{0x41, 0x02}

	// STAP-A header = 0x18 (NAL type 24), then 2-byte length + NAL for each unit
	payload := []byte{0x18}
	payload = append(payload, byte(len(nal1)>>8), byte(len(nal1)))
	payload = append(payload, nal1...)
	payload = append(payload, byte(len(nal2)>>8), byte(len(nal2)))
	payload = append(payload, nal2...)

	aus := d.Push(makeH264Pkt(payload, true))
	if len(aus) != 2 {
		t.Fatalf("STAP-A: want 2 AUs, got %d", len(aus))
	}
}

func TestH264_FUA_Fragmentation(t *testing.T) {
	d := &H264Depacketizer{}

	// Fragment an IDR NAL into 3 FU-A packets.
	// FU indicator: forbidden=0, NRI=3, type=28 → 0x7C
	// FU header start:  0x80 | nalType=5 → 0x85
	// FU header middle: 0x05
	// FU header end:    0x40 | 0x05 → 0x45

	fuInd := byte(0x7C)
	body := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}

	pkt1 := makeH264Pkt([]byte{fuInd, 0x85, body[0], body[1]}, false)
	pkt2 := makeH264Pkt([]byte{fuInd, 0x05, body[2], body[3]}, false)
	pkt3 := makeH264Pkt([]byte{fuInd, 0x45, body[4], body[5]}, true)

	aus1 := d.Push(pkt1)
	aus2 := d.Push(pkt2)
	aus3 := d.Push(pkt3)

	if len(aus1) != 0 || len(aus2) != 0 {
		t.Error("intermediate FU-A packets should not produce AUs")
	}
	if len(aus3) != 1 {
		t.Fatalf("final FU-A packet should produce 1 AU, got %d", len(aus3))
	}
	if !aus3[0].Keyframe {
		t.Error("reassembled IDR should be keyframe")
	}
	// The reassembled NAL body should contain all fragments.
	for _, b := range body {
		if !bytes.Contains(aus3[0].Data, []byte{b}) {
			t.Errorf("reassembled AU missing byte 0x%02X", b)
		}
	}
}

func TestH264_EmptyPayload(t *testing.T) {
	d := &H264Depacketizer{}
	aus := d.Push(makeH264Pkt([]byte{}, false))
	if len(aus) != 0 {
		t.Error("empty payload should produce no AUs")
	}
}

// ── H265Depacketizer ──────────────────────────────────────────────────────────

func makeH265Pkt(payload []byte, marker bool) *RTPPacket {
	return &RTPPacket{
		PayloadType: 98,
		Timestamp:   90000,
		Marker:      marker,
		Payload:     payload,
	}
}

func TestH265_VPS_SPS_PPS_IDR(t *testing.T) {
	d := &H265Depacketizer{}

	// NAL type 32 = VPS: header bytes = (32<<1) = 0x40, 0x01
	vps := []byte{0x40, 0x01, 0xAA, 0xBB}
	// NAL type 33 = SPS: header = 0x42, 0x01
	sps := []byte{0x42, 0x01, 0xCC, 0xDD}
	// NAL type 34 = PPS: header = 0x44, 0x01
	pps := []byte{0x44, 0x01, 0xEE, 0xFF}
	// NAL type 19 = IDR_W_RADL: header = 0x26, 0x01
	idr := []byte{0x26, 0x01, 0x11, 0x22}

	d.Push(makeH265Pkt(vps, false))
	d.Push(makeH265Pkt(sps, false))
	d.Push(makeH265Pkt(pps, false))

	if d.VPS == nil || d.SPS == nil || d.PPS == nil {
		t.Fatal("VPS/SPS/PPS should be stored")
	}

	aus := d.Push(makeH265Pkt(idr, true))
	if len(aus) == 0 {
		t.Fatal("IDR should produce AU")
	}
	au := aus[len(aus)-1]
	if !au.Keyframe {
		t.Error("IDR NAL type 19 should be keyframe")
	}
	if au.Codec != "h265" {
		t.Errorf("Codec = %q, want h265", au.Codec)
	}
}

func TestH265_FU_Fragmentation(t *testing.T) {
	d := &H265Depacketizer{}

	// NAL type 49 = FU: header bytes hdr0 = (49<<1) = 0x62, hdr1 = 0x01
	// FU header: start=0x80|nalType=19 → 0x93
	hdr0, hdr1 := byte(0x62), byte(0x01)
	body := []byte{0x11, 0x22, 0x33, 0x44}

	pkt1 := makeH265Pkt([]byte{hdr0, hdr1, 0x93, body[0], body[1]}, false) // start
	pkt2 := makeH265Pkt([]byte{hdr0, hdr1, 0x13, body[2]}, false)           // middle
	pkt3 := makeH265Pkt([]byte{hdr0, hdr1, 0x53, body[3]}, true)            // end (0x40|0x13)

	if len(d.Push(pkt1)) != 0 || len(d.Push(pkt2)) != 0 {
		t.Error("intermediate FU packets should not produce AUs")
	}
	aus := d.Push(pkt3)
	if len(aus) != 1 {
		t.Fatalf("final FU packet should produce 1 AU, got %d", len(aus))
	}
	if !aus[0].Keyframe {
		t.Error("reassembled IDR FU should be keyframe")
	}
}

func TestH265_TooShortPayload(t *testing.T) {
	d := &H265Depacketizer{}
	aus := d.Push(makeH265Pkt([]byte{0x62}, false)) // only 1 byte, need >= 2
	if len(aus) != 0 {
		t.Error("1-byte payload should produce no AUs")
	}
}
