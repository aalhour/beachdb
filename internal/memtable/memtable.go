package memtable

import "github.com/aalhour/beachdb/internal/keys"

// Memtable is an in-memory sorted key-value store that buffers writes before flushing to SSTables.
type Memtable interface {
	Put(key keys.InternalKey, value []byte)          // Insert a key-value pair
	Get(userKey []byte, seqno uint64) ([]byte, bool) // Retrieve value at seqno; returns (nil, false) if not found or deleted
	NewIterator() Iterator                           // Create an iterator over entries
	Len() int                                        // Number of entries
	Size() int64                                     // Approximate memory usage in bytes
	Empty() bool                                     // True if no entries
}
