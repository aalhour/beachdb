package memtable

import "github.com/aalhour/beachdb/internal/keys"

// Memtable is an in-memory sorted key-value store that buffers writes before flushing to SSTables.
type Memtable interface {
	Put(key keys.InternalKey, value []byte) // Insert a key-value pair
	// Get retrieves a value at the given seqno.
	// Returns (nil, false) if the key was not found,
	// and (nil, true) when the newest visible version is a tombstone.
	Get(userKey []byte, seqno uint64) ([]byte, bool)
	NewIterator() Iterator // Create an iterator over entries
	Len() int              // Number of entries
	Size() int64           // Approximate memory usage in bytes
	Empty() bool           // True if no entries
}
