package testutil

import (
	"bytes"
	"math"
)

// entry represents a single versioned write (put or delete
type entry struct {
	key      []byte
	value    []byte // nil for tombstones
	seqno    uint64
	isDelete bool
}

// Model is a reference implementation for testing memtable correctness.
// It stores all versions of all keys to support snapshot reads.
type Model struct {
	entries []entry // All writes, in insertion order
}

// NewModel creates a new empty model.
func NewModel() *Model {
	return &Model{
		entries: make([]entry, 0, 10),
	}
}

// PutAt stores a key-value pair at a specific sequence number.
func (m *Model) PutAt(key, value []byte, seqno uint64) {
	// Copy key & value to prevent external mutation
	keyCopy := append([]byte(nil), key...)
	valueCopy := append([]byte(nil), value...)

	m.entries = append(m.entries, entry{
		key:      keyCopy,
		value:    valueCopy,
		seqno:    seqno,
		isDelete: false,
	})
}

// DeleteAt stores a tombstone at a specific sequence number.
func (m *Model) DeleteAt(key []byte, seqno uint64) {
	// Copy key & value to prevent external mutation
	keyCopy := append([]byte(nil), key...)

	m.entries = append(m.entries, entry{
		key:      keyCopy,
		value:    nil,
		seqno:    seqno,
		isDelete: true,
	})
}

// GetAt returns the value for key at the given seqno.
// Returns the newest version with seqno <= target.
// Returns (nil, true) if the newest visible version is a tombstone.
// Returns (nil, false) if no version exists for the key.
func (m *Model) GetAt(key []byte, seqno uint64) ([]byte, bool) {
	var best *entry

	for i := range m.entries {
		e := &m.entries[i]
		if !bytes.Equal(e.key, key) {
			continue
		}
		if e.seqno > seqno {
			continue // Too new
		}
		// Use >= so later entries win when seqnos are equal (last-write-wins)
		if best == nil || e.seqno >= best.seqno {
			best = e
		}
	}

	if best == nil {
		return nil, false
	}
	if best.isDelete {
		return nil, true // Tombstone: key found, but deleted
	}

	// Return a copy
	result := make([]byte, len(best.value))
	copy(result, best.value)
	return result, true
}

// Put stores a key-value pair. Key & value are copied.
func (m *Model) Put(key, value []byte) {
	m.PutAt(key, value, math.MaxUint64)
}

// Delete removes a key. Key is copied.
func (m *Model) Delete(key []byte) {
	m.DeleteAt(key, math.MaxUint64)
}

// Get retrieves a value. Returns (value, true) if found, (nil, false) if not.
func (m *Model) Get(key []byte) ([]byte, bool) {
	return m.GetAt(key, math.MaxUint64)
}

// Keys returns all keys in the model (for iteration in tests).
func (m *Model) Keys() [][]byte {
	seen := make(map[string]bool)
	var keys [][]byte

	for _, entry := range m.entries {
		keyStr := string(entry.key)
		if !seen[keyStr] {
			seen[keyStr] = true
			keyCopy := append([]byte(nil), entry.key...)
			keys = append(keys, keyCopy)
		}
	}
	return keys
}

// Len returns the number of keys in the model.
func (m *Model) Len() int {
	return len(m.entries)
}
