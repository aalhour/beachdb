package wal

import (
	"github.com/aalhour/beachdb/internal/record"
)

// walMagic is the 8-byte ASCII magic identifying BeachDB WAL records.
const walMagic string = "BEACHWAL"

// walRecordFormat is the WAL-specific record framing format.
var walRecordFormat = mustNewWALFormat()

// mustNewWALFormat builds the WAL record format and panics on misconfiguration.
// The magic and size are compile-time constants so this can never fail in practice.
func mustNewWALFormat() *record.Format {
	f, err := record.NewFormat(walMagic, record.DefaultMaxPayloadSize)
	if err != nil {
		panic(err)
	}
	return f
}

// EncodeRecord encodes a payload into a WAL record.
// Returns ErrRecordTooLarge when payload exceeds the format's MaxPayloadSize.
func EncodeRecord(payload []byte) ([]byte, error) {
	return walRecordFormat.Encode(payload)
}

// DecodeRecordHeader verifies a record header and returns the payload length and checksum.
func DecodeRecordHeader(header []byte) (payloadLen uint32, crc uint32, err error) {
	hdr, err := walRecordFormat.DecodeHeader(header)
	if err != nil {
		return 0, 0, err
	}
	return hdr.Length, hdr.Checksum, nil
}

// ValidateRecord verifies that the payload's checksum matches the expected checksum.
func ValidateRecord(payload []byte, expectedChecksum uint32) error {
	return record.ValidatePayload(payload, expectedChecksum)
}
