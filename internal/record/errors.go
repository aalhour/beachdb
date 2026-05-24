package record

import "errors"

var (
	// ErrCorruptRecord indicates when a record is corrupt.
	ErrCorruptRecord = errors.New("beachdb/record: corrupt record")

	// ErrBadHeader indicates when the header has an invalid length.
	// This occurs when len(header) != HeaderSize.
	ErrBadHeader = errors.New("beachdb/record: invalid header length")

	// ErrChecksum indicates that a checksum is not matched.
	ErrChecksum = errors.New("beachdb/record: checksum mismatch")

	// ErrTruncated indicates when a record is truncated or incomplete.
	ErrTruncated = errors.New("beachdb/record: truncated record")

	// ErrBadMagic indicates when the magic part in the header is not supported.
	ErrBadMagic = errors.New("beachdb/record: bad magic")

	// ErrUnsupportedVersion indicates when the version part in the header is not supported.
	ErrUnsupportedVersion = errors.New("beachdb/record: unsupported version")

	// ErrUnsupportedRecordType indicates when the record type part in the header is not supported.
	ErrUnsupportedRecordType = errors.New("beachdb/record: unsupported record type")

	// ErrRecordTooLarge indicates that a record exceeds the supported payload size.
	ErrRecordTooLarge = errors.New("beachdb/record: record payload too large")
)
