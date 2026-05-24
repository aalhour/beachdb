// Package wal implements the Write-Ahead Log for durability and crash recovery.
package wal

import (
	"errors"
)

var (
	// ErrWriterClosed indicates when the wal writer is closed.
	ErrWriterClosed = errors.New("beachdb/wal: writer is closed")

	// ErrReaderClosed indicates when the wal reader is closed.
	ErrReaderClosed = errors.New("beachdb/wal: reader is closed")
)
