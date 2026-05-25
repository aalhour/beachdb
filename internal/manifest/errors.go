package manifest

import "errors"

var (
	// ErrUnknownTag denotes that a tag in the version edit is not supported.
	ErrUnknownTag = errors.New("beachdb/manifest: unknown version edit tag")

	// ErrTruncatedEdit denotes that a version edit body ran out of bytes
	// mid-field during decode. The framing layer (record) already verified
	// the payload length and checksum, so a short read here means the body
	// itself is malformed — not the file. Parallels engine.ErrTruncatedBatch.
	ErrTruncatedEdit = errors.New("beachdb/manifest: truncated version edit body")

	// ErrEditFieldTooLarge denotes that a length-prefixed field in a version
	// edit declared a size beyond what v1 accepts. Caps untrusted length
	// prefixes so corruption can't trigger a multi-GiB allocation.
	ErrEditFieldTooLarge = errors.New("beachdb/manifest: version edit field exceeds maximum size")

	// ErrWriterClosed indicates when the manifest writer is closed.
	ErrWriterClosed = errors.New("beachdb/manifest: writer is closed")

	// ErrReaderClosed indicates when the manifest reader is closed.
	ErrReaderClosed = errors.New("beachdb/manifest: reader is closed")
)
