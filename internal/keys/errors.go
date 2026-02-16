package keys

import "errors"

var (
	// ErrCorruptInternalKey indicates that an error occurred when decoding internal key from bytes.
	ErrCorruptInternalKey = errors.New("beachdb: corrupt internal key")
)
