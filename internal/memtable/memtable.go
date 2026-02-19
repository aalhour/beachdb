package memtable

import "github.com/aalhour/beachdb/internal/keys"

// Memtable is an in-memory sorted key-value store that buffers writes before flushing to SSTables.
type Memtable interface {
	Put(key keys.InternalKey, value []byte)
	Get(userKey []byte, seqno uint64) ([]byte, bool)
	NewIterator() Iterator
	Len() int
	Size() int64
	Empty() bool
}

var _ Memtable = (*SkipList)(nil)
