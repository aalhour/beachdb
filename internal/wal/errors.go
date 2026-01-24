// Package wal implements the Write-Ahead Log for durability and crash recovery.
package wal

import "errors"

var (
	// ErrCorruptRecord indicates when a record is corrupt
	ErrCorruptRecord = errors.New("wal: corrupt record")

	// ErrChecksum indicates that a checksum is not matched.
	ErrChecksum = errors.New("wal: checksum mismatch")

	// ErrTruncated indicates when a record is truncated or incomplete.
	ErrTruncated = errors.New("wal: truncated record")

	// ErrBadMagic indicates when the magic part in the header is not supported.
	ErrBadMagic = errors.New("wal: bad magic")

	// ErrUnsupportedVersion indicates when the version part in the header is not supported.
	ErrUnsupportedVersion = errors.New("wal: unsupported version")

	// ErrUnsupportedRecordType indicates when the record type part in the header is not supported.
	ErrUnsupportedRecordType = errors.New("wal: unsupported record type")

	// ErrWriterClosed indicates when the wal writer is closed.
	ErrWriterClosed = errors.New("wal: writer is closed")

	// ErrReaderClosed indicates when the wal reader is closed.
	ErrReaderClosed = errors.New("wal: reader is closed")
)
