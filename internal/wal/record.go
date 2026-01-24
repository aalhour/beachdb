package wal

import (
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

const (
	// walMagic is the magic number identifying BeachDB WAL records.
	// Value: 0xBEAC (big-endian: 0xBE 0xAC, "BEach" truncated).
	walMagic = 0xBEAC

	// walVersion is the current WAL format version.
	walVersion = 0x01 // v1

	// recordHeaderSize is the size of a WAL record header in bytes.
	recordHeaderSize = 12

	// RecordTypeFull indicates a complete record.
	RecordTypeFull byte = 0x01
)

// EncodeRecord encodes a payload into a WAL record.
// The payload can be nil or empty; both are valid.
func EncodeRecord(payload []byte) []byte {
	totalSize := recordHeaderSize + len(payload)
	buffer := make([]byte, totalSize)

	// Calculate checksum of payload
	checksum := checksum.CRC32C(payload)

	// Write the header
	coding.PutUint16(buffer[0:], walMagic)
	buffer[2] = walVersion
	buffer[3] = RecordTypeFull
	//nolint:gosec // G115: len(payload) is bounded by system limits, overflow not possible in practice
	coding.PutUint32(buffer[4:], uint32(len(payload)))
	coding.PutUint32(buffer[8:], checksum)

	// Write the payload
	copy(buffer[12:], payload)

	return buffer
}

// DecodeRecordHeader verifies a record header and returns the payload length and checksum.
func DecodeRecordHeader(header []byte) (payloadLen uint32, checksum uint32, err error) {
	if len(header) < recordHeaderSize {
		return 0, 0, ErrTruncated
	} else if len(header) > recordHeaderSize {
		return 0, 0, ErrBadHeader
	}

	magic := coding.Uint16(header[0:2])
	if magic != walMagic {
		return 0, 0, ErrBadMagic
	}

	version := header[2]
	if version != walVersion {
		return 0, 0, ErrUnsupportedVersion
	}

	recordType := header[3]
	if recordType != RecordTypeFull {
		return 0, 0, ErrUnsupportedRecordType
	}

	payloadLen = coding.Uint32(header[4:8])
	checksum = coding.Uint32(header[8:])

	return payloadLen, checksum, nil
}

// ValidateRecord verifies that the payload's checksum matches the expected checksum.
func ValidateRecord(payload []byte, expectedChecksum uint32) error {
	gotChecksum := checksum.CRC32C(payload)
	if gotChecksum != expectedChecksum {
		return ErrChecksum
	}
	return nil
}
