package engine

import "errors"

var (
	ErrCorruptBatch error = errors.New("beachdb: corrupt batch")
)
