// Package memtable implements the skiplist and other types for the Memtable
package memtable

import (
	"bytes"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/aalhour/beachdb/internal/keys"
)

// skipNode defines the internal node type in the SkipList
type skipNode struct {
	key   keys.InternalKey
	value []byte
	next  []*skipNode
}

// memSize returns an estimation for the skipNode's size in memory
func (n *skipNode) memSize() int {
	return len(n.key.UserKey) +
		len(n.value) +
		8 + 1 + // seqno + kind
		len(n.next)*8 + // pointer slice
		24 // struct overhead estimate
}

// SkipList is a probabilistic data structure that provides O(log n) insertion
// and search with lock-friendly concurrent access. It implements the Memtable interface.
type SkipList struct {
	mu       sync.RWMutex // Reader-writer lock
	rng      *rand.Rand   // Random number generator for level selection
	head     *skipNode    // Sentinel head node (never holds data)
	maxLevel int          // Maximum possible level (typically 12-20)
	level    int          // Current highest level in use
	length   int          // Number of entries
	size     int64        // Approximate memory usage in bytes
}

// NewSkipList creates a new SkipList struct pointer and returns it
func NewSkipList() *SkipList {
	// Use UnixNano as seed; convert via bit cast to avoid overflow lint
	seed1 := uint64(time.Now().UnixNano()) //nolint:gosec // seed doesn't need to be secure
	seed2 := seed1 ^ 0xDEADBEEF            // mix it up a bit

	const maxLevel = 12

	return &SkipList{
		maxLevel: maxLevel,
		head: &skipNode{
			next: make([]*skipNode, maxLevel), // 12 nil pointers
		},
		level: 1,
		rng:   rand.New(rand.NewPCG(seed1, seed2)), //nolint:gosec // crypto/rand unnecessary for skip list levels
	}
}

// NewIterator returns an iterator over the skip list entries.
func (sl *SkipList) NewIterator() Iterator {
	return NewSkipListIterator(sl)
}

// randomLevel returns a random level for a new node.
// Returns a level between 1 and maxLevel (inclusive), where the probability
// decreases geometrically: ~75% level 1, ~18.75% level 2, ~4.7% level 3, etc.
func (sl *SkipList) randomLevel() int {
	level := 1
	for level < sl.maxLevel {
		if sl.rng.Float64() >= 0.25 {
			break
		}
		level++
	}
	return level
}

// findPredecessors returns the predecessor node for each level where a new node
// with the given key should be inserted.
//
// Returns: preds[i] = the rightmost node at level i whose key < target key.
//
// After calling this:
//   - preds[0].next[0] is the first node >= target (or nil if target is largest)
//   - To insert: wire newNode.next[i] = preds[i].next[i], then preds[i].next[i] = newNode
//
// Example: list has [A] -> [C] -> [E], inserting "D":
//
//	preds[0] = C  (C < D, but C.next[0] = E >= D)
//	After insert: [A] -> [C] -> [D] -> [E]
func (sl *SkipList) findPredecessors(key keys.InternalKey) []*skipNode {
	preds := make([]*skipNode, sl.maxLevel)

	// Default all levels to head (handles empty list and levels above current height)
	for i := range preds {
		preds[i] = sl.head
	}

	// Start at the highest level and work down.
	// At each level, move right until the next node is >= target key.
	// The "current" pointer carries forward to lower levels (optimization:
	// we don't restart from head, we continue from where we dropped down).
	current := sl.head
	for level := sl.maxLevel - 1; level >= 0; level-- {
		// Advance right while: (1) next node exists, AND (2) next node's key < target
		for current.next[level] != nil && current.next[level].key.Compare(key) < 0 {
			current = current.next[level]
		}
		// current is now the rightmost node at this level with key < target
		preds[level] = current
	}

	return preds
}

// Put inserts a key-value pair into the SkipList.
// In a memtable, we always insert (never update in place) because different
// sequence numbers represent different versions of the same user key.
func (sl *SkipList) Put(key keys.InternalKey, value []byte) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	// Find where to insert at each level
	preds := sl.findPredecessors(key)

	// Pick a random level for the new node
	nodeLevel := sl.randomLevel()

	// If new node is taller than current list height, update list level
	if nodeLevel > sl.level {
		// Higher levels have no predecessors yet, so point them to head
		for i := sl.level; i < nodeLevel; i++ {
			preds[i] = sl.head
		}
		sl.level = nodeLevel
	}

	// Create the new node (copy value to avoid caller mutation)
	valueCopy := append([]byte(nil), value...)
	newNode := &skipNode{
		key:   key,
		value: valueCopy,
		next:  make([]*skipNode, nodeLevel),
	}

	// Wire up forward pointers at each level:
	//   newNode.next[i] = preds[i].next[i]  (new node points to what pred pointed to)
	//   preds[i].next[i] = newNode          (pred now points to new node)
	for i := range nodeLevel {
		newNode.next[i] = preds[i].next[i]
		preds[i].next[i] = newNode
	}

	sl.length++
	sl.size += int64(newNode.memSize())
}

// Get returns the value for the given userKey at or before the given seqno.
// Returns (value, true) if found, (nil, false) if not found or deleted.
//
// Semantics: finds the newest version of userKey with seqno <= requested seqno.
// If that version is a tombstone (KindDelete), returns (nil, false).
func (sl *SkipList) Get(userKey []byte, seqno uint64) ([]byte, bool) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	// Create a search key with the target seqno
	// because seqno is descending, searching for (userKey, seqno) finds
	// the first entry with that userKey and seqno <= target
	searchKey := keys.InternalKey{
		UserKey: userKey,
		Seqno:   seqno,
	}
	preds := sl.findPredecessors(searchKey)

	// Check the node after predecessor
	node := preds[0].next[0]
	if node == nil {
		return nil, false
	}

	// Check if it matches our user key
	if !bytes.Equal(node.key.UserKey, userKey) {
		return nil, false
	}

	// Check if the seqno is <= our target (it should be, by ordering)
	if node.key.Seqno > seqno {
		return nil, false // Version is too new
	}

	// Check if it's a tombstone
	if node.key.Kind == keys.InternalKeyKindDelete {
		return nil, false // Key was deleted
	}

	// Found it! Return a *copy* of the value
	result := make([]byte, len(node.value))
	copy(result, node.value)
	return result, true
}

// Len returns the number of entries in the skip list.
func (sl *SkipList) Len() int {
	sl.mu.RLock()
	defer sl.mu.RUnlock()
	return sl.length
}

// Size returns the approximate memory usage in bytes.
func (sl *SkipList) Size() int64 {
	sl.mu.RLock()
	defer sl.mu.RUnlock()
	return sl.size
}

// Empty returns true if the skip list has no entries.
func (sl *SkipList) Empty() bool {
	sl.mu.RLock()
	defer sl.mu.RUnlock()
	return sl.length == 0
}

// SkipListIterator provides forward iteration over a SkipList's entries.
type SkipListIterator struct {
	list *SkipList // The skip list being iterated
	node *skipNode // Current node
}

// NewSkipListIterator creates a new iterator for the given skip list.
func NewSkipListIterator(list *SkipList) *SkipListIterator {
	return &SkipListIterator{
		list: list,
		node: nil,
	}
}

// SeekToFirst positions the iterator at the first entry. (TODO: implement)
func (it *SkipListIterator) SeekToFirst() {}

// Seek positions the iterator at the first entry with key >= target. (TODO: implement)
func (it *SkipListIterator) Seek(_ []byte) {}

// Valid returns true if the iterator is positioned at a valid entry.
func (it *SkipListIterator) Valid() bool { return false }

// Next advances the iterator to the next entry. (TODO: implement)
func (it *SkipListIterator) Next() {}

// Key returns the key at the current position.
func (it *SkipListIterator) Key() keys.InternalKey { return keys.InternalKey{} }

// Value returns the value at the current position.
func (it *SkipListIterator) Value() []byte { return []byte(nil) }

// Close releases any resources held by the iterator.
func (it *SkipListIterator) Close() error { return nil }
