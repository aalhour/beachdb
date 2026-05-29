// Package keys define the internal key type of beachdb
package keys

import (
	"bytes"

	"github.com/aalhour/beachdb/internal/util/coding"
)

const (
	// InternalKeyKindPut marks a Put entry
	InternalKeyKindPut byte = 1

	// InternalKeyKindDelete marks a Delete entry
	InternalKeyKindDelete byte = 2
)

// InternalKey holds user key, seqno and kind
type InternalKey struct {
	UserKey []byte
	Seqno   uint64
	Kind    byte
}

// isValidInternalKeyKind reports whether kind is supported by the current encoding.
func isValidInternalKeyKind(kind byte) bool {
	switch kind {
	case InternalKeyKindPut, InternalKeyKindDelete:
		return true
	default:
		return false
	}
}

// Compare compares an internal key with another
func (k InternalKey) Compare(other InternalKey) int {
	cmp := bytes.Compare(k.UserKey, other.UserKey)
	if cmp != 0 {
		return cmp
	}
	// Larger sequence number means that `k` is newer, which means
	// "recent/smaller", hence -1
	if k.Seqno > other.Seqno {
		return -1
	}
	// Smaller sequence number means that `k` is older, which means
	// "older/bigger", hence 1
	if k.Seqno < other.Seqno {
		return 1
	}
	return 0
}

// Encode serializes an internal key to `[userKey][seqno:8][kind:1]`
func (k InternalKey) Encode() []byte {
	// Calculate the key size and allocation size
	keySize := len(k.UserKey)
	totalSize := keySize + 8 + 1

	// Create the buffer
	buffer := make([]byte, totalSize)

	// Serialize the fields
	offset := 0
	copy(buffer[offset:], k.UserKey)

	offset += keySize
	coding.PutUint64(buffer[offset:], k.Seqno)

	offset += 8
	buffer[offset] = k.Kind

	return buffer
}

// DecodeInternalKey deserializes data from byte array
func DecodeInternalKey(data []byte) (InternalKey, error) {
	// Validate data length
	if len(data) < 9 {
		return InternalKey{}, ErrCorruptInternalKey
	}

	n := len(data)
	kind := data[n-1]

	// Validate Kind byte in data
	if !isValidInternalKeyKind(kind) {
		return InternalKey{}, ErrCorruptInternalKey
	}

	key := InternalKey{
		UserKey: data[:n-9],
		Seqno:   coding.Uint64(data[n-9 : n-1]),
		Kind:    kind,
	}

	return key, nil
}
