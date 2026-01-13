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
	"sync"

	"github.com/aalhour/beachdb/internal/util/coding"
)

type OpType byte

const (
	EncodingVersion uint32 = 1
	OpTypePut       OpType = 1
	OpTypeDelete    OpType = 2
)

type operation struct {
	opType OpType
	key    []byte
	value  []byte
}

type Batch struct {
	mu  sync.RWMutex
	ops []operation
}

func NewBatch() *Batch {
	return &Batch{
		ops: make([]operation, 0, 10),
	}
}

// Put appends a put op to the batch
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

// Delete appends a delete op to the batch
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

// Count returns the number of ops in the batch
func (b *Batch) Count() int {
	// Hold a read lock
	b.mu.RLock()
	defer b.mu.RUnlock()

	return len(b.ops)
}

// Reset clears the internal state of the Batch
func (b *Batch) Reset() {
	// Hold the lock
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ops = make([]operation, 0, 10)
}

// Encode serializes operations to a byte array
func (b *Batch) Encode() []byte {
	// Grab a read lock
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Pre-calculate the size of the buffer
	size := 8 // header
	for _, op := range b.ops {
		size += 1 + 4 + len(op.key) // op_type + key_len + key
		if op.opType == OpTypePut {
			size += 4 + len(op.value) // value_len + value
		}
	}

	// Create the buffer
	buf := make([]byte, size)

	// Write the header
	buf[0] = byte(EncodingVersion)                // version, e.g.: 0x01
	buf[1], buf[2], buf[3] = 0, 0, 0              // reserved
	coding.PutUint32(buf[4:], uint32(len(b.ops))) // op_count

	// Wrrite each op
	offset := 8
	for _, op := range b.ops {
		// Write the op type and increment offset (1 byte)
		buf[offset] = byte(op.opType)
		offset += 1

		// Write the key length (4 bytes)
		coding.PutUint32(buf[offset:], uint32(len(op.key)))
		offset += 4

		// Write the key (variable)
		copy(buf[offset:], op.key)
		offset += len(op.key)

		// Write the value, if the op is a Put
		if op.opType == OpTypePut {
			// Write the value length (4 bytes)
			coding.PutUint32(buf[offset:], uint32(len(op.value)))
			offset += 4

			// Write the value (variable)
			copy(buf[offset:], op.value)
			offset += len(op.value)
		}
	}

	// Return the buffer
	return buf
}

// DecodeBatch takes a byte array and tries to decode it to a Batch struct
func DecodeBatch(data []byte) (*Batch, error) {
	r := coding.NewByteReader(data)

	// Read header
	version, err := r.ReadByte()
	if err != nil || version != byte(EncodingVersion) {
		return nil, ErrCorruptBatch
	}

	// Skip reserved bytes
	if _, err := r.ReadBytes(3); err != nil {
		return nil, ErrCorruptBatch
	}

	// Read op count
	opCount, err := r.ReadUint32()
	if err != nil {
		return nil, ErrCorruptBatch
	}

	batch := NewBatch()

	for range opCount {
		opTypeByte, err := r.ReadByte()
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
			return nil, ErrCorruptBatch
		}

		keyLen, err := r.ReadUint32()
		if err != nil {
			return nil, ErrCorruptBatch
		}

		key, err := r.ReadBytes(int(keyLen))
		if err != nil {
			return nil, ErrCorruptBatch
		}

		var value []byte
		if opType == OpTypePut {
			valueLen, err := r.ReadUint32()
			if err != nil {
				return nil, ErrCorruptBatch
			}
			value, err = r.ReadBytes(int(valueLen))
			if err != nil {
				return nil, ErrCorruptBatch
			}
		}

		batch.ops = append(batch.ops, operation{
			opType: opType,
			key:    key,
			value:  value,
		})
	}

	return batch, nil
}
