package manifest

import (
	"github.com/aalhour/beachdb/internal/record"
)

// manifestMagic is the 8-byte ASCII magic identifying BeachDB MANIFEST records.
const manifestMagic string = "BEACHMAN"

// manifestRecordFormat is the manifest-specific record framing format.
var manifestRecordFormat = mustNewManifestFormat()

// mustNewManifestFormat builds the manifest record format and panics on misconfiguration.
// The magic and size are compile-time constants so this can never fail in practice.
func mustNewManifestFormat() *record.Format {
	f, err := record.NewFormat(manifestMagic, record.DefaultMaxPayloadSize)
	if err != nil {
		panic(err)
	}
	return f
}

// EncodeRecord encodes a payload into a manifest record.
// Returns ErrRecordTooLarge when payload exceeds the format's MaxPayloadSize.
func EncodeRecord(payload []byte) ([]byte, error) {
	return manifestRecordFormat.Encode(payload)
}

// DecodeRecordHeader verifies a manifest record header and returns the payload length and checksum.
func DecodeRecordHeader(header []byte) (payloadLen uint32, crc uint32, err error) {
	hdr, err := manifestRecordFormat.DecodeHeader(header)
	if err != nil {
		return 0, 0, err
	}
	return hdr.Length, hdr.Checksum, nil
}

// ValidateRecord verifies that the payload's checksum matches the expected checksum.
func ValidateRecord(payload []byte, expectedChecksum uint32) error {
	return record.ValidatePayload(payload, expectedChecksum)
}
