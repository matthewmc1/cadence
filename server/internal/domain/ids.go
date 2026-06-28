package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewID returns a UUIDv7: a 48-bit big-endian millisecond timestamp followed by
// random bits, with the RFC 9562 version/variant nibbles set. v7 ids are
// time-ordered, which keeps primary-key b-tree inserts append-mostly and makes
// them friendly to range partitioning and index locality at scale.
func NewID() string {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// remaining 10 bytes random
	_, _ = rand.Read(b[6:])
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return FormatUUIDBytes(b)
}

// FormatUUIDBytes renders 16 bytes as a canonical hyphenated UUID string.
func FormatUUIDBytes(b [16]byte) string {
	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}
