package record

import (
	"fmt"

	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// Type identifies how a physical record contributes to a logical payload.
type Type byte

const (
	// TypeFull marks a complete payload stored in one physical record.
	TypeFull Type = 0x01

	// TypeFirst marks the first fragment of a multi-record payload.
	TypeFirst Type = 0x02

	// TypeMiddle marks an intermediate fragment of a multi-record payload.
	TypeMiddle Type = 0x03

	// TypeLast marks the final fragment of a multi-record payload.
	TypeLast Type = 0x04
)

const (
	// Version is the current shared record framing version.
	Version byte = 0x01

	// DefaultMaxPayloadSize is the default maximum payload size for one logical record.
	DefaultMaxPayloadSize uint32 = 64 << 20
)

// Layout sizes and offsets for the on-disk record header.
const (
	// Field sizes (in bytes).
	magicSize    uint32 = 8 // magic
	versionSize  uint32 = 1 // version
	typeSize     uint32 = 1 // record type
	lengthSize   uint32 = 4 // payload length
	checksumSize uint32 = 4 // payload checksum

	// Field offsets within the record header.
	magicOffset    uint32 = 0                           // magic
	versionOffset  uint32 = magicOffset + magicSize     // version
	typeOffset     uint32 = versionOffset + versionSize // record type
	lengthOffset   uint32 = typeOffset + typeSize       // payload length
	checksumOffset uint32 = lengthOffset + lengthSize   // payload checksum

	// HeaderSize is the on-disk size of the record header in bytes.
	// Layout: magic(8) + version(1) + type(1) + length(4) + checksum(4) = 18 bytes.
	HeaderSize = int(checksumOffset + checksumSize)
)

// Format describes the file-specific parts of the shared record framing.
type Format struct {
	// Magic identifies the kind of file using this record format.
	Magic [8]byte

	// MaxPayloadSize rejects records whose declared payload length is too large.
	MaxPayloadSize uint32
}

// NewFormat returns a Format with the given 8-byte magic and payload limit.
// A maxPayloadSize of 0 falls back to DefaultMaxPayloadSize.
func NewFormat(magic string, maxPayloadSize uint32) (*Format, error) {
	if len(magic) != int(magicSize) {
		return nil, fmt.Errorf("beachdb/record: magic must be exactly %d bytes, got %d", magicSize, len(magic))
	}

	var magicBytes [8]byte
	copy(magicBytes[:], magic)

	if maxPayloadSize == 0 {
		maxPayloadSize = DefaultMaxPayloadSize
	}

	return &Format{
		Magic:          magicBytes,
		MaxPayloadSize: maxPayloadSize,
	}, nil
}

// Header is the decoded fixed-size metadata that precedes each record payload.
type Header struct {
	// Type identifies whether the payload is complete or fragmented.
	Type Type

	// Length is the payload size in bytes.
	Length uint32

	// Checksum is the CRC32C checksum of the payload bytes.
	Checksum uint32
}

// Encode wraps payload bytes in the record envelope for this format.
// Returns ErrRecordTooLarge when len(payload) exceeds f.MaxPayloadSize.
func (f *Format) Encode(payload []byte) ([]byte, error) {
	//nolint:gosec // G115: len returns int; bounds-checked against MaxPayloadSize below.
	payloadLen := uint32(len(payload))
	if payloadLen > f.MaxPayloadSize {
		return nil, ErrRecordTooLarge
	}

	totalSize := HeaderSize + len(payload)
	buffer := make([]byte, totalSize)

	crc := checksum.CRC32C(payload)

	copy(buffer[magicOffset:magicOffset+magicSize], f.Magic[:])
	buffer[versionOffset] = Version
	buffer[typeOffset] = byte(TypeFull)
	coding.PutUint32(buffer[lengthOffset:lengthOffset+lengthSize], payloadLen)
	coding.PutUint32(buffer[checksumOffset:checksumOffset+checksumSize], crc)

	copy(buffer[HeaderSize:], payload)

	return buffer, nil
}

// DecodeHeader validates and decodes a fixed-size record header for this format.
func (f *Format) DecodeHeader(data []byte) (Header, error) {
	if len(data) < HeaderSize {
		return Header{}, ErrTruncated
	} else if len(data) > HeaderSize {
		return Header{}, ErrBadHeader
	}

	if string(data[magicOffset:magicOffset+magicSize]) != string(f.Magic[:]) {
		return Header{}, ErrBadMagic
	}

	ver := data[versionOffset]
	if ver != Version {
		return Header{}, ErrUnsupportedVersion
	}

	recordType := Type(data[typeOffset])
	if recordType != TypeFull {
		return Header{}, ErrUnsupportedRecordType
	}

	payloadLen := coding.Uint32(data[lengthOffset : lengthOffset+lengthSize])
	if payloadLen > f.MaxPayloadSize {
		return Header{}, ErrRecordTooLarge
	}
	crc := coding.Uint32(data[checksumOffset : checksumOffset+checksumSize])

	return Header{
		Type:     recordType,
		Length:   payloadLen,
		Checksum: crc,
	}, nil
}

// ValidatePayload verifies that payload matches expectedChecksum.
func ValidatePayload(payload []byte, expectedChecksum uint32) error {
	gotChecksum := checksum.CRC32C(payload)
	if gotChecksum != expectedChecksum {
		return ErrChecksum
	}
	return nil
}
