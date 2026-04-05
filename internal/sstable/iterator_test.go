package sstable

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
)

// --- SeekToFirst tests ---

// Spec: "SeekToFirst on empty SSTable → Valid() returns false"
func TestIterator_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.sst")
	writeSSTable(t, path, nil)
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.SeekToFirst()

	if it.Valid() {
		t.Fatal("expected Valid() == false on empty SSTable")
	}
	if it.Err() != nil {
		t.Fatalf("expected nil error on empty table, got %v", it.Err())
	}
}

// Spec: "SeekToFirst on non-empty SSTable → first entry"
func TestIterator_SeekToFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("alpha", 1), []byte("val-alpha")},
		{putKey("beta", 1), []byte("val-beta")},
		{putKey("gamma", 1), []byte("val-gamma")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.SeekToFirst()

	if !it.Valid() {
		t.Fatalf("expected Valid() == true, err=%v", it.Err())
	}

	key := it.Key()
	if !bytes.Equal(key.UserKey, []byte("alpha")) {
		t.Fatalf("expected first key 'alpha', got %q", key.UserKey)
	}
	val := it.Value()
	if !bytes.Equal(val, []byte("val-alpha")) {
		t.Fatalf("expected first value 'val-alpha', got %q", val)
	}
}

// SeekToFirst is idempotent — calling it after partial iteration resets to the beginning
func TestIterator_SeekToFirstIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
		{putKey("c", 1), []byte("3")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.SeekToFirst()
	it.Next()

	// Reset
	it.SeekToFirst()
	if !it.Valid() {
		t.Fatalf("expected valid after second SeekToFirst, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("a")) {
		t.Fatalf("expected 'a' after reset, got %q", it.Key().UserKey)
	}
}

// --- Full scan tests ---

// Spec: "Full scan visits all entries in order, does not collapse versions,
// does not filter tombstones"
func TestIterator_FullScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scan.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 3), []byte("v1")},
		{putKey("a", 1), []byte("v2")},
		{putKey("b", 2), []byte("v3")},
		{deleteKey("c", 5), nil},
		{putKey("d", 4), []byte("v4")},
	}
	writeSSTable(t, path, entries)
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	var collected []keys.InternalKey
	for it.SeekToFirst(); it.Valid(); it.Next() {
		collected = append(collected, it.Key())
	}
	if err := it.Err(); err != nil {
		t.Fatalf("unexpected error after full scan: %v", err)
	}
	if len(collected) != len(entries) {
		t.Fatalf("expected %d entries, got %d", len(entries), len(collected))
	}
	for i, got := range collected {
		want := entries[i].key
		if !bytes.Equal(got.UserKey, want.UserKey) || got.Seqno != want.Seqno || got.Kind != want.Kind {
			t.Errorf("entry %d: got %+v, want %+v", i, got, want)
		}
	}
}

// Single entry table: SeekToFirst, confirm valid, Next exhausts
func TestIterator_SingleEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "single.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("only", 1), []byte("v")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.SeekToFirst()
	if !it.Valid() {
		t.Fatalf("expected valid, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("only")) {
		t.Fatalf("expected 'only', got %q", it.Key().UserKey)
	}

	it.Next()
	if it.Valid() {
		t.Fatal("expected invalid after Next past single entry")
	}
	if it.Err() != nil {
		t.Fatalf("expected nil error after exhaustion, got %v", it.Err())
	}
}

// --- Seek tests ---

// Spec: "Seek to an exact key → lands on that key"
func TestIterator_SeekExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seek.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("apple", 1), []byte("a")},
		{putKey("banana", 1), []byte("b")},
		{putKey("cherry", 1), []byte("c")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("banana"))

	if !it.Valid() {
		t.Fatalf("expected valid after Seek('banana'), err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("banana")) {
		t.Fatalf("expected 'banana', got %q", it.Key().UserKey)
	}
}

// Spec: "Seek between keys → lands on the next higher key"
func TestIterator_SeekMiddle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seek-mid.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("apple", 1), []byte("a")},
		{putKey("cherry", 1), []byte("c")},
		{putKey("grape", 1), []byte("g")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("banana"))

	if !it.Valid() {
		t.Fatalf("expected valid after Seek('banana'), err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("cherry")) {
		t.Fatalf("expected 'cherry', got %q", it.Key().UserKey)
	}
}

// Spec: "Seek past all keys → Valid() returns false"
func TestIterator_SeekPastEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seek-past.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("alpha", 1), []byte("a")},
		{putKey("beta", 1), []byte("b")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("zzz"))

	if it.Valid() {
		t.Fatal("expected invalid after Seek past all keys")
	}
	if it.Err() != nil {
		t.Fatalf("expected nil error, got %v", it.Err())
	}
}

// Seek before all keys → lands on first key
func TestIterator_SeekBeforeAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seek-before.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("beta", 1), []byte("b")},
		{putKey("gamma", 1), []byte("g")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("alpha"))

	if !it.Valid() {
		t.Fatalf("expected valid after Seek before all keys, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("beta")) {
		t.Fatalf("expected 'beta', got %q", it.Key().UserKey)
	}
}

// Seek to exact first key should behave like SeekToFirst
func TestIterator_SeekToFirstKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("alpha", 1), []byte("a")},
		{putKey("beta", 1), []byte("b")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("alpha"))

	if !it.Valid() {
		t.Fatalf("expected valid, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("alpha")) {
		t.Fatalf("expected 'alpha', got %q", it.Key().UserKey)
	}
}

// Seek to exact last key, then Next should exhaust
func TestIterator_SeekToLastKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("alpha", 1), []byte("a")},
		{putKey("beta", 1), []byte("b")},
		{putKey("gamma", 1), []byte("g")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("gamma"))

	if !it.Valid() {
		t.Fatalf("expected valid, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("gamma")) {
		t.Fatalf("expected 'gamma', got %q", it.Key().UserKey)
	}

	it.Next()
	if it.Valid() {
		t.Fatal("expected invalid after Next past last key")
	}
}

// Seek with multiple versions should land on the newest (highest seqno)
func TestIterator_SeekLandsOnNewestVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "versions.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("foo", 10), []byte("v10")},
		{putKey("foo", 5), []byte("v5")},
		{putKey("foo", 1), []byte("v1")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("foo"))

	if !it.Valid() {
		t.Fatalf("expected valid, err=%v", it.Err())
	}
	if it.Key().Seqno != 10 {
		t.Fatalf("expected seqno 10, got %d", it.Key().Seqno)
	}
}

// Seek on empty SSTable should not panic
func TestIterator_SeekOnEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.sst")
	writeSSTable(t, path, nil)
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte("anything"))

	if it.Valid() {
		t.Fatal("expected invalid on Seek into empty SSTable")
	}
}

// Multiple Seek calls reposition correctly
func TestIterator_MultipleSeeks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
		{putKey("c", 1), []byte("3")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()

	it.Seek([]byte("c"))
	if !it.Valid() || !bytes.Equal(it.Key().UserKey, []byte("c")) {
		t.Fatalf("first Seek: expected 'c', got valid=%v key=%q", it.Valid(), it.Key().UserKey)
	}

	it.Seek([]byte("a"))
	if !it.Valid() || !bytes.Equal(it.Key().UserKey, []byte("a")) {
		t.Fatalf("second Seek: expected 'a', got valid=%v key=%q", it.Valid(), it.Key().UserKey)
	}
}

// SeekToFirst after Seek-past-end should reposition at the beginning
func TestIterator_SeekToFirstAfterExhaustion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()

	// Exhaust via Seek past end
	it.Seek([]byte("zzz"))
	if it.Valid() {
		t.Fatal("expected invalid after Seek past end")
	}

	// Re-seek to beginning
	it.SeekToFirst()
	if !it.Valid() {
		t.Fatalf("expected valid after SeekToFirst following exhaustion, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("a")) {
		t.Fatalf("expected 'a', got %q", it.Key().UserKey)
	}
}

// Seek after full-scan exhaustion should reposition
func TestIterator_SeekAfterFullScanExhaustion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
		{putKey("c", 1), []byte("3")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()

	// Exhaust via full scan
	it.SeekToFirst()
	for it.Valid() {
		it.Next()
	}

	// Seek should still work
	it.Seek([]byte("b"))
	if !it.Valid() {
		t.Fatalf("expected valid after Seek following exhaustion, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("b")) {
		t.Fatalf("expected 'b', got %q", it.Key().UserKey)
	}
}

// Seek with empty/nil target should land on first entry
func TestIterator_SeekEmptyTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
	})
	r := openReader(t, path)
	defer r.Close()

	// Empty slice — everything is >= empty
	it := r.NewIterator()
	it.Seek([]byte{})
	if !it.Valid() {
		t.Fatalf("expected valid after Seek(empty), err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("a")) {
		t.Fatalf("expected 'a', got %q", it.Key().UserKey)
	}

	// Nil target
	it.Seek(nil)
	if !it.Valid() {
		t.Fatalf("expected valid after Seek(nil), err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte("a")) {
		t.Fatalf("expected 'a', got %q", it.Key().UserKey)
	}
}

// --- Block boundary tests ---

// Spec: "Block transitions are transparent"
func TestIterator_CrossBlockBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cross-block.sst")

	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, 50)
	for i := range 50 {
		k := []byte{byte('a' + i/26), byte('a' + i%26)}
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{
			key:   keys.InternalKey{UserKey: k, Seqno: 1, Kind: keys.InternalKeyKindPut},
			value: bytes.Repeat([]byte("x"), 64),
		})
	}

	writeSSTable(t, path, entries, WithBlockSize(100))
	r := openReader(t, path)
	defer r.Close()

	if len(r.index) < 2 {
		t.Fatalf("expected multiple blocks, got %d", len(r.index))
	}

	it := r.NewIterator()
	count := 0
	var prev keys.InternalKey
	for it.SeekToFirst(); it.Valid(); it.Next() {
		key := it.Key()
		if count > 0 && prev.Compare(key) >= 0 {
			t.Fatalf("entry %d out of order: %+v >= %+v", count, prev, key)
		}
		prev = key
		count++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != len(entries) {
		t.Fatalf("expected %d entries, got %d", len(entries), count)
	}
}

// Seek + Next continues correctly across block boundaries
func TestIterator_SeekThenNextAcrossBlocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seek-next.sst")

	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, 20)
	for i := range 20 {
		k := []byte{byte('a' + i)}
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{
			key:   keys.InternalKey{UserKey: k, Seqno: 1, Kind: keys.InternalKeyKindPut},
			value: bytes.Repeat([]byte("v"), 64),
		})
	}

	writeSSTable(t, path, entries, WithBlockSize(100))
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Seek([]byte{byte('a' + 5)})

	if !it.Valid() {
		t.Fatalf("expected valid after Seek, err=%v", it.Err())
	}

	count := 0
	for ; it.Valid(); it.Next() {
		count++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 15 {
		t.Fatalf("expected 15 entries after Seek to index 5, got %d", count)
	}
}

// Seek to a key that is the last key in a block (block boundary key)
func TestIterator_SeekToBlockBoundaryKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "boundary.sst")

	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, 20)
	for i := range 20 {
		k := []byte{byte('a' + i)}
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{
			key:   keys.InternalKey{UserKey: k, Seqno: 1, Kind: keys.InternalKeyKindPut},
			value: bytes.Repeat([]byte("v"), 64),
		})
	}

	writeSSTable(t, path, entries, WithBlockSize(100))
	r := openReader(t, path)
	defer r.Close()

	if len(r.index) < 2 {
		t.Fatalf("expected multiple blocks, got %d", len(r.index))
	}

	// The last key in block 0 is the boundary key
	boundaryKey := r.index[0].lastKey.UserKey

	it := r.NewIterator()
	it.Seek(boundaryKey)

	if !it.Valid() {
		t.Fatalf("expected valid after Seek to boundary key, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, boundaryKey) {
		t.Fatalf("expected boundary key %q, got %q", boundaryKey, it.Key().UserKey)
	}
}

// --- Safety tests ---

// Next on a fresh (un-seeked) iterator should not panic
func TestIterator_NextWithoutSeek(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("v")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.Next()
	if it.Valid() {
		t.Fatal("expected invalid after Next without prior Seek")
	}
}

// Next past end: exhaustion is not an error, repeated Next stays invalid
func TestIterator_NextPastEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "end.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("only", 1), []byte("v")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.SeekToFirst()
	it.Next()

	if it.Valid() {
		t.Fatal("expected invalid after Next past end")
	}
	if it.Err() != nil {
		t.Fatalf("expected nil error after exhaustion, got %v", it.Err())
	}

	// Repeated Next should remain safe
	it.Next()
	it.Next()
	if it.Valid() {
		t.Fatal("expected still invalid after extra Next calls")
	}
}

// Key() and Value() on invalid iterator should not panic
func TestIterator_KeyValueWhenInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, nil)
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.SeekToFirst()

	_ = it.Key()
	_ = it.Value()
}

// --- Close / lifecycle tests ---

// Operations after Close should not panic
func TestIterator_UseAfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("v")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	if err := it.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	it.SeekToFirst()
	if it.Valid() {
		t.Fatal("expected invalid after Close + SeekToFirst")
	}
	it.Seek([]byte("a"))
	if it.Valid() {
		t.Fatal("expected invalid after Close + Seek")
	}
	it.Next()
	if it.Valid() {
		t.Fatal("expected invalid after Close + Next")
	}
	_ = it.Key()
	_ = it.Value()
}

// Double close should return error, not panic
func TestIterator_DoubleClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("v")},
	})
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	if err := it.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := it.Close(); err == nil {
		t.Fatal("expected error on double Close")
	}
}

// Reader closed under an active iterator should not panic
func TestIterator_ReaderClosedUnderIterator(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
		{putKey("c", 1), []byte("3")},
	})
	r := openReader(t, path)

	it := r.NewIterator()
	it.SeekToFirst()
	if !it.Valid() {
		t.Fatalf("expected valid, err=%v", it.Err())
	}

	// Close the reader while iterator is mid-scan
	if err := r.Close(); err != nil {
		t.Fatalf("Reader.Close failed: %v", err)
	}

	// Next should fail gracefully (error or invalid), not panic
	it.Next()
	// We don't prescribe the exact behavior, just that it doesn't crash
}

// --- Independence tests ---

// Two iterators on the same reader should be independent
func TestIterator_TwoIteratorsIndependent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	writeSSTable(t, path, []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
		{putKey("c", 1), []byte("3")},
	})
	r := openReader(t, path)
	defer r.Close()

	it1 := r.NewIterator()
	it2 := r.NewIterator()

	it1.SeekToFirst()
	it2.Seek([]byte("c"))

	if !bytes.Equal(it1.Key().UserKey, []byte("a")) {
		t.Fatalf("it1 expected 'a', got %q", it1.Key().UserKey)
	}
	if !bytes.Equal(it2.Key().UserKey, []byte("c")) {
		t.Fatalf("it2 expected 'c', got %q", it2.Key().UserKey)
	}

	// Advancing it1 should not affect it2
	it1.Next()
	if !bytes.Equal(it1.Key().UserKey, []byte("b")) {
		t.Fatalf("it1 expected 'b', got %q", it1.Key().UserKey)
	}
	if !bytes.Equal(it2.Key().UserKey, []byte("c")) {
		t.Fatalf("it2 should still be at 'c', got %q", it2.Key().UserKey)
	}
}

// --- Adversarial input tests ---

// Binary keys with null bytes and 0xFF
func TestIterator_BinaryKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("\x00\x00", 1), []byte("null-key")},
		{putKey("\x00\x01", 1), []byte("low-key")},
		{putKey("\x80\x81", 1), []byte("high-key")},
		{putKey("\xff\xff", 1), []byte("max-key")},
	}
	writeSSTable(t, path, entries)
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	count := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		count++
	}
	if count != len(entries) {
		t.Fatalf("expected %d entries, got %d", len(entries), count)
	}

	// Seek to a high-bit key
	it.Seek([]byte{0x80, 0x81})
	if !it.Valid() {
		t.Fatalf("expected valid after Seek to high-bit key, err=%v", it.Err())
	}
	if !bytes.Equal(it.Key().UserKey, []byte{0x80, 0x81}) {
		t.Fatalf("expected key \\x80\\x81, got %x", it.Key().UserKey)
	}
}

// All entries are tombstones — full scan should visit every one
func TestIterator_AllTombstones(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tombstones.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{deleteKey("a", 3), nil},
		{deleteKey("b", 2), nil},
		{deleteKey("c", 1), nil},
	}
	writeSSTable(t, path, entries)
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	count := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if it.Key().Kind != keys.InternalKeyKindDelete {
			t.Fatalf("entry %d: expected delete, got kind=%d", count, it.Key().Kind)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("expected 3 tombstones, got %d", count)
	}
}
