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

	// ErrDBClosed indicates that the database is already closed.
	ErrDBClosed = errors.New("beachdb: database already closed")

	// ErrKeyNotFound indicates that a key doesn't exist in the database.
	ErrKeyNotFound = errors.New("beachdb: key not found")

	// ErrCreatingSSTFile indicates that an error occurred while creating a new sstable file.
	ErrCreatingSSTFile = errors.New("beachdb: couldn't create sstable file")

	// ErrInvalidMemtableFlushSize indicates that the size specified for flushing memtable is invalid.
	ErrInvalidMemtableFlushSize = errors.New("beachdb: invalid memtable size flush trigger")

	// ErrInvalidSSTBlockSize indicates that the SSTable block size is invalid.
	ErrInvalidSSTBlockSize = errors.New("beachdb: invalid SSTable block size, must be >= 0")

	// ErrFlushEmptyMemtable indicates an attempt to flush an empty memtable,
	// which would record a zero-value key range in the manifest.
	ErrFlushEmptyMemtable = errors.New("beachdb: refusing to flush empty memtable")
)
