// Package engine implements the public storage engine API
package engine

/*
	The batch gets encoded to the following binary format:

	┌─────────────────────────────────────────────────────────────┐
	│                      BATCH HEADER (8 bytes)                 │
	├─────────┬─────────────────────┬─────────────────────────────┤
	│ version │ reserved            │ op_count                    │
	│ 1 byte  │ 3 bytes (0x000000)  │ 4 bytes (big-endian uint32) │
	├─────────┴─────────────────────┴─────────────────────────────┤
	│                      OPERATIONS (variable)                  │
	├─────────────────────────────────────────────────────────────┤
	│ ┌─────────────────────────────────────────────────────────┐ │
	│ │ Op #1 (Put)                                             │ │
	│ │ ┌─────────┬─────────┬───────┬───────────┬─────────────┐ │ │
	│ │ │ op_type │ key_len │ key   │ value_len │ value       │ │ │
	│ │ │ 1 byte  │ 4 bytes │ K     │ 4 bytes   │ V           │ │ │
	│ │ └─────────┴─────────┴───────┴───────────┴─────────────┘ │ │
	│ └─────────────────────────────────────────────────────────┘ │
	│ ┌─────────────────────────────────────────────────────────┐ │
	│ │ Op #2 (Delete)                                          │ │
	│ │ ┌─────────┬─────────┬───────┐                           │ │
	│ │ │ op_type │ key_len │ key   │  (no value for Delete!)   │ │
	│ │ │ 1 byte  │ 4 bytes │ K     │                           │ │
	│ │ └─────────┴─────────┴───────┘                           │ │
	│ └─────────────────────────────────────────────────────────┘ │
	└─────────────────────────────────────────────────────────────┘
*/

import (
	"math"
	"sync"

	"github.com/aalhour/beachdb/internal/util/coding"
)

// OpType represents the type of operation in a batch (Put or Delete).
type OpType byte

// EncodingVersion is the current batch encoding format version.
const EncodingVersion byte = 0x01 // v1

// batchHeaderSize is the fixed-size prefix for every encoded batch.
const batchHeaderSize = 8

// Operation Types
const (
	OpTypePut OpType = iota + 1
	OpTypeDelete
)

// operation stores one logical mutation inside a batch.
type operation struct {
	opType OpType
	key    []byte
	value  []byte
}

// Batch holds a sequence of Put and Delete operations to be applied atomically.
type Batch struct {
	mu  sync.RWMutex
	ops []operation
}

// cloneBytes copies a byte slice while preserving nil slices.
func cloneBytes(src []byte) []byte {
	if src == nil {
		return nil
	}

	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

// cloneOperations deep-copies a batch operation slice.
func cloneOperations(ops []operation) []operation {
	if len(ops) == 0 {
		return nil
	}

	cloned := make([]operation, len(ops))
	for i, op := range ops {
		cloned[i] = operation{
			opType: op.opType,
			key:    cloneBytes(op.key),
			value:  cloneBytes(op.value),
		}
	}

	return cloned
}

// NewBatch creates an empty Batch ready for Put and Delete operations.
func NewBatch() *Batch {
	return &Batch{
		ops: make([]operation, 0, 10),
	}
}

// Put appends a put operation to the batch.
// Empty keys and values are allowed.
func (b *Batch) Put(key, value []byte) {
	// Copy the key and value first
	keyCopy := append([]byte(nil), key...)
	valueCopy := append([]byte(nil), value...)

	// Hold the lock
	b.mu.Lock()
	defer b.mu.Unlock()

	// Append a new operation to the batch
	b.ops = append(b.ops, operation{
		opType: OpTypePut,
		key:    keyCopy,
		value:  valueCopy,
	})
}

// Delete appends a delete operation to the batch.
// Empty keys are allowed.
func (b *Batch) Delete(key []byte) {
	// Copy the key slice first
	keyCopy := append([]byte(nil), key...)

	// Hold the lock
	b.mu.Lock()
	defer b.mu.Unlock()

	// Append a new operation to the batch
	b.ops = append(b.ops, operation{
		opType: OpTypeDelete,
		key:    keyCopy,
	})
}

// Empty returns whether the batch is empty or not.
func (b *Batch) Empty() bool {
	return b.Count() == 0
}

// Count returns the number of operations in the batch.
func (b *Batch) Count() int {
	// Hold a read lock
	b.mu.RLock()
	defer b.mu.RUnlock()

	return len(b.ops)
}

// Reset clears the internal state of the Batch.
// It reuses the underlying slice capacity to reduce allocations.
func (b *Batch) Reset() {
	// Hold the lock
	b.mu.Lock()
	defer b.mu.Unlock()

	// Clear but reuse capacity
	b.ops = b.ops[:0]
}

// snapshot returns an immutable copy of the batch contents.
func (b *Batch) snapshot() []operation {
	if b == nil {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return cloneOperations(b.ops)
}

// Encode serializes the batch operations to a byte slice.
// The encoding is deterministic: the same batch always produces the same bytes.
func (b *Batch) Encode() []byte {
	// Grab a read lock
	b.mu.RLock()
	defer b.mu.RUnlock()

	return encodeOperations(b.ops)
}

// encodeOperations serializes a pre-snapshotted operation slice.
func encodeOperations(ops []operation) []byte {
	// Pre-calculate the size of the buffer
	size := batchHeaderSize // header
	for _, op := range ops {
		size += 1 + 4 + len(op.key) // op_type + key_len + key
		if op.opType == OpTypePut {
			size += 4 + len(op.value) // value_len + value
		}
	}

	// Create the buffer
	buf := make([]byte, size)

	// Write the header
	buf[0] = EncodingVersion         // version, e.g.: 0x01
	buf[1], buf[2], buf[3] = 0, 0, 0 // reserved
	if len(ops) > math.MaxUint32 {
		panic("batch: too many operations")
	}
	//nolint:gosec // G115: len(ops) is validated above, overflow not possible
	coding.PutUint32(buf[4:], uint32(len(ops)))

	// Write each op
	offset := batchHeaderSize
	for _, op := range ops {
		// Write the op type and increment offset (1 byte)
		buf[offset] = byte(op.opType)
		offset++

		// Write the key length (4 bytes)
		coding.PutUint32(buf[offset:], uint32(len(op.key))) //nolint:gosec // key size is bounded
		offset += 4

		// Write the key (variable)
		copy(buf[offset:], op.key)
		offset += len(op.key)

		// Write the value, if the op is a Put
		if op.opType == OpTypePut {
			// Write the value length (4 bytes)
			coding.PutUint32(buf[offset:], uint32(len(op.value))) //nolint:gosec // value size is bounded
			offset += 4

			// Write the value (variable)
			copy(buf[offset:], op.value)
			offset += len(op.value)
		}
	}

	// Return the buffer
	return buf
}

// DecodeBatch decodes a byte slice into a Batch.
// Returns an error if the data is malformed, truncated, or contains invalid operations.
func DecodeBatch(data []byte) (*Batch, error) {
	r := coding.NewByteReader(data)

	// Read header
	version, err := r.ReadByte()
	if err != nil {
		return nil, ErrCorruptBatch
	} else if version != EncodingVersion {
		return nil, ErrBadVersion
	}

	// Reserved bytes must stay zero in v1 so future extensions stay explicit.
	reserved, err := r.ReadBytes(3)
	if err != nil {
		return nil, ErrCorruptBatch
	}
	for _, b := range reserved {
		if b != 0 {
			return nil, ErrCorruptBatch
		}
	}

	// Read op count
	opCount, err := r.ReadUint32()
	if err != nil {
		return nil, ErrCorruptBatch
	}

	batch := NewBatch()

	for range opCount {
		op, err := decodeOperation(r)
		if err != nil {
			return nil, err
		}
		batch.ops = append(batch.ops, *op)
	}

	if r.Remaining() != 0 {
		return nil, ErrCorruptBatch
	}

	return batch, nil
}

// decodeOperation decodes one batch operation from the byte reader.
func decodeOperation(reader *coding.ByteReader) (*operation, error) {
	opTypeByte, err := reader.ReadByte()
	if err != nil {
		return nil, ErrCorruptBatch
	}

	var opType OpType
	switch opTypeByte {
	case byte(OpTypePut):
		opType = OpTypePut
	case byte(OpTypeDelete):
		opType = OpTypeDelete
	default:
		return nil, ErrUnknownOpType
	}

	keyLen, err := reader.ReadUint32()
	if err != nil {
		return nil, ErrTruncatedBatch
	}

	keyBytes, err := reader.ReadBytes(int(keyLen))
	if err != nil {
		return nil, ErrTruncatedBatch
	}
	// Copy the key, because ReadBytes returns a slice view
	key := make([]byte, keyLen)
	copy(key, keyBytes)

	var value []byte
	if opType == OpTypePut {
		valueLen, err := reader.ReadUint32()
		if err != nil {
			return nil, ErrTruncatedBatch
		}
		valueBytes, err := reader.ReadBytes(int(valueLen))
		if err != nil {
			return nil, ErrTruncatedBatch
		}
		// Copy the value, because ReadBytes returns a slice view
		value = make([]byte, valueLen)
		copy(value, valueBytes)
	}

	return &operation{
		opType: opType,
		key:    key,
		value:  value,
	}, nil
}
