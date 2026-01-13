package engine

import "errors"

var (
	// ErrCorruptBatch indicates the batch data is malformed.
	ErrCorruptBatch = errors.New("beachdb: corrupt batch")

	// ErrBadVersion indicates an unsupported batch encoding version.
	ErrBadVersion = errors.New("beachdb: unsupported batch version")

	// ErrUnknownOpType indicates an unknown operation type.
	ErrUnknownOpType = errors.New("beachdb: unknown operation type")

	// ErrTruncatedBatch indicates a truncated or incomplete batch.
	ErrTruncatedBatch = errors.New("beachdb: truncated batch")
)
