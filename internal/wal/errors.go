// Package wal implements the Write-Ahead Log for durability and crash recovery.
package wal

import "errors"

var (
	// ErrCorruptRecord indicates when a record is corrupt.
	ErrCorruptRecord = errors.New("beachdb/wal: corrupt record")

	// ErrBadHeader indicates when the header has an invalid length.
	// This occurs when len(header) != recordHeaderSize (12 bytes).
	ErrBadHeader = errors.New("beachdb/wal: invalid header length")

	// ErrChecksum indicates that a checksum is not matched.
	ErrChecksum = errors.New("beachdb/wal: checksum mismatch")

	// ErrTruncated indicates when a record is truncated or incomplete.
	ErrTruncated = errors.New("beachdb/wal: truncated record")

	// ErrBadMagic indicates when the magic part in the header is not supported.
	ErrBadMagic = errors.New("beachdb/wal: bad magic")

	// ErrUnsupportedVersion indicates when the version part in the header is not supported.
	ErrUnsupportedVersion = errors.New("beachdb/wal: unsupported version")

	// ErrUnsupportedRecordType indicates when the record type part in the header is not supported.
	ErrUnsupportedRecordType = errors.New("beachdb/wal: unsupported record type")

	// ErrWriterClosed indicates when the wal writer is closed.
	ErrWriterClosed = errors.New("beachdb/wal: writer is closed")

	// ErrReaderClosed indicates when the wal reader is closed.
	ErrReaderClosed = errors.New("beachdb/wal: reader is closed")
)
