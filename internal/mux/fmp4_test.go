package mux

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ── Box primitives ────────────────────────────────────────────────────────────

func TestBox_SizeAndFourCC(t *testing.T) {
	b := box("test", []byte{0x01, 0x02})
	if len(b) != 10 {
		t.Fatalf("box len = %d, want 10", len(b))
	}
	size := binary.BigEndian.Uint32(b[0:4])
	if size != 10 {
		t.Errorf("box size field = %d, want 10", size)
	}
	if string(b[4:8]) != "test" {
		t.Errorf("fourcc = %q, want \"test\"", b[4:8])
	}
	if b[8] != 0x01 || b[9] != 0x02 {
		t.Error("payload mismatch")
	}
}

func TestFullBox_VersionAndFlags(t *testing.T) {
	b := fullbox("abcd", 1, 0x000305, []byte{0xFF})
	// fullbox: 4(size) + 4(fourcc) + 1(version) + 3(flags) + payload
	if len(b) != 13 {
		t.Fatalf("fullbox len = %d, want 13", len(b))
	}
	version := b[8]
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	flags := uint32(b[9])<<16 | uint32(b[10])<<8 | uint32(b[11])
	if flags != 0x000305 {
		t.Errorf("flags = 0x%06X, want 0x000305", flags)
	}
}

// ── CodecString ───────────────────────────────────────────────────────────────

func TestCodecString_H264(t *testing.T) {
	// Baseline SPS: profile=0x42, constraint=0xC0, level=0x28
	sps := []byte{0x67, 0x42, 0xC0, 0x28}
	cs := CodecString("h264", sps)
	want := "avc1.42C028"
	if cs != want {
		t.Errorf("CodecString = %q, want %q", cs, want)
	}
}

func TestCodecString_H264_ShortSPS(t *testing.T) {
	cs := CodecString("h264", []byte{0x67})
	if cs != "avc1.42E01E" {
		t.Errorf("short SPS fallback = %q, want avc1.42E01E", cs)
	}
}

func TestCodecString_H265(t *testing.T) {
	cs := CodecString("h265", nil)
	if cs != "hvc1.1.6.L93.B0" {
		t.Errorf("H265 codec string = %q, want hvc1.1.6.L93.B0", cs)
	}
}

// ── AnnexBtoAVCC ─────────────────────────────────────────────────────────────

func TestAnnexBtoAVCC_ThreeStartCode(t *testing.T) {
	// 3-byte start code
	annexB := []byte{0x00, 0x00, 0x01, 0x41, 0x42, 0x43}
	avcc := AnnexBtoAVCC(annexB)
	// Expect: 4-byte length (3) + 3 bytes
	if len(avcc) != 7 {
		t.Fatalf("AVCC len = %d, want 7", len(avcc))
	}
	size := binary.BigEndian.Uint32(avcc[0:4])
	if size != 3 {
		t.Errorf("AVCC length prefix = %d, want 3", size)
	}
	if !bytes.Equal(avcc[4:], []byte{0x41, 0x42, 0x43}) {
		t.Error("AVCC payload mismatch")
	}
}

func TestAnnexBtoAVCC_FourByteStartCode(t *testing.T) {
	// 4-byte start code
	annexB := []byte{0x00, 0x00, 0x00, 0x01, 0xAA, 0xBB}
	avcc := AnnexBtoAVCC(annexB)
	if len(avcc) != 6 {
		t.Fatalf("AVCC len = %d, want 6", len(avcc))
	}
	size := binary.BigEndian.Uint32(avcc[0:4])
	if size != 2 {
		t.Errorf("AVCC length prefix = %d, want 2", size)
	}
}

func TestAnnexBtoAVCC_MultipleNALs(t *testing.T) {
	// Two NALs separated by 4-byte start code
	annexB := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x01, // SPS
		0x00, 0x00, 0x00, 0x01, 0x68, 0x02, // PPS
	}
	avcc := AnnexBtoAVCC(annexB)
	// Each NAL is 2 bytes → 4+2 + 4+2 = 12 bytes
	if len(avcc) != 12 {
		t.Fatalf("two-NAL AVCC len = %d, want 12", len(avcc))
	}
}

func TestAnnexBtoAVCC_Empty(t *testing.T) {
	if out := AnnexBtoAVCC(nil); len(out) != 0 {
		t.Errorf("empty annexB should produce empty AVCC, got %v", out)
	}
}

// ── InitSegment ───────────────────────────────────────────────────────────────

func TestInitSegment_AVC_ContainsFtypAndMoov(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xC0, 0x28, 0xDA, 0x01}
	pps := []byte{0x68, 0xCE, 0x38, 0x80}
	seg := InitSegment("h264", sps, pps, nil)

	if !bytes.Contains(seg, []byte("ftyp")) {
		t.Error("init segment should contain ftyp box")
	}
	if !bytes.Contains(seg, []byte("moov")) {
		t.Error("init segment should contain moov box")
	}
	if !bytes.Contains(seg, []byte("avc1")) {
		t.Error("init segment should contain avc1 box")
	}
	if !bytes.Contains(seg, []byte("avcC")) {
		t.Error("init segment should contain avcC box")
	}
	// SPS bytes should appear verbatim inside avcC
	if !bytes.Contains(seg, sps) {
		t.Error("init segment should contain raw SPS bytes")
	}
}

func TestInitSegment_HEVC_ContainsHvc1(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0C}
	sps := []byte{0x42, 0x01, 0x01}
	pps := []byte{0x44, 0x01, 0xC0}
	seg := InitSegment("h265", sps, pps, vps)

	if !bytes.Contains(seg, []byte("hvc1")) {
		t.Error("HEVC init segment should contain hvc1 box")
	}
	if !bytes.Contains(seg, []byte("hvcC")) {
		t.Error("HEVC init segment should contain hvcC box")
	}
}

// ── MediaSegment ──────────────────────────────────────────────────────────────

func TestMediaSegment_ContainsMoofAndMdat(t *testing.T) {
	data := []byte{0x00, 0x00, 0x00, 0x04, 0x65, 0x88, 0x00, 0x00} // fake AVCC IDR
	seg := MediaSegment(1, 90000, 3000, true, data)

	if !bytes.Contains(seg, []byte("moof")) {
		t.Error("media segment should contain moof box")
	}
	if !bytes.Contains(seg, []byte("mdat")) {
		t.Error("media segment should contain mdat box")
	}
	if !bytes.Contains(seg, []byte("mfhd")) {
		t.Error("moof should contain mfhd box")
	}
	if !bytes.Contains(seg, []byte("traf")) {
		t.Error("moof should contain traf box")
	}
	// mdat payload should match our input
	if !bytes.Contains(seg, data) {
		t.Error("mdat should contain the AVCC data verbatim")
	}
}

func TestMediaSegment_DefaultDuration(t *testing.T) {
	// Passing dur=0 should use defaultDuration without panic.
	seg := MediaSegment(1, 0, 0, false, []byte{0x00, 0x00, 0x00, 0x01, 0x41})
	if len(seg) == 0 {
		t.Error("media segment should not be empty")
	}
}

func TestMediaSegment_SequenceNumber(t *testing.T) {
	// Sequence number 42 should appear in the mfhd box (big-endian uint32).
	seg := MediaSegment(42, 0, 3000, false, []byte{0x01})
	seqBytes := []byte{0x00, 0x00, 0x00, 0x2A} // 42 big-endian
	if !bytes.Contains(seg, seqBytes) {
		t.Error("mfhd sequence number 42 not found in segment")
	}
}

// ── SplitAnnexB ───────────────────────────────────────────────────────────────

func TestSplitAnnexB_Three(t *testing.T) {
	data := []byte{0x00, 0x00, 0x01, 0xAA, 0xBB}
	nals := SplitAnnexB(data)
	if len(nals) != 1 {
		t.Fatalf("want 1 NAL, got %d", len(nals))
	}
	if !bytes.Equal(nals[0], []byte{0xAA, 0xBB}) {
		t.Errorf("NAL = %v, want [AA BB]", nals[0])
	}
}

func TestSplitAnnexB_TwoNALs_FourByteStart(t *testing.T) {
	data := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42,
		0x00, 0x00, 0x00, 0x01, 0x68, 0xCE,
	}
	nals := SplitAnnexB(data)
	if len(nals) != 2 {
		t.Fatalf("want 2 NALs, got %d", len(nals))
	}
	if !bytes.Equal(nals[0], []byte{0x67, 0x42}) {
		t.Errorf("NAL[0] = %v", nals[0])
	}
	if !bytes.Equal(nals[1], []byte{0x68, 0xCE}) {
		t.Errorf("NAL[1] = %v", nals[1])
	}
}

func TestSplitAnnexB_Empty(t *testing.T) {
	if nals := SplitAnnexB(nil); len(nals) != 0 {
		t.Errorf("expected no NALs from empty input, got %d", len(nals))
	}
}
