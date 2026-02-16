// Package memtable implements the skiplist and other types for the Memtable
package memtable

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/aalhour/beachdb/internal/keys"
)

type skipNode struct {
	key   keys.InternalKey
	value []byte
	next  []*skipNode
}

// SkipList is a probabilistic data structure that provides
// lock-friendly O(log n) insertion and search
type SkipList struct {
	mu       sync.RWMutex // Reader-writer lock
	rng      *rand.Rand   // Random number generator for level selection
	head     *skipNode    // Sentinel head node (never holds data)
	maxLevel int          // Current highest level in use
	level    int          // Current highest level in use
	length   int          // Number of entries
	size     int64        // Approximate memory usage in bytes
}

// NewSkipList creates a new SkipList struct pointer and returns it
func NewSkipList() *SkipList {
	seed1 := uint64(time.Now().UnixNano())
	seed2 := uint64(time.Now().UnixNano() ^ 0xDEADBEEF) // mix it up a bit

	const maxLevel = 12

	return &SkipList{
		maxLevel: maxLevel,
		head: &skipNode{
			next: make([]*skipNode, maxLevel), // 12 nil pointers
		},
		level: 1,
		rng:   rand.New(rand.NewPCG(seed1, seed2)),
	}
}

// randomLevel returns a random level for a new node
func (sl *SkipList) randomLevel() int {
	// Returns a random level between 1 and sl.maxLevel (inclusive),
	// where the probability decreases geometrically with each higher level.
	level := 1
	for level < sl.maxLevel {
		if sl.rng.Float64() >= 0.25 {
			break
		}
		level++
	}
	return level
}

// findPredecessors iterates the levels in reverse order and returns an array
// of nodes where `preds[i]“ is the last node at level `i` whose key is <
// of the target key. The node we're looking for (or where to insert) is at:
// `preds[0].next[0]`
func (sl *SkipList) findPredecessors(key keys.InternalKey) []*skipNode {
	// Initialize preds slice
	preds := make([]*skipNode, sl.maxLevel)

	// Assign head as the default pred for all levels
	for i := range preds {
		preds[i] = sl.head
	}
	current := sl.head

	// Start at previous level and iterate backwards
	for level := sl.maxLevel - 1; level >= 0; level-- {
		// Keep going forward in the next pointers as long as there are
		// nodes and they are < the target key
		for current.next[level] != nil && current.next[level].key.Compare(key) < 0 {
			current = current.next[level]
		}

		// Found the predecessor and it could be nil!
		preds[level] = current
	}

	return preds
}
