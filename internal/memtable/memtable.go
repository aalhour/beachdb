package memtable

import "github.com/aalhour/beachdb/internal/keys"

// Memtable is ... TODO ...
type Memtable interface {
	Put(key keys.InternalKey, value []byte)
	Get(userKey []byte, seqno uint64) ([]byte, bool)
	// TODO: add `NewIterator() Iterator`
	Len() int
	Size() int64
	Empty() bool
}

var _ Memtable = (*SkipList)(nil)
