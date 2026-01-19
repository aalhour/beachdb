package wal

import (
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

const (
	walMagic         = 0xBEAC // BEACH truncated ;)
	walVersion       = 0x01   // v1
	recordHeaderSize = 12

	// RecordTypeFull indicates a complete record.
	RecordTypeFull byte = 0x01
)

func EncodeRecord(payload []byte) []byte {
	totalSize := recordHeaderSize + len(payload)
	buffer := make([]byte, totalSize)

	// Calculate checksum of payload
	checksum := checksum.CRC32C(payload)

	// Write the header
	coding.PutUint16(buffer[0:], walMagic)
	buffer[2] = walVersion
	buffer[3] = RecordTypeFull
	coding.PutUint32(buffer[4:], uint32(len(payload)))
	coding.PutUint32(buffer[8:], checksum)

	// Write the payload
	copy(buffer[12:], payload)

	return buffer
}
