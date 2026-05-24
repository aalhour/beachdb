// Package wal implements the Write-Ahead Log for durability and crash recovery.
package wal

import (
	"errors"

	"github.com/aalhour/beachdb/internal/record"
)

// Framing errors re-exported from the shared record package so existing
// callers (engine, cmd/wal_dump) keep working through wal.Err* aliases.
var (
	// ErrCorruptRecord indicates when a record is corrupt.
	ErrCorruptRecord = record.ErrCorruptRecord

	// ErrBadHeader indicates when the header has an invalid length.
	ErrBadHeader = record.ErrBadHeader

	// ErrChecksum indicates that a checksum is not matched.
	ErrChecksum = record.ErrChecksum

	// ErrTruncated indicates when a record is truncated or incomplete.
	ErrTruncated = record.ErrTruncated

	// ErrBadMagic indicates when the magic part in the header is not supported.
	ErrBadMagic = record.ErrBadMagic

	// ErrUnsupportedVersion indicates when the version part in the header is not supported.
	ErrUnsupportedVersion = record.ErrUnsupportedVersion

	// ErrUnsupportedRecordType indicates when the record type part in the header is not supported.
	ErrUnsupportedRecordType = record.ErrUnsupportedRecordType

	// ErrRecordTooLarge indicates that a record exceeds the supported payload size.
	ErrRecordTooLarge = record.ErrRecordTooLarge
)

// WAL lifecycle errors (not framing concerns).
var (
	// ErrWriterClosed indicates when the wal writer is closed.
	ErrWriterClosed = errors.New("beachdb/wal: writer is closed")

	// ErrReaderClosed indicates when the wal reader is closed.
	ErrReaderClosed = errors.New("beachdb/wal: reader is closed")
)
