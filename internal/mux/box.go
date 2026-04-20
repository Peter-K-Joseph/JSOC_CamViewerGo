package mux

import "encoding/binary"

// ── Primitive writers ────────────────────────────────────────────────────────

func u8(v uint8) []byte { return []byte{v} }

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func zeros(n int) []byte { return make([]byte, n) }

func cat(parts ...[]byte) []byte {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]byte, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// box writes a standard ISO BMFF box: size(4) + fourcc(4) + payload.
func box(fourcc string, payload []byte) []byte {
	size := uint32(8 + len(payload))
	return cat(u32(size), []byte(fourcc), payload)
}

// fullbox writes a full box: size(4) + fourcc(4) + version(1) + flags(3) + payload.
func fullbox(fourcc string, version uint8, flags uint32, payload []byte) []byte {
	vf := cat(u8(version), u8(uint8(flags>>16)), u8(uint8(flags>>8)), u8(uint8(flags)))
	return box(fourcc, cat(vf, payload))
}
