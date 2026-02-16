package memtable

import (
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
)

func TestRandomLevel(t *testing.T) {
	const iterations = 10000
	sl := NewSkipList()
	levelCount := make([]int, sl.maxLevel+1) // levels 1..maxLevel

	for range iterations {
		level := sl.randomLevel()
		if level < 1 || level > sl.maxLevel {
			t.Errorf("randomLevel() = %d, want in [1,%d]", level, sl.maxLevel)
		}
		levelCount[level]++
	}

	t.Logf("randomLevel() distribution after %d iterations:", iterations)
	for l := 1; l <= sl.maxLevel; l++ {
		t.Logf("  Level %2d: %4d (%.2f%%)", l, levelCount[l], float64(levelCount[l])*100/float64(iterations))
	}

	// Simple geometric distribution property: about 25% of nodes should be level 2, 1/16 for level 3, etc.
	if levelCount[1] < iterations/2 {
		t.Errorf("Expected most nodes to be at level 1, got %d", levelCount[1])
	}
	if sl.maxLevel > 3 && (levelCount[3] > levelCount[2]) {
		t.Errorf("Level 3 count should be less than level 2 count (%d vs %d)", levelCount[3], levelCount[2])
	}
}

func TestFindPredecessors(t *testing.T) {
	sl := NewSkipList()

	// Helper to generate keys
	makeKey := func(userKey string, seqno uint64) keys.InternalKey {
		return keys.InternalKey{
			UserKey: []byte(userKey),
			Seqno:   seqno,
			Kind:    keys.InternalKeyKindPut,
		}
	}

	// Case 1: Empty list should return head for all levels
	targetKey := makeKey("foo", 0)
	preds := sl.findPredecessors(targetKey)
	for i, pred := range preds {
		if pred != sl.head {
			t.Errorf("[empty] preds[%d] = %v, want head", i, pred)
		}
	}

	// Build up a small list: [a] -> [c] -> [e]
	// All Seqno = 1, so userKey lexicographical order determines position.
	nA := &skipNode{key: makeKey("a", 1), next: make([]*skipNode, 2)}
	nC := &skipNode{key: makeKey("c", 1), next: make([]*skipNode, 3)}
	nE := &skipNode{key: makeKey("e", 1), next: make([]*skipNode, 1)}
	sl.level = 3 // simulate that highest level is 3

	// Connect: head (level 2/1/0)
	sl.head.next[2] = nC // head -2-> c
	sl.head.next[1] = nA // head -1-> a
	sl.head.next[0] = nA // head -0-> a

	// [a] forwards
	nA.next[0] = nC // a level 0 to c
	nA.next[1] = nC // a level 1 to c

	// [c] forwards
	nC.next[0] = nE // c level 0 to e
	nC.next[1] = nil
	nC.next[2] = nil

	// [e] terminal
	nE.next[0] = nil

	// Now test various target keys:

	// (a) target < "a": expect all preds = head
	targetKey = makeKey("0", 1) // '0' < 'a'
	preds = sl.findPredecessors(targetKey)
	for i, pred := range preds {
		if pred != sl.head {
			t.Errorf("[before first] preds[%d]=%v, want head", i, pred)
		}
	}

	// (b) target = "a": expect all preds = head
	targetKey = makeKey("a", 1)
	preds = sl.findPredecessors(targetKey)
	for i, pred := range preds {
		if pred != sl.head {
			t.Errorf("[exact a] preds[%d]=%v, want head", i, pred)
		}
	}

	// (c) target = "b": expect preds: 0 and 1: a, 2: head (since a is missing level 2)
	targetKey = makeKey("b", 1)
	preds = sl.findPredecessors(targetKey)
	if preds[0] != nA {
		t.Errorf("[b] preds[0]=%v, want a", preds[0])
	}
	if preds[1] != nA {
		t.Errorf("[b] preds[1]=%v, want a", preds[1])
	}
	if preds[2] != sl.head {
		t.Errorf("[b] preds[2]=%v, want head", preds[2])
	}

	// (d) target = "c": expect preds[0]=a, preds[1]=a, preds[2]=head
	targetKey = makeKey("c", 1)
	preds = sl.findPredecessors(targetKey)
	if preds[0] != nA {
		t.Errorf("[c] preds[0]=%v, want a", preds[0])
	}
	if preds[1] != nA {
		t.Errorf("[c] preds[1]=%v, want a", preds[1])
	}
	if preds[2] != sl.head {
		t.Errorf("[c] preds[2]=%v, want head", preds[2])
	}

	// (e) target = "d": expect preds[0]=c, preds[1]=c, preds[2]=c
	targetKey = makeKey("d", 1)
	preds = sl.findPredecessors(targetKey)
	if preds[0] != nC {
		t.Errorf("[d] preds[0]=%v, want c", preds[0])
	}
	if preds[1] != nC {
		t.Errorf("[d] preds[1]=%v, want c", preds[1])
	}
	if preds[2] != nC {
		t.Errorf("[d] preds[2]=%v, want c", preds[2])
	}

	// (f) target = "f": expect preds[0]=e, preds[1]=c, preds[2]=c
	targetKey = makeKey("f", 1)
	preds = sl.findPredecessors(targetKey)
	if preds[0] != nE {
		t.Errorf("[f] preds[0]=%v, want e", preds[0])
	}
	if preds[1] != nC {
		t.Errorf("[f] preds[1]=%v, want c", preds[1])
	}
	if preds[2] != nC {
		t.Errorf("[f] preds[2]=%v, want c", preds[2])
	}

	// === Seqno edge cases ===
	// InternalKey ordering: user key ascending, then seqno DESCENDING.
	// So ("x", 10) < ("x", 5) < ("x", 1) in sort order.

	// Build a new list with same user key at different seqnos: x@10 -> x@5 -> x@1
	sl2 := NewSkipList()
	nX10 := &skipNode{key: makeKey("x", 10), next: make([]*skipNode, 1)}
	nX5 := &skipNode{key: makeKey("x", 5), next: make([]*skipNode, 1)}
	nX1 := &skipNode{key: makeKey("x", 1), next: make([]*skipNode, 1)}
	sl2.level = 1

	sl2.head.next[0] = nX10
	nX10.next[0] = nX5
	nX5.next[0] = nX1
	nX1.next[0] = nil

	// (g) target = ("x", 7): should land between x@10 and x@5
	// x@10 < x@7 (10 > 7, so x@10 comes first), x@7 < x@5 (7 > 5)
	// Predecessor should be x@10
	targetKey = makeKey("x", 7)
	preds = sl2.findPredecessors(targetKey)
	if preds[0] != nX10 {
		t.Errorf("[x@7] preds[0]=%v, want x@10", preds[0])
	}

	// (h) target = ("x", 5): exact match, predecessor is x@10
	targetKey = makeKey("x", 5)
	preds = sl2.findPredecessors(targetKey)
	if preds[0] != nX10 {
		t.Errorf("[x@5 exact] preds[0]=%v, want x@10", preds[0])
	}

	// (i) target = ("x", 3): between x@5 and x@1, predecessor is x@5
	targetKey = makeKey("x", 3)
	preds = sl2.findPredecessors(targetKey)
	if preds[0] != nX5 {
		t.Errorf("[x@3] preds[0]=%v, want x@5", preds[0])
	}

	// (j) target = ("x", 100): higher seqno than any existing
	// x@100 < x@10 (100 > 10), so predecessor is head
	targetKey = makeKey("x", 100)
	preds = sl2.findPredecessors(targetKey)
	if preds[0] != sl2.head {
		t.Errorf("[x@100] preds[0]=%v, want head", preds[0])
	}

	// (k) target = ("x", 0): lower seqno than any existing
	// x@1 < x@0 (1 > 0), so predecessor is x@1
	targetKey = makeKey("x", 0)
	preds = sl2.findPredecessors(targetKey)
	if preds[0] != nX1 {
		t.Errorf("[x@0] preds[0]=%v, want x@1", preds[0])
	}
}
