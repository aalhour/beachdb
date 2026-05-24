package wal

import (
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

const (
	walMagic   string = "BEACHWAL" // 8-byte ASCII magic identifying BeachDB WAL records
	walVersion byte   = 0x01       // current WAL format version (v1)

	// RecordTypeFull marks a complete record (v1 never fragments).
	RecordTypeFull byte = 0x01

	// Field sizes (in bytes).
	walMagicSize    uint32 = 8 // magic
	walVersionSize  uint32 = 1 // version
	walTypeSize     uint32 = 1 // record type
	walLengthSize   uint32 = 4 // payload length
	walChecksumSize uint32 = 4 // payload checksum

	// Field offsets within the record header.
	walMagicOffset    uint32 = 0                                 // magic
	walVersionOffset  uint32 = walMagicOffset + walMagicSize     // version
	walTypeOffset     uint32 = walVersionOffset + walVersionSize // record type
	walLengthOffset   uint32 = walTypeOffset + walTypeSize       // payload length
	walChecksumOffset uint32 = walLengthOffset + walLengthSize   // payload checksum

	// recordHeaderSize is the on-disk size of the record header in bytes.
	// Layout: magic(8) + version(1) + type(1) + length(4) + checksum(4) = 18 bytes.
	recordHeaderSize = int(walChecksumOffset + walChecksumSize)

	// maxRecordPayloadSize caps record allocations during recovery.
	// v1 does not support fragmentation, so oversized batches are rejected.
	maxRecordPayloadSize = 64 << 20
)

// EncodeRecord encodes a payload into a WAL record.
// The payload can be nil or empty; both are valid.
func EncodeRecord(payload []byte) []byte {
	totalSize := recordHeaderSize + len(payload)
	buffer := make([]byte, totalSize)

	// Calculate checksum of payload
	crc := checksum.CRC32C(payload)

	// Write the header
	copy(buffer[walMagicOffset:walMagicOffset+walMagicSize], walMagic)
	buffer[walVersionOffset] = walVersion
	buffer[walTypeOffset] = RecordTypeFull
	//nolint:gosec // G115: len(payload) is bounded by maxRecordPayloadSize, overflow not possible in practice
	coding.PutUint32(buffer[walLengthOffset:walLengthOffset+walLengthSize], uint32(len(payload)))
	coding.PutUint32(buffer[walChecksumOffset:walChecksumOffset+walChecksumSize], crc)

	// Write the payload
	copy(buffer[recordHeaderSize:], payload)

	return buffer
}

// DecodeRecordHeader verifies a record header and returns the payload length and checksum.
func DecodeRecordHeader(header []byte) (payloadLen uint32, crc uint32, err error) {
	if len(header) < recordHeaderSize {
		return 0, 0, ErrTruncated
	} else if len(header) > recordHeaderSize {
		return 0, 0, ErrBadHeader
	}

	if string(header[walMagicOffset:walMagicOffset+walMagicSize]) != walMagic {
		return 0, 0, ErrBadMagic
	}

	version := header[walVersionOffset]
	if version != walVersion {
		return 0, 0, ErrUnsupportedVersion
	}

	recordType := header[walTypeOffset]
	if recordType != RecordTypeFull {
		return 0, 0, ErrUnsupportedRecordType
	}

	payloadLen = coding.Uint32(header[walLengthOffset : walLengthOffset+walLengthSize])
	if payloadLen > maxRecordPayloadSize {
		return 0, 0, ErrRecordTooLarge
	}
	crc = coding.Uint32(header[walChecksumOffset : walChecksumOffset+walChecksumSize])

	return payloadLen, crc, nil
}

// ValidateRecord verifies that the payload's checksum matches the expected checksum.
func ValidateRecord(payload []byte, expectedChecksum uint32) error {
	gotChecksum := checksum.CRC32C(payload)
	if gotChecksum != expectedChecksum {
		return ErrChecksum
	}
	return nil
}
