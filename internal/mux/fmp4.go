// Package mux produces fragmented MP4 (fMP4) segments consumed by the browser MSE API.
package mux

import "fmt"

const (
	timescale      = 90000 // matches RTP clock rate
	defaultDuration = 3000  // 90000/30fps — overridden by real timestamp delta
)

// ── Public API ───────────────────────────────────────────────────────────────

// CodecString returns the MSE codec string derived from SPS bytes.
// sps must be the raw SPS NAL unit (first byte = 0x67 for H.264).
func CodecString(codec string, sps []byte) string {
	if codec == "h265" {
		return hevcCodecString(sps)
	}
	if len(sps) >= 4 {
		return fmt.Sprintf("avc1.%02X%02X%02X", sps[1], sps[2], sps[3])
	}
	return "avc1.42E01E"
}

// hevcCodecString builds an hvc1 codec string from the raw H.265 SPS NAL.
// Format: hvc1.<profile>.<profile_compat_hex>.<tier><level>
// Falls back to a safe default if the SPS is too short.
func hevcCodecString(sps []byte) string {
	// H.265 SPS NAL has a 2-byte NAL header, then profile_tier_level starts
	// at byte offset 2 (after skipping sps_video_parameter_set_id and
	// sps_max_sub_layers_minus1 packed in the first nibble).
	// The raw SPS layout (byte offsets from NAL start):
	//   [0:2]  NAL header
	//   [2]    vps_id(4 bits) | max_sub_layers_minus1(3 bits) | temporal_id_nesting(1 bit)
	//   [3]    general_profile_space(2) | general_tier_flag(1) | general_profile_idc(5)
	//   [4:8]  general_profile_compatibility_flags (32 bits)
	//   [8:14] general_constraint_indicator_flags (48 bits)
	//   [14]   general_level_idc
	if len(sps) < 15 {
		return "hvc1.1.6.L93.B0"
	}
	profileIdc := int(sps[3] & 0x1f)
	tierFlag := (sps[3] >> 5) & 0x01
	compatFlags := uint32(sps[4])<<24 | uint32(sps[5])<<16 | uint32(sps[6])<<8 | uint32(sps[7])
	levelIdc := int(sps[14])

	tier := "L"
	if tierFlag == 1 {
		tier = "H"
	}
	// Reverse bits of compatFlags for the codec string (ISO 14496-15 convention).
	var rev uint32
	for i := 0; i < 32; i++ {
		rev |= ((compatFlags >> uint(i)) & 1) << uint(31-i)
	}
	return fmt.Sprintf("hvc1.%d.%X.%s%d.B0", profileIdc, rev, tier, levelIdc)
}

// MIMEType returns the full MSE MIME type string.
func MIMEType(codec string, sps []byte) string {
	return fmt.Sprintf(`video/mp4; codecs="%s"`, CodecString(codec, sps))
}

// InitSegment creates the ftyp + moov init segment from codec parameter sets.
func InitSegment(codec string, sps, pps, vps []byte) []byte {
	if codec == "h265" {
		return cat(ftypHEVC(), moovHEVC(sps, pps, vps))
	}
	return cat(ftypAVC(), moovAVC(sps, pps))
}

// MediaSegment creates a moof + mdat pair for one access unit.
// avccData must be in AVCC format (4-byte length-prefixed NALs, not AnnexB).
// dts is the decode timestamp in timescale units.
// dur is the sample duration in timescale units (pass 0 to use default).
func MediaSegment(seq uint32, dts uint64, dur uint32, keyframe bool, avccData []byte) []byte {
	if dur == 0 {
		dur = defaultDuration
	}
	mf := buildMoof(seq, dts, dur, keyframe, len(avccData))
	md := box("mdat", avccData)
	return cat(mf, md)
}

// AnnexBtoAVCC converts AnnexB start-code NAL units to AVCC 4-byte-length-prefix format.
func AnnexBtoAVCC(annexB []byte) []byte {
	nals := SplitAnnexB(annexB)
	var out []byte
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		out = append(out, u32(uint32(len(nal)))...)
		out = append(out, nal...)
	}
	return out
}

// ── ftyp ─────────────────────────────────────────────────────────────────────

func ftypAVC() []byte {
	return box("ftyp", cat(
		[]byte("isom"), // major brand
		u32(0x200),     // minor version
		[]byte("isom"), []byte("iso5"), []byte("iso6"), []byte("mp41"),
	))
}

func ftypHEVC() []byte {
	return box("ftyp", cat(
		[]byte("isom"),
		u32(0x200),
		[]byte("isom"), []byte("iso5"), []byte("iso6"), []byte("hvc1"),
	))
}

// ── moov ─────────────────────────────────────────────────────────────────────

func moovAVC(sps, pps []byte) []byte {
	return box("moov", cat(mvhd(), trakAVC(sps, pps), mvex()))
}

func moovHEVC(sps, pps, vps []byte) []byte {
	return box("moov", cat(mvhd(), trakHEVC(sps, pps, vps), mvex()))
}

func mvhd() []byte {
	// version=0
	return fullbox("mvhd", 0, 0, cat(
		u32(0),          // creation_time
		u32(0),          // modification_time
		u32(timescale),  // timescale
		u32(0),          // duration = 0 (unknown/live)
		u32(0x00010000), // rate 1.0
		u16(0x0100),     // volume 1.0
		zeros(10),       // reserved
		// identity matrix
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		zeros(24),       // pre-defined
		u32(2),          // next-track-ID
	))
}

func mvex() []byte {
	return box("mvex", trex())
}

func trex() []byte {
	return fullbox("trex", 0, 0, cat(
		u32(1), // track_ID
		u32(1), // default_sample_description_index
		u32(0), // default_sample_duration
		u32(0), // default_sample_size
		u32(0), // default_sample_flags
	))
}

// ── trak ─────────────────────────────────────────────────────────────────────

func trakAVC(sps, pps []byte) []byte {
	return box("trak", cat(tkhd(), mdiaAVC(sps, pps)))
}

func trakHEVC(sps, pps, vps []byte) []byte {
	return box("trak", cat(tkhd(), mdiaHEVC(sps, pps, vps)))
}

func tkhd() []byte {
	// version=0, flags=3 (track enabled + in movie)
	return fullbox("tkhd", 0, 3, cat(
		u32(0), // creation_time
		u32(0), // modification_time
		u32(1), // track_ID
		u32(0), // reserved
		u32(0), // duration = 0 (live)
		zeros(8),
		u16(0),  // layer
		u16(0),  // alternate_group
		u16(0),  // volume (video = 0)
		u16(0),  // reserved
		// identity matrix
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		u32(0), // width  (will be overridden by SPS)
		u32(0), // height
	))
}

func mdiaAVC(sps, pps []byte) []byte {
	return box("mdia", cat(mdhd(), hdlr(), minfAVC(sps, pps)))
}

func mdiaHEVC(sps, pps, vps []byte) []byte {
	return box("mdia", cat(mdhd(), hdlr(), minfHEVC(sps, pps, vps)))
}

func mdhd() []byte {
	return fullbox("mdhd", 0, 0, cat(
		u32(0),         // creation_time
		u32(0),         // modification_time
		u32(timescale), // timescale
		u32(0),         // duration = 0 (live)
		u16(0x55C4),    // language = 'und'
		u16(0),         // pre-defined
	))
}

func hdlr() []byte {
	return fullbox("hdlr", 0, 0, cat(
		u32(0),             // pre-defined
		[]byte("vide"),     // handler_type
		zeros(12),          // reserved
		[]byte("VideoHandler\x00"),
	))
}

func minfAVC(sps, pps []byte) []byte {
	return box("minf", cat(vmhd(), dinf(), stblAVC(sps, pps)))
}

func minfHEVC(sps, pps, vps []byte) []byte {
	return box("minf", cat(vmhd(), dinf(), stblHEVC(sps, pps, vps)))
}

func vmhd() []byte {
	return fullbox("vmhd", 0, 1, cat(u16(0), zeros(6)))
}

func dinf() []byte {
	urlBox := fullbox("url ", 0, 1, nil) // self-contained
	dref := fullbox("dref", 0, 0, cat(u32(1), urlBox))
	return box("dinf", dref)
}

// ── stbl ─────────────────────────────────────────────────────────────────────

func stblAVC(sps, pps []byte) []byte {
	return box("stbl", cat(stsdAVC(sps, pps), stts(), stsc(), stsz(), stco()))
}

func stblHEVC(sps, pps, vps []byte) []byte {
	return box("stbl", cat(stsdHEVC(sps, pps, vps), stts(), stsc(), stsz(), stco()))
}

func stts() []byte { return fullbox("stts", 0, 0, cat(u32(0))) }
func stsc() []byte { return fullbox("stsc", 0, 0, cat(u32(0))) }
func stsz() []byte { return fullbox("stsz", 0, 0, cat(u32(0), u32(0))) }
func stco() []byte { return fullbox("stco", 0, 0, cat(u32(0))) }

func stsdAVC(sps, pps []byte) []byte {
	avc1 := avc1Box(sps, pps)
	return fullbox("stsd", 0, 0, cat(u32(1), avc1))
}

func stsdHEVC(sps, pps, vps []byte) []byte {
	hvc1 := hvc1Box(sps, pps, vps)
	return fullbox("stsd", 0, 0, cat(u32(1), hvc1))
}

// ── avc1 / avcC ──────────────────────────────────────────────────────────────

func avc1Box(sps, pps []byte) []byte {
	avcc := avcCBox(sps, pps)
	payload := cat(
		zeros(6),        // reserved
		u16(1),          // data_reference_index
		zeros(16),       // pre_defined + reserved
		u16(1920),       // width  (browser reads from SPS, so value doesn't matter)
		u16(1080),       // height
		u32(0x00480000), // horizresolution 72 dpi
		u32(0x00480000), // vertresolution 72 dpi
		u32(0),          // reserved
		u16(1),          // frame_count
		zeros(32),       // compressorname
		u16(0x0018),     // depth
		u16(0xFFFF),     // pre_defined
		avcc,
	)
	return box("avc1", payload)
}

func avcCBox(sps, pps []byte) []byte {
	profile := uint8(0x42)
	compat := uint8(0x00)
	level := uint8(0x1E)
	if len(sps) >= 4 {
		profile = sps[1]
		compat = sps[2]
		level = sps[3]
	}
	payload := cat(
		u8(1),           // configurationVersion
		u8(profile),
		u8(compat),
		u8(level),
		u8(0xFF),        // lengthSizeMinusOne = 3 (4-byte lengths)
		u8(0xE1),        // numSequenceParameterSets = 1
		u16(uint16(len(sps))),
		sps,
		u8(1),           // numPictureParameterSets
		u16(uint16(len(pps))),
		pps,
	)
	return box("avcC", payload)
}

// ── hvc1 / hvcC ──────────────────────────────────────────────────────────────

func hvc1Box(sps, pps, vps []byte) []byte {
	hvcc := hvcCBox(sps, pps, vps)
	payload := cat(
		zeros(6),
		u16(1),
		zeros(16),
		u16(1920),
		u16(1080),
		u32(0x00480000),
		u32(0x00480000),
		u32(0),
		u16(1),
		zeros(32),
		u16(0x0018),
		u16(0xFFFF),
		hvcc,
	)
	return box("hvc1", payload)
}

func hvcCBox(sps, pps, vps []byte) []byte {
	// Minimal hvcC — browsers parse from bitstream anyway
	var nals []byte
	// VPS array
	nals = append(nals, hvcArrayEntry(0x20, vps)...)
	// SPS array
	nals = append(nals, hvcArrayEntry(0x21, sps)...)
	// PPS array
	nals = append(nals, hvcArrayEntry(0x22, pps)...)

	payload := cat(
		u8(1),      // configurationVersion
		u8(0),      // general_profile_space(2)|general_tier_flag(1)|general_profile_idc(5)
		u32(0),     // general_profile_compatibility_flags
		zeros(6),   // general_constraint_indicator_flags
		u8(0),      // general_level_idc
		u16(0xF000), // min_spatial_segmentation_idc (reserved 0xF000)
		u8(0xFC),   // parallelismType (reserved 0xFC)
		u8(0xFD),   // chromaFormat (reserved 0xFC | chroma_format_idc)
		u8(0xF8),   // bitDepthLumaMinus8 (reserved 0xF8)
		u8(0xF8),   // bitDepthChromaMinus8
		u16(0),     // avgFrameRate
		u8(0x0F),   // constantFrameRate(2)|numTemporalLayers(3)|temporalIdNested(1)|lengthSizeMinusOne(2) = 0x0F
		u8(3),      // numOfArrays
	)
	payload = cat(payload, nals)
	return box("hvcC", payload)
}

func hvcArrayEntry(nalType uint8, nal []byte) []byte {
	return cat(
		u8(nalType), // array_completeness(1)|reserved(1)|nal_unit_type(6)
		u16(1),      // numNalus
		u16(uint16(len(nal))),
		nal,
	)
}

// ── moof + mdat ──────────────────────────────────────────────────────────────

func buildMoof(seq uint32, dts uint64, dur uint32, keyframe bool, dataLen int) []byte {
	mfhd := buildMFHD(seq)
	traf := buildTraf(dts, dur, keyframe, dataLen, 0) // placeholder dataOffset

	// Compute actual moof size to set correct data_offset
	moofSize := 8 + len(mfhd) + 8 + len(buildTfhd()) + len(buildTfdt(dts)) + len(buildTrun(dur, keyframe, dataLen, 0))
	dataOffset := int32(moofSize + 8) // +8 for mdat header

	// Rebuild traf with correct data_offset
	traf = buildTraf(dts, dur, keyframe, dataLen, dataOffset)
	return box("moof", cat(mfhd, traf))
}

func buildMFHD(seq uint32) []byte {
	return fullbox("mfhd", 0, 0, u32(seq))
}

func buildTraf(dts uint64, dur uint32, keyframe bool, dataLen int, dataOffset int32) []byte {
	return box("traf", cat(
		buildTfhd(),
		buildTfdt(dts),
		buildTrun(dur, keyframe, dataLen, dataOffset),
	))
}

func buildTfhd() []byte {
	// flags = 0x020000 (default-base-is-moof)
	return fullbox("tfhd", 0, 0x020000, u32(1)) // track_ID = 1
}

func buildTfdt(dts uint64) []byte {
	// version=1 → 8-byte baseMediaDecodeTime
	return fullbox("tfdt", 1, 0, u64(dts))
}

func buildTrun(dur uint32, keyframe bool, dataLen int, dataOffset int32) []byte {
	// flags: 0x000001(data-offset) | 0x000004(first-sample-flags) | 0x000100(sample-duration) | 0x000200(sample-size)
	const flags = uint32(0x000305)

	var sampleFlags uint32
	if keyframe {
		sampleFlags = 0x02000000 // sync sample
	} else {
		sampleFlags = 0x01010000 // non-sync, depends on others
	}

	// data_offset is signed int32
	doff := u32(uint32(dataOffset))

	return fullbox("trun", 0, flags, cat(
		u32(1),          // sample_count
		doff,            // data_offset
		u32(sampleFlags), // first_sample_flags
		u32(dur),        // sample_duration
		u32(uint32(dataLen)), // sample_size
	))
}

// ── helpers ───────────────────────────────────────────────────────────────────

// SplitAnnexB splits an AnnexB byte stream into raw NAL units (start codes stripped).
func SplitAnnexB(data []byte) [][]byte {
	var nals [][]byte
	start := -1
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 {
			if data[i+2] == 1 {
				if start >= 0 {
					nals = append(nals, data[start:i])
				}
				start = i + 3
				i += 3
				continue
			}
			if i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1 {
				if start >= 0 {
					nals = append(nals, data[start:i])
				}
				start = i + 4
				i += 4
				continue
			}
		}
		i++
	}
	if start >= 0 && start < len(data) {
		nals = append(nals, data[start:])
	}
	return nals
}
