package manifest

import "errors"

var (
	// ErrUnknownTag denotes that a tag in the version edit is not supported.
	ErrUnknownTag = errors.New("beachdb/manifest: unknown version edit tag")
)
