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

// EncodeRecord takes a payload and returns an encoded WAL record
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

// DecodeRecordHeader returns verifies a record header and returns checksum and payload's length
func DecodeRecordHeader(header []byte) (payloadLen uint32, checksum uint32, err error) {
	if len(header) < recordHeaderSize {
		return 0, 0, ErrTruncated
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

// ValidateRecord verifies the checksum given a payload with expected checksum
func ValidateRecord(payload []byte, expectedChecksum uint32) error {
	gotChecksum := checksum.CRC32C(payload)
	if gotChecksum != expectedChecksum {
		return ErrChecksum
	}
	return nil
}
