package sstable

import "errors"

var (
	// ErrWriterClosed is returned when Add or Close is called on a closed writer.
	ErrWriterClosed = errors.New("beachdb/sstable: writer is closed")

	// ErrOutOfOrderKey is returned when Add receives a key that sorts before the previous key.
	ErrOutOfOrderKey = errors.New("beachdb/sstable: keys must be added in sorted order")

	// ErrInvalidBlockSize is returned when the configured block size is not usable.
	ErrInvalidBlockSize = errors.New("beachdb/sstable: block size is invalid")

	// ErrNilFile is returned when NewWriter is given a nil *os.File.
	ErrNilFile = errors.New("beachdb/sstable: file cannot be nil")

	// ErrFileTooSmall is returned when the file length is less than the footer size.
	ErrFileTooSmall = errors.New("beachdb/sstable: file is smaller than footer size")

	// ErrBadMagic is returned when the footer magic does not match the SSTable format.
	ErrBadMagic = errors.New("beachdb/sstable: invalid magic number")

	// ErrUnsupportedVersion is returned when the format version in the footer is unknown.
	ErrUnsupportedVersion = errors.New("beachdb/sstable: unsupported format version")

	// ErrCorruptFooter is returned when the footer checksum does not verify.
	ErrCorruptFooter = errors.New("beachdb/sstable: corrupt footer checksum")

	// ErrCorruptIndex is returned when the index block cannot be parsed or verified.
	ErrCorruptIndex = errors.New("beachdb/sstable: corrupt index block")

	// ErrCorruptBlock is returned when a data block cannot be parsed or verified.
	ErrCorruptBlock = errors.New("beachdb/sstable: corrupt data block")

	// ErrChecksumMismatch is returned when a block's CRC does not match its data.
	ErrChecksumMismatch = errors.New("beachdb/sstable: block checksum mismatch")

	// ErrKeyNotFound is returned when Get finds no entry for the key in the table.
	ErrKeyNotFound = errors.New("beachdb/sstable: key not found")
)
