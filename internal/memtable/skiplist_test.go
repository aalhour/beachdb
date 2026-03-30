package memtable

import (
	"fmt"
	"math"
	"sync"
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

// === Put tests ===

func TestPut_BasicCases(t *testing.T) {
	makeKey := func(userKey string, seqno uint64) keys.InternalKey {
		return keys.InternalKey{
			UserKey: []byte(userKey),
			Seqno:   seqno,
			Kind:    keys.InternalKeyKindPut,
		}
	}

	// lookup does a linear scan at level 0 to find an exact key match.
	lookup := func(sl *SkipList, key keys.InternalKey) ([]byte, bool) {
		n := sl.head.next[0]
		for n != nil {
			if n.key.Compare(key) == 0 {
				return n.value, true
			}
			n = n.next[0]
		}
		return nil, false
	}

	t.Run("single entry", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(makeKey("foo", 1), []byte("bar"))

		got, ok := lookup(sl, makeKey("foo", 1))
		if !ok || string(got) != "bar" {
			t.Errorf("got (%q, %v), want (bar, true)", got, ok)
		}

		// Non-existent key
		_, ok = lookup(sl, makeKey("missing", 1))
		if ok {
			t.Error("found non-existent key")
		}
	})

	t.Run("multiple keys lexicographic order", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(makeKey("c", 1), []byte("c-val"))
		sl.Put(makeKey("a", 1), []byte("a-val"))
		sl.Put(makeKey("b", 1), []byte("b-val"))

		// All should be findable
		for _, k := range []string{"a", "b", "c"} {
			got, ok := lookup(sl, makeKey(k, 1))
			if !ok || string(got) != k+"-val" {
				t.Errorf("key %q: got (%q, %v), want (%s-val, true)", k, got, ok, k)
			}
		}
	})

	t.Run("same user key different seqnos", func(t *testing.T) {
		// Seqno ordering is DESCENDING: higher seqno comes first in sort order.
		// So z@9 < z@3 < z@2 in internal key order.
		sl := NewSkipList()
		sl.Put(makeKey("z", 3), []byte("v3"))
		sl.Put(makeKey("z", 2), []byte("v2"))
		sl.Put(makeKey("z", 9), []byte("v9"))

		// Each seqno should be independently findable
		for seqno, want := range map[uint64]string{2: "v2", 3: "v3", 9: "v9"} {
			got, ok := lookup(sl, makeKey("z", seqno))
			if !ok || string(got) != want {
				t.Errorf("z@%d: got (%q, %v), want (%s, true)", seqno, got, ok, want)
			}
		}

		// Non-existent seqno
		_, ok := lookup(sl, makeKey("z", 5))
		if ok {
			t.Error("found z@5 which was never inserted")
		}
	})
}

func TestPut_DuplicateKeySeqno(t *testing.T) {
	// Edge case: inserting the same (userKey, seqno) twice.
	// This shouldn't happen in practice (seqno is monotonic), but test the behavior.
	//
	// Behavior: both entries are stored. The LAST insert comes FIRST in sort order
	// because equal keys are inserted before existing equal keys.

	makeKey := func(userKey string, seqno uint64) keys.InternalKey {
		return keys.InternalKey{
			UserKey: []byte(userKey),
			Seqno:   seqno,
			Kind:    keys.InternalKeyKindPut,
		}
	}

	// collectAll returns all values for entries matching the key (there may be duplicates)
	collectAll := func(sl *SkipList, key keys.InternalKey) []string {
		var results []string
		n := sl.head.next[0]
		for n != nil {
			if n.key.Compare(key) == 0 {
				results = append(results, string(n.value))
			}
			n = n.next[0]
		}
		return results
	}

	sl := NewSkipList()
	sl.Put(makeKey("dup", 1), []byte("first"))
	sl.Put(makeKey("dup", 1), []byte("second"))

	values := collectAll(sl, makeKey("dup", 1))
	if len(values) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(values), values)
	}

	// Last insert comes first (inserted before existing equal key)
	if values[0] != "second" {
		t.Errorf("values[0] = %q, want 'second' (last insert should come first)", values[0])
	}
	if values[1] != "first" {
		t.Errorf("values[1] = %q, want 'first'", values[1])
	}
}

func TestPut_EdgeCases(t *testing.T) {
	makeKey := func(userKey []byte, seqno uint64) keys.InternalKey {
		return keys.InternalKey{
			UserKey: userKey,
			Seqno:   seqno,
			Kind:    keys.InternalKeyKindPut,
		}
	}

	lookup := func(sl *SkipList, key keys.InternalKey) ([]byte, bool) {
		n := sl.head.next[0]
		for n != nil {
			if n.key.Compare(key) == 0 {
				return n.value, true
			}
			n = n.next[0]
		}
		return nil, false
	}

	t.Run("empty user key", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(makeKey([]byte{}, 1), []byte("empty-key-value"))

		got, ok := lookup(sl, makeKey([]byte{}, 1))
		if !ok || string(got) != "empty-key-value" {
			t.Errorf("empty key: got (%q, %v), want (empty-key-value, true)", got, ok)
		}
	})

	t.Run("nil value", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(makeKey([]byte("nilval"), 1), nil)

		got, ok := lookup(sl, makeKey([]byte("nilval"), 1))
		if !ok {
			t.Error("nil value entry not found")
		}
		if len(got) != 0 {
			t.Errorf("expected empty value, got %q", got)
		}
	})

	t.Run("binary key with null bytes", func(t *testing.T) {
		sl := NewSkipList()
		binaryKey := []byte{0x00, 0x01, 0x00, 0xFF}
		sl.Put(makeKey(binaryKey, 1), []byte("binary"))

		got, ok := lookup(sl, makeKey(binaryKey, 1))
		if !ok || string(got) != "binary" {
			t.Errorf("binary key: got (%q, %v), want (binary, true)", got, ok)
		}
	})

	t.Run("max seqno", func(t *testing.T) {
		sl := NewSkipList()
		maxSeqno := uint64(math.MaxUint64)
		sl.Put(makeKey([]byte("max"), maxSeqno), []byte("maxval"))

		got, ok := lookup(sl, makeKey([]byte("max"), maxSeqno))
		if !ok || string(got) != "maxval" {
			t.Errorf("max seqno: got (%q, %v), want (maxval, true)", got, ok)
		}
	})

	t.Run("zero seqno", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(makeKey([]byte("zero"), 0), []byte("zeroval"))

		got, ok := lookup(sl, makeKey([]byte("zero"), 0))
		if !ok || string(got) != "zeroval" {
			t.Errorf("zero seqno: got (%q, %v), want (zeroval, true)", got, ok)
		}
	})

	t.Run("value is not mutated by caller", func(t *testing.T) {
		// Verify Put copies the value slice (caller mutation doesn't affect stored value)
		sl := NewSkipList()
		val := []byte("original")
		sl.Put(makeKey([]byte("key"), 1), val)

		// Mutate the original slice
		val[0] = 'X'

		got, _ := lookup(sl, makeKey([]byte("key"), 1))
		if string(got) != "original" {
			t.Errorf("value was mutated: got %q, want 'original'", got)
		}
	})
}

func TestPut_LenAndSize(t *testing.T) {
	sl := NewSkipList()

	if sl.Len() != 0 {
		t.Errorf("empty list Len() = %d, want 0", sl.Len())
	}
	if sl.Size() != 0 {
		t.Errorf("empty list Size() = %d, want 0", sl.Size())
	}
	if !sl.Empty() {
		t.Error("empty list Empty() = false, want true")
	}

	// Insert some entries
	for i := range 100 {
		key := keys.InternalKey{
			UserKey: []byte("key"),
			Seqno:   uint64(i), //nolint:gosec // test code, i is small
			Kind:    keys.InternalKeyKindPut,
		}
		sl.Put(key, []byte("value"))
	}

	if sl.Len() != 100 {
		t.Errorf("after 100 inserts, Len() = %d, want 100", sl.Len())
	}
	if sl.Size() <= 0 {
		t.Errorf("after 100 inserts, Size() = %d, want > 0", sl.Size())
	}
	if sl.Empty() {
		t.Error("after 100 inserts, Empty() = true, want false")
	}
}

func TestPut_LargeScale(t *testing.T) {
	// Insert many entries and verify they're all findable
	sl := NewSkipList()
	const n = 10000

	lookup := func(sl *SkipList, key keys.InternalKey) ([]byte, bool) {
		node := sl.head.next[0]
		for node != nil {
			if node.key.Compare(key) == 0 {
				return node.value, true
			}
			node = node.next[0]
		}
		return nil, false
	}

	// Insert n entries with different keys
	for i := range n {
		key := keys.InternalKey{
			UserKey: []byte("key-" + string(rune('A'+i%26)) + "-" + string(rune('0'+i/26))),
			Seqno:   uint64(i), //nolint:gosec // test code, i is small
			Kind:    keys.InternalKeyKindPut,
		}
		sl.Put(key, []byte("val"))
	}

	if sl.Len() != n {
		t.Errorf("Len() = %d, want %d", sl.Len(), n)
	}

	// Verify all entries exist
	for i := range n {
		key := keys.InternalKey{
			UserKey: []byte("key-" + string(rune('A'+i%26)) + "-" + string(rune('0'+i/26))),
			Seqno:   uint64(i), //nolint:gosec // test code, i is small
			Kind:    keys.InternalKeyKindPut,
		}
		_, ok := lookup(sl, key)
		if !ok {
			t.Errorf("entry %d not found", i)
		}
	}
}

func TestPut_Concurrent(t *testing.T) {
	// Stress test: multiple goroutines inserting concurrently.
	// Should not panic or corrupt data.
	sl := NewSkipList()
	const numGoroutines = 10
	const insertsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := range numGoroutines {
		go func(goroutineID int) {
			defer wg.Done()
			for i := range insertsPerGoroutine {
				key := keys.InternalKey{
					//nolint:gosec // G115: test code with small bounded values
					UserKey: []byte("g" + string(rune('0'+goroutineID)) + "-" + string(rune('A'+i%26))),
					Seqno:   uint64(goroutineID*1000 + i), //nolint:gosec // test code, values are small
					Kind:    keys.InternalKeyKindPut,
				}
				sl.Put(key, []byte("val"))
			}
		}(g)
	}

	wg.Wait()

	expectedLen := numGoroutines * insertsPerGoroutine
	if sl.Len() != expectedLen {
		t.Errorf("Len() = %d, want %d", sl.Len(), expectedLen)
	}
}

func TestPut_ConcurrentReadWrite(t *testing.T) {
	// Mixed reads and writes from multiple goroutines.
	sl := NewSkipList()
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(3)

	// Writer 1
	go func() {
		defer wg.Done()
		for i := range iterations {
			key := keys.InternalKey{
				UserKey: []byte("w1"),
				Seqno:   uint64(i), //nolint:gosec // test code, i is small
				Kind:    keys.InternalKeyKindPut,
			}
			sl.Put(key, []byte("writer1"))
		}
	}()

	// Writer 2
	go func() {
		defer wg.Done()
		for i := range iterations {
			key := keys.InternalKey{
				UserKey: []byte("w2"),
				Seqno:   uint64(i), //nolint:gosec // test code, i is small
				Kind:    keys.InternalKeyKindPut,
			}
			sl.Put(key, []byte("writer2"))
		}
	}()

	// Reader (calls Len/Size/Empty concurrently)
	go func() {
		defer wg.Done()
		for range iterations {
			_ = sl.Len()
			_ = sl.Size()
			_ = sl.Empty()
		}
	}()

	wg.Wait()

	// Should have all entries
	expectedLen := 2 * iterations
	if sl.Len() != expectedLen {
		t.Errorf("Len() = %d, want %d", sl.Len(), expectedLen)
	}
}

// =============================================================================
// Get tests
//
// Key concept: InternalKey ordering is (userKey ASC, seqno DESC).
// So for the same userKey, higher seqno entries come FIRST in the skiplist.
//
// Get(userKey, seqno) finds the newest version of userKey that is visible
// at the given seqno. "Visible" means the entry's seqno <= requested seqno.
// =============================================================================

func TestGet_EmptyList(t *testing.T) {
	sl := NewSkipList()

	// Any Get on an empty list should return (nil, false)
	val, ok := sl.Get([]byte("anything"), 100)
	if ok || val != nil {
		t.Errorf("empty list: Get returned (%q, %v), want (nil, false)", val, ok)
	}
}

func TestGet_SingleEntry(t *testing.T) {
	t.Run("exact match", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("foo"), Seqno: 5, Kind: keys.InternalKeyKindPut}, []byte("bar"))

		val, ok := sl.Get([]byte("foo"), 5)
		if !ok || string(val) != "bar" {
			t.Errorf("Get(foo, 5) = (%q, %v), want (bar, true)", val, ok)
		}
	})

	t.Run("higher seqno sees the entry", func(t *testing.T) {
		// Entry at seqno 5 should be visible at seqno 10
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("foo"), Seqno: 5, Kind: keys.InternalKeyKindPut}, []byte("bar"))

		val, ok := sl.Get([]byte("foo"), 10)
		if !ok || string(val) != "bar" {
			t.Errorf("Get(foo, 10) = (%q, %v), want (bar, true)", val, ok)
		}
	})

	t.Run("lower seqno cannot see the entry", func(t *testing.T) {
		// Entry at seqno 5 should NOT be visible at seqno 4
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("foo"), Seqno: 5, Kind: keys.InternalKeyKindPut}, []byte("bar"))

		val, ok := sl.Get([]byte("foo"), 4)
		if ok || val != nil {
			t.Errorf("Get(foo, 4) = (%q, %v), want (nil, false)", val, ok)
		}
	})

	t.Run("wrong userKey not found", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("foo"), Seqno: 5, Kind: keys.InternalKeyKindPut}, []byte("bar"))

		val, ok := sl.Get([]byte("bar"), 5) // different userKey
		if ok || val != nil {
			t.Errorf("Get(bar, 5) = (%q, %v), want (nil, false)", val, ok)
		}
	})
}

func TestGet_MultipleKeys(t *testing.T) {
	sl := NewSkipList()

	// Insert multiple distinct keys
	sl.Put(keys.InternalKey{UserKey: []byte("apple"), Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte("red"))
	sl.Put(keys.InternalKey{UserKey: []byte("banana"), Seqno: 2, Kind: keys.InternalKeyKindPut}, []byte("yellow"))
	sl.Put(keys.InternalKey{UserKey: []byte("cherry"), Seqno: 3, Kind: keys.InternalKeyKindPut}, []byte("red"))

	tests := []struct {
		userKey string
		seqno   uint64
		want    string
		ok      bool
	}{
		{"apple", 1, "red", true},
		{"apple", 100, "red", true}, // higher seqno still sees it
		{"banana", 2, "yellow", true},
		{"cherry", 3, "red", true},
		{"cherry", 2, "", false},   // seqno too low
		{"durian", 100, "", false}, // key doesn't exist
	}

	for _, tt := range tests {
		val, ok := sl.Get([]byte(tt.userKey), tt.seqno)
		if ok != tt.ok || (ok && string(val) != tt.want) {
			t.Errorf("Get(%q, %d) = (%q, %v), want (%q, %v)",
				tt.userKey, tt.seqno, val, ok, tt.want, tt.ok)
		}
	}
}

func TestGet_VersionSemantics(t *testing.T) {
	// Multiple versions of the same userKey at different seqnos.
	// Get should return the newest version that is <= requested seqno.
	//
	// Skiplist order (seqno DESC): ver@5 -> ver@3 -> ver@1
	sl := NewSkipList()
	sl.Put(keys.InternalKey{UserKey: []byte("ver"), Seqno: 3, Kind: keys.InternalKeyKindPut}, []byte("v3"))
	sl.Put(keys.InternalKey{UserKey: []byte("ver"), Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte("v1"))
	sl.Put(keys.InternalKey{UserKey: []byte("ver"), Seqno: 5, Kind: keys.InternalKeyKindPut}, []byte("v5"))

	tests := []struct {
		name   string
		seqno  uint64
		want   string
		wantOK bool
	}{
		// Request seqno higher than any entry: get newest (v5)
		{"seqno=100 -> newest visible is v5", 100, "v5", true},
		{"seqno=6 -> newest visible is v5", 6, "v5", true},

		// Exact matches
		{"seqno=5 exact match -> v5", 5, "v5", true},
		{"seqno=3 exact match -> v3", 3, "v3", true},
		{"seqno=1 exact match -> v1", 1, "v1", true},

		// Between versions: get the next older one
		{"seqno=4 -> newest visible is v3", 4, "v3", true},
		{"seqno=2 -> newest visible is v1", 2, "v1", true},

		// Below minimum seqno: nothing visible
		{"seqno=0 -> nothing visible", 0, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, ok := sl.Get([]byte("ver"), tt.seqno)
			if ok != tt.wantOK {
				t.Errorf("Get(ver, %d): ok=%v, want ok=%v", tt.seqno, ok, tt.wantOK)
			}
			if ok && string(val) != tt.want {
				t.Errorf("Get(ver, %d) = %q, want %q", tt.seqno, val, tt.want)
			}
		})
	}
}

func TestGet_Tombstones(t *testing.T) {
	// Tombstones (KindDelete) should cause Get to return (nil, false).
	// A tombstone at seqno N means "key was deleted at time N".

	t.Run("tombstone hides key at same seqno", func(t *testing.T) {
		sl := NewSkipList()
		// Put then delete at same seqno (unlikely in practice, but tests the logic)
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 5, Kind: keys.InternalKeyKindPut}, []byte("val"))
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 5, Kind: keys.InternalKeyKindDelete}, nil)

		// The tombstone comes first in sort order (same seqno, inserted later)
		val, ok := sl.Get([]byte("k"), 5)
		if ok || val != nil {
			t.Errorf("Get(k, 5) = (%q, %v), want (nil, false) due to tombstone", val, ok)
		}
	})

	t.Run("tombstone at newer seqno hides older value", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 3, Kind: keys.InternalKeyKindPut}, []byte("old"))
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 5, Kind: keys.InternalKeyKindDelete}, nil) // delete

		// At seqno 5: tombstone is visible -> not found
		val, ok := sl.Get([]byte("k"), 5)
		if ok || val != nil {
			t.Errorf("Get(k, 5) = (%q, %v), want (nil, false)", val, ok)
		}

		// At seqno 6: tombstone is still the newest visible -> not found
		val, ok = sl.Get([]byte("k"), 6)
		if ok || val != nil {
			t.Errorf("Get(k, 6) = (%q, %v), want (nil, false)", val, ok)
		}

		// At seqno 4: tombstone not visible, but put@3 is -> found
		val, ok = sl.Get([]byte("k"), 4)
		if !ok || string(val) != "old" {
			t.Errorf("Get(k, 4) = (%q, %v), want (old, true)", val, ok)
		}
	})

	t.Run("put after delete resurrects key", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte("first"))
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 3, Kind: keys.InternalKeyKindDelete}, nil)
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 5, Kind: keys.InternalKeyKindPut}, []byte("resurrected"))

		// At seqno 5: put@5 is newest -> found
		val, ok := sl.Get([]byte("k"), 5)
		if !ok || string(val) != "resurrected" {
			t.Errorf("Get(k, 5) = (%q, %v), want (resurrected, true)", val, ok)
		}

		// At seqno 4: tombstone@3 is newest visible -> not found
		val, ok = sl.Get([]byte("k"), 4)
		if ok || val != nil {
			t.Errorf("Get(k, 4) = (%q, %v), want (nil, false)", val, ok)
		}

		// At seqno 2: put@1 is newest visible -> found
		val, ok = sl.Get([]byte("k"), 2)
		if !ok || string(val) != "first" {
			t.Errorf("Get(k, 2) = (%q, %v), want (first, true)", val, ok)
		}
	})
}

func TestGet_EdgeCases(t *testing.T) {
	t.Run("empty userKey", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte{}, Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte("empty"))

		val, ok := sl.Get([]byte{}, 1)
		if !ok || string(val) != "empty" {
			t.Errorf("Get(empty, 1) = (%q, %v), want (empty, true)", val, ok)
		}
	})

	t.Run("nil userKey", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: nil, Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte("nil-key"))

		val, ok := sl.Get(nil, 1)
		if !ok || string(val) != "nil-key" {
			t.Errorf("Get(nil, 1) = (%q, %v), want (nil-key, true)", val, ok)
		}
	})

	t.Run("empty value", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte{})

		val, ok := sl.Get([]byte("k"), 1)
		if !ok {
			t.Error("Get(k, 1): want found, got not found")
		}
		if len(val) != 0 {
			t.Errorf("Get(k, 1) value = %q, want empty", val)
		}
	})

	t.Run("nil value", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 1, Kind: keys.InternalKeyKindPut}, nil)

		val, ok := sl.Get([]byte("k"), 1)
		if !ok {
			t.Error("Get(k, 1): want found, got not found")
		}
		if len(val) != 0 {
			t.Errorf("Get(k, 1) value = %q, want empty", val)
		}
	})

	t.Run("binary key with null bytes", func(t *testing.T) {
		sl := NewSkipList()
		binKey := []byte{0x00, 0x01, 0x00, 0xFF}
		sl.Put(keys.InternalKey{UserKey: binKey, Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte("binary"))

		val, ok := sl.Get(binKey, 1)
		if !ok || string(val) != "binary" {
			t.Errorf("Get(binary, 1) = (%q, %v), want (binary, true)", val, ok)
		}
	})

	t.Run("max seqno", func(t *testing.T) {
		sl := NewSkipList()
		maxSeq := ^uint64(0) // math.MaxUint64
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: maxSeq, Kind: keys.InternalKeyKindPut}, []byte("max"))

		val, ok := sl.Get([]byte("k"), maxSeq)
		if !ok || string(val) != "max" {
			t.Errorf("Get(k, max) = (%q, %v), want (max, true)", val, ok)
		}
	})

	t.Run("seqno 0", func(t *testing.T) {
		sl := NewSkipList()
		sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 0, Kind: keys.InternalKeyKindPut}, []byte("zero"))

		val, ok := sl.Get([]byte("k"), 0)
		if !ok || string(val) != "zero" {
			t.Errorf("Get(k, 0) = (%q, %v), want (zero, true)", val, ok)
		}
	})
}

func TestGet_ValueIsolation(t *testing.T) {
	// Get should return a copy of the value, not a reference to internal storage.
	sl := NewSkipList()
	sl.Put(keys.InternalKey{UserKey: []byte("k"), Seqno: 1, Kind: keys.InternalKeyKindPut}, []byte("original"))

	val, _ := sl.Get([]byte("k"), 1)
	original := string(val)

	// Mutate the returned slice
	val[0] = 'X'

	// Get again and verify it's unchanged
	val2, _ := sl.Get([]byte("k"), 1)
	if string(val2) != original {
		t.Errorf("value was mutated: got %q, want %q", val2, original)
	}
}

func TestGet_Concurrent(t *testing.T) {
	// Concurrent Gets while Puts are happening should not panic or corrupt data.
	sl := NewSkipList()
	const iterations = 500

	var wg sync.WaitGroup
	wg.Add(3)

	// Writer: continuously insert versions of "key"
	go func() {
		defer wg.Done()
		for i := range iterations {
			sl.Put(keys.InternalKey{
				UserKey: []byte("key"),
				Seqno:   uint64(i), //nolint:gosec // test code
				Kind:    keys.InternalKeyKindPut,
			}, []byte("val"))
		}
	}()

	// Reader 1: continuously Get "key"
	go func() {
		defer wg.Done()
		for range iterations {
			_, _ = sl.Get([]byte("key"), 1000)
		}
	}()

	// Reader 2: continuously Get a non-existent key
	go func() {
		defer wg.Done()
		for range iterations {
			_, _ = sl.Get([]byte("nonexistent"), 1000)
		}
	}()

	wg.Wait()

	// Verify final state
	if sl.Len() != iterations {
		t.Errorf("Len() = %d, want %d", sl.Len(), iterations)
	}
}

// === Panic-inducing edge cases ===

func TestSkipList_NilKeyUserKey(t *testing.T) {
	// A key with nil UserKey (not empty slice, but actual nil)
	sl := NewSkipList()
	key := keys.InternalKey{
		UserKey: nil, // nil, not []byte{}
		Seqno:   1,
		Kind:    keys.InternalKeyKindPut,
	}
	sl.Put(key, []byte("nilkey"))

	// Should be able to retrieve it
	n := sl.head.next[0]
	if n == nil {
		t.Fatal("entry not found")
	}
	if string(n.value) != "nilkey" {
		t.Errorf("value = %q, want nilkey", n.value)
	}
}

func TestSkipList_VeryLargeKey(t *testing.T) {
	// Large user key (1MB) - should not panic
	sl := NewSkipList()
	largeKey := make([]byte, 1<<20) // 1 MB
	for i := range largeKey {
		largeKey[i] = byte(i % 256)
	}

	key := keys.InternalKey{
		UserKey: largeKey,
		Seqno:   1,
		Kind:    keys.InternalKeyKindPut,
	}
	sl.Put(key, []byte("large"))

	if sl.Len() != 1 {
		t.Errorf("Len() = %d, want 1", sl.Len())
	}
}

func TestSkipList_VeryLargeValue(t *testing.T) {
	// Large value (1MB) - should not panic
	sl := NewSkipList()
	largeVal := make([]byte, 1<<20) // 1 MB

	key := keys.InternalKey{
		UserKey: []byte("k"),
		Seqno:   1,
		Kind:    keys.InternalKeyKindPut,
	}
	sl.Put(key, largeVal)

	if sl.Len() != 1 {
		t.Errorf("Len() = %d, want 1", sl.Len())
	}
	if sl.Size() < int64(len(largeVal)) {
		t.Errorf("Size() = %d, should be at least %d", sl.Size(), len(largeVal))
	}
}

func TestSkipList_DeleteKind(t *testing.T) {
	// Test that tombstones (KindDelete) work correctly
	sl := NewSkipList()

	putKey := keys.InternalKey{
		UserKey: []byte("foo"),
		Seqno:   1,
		Kind:    keys.InternalKeyKindPut,
	}
	deleteKey := keys.InternalKey{
		UserKey: []byte("foo"),
		Seqno:   2, // Higher seqno = comes first
		Kind:    keys.InternalKeyKindDelete,
	}

	sl.Put(putKey, []byte("value"))
	sl.Put(deleteKey, nil) // Tombstone has no value

	if sl.Len() != 2 {
		t.Errorf("Len() = %d, want 2", sl.Len())
	}

	// Verify ordering: delete@2 should come before put@1
	// (same user key, higher seqno sorts first)
	first := sl.head.next[0]
	if first == nil || first.key.Kind != keys.InternalKeyKindDelete {
		t.Error("first entry should be the delete tombstone")
	}
	second := first.next[0]
	if second == nil || first.key.Kind != keys.InternalKeyKindDelete {
		t.Error("second entry should be the put")
	}
}

func TestFindPredecessors_OnEmptyAfterInit(t *testing.T) {
	// Ensure findPredecessors doesn't panic on a fresh, empty list
	sl := NewSkipList()

	key := keys.InternalKey{
		UserKey: []byte("anything"),
		Seqno:   999,
		Kind:    keys.InternalKeyKindPut,
	}

	// Should not panic
	preds := sl.findPredecessors(key)

	// All predecessors should be head
	for i, p := range preds {
		if p != sl.head {
			t.Errorf("preds[%d] = %v, want head", i, p)
		}
	}
}

// --- Benchmarks ---

func BenchmarkSkipList_Put(b *testing.B) {
	counts := []int{100, 1000, 10000}
	for _, n := range counts {
		// Pre-generate keys outside the timed loop
		ks := make([]keys.InternalKey, n)
		vals := make([][]byte, n)
		for i := range n {
			ks[i] = keys.InternalKey{
				UserKey: fmt.Appendf(nil, "key-%08d", i),
				Seqno:   uint64(i + 1),
				Kind:    keys.InternalKeyKindPut,
			}
			vals[i] = fmt.Appendf(nil, "val-%08d", i)
		}
		b.Run(fmt.Sprintf("%d-entries", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sl := NewSkipList()
				for j := range n {
					sl.Put(ks[j], vals[j])
				}
			}
		})
	}
}

func BenchmarkSkipList_Get_Hit(b *testing.B) {
	const n = 1000
	sl := NewSkipList()
	for i := range n {
		sl.Put(keys.InternalKey{
			UserKey: fmt.Appendf(nil, "key-%08d", i),
			Seqno:   uint64(i + 1),
			Kind:    keys.InternalKeyKindPut,
		}, []byte("value"))
	}
	target := []byte("key-00000500")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = sl.Get(target, uint64(n))
	}
}

func BenchmarkSkipList_Get_Miss(b *testing.B) {
	const n = 1000
	sl := NewSkipList()
	for i := range n {
		sl.Put(keys.InternalKey{
			UserKey: fmt.Appendf(nil, "key-%08d", i),
			Seqno:   uint64(i + 1),
			Kind:    keys.InternalKeyKindPut,
		}, []byte("value"))
	}
	target := []byte("zzz-missing-key")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = sl.Get(target, uint64(n))
	}
}
