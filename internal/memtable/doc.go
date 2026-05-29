// Package memtable implements BeachDB's in-memory write buffer using a skip list.
//
// # What is a memtable?
//
// In an LSM-tree database, writes don't go directly to disk. They land here first:
// a sorted, in-memory structure that accumulates recent mutations until it's
// full enough to flush to an SSTable on disk. The memtable is where "fast writes"
// come from — appending to a sorted structure in RAM is orders of magnitude
// cheaper than random I/O to stable storage.
//
// # Why a skip list?
//
// Skip lists are a probabilistic data structure that give you O(log n) insertion
// and lookup without the rebalancing headaches of a tree. Think of it as a linked
// list that learned to skip ahead: each node has a random "height," and taller
// nodes act as express lanes that let you jump over large sections of the list.
//
// The appeal for a memtable:
//   - Sorted iteration is trivial (it's a linked list at level 0)
//   - No rotations or rebalancing (insertion is local pointer surgery)
//   - Concurrent-friendly (fine-grained locking is possible, though we use a single RWMutex for v1)
//   - Simple to implement without getting clever
//
// Redis, LevelDB, and RocksDB all use skip lists for their memtables. It's a
// battle-tested choice.
//
// # Key ordering
//
// This is the most important invariant in the whole package.
//
// Keys are sorted by (user_key ASC, seqno DESC). For the same user key, the
// entry with the highest sequence number appears first. This ordering means
// "newest version wins" falls out naturally: when you iterate or search, you
// hit the freshest version before any older ones.
//
// Example:
//
//	Put("foo", "v1") at seqno 5
//	Put("foo", "v2") at seqno 10
//	Delete("foo") at seqno 15
//
// The skip list stores these as:
//
//	("foo", 15, Delete) -> nil
//	("foo", 10, Put)    -> "v2"
//	("foo", 5, Put)     -> "v1"
//
// A Get("foo") at seqno 20 finds the tombstone first and returns "not found."
// A Get("foo") at seqno 12 skips the tombstone (seqno 15 > 12) and returns "v2."
//
// # Concurrency model
//
// The SkipList uses a single sync.RWMutex:
//   - Put takes an exclusive lock (we assume single-writer for v1)
//   - Get takes a shared lock
//   - Iterators acquire a shared lock on first Seek/SeekToFirst and hold it
//     until Close is called
//
// This means iterators block writers. It's a deliberate v1 simplicity choice —
// the frozen-memtable flush pattern routes new writes to a fresh memtable
// while the old one drains, so iterator lock contention becomes a non-issue
// in practice.
//
// IMPORTANT: Callers MUST call Iterator.Close() to release the lock. Forgetting
// this will deadlock writers indefinitely. The compiler won't save you here.
//
// # What the iterator sees
//
// The iterator exposes ALL entries: puts, tombstones, every version of every key.
// It does not filter, deduplicate, or hide anything. That's intentional.
//
// Filtering (hide old versions, skip tombstones, respect snapshot boundaries)
// happens at a higher layer — the merge iterator that combines memtable +
// SSTables during reads. The memtable iterator's job is to produce a sorted
// stream of internal keys. Nothing more.
//
// # Memory accounting
//
// Size() returns an approximate byte count of memory used. It's not exact —
// we estimate per-node overhead and don't account for allocator fragmentation —
// but it's good enough for "is this memtable full yet?" decisions.
//
// # The p=0.25 choice
//
// Skip list level probability affects the height distribution:
//   - p=0.5: Average node height ~2. More memory, faster search.
//   - p=0.25: Average node height ~1.33. Less memory, still O(log n).
//
// We use p=0.25 because it's the standard choice (LevelDB, Redis) and keeps
// memory overhead reasonable. With maxLevel=12 and p=0.25, we can handle
// billions of entries before the math breaks down.
package memtable
