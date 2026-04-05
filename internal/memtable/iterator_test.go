package memtable

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aalhour/beachdb/internal/keys"
)

// Helper to insert a Put entry
func putEntry(sl *SkipList, userKey string, seqno uint64, value string) {
	sl.Put(keys.InternalKey{
		UserKey: []byte(userKey),
		Seqno:   seqno,
		Kind:    keys.InternalKeyKindPut,
	}, []byte(value))
}

// Helper to insert a Delete entry
func deleteEntry(sl *SkipList, userKey string, seqno uint64) {
	sl.Put(keys.InternalKey{
		UserKey: []byte(userKey),
		Seqno:   seqno,
		Kind:    keys.InternalKeyKindDelete,
	}, nil)
}

// collectAll iterates and collects all entries as (userKey, seqno, value) tuples
func collectAll(it Iterator) []struct {
	key   string
	seqno uint64
	value string
} {
	var result []struct {
		key   string
		seqno uint64
		value string
	}
	for it.SeekToFirst(); it.Valid(); it.Next() {
		result = append(result, struct {
			key   string
			seqno uint64
			value string
		}{
			key:   string(it.Key().UserKey),
			seqno: it.Key().Seqno,
			value: string(it.Value()),
		})
	}
	return result
}

func TestIterator_Empty(t *testing.T) {
	sl := NewSkipList()
	it := sl.NewIterator()
	defer it.Close()

	it.SeekToFirst()
	if it.Valid() {
		t.Error("Iterator should be invalid on empty list")
	}
}

func TestIterator_FullScan(t *testing.T) {
	sl := NewSkipList()

	// Insert [a, c, e, g] at the same seqno
	putEntry(sl, "a", 1, "val-a")
	putEntry(sl, "c", 1, "val-c")
	putEntry(sl, "e", 1, "val-e")
	putEntry(sl, "g", 1, "val-g")

	it := sl.NewIterator()
	defer it.Close()

	entries := collectAll(it)

	if len(entries) != 4 {
		t.Fatalf("Expected 4 entries, got %d", len(entries))
	}

	expected := []string{"a", "c", "e", "g"}
	for i, e := range entries {
		if e.key != expected[i] {
			t.Errorf("Entry %d: expected key %q, got %q", i, expected[i], e.key)
		}
	}
}

func TestIterator_SeekExact(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "a", 1, "val-a")
	putEntry(sl, "c", 1, "val-c")
	putEntry(sl, "e", 1, "val-e")
	putEntry(sl, "g", 1, "val-g")

	it := sl.NewIterator()
	defer it.Close()

	it.Seek([]byte("c"))

	if !it.Valid() {
		t.Fatal("Iterator should be valid after Seek(c)")
	}
	if string(it.Key().UserKey) != "c" {
		t.Errorf("Expected key 'c', got %q", string(it.Key().UserKey))
	}
	if string(it.Value()) != "val-c" {
		t.Errorf("Expected value 'val-c', got %q", string(it.Value()))
	}
}

func TestIterator_SeekBetween(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "a", 1, "val-a")
	putEntry(sl, "c", 1, "val-c")
	putEntry(sl, "e", 1, "val-e")
	putEntry(sl, "g", 1, "val-g")

	it := sl.NewIterator()
	defer it.Close()

	// Seek to "d" which doesn't exist; should land on "e"
	it.Seek([]byte("d"))

	if !it.Valid() {
		t.Fatal("Iterator should be valid after Seek(d)")
	}
	if string(it.Key().UserKey) != "e" {
		t.Errorf("Expected key 'e', got %q", string(it.Key().UserKey))
	}
}

func TestIterator_SeekPastEnd(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "a", 1, "val-a")
	putEntry(sl, "c", 1, "val-c")

	it := sl.NewIterator()
	defer it.Close()

	// Seek past all entries
	it.Seek([]byte("z"))

	if it.Valid() {
		t.Errorf("Iterator should be invalid after Seek past end, got key %q", string(it.Key().UserKey))
	}
}

func TestIterator_SeekBeforeFirst(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "c", 1, "val-c")
	putEntry(sl, "e", 1, "val-e")

	it := sl.NewIterator()
	defer it.Close()

	// Seek to "a" which is before all entries; should land on "c"
	it.Seek([]byte("a"))

	if !it.Valid() {
		t.Fatal("Iterator should be valid after Seek(a)")
	}
	if string(it.Key().UserKey) != "c" {
		t.Errorf("Expected key 'c', got %q", string(it.Key().UserKey))
	}
}

func TestIterator_MultipleVersions(t *testing.T) {
	sl := NewSkipList()

	// Insert same key at multiple seqnos (descending seqno = newer first in iteration)
	putEntry(sl, "mykey", 3, "v3")
	putEntry(sl, "mykey", 2, "v2")
	putEntry(sl, "mykey", 1, "v1")

	it := sl.NewIterator()
	defer it.Close()

	// Collect all entries
	var versions []struct {
		seqno uint64
		value string
	}
	for it.SeekToFirst(); it.Valid(); it.Next() {
		versions = append(versions, struct {
			seqno uint64
			value string
		}{
			seqno: it.Key().Seqno,
			value: string(it.Value()),
		})
	}

	if len(versions) != 3 {
		t.Fatalf("Expected 3 versions, got %d", len(versions))
	}

	// InternalKey ordering: same user key, seqno DESC
	// So we should see seqno 3, 2, 1 in that order
	expectedSeqnos := []uint64{3, 2, 1}
	expectedValues := []string{"v3", "v2", "v1"}

	for i, v := range versions {
		if v.seqno != expectedSeqnos[i] {
			t.Errorf("Version %d: expected seqno %d, got %d", i, expectedSeqnos[i], v.seqno)
		}
		if v.value != expectedValues[i] {
			t.Errorf("Version %d: expected value %q, got %q", i, expectedValues[i], v.value)
		}
	}
}

func TestIterator_WithTombstones(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "a", 1, "val-a")
	deleteEntry(sl, "b", 2) // tombstone
	putEntry(sl, "c", 1, "val-c")

	it := sl.NewIterator()
	defer it.Close()

	// Iterator should see ALL entries including tombstones
	// (tombstone filtering happens at higher level - merge iterator / snapshot filter)
	var entries []struct {
		key  string
		kind byte
	}

	for it.SeekToFirst(); it.Valid(); it.Next() {
		entries = append(entries, struct {
			key  string
			kind byte
		}{
			key:  string(it.Key().UserKey),
			kind: it.Key().Kind,
		})
	}

	if len(entries) != 3 {
		t.Fatalf("Expected 3 entries (including tombstone), got %d", len(entries))
	}

	// Verify the tombstone is present
	if entries[1].key != "b" || entries[1].kind != keys.InternalKeyKindDelete {
		t.Errorf("Entry 1 should be tombstone for 'b', got key=%q kind=%d", entries[1].key, entries[1].kind)
	}
}

func TestIterator_Next_PastEnd(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "a", 1, "val-a")

	it := sl.NewIterator()
	defer it.Close()

	it.SeekToFirst()
	if !it.Valid() {
		t.Fatal("Should be valid at first entry")
	}

	it.Next()
	if it.Valid() {
		t.Error("Should be invalid after advancing past single entry")
	}

	// Calling Next again should be safe (no panic)
	it.Next()
	if it.Valid() {
		t.Error("Should still be invalid")
	}
}

func TestIterator_KeyValueOnInvalid(t *testing.T) {
	sl := NewSkipList()
	it := sl.NewIterator()
	defer it.Close()

	it.SeekToFirst() // empty list

	// Key and Value should return zero values, not panic
	key := it.Key()
	value := it.Value()

	if key.UserKey != nil {
		t.Error("Key().UserKey should be nil on invalid iterator")
	}
	if value != nil {
		t.Error("Value() should be nil on invalid iterator")
	}
}

func TestIterator_ReseekAfterIteration(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "a", 1, "val-a")
	putEntry(sl, "b", 1, "val-b")
	putEntry(sl, "c", 1, "val-c")

	it := sl.NewIterator()
	defer it.Close()

	// First iteration
	it.SeekToFirst()
	it.Next()
	if string(it.Key().UserKey) != "b" {
		t.Errorf("Expected 'b', got %q", string(it.Key().UserKey))
	}

	// Re-seek to beginning
	it.SeekToFirst()
	if string(it.Key().UserKey) != "a" {
		t.Errorf("After re-seek, expected 'a', got %q", string(it.Key().UserKey))
	}

	// Seek to specific key
	it.Seek([]byte("c"))
	if string(it.Key().UserKey) != "c" {
		t.Errorf("After Seek(c), expected 'c', got %q", string(it.Key().UserKey))
	}
}

func TestIterator_ValueIsCopy(t *testing.T) {
	sl := NewSkipList()

	putEntry(sl, "key", 1, "original")

	it := sl.NewIterator()
	defer it.Close()

	it.SeekToFirst()
	value := it.Value()

	// Modify the returned value
	value[0] = 'X'

	// Get it again
	value2 := it.Value()
	if bytes.Equal(value, value2) {
		t.Error("Value() should return a copy, but mutation was visible")
	}
	if string(value2) != "original" {
		t.Errorf("Expected 'original', got %q", string(value2))
	}
}

func TestIterator_LargeScale(t *testing.T) {
	sl := NewSkipList()
	const n = 1000

	// Insert n entries
	for i := range n {
		key := fmt.Sprintf("key-%05d", i)
		val := fmt.Sprintf("val-%05d", i)
		putEntry(sl, key, 1, val)
	}

	it := sl.NewIterator()
	defer it.Close()

	// Count and verify order
	count := 0
	var prevKey string
	for it.SeekToFirst(); it.Valid(); it.Next() {
		key := string(it.Key().UserKey)
		if prevKey != "" && key <= prevKey {
			t.Errorf("Keys out of order: %q <= %q", key, prevKey)
		}
		prevKey = key
		count++
	}

	if count != n {
		t.Errorf("Expected %d entries, got %d", n, count)
	}
}

func TestIterator_SeekWithMultipleVersions(t *testing.T) {
	sl := NewSkipList()

	// Multiple versions of "key2"
	putEntry(sl, "key1", 1, "v1")
	putEntry(sl, "key2", 3, "v3") // newest
	putEntry(sl, "key2", 2, "v2")
	putEntry(sl, "key2", 1, "v1") // oldest
	putEntry(sl, "key3", 1, "v1")

	it := sl.NewIterator()
	defer it.Close()

	// Seek to "key2" should land on the newest version (seqno 3)
	it.Seek([]byte("key2"))

	if !it.Valid() {
		t.Fatal("Should be valid after seek")
	}

	if string(it.Key().UserKey) != "key2" {
		t.Errorf("Expected user key 'key2', got %q", string(it.Key().UserKey))
	}

	if it.Key().Seqno != 3 {
		t.Errorf("Expected seqno 3 (newest), got %d", it.Key().Seqno)
	}

	// Walk through all versions of key2
	expectedSeqnos := []uint64{3, 2, 1}
	for i, expected := range expectedSeqnos {
		if !it.Valid() {
			t.Fatalf("Iterator became invalid at version %d", i)
		}
		if string(it.Key().UserKey) != "key2" {
			break // moved past key2
		}
		if it.Key().Seqno != expected {
			t.Errorf("Version %d: expected seqno %d, got %d", i, expected, it.Key().Seqno)
		}
		it.Next()
	}
}

func TestIterator_CloseIsIdempotent(t *testing.T) {
	sl := NewSkipList()
	putEntry(sl, "a", 1, "val")

	it := sl.NewIterator()
	it.SeekToFirst()

	// First close should succeed
	if err := it.Close(); err != nil {
		t.Errorf("First Close() failed: %v", err)
	}

	// Second close should also succeed (not panic or error)
	if err := it.Close(); err != nil {
		t.Errorf("Second Close() failed: %v", err)
	}
}

func TestIterator_CloseWithoutSeek(t *testing.T) {
	sl := NewSkipList()
	putEntry(sl, "a", 1, "val")

	it := sl.NewIterator()

	// Close without ever calling Seek/SeekToFirst
	// Should not panic or cause issues
	if err := it.Close(); err != nil {
		t.Errorf("Close() without seek failed: %v", err)
	}
}

func TestIterator_ConcurrentReaders(t *testing.T) {
	sl := NewSkipList()

	// Populate with some data
	for i := range 100 {
		putEntry(sl, fmt.Sprintf("key-%03d", i), 1, fmt.Sprintf("val-%03d", i))
	}

	const numReaders = 10
	var wg sync.WaitGroup
	errors := make(chan error, numReaders)

	// Launch multiple concurrent readers
	for range numReaders {
		wg.Go(func() {
			it := sl.NewIterator()
			defer it.Close()

			// Full scan
			count := 0
			var prevKey string
			for it.SeekToFirst(); it.Valid(); it.Next() {
				key := string(it.Key().UserKey)
				if prevKey != "" && key <= prevKey {
					errors <- fmt.Errorf("keys out of order: %q <= %q", key, prevKey)
					return
				}
				prevKey = key
				count++
			}

			if count != 100 {
				errors <- fmt.Errorf("expected 100 entries, got %d", count)
			}
		})
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}
}

func TestIterator_ConcurrentReadWrite(t *testing.T) {
	sl := NewSkipList()

	// Populate with initial data
	for i := range 50 {
		putEntry(sl, fmt.Sprintf("key-%03d", i), 1, fmt.Sprintf("val-%03d", i))
	}

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Start a reader that holds the lock for a while
	wg.Go(func() {
		it := sl.NewIterator()
		it.SeekToFirst()

		// Hold the read lock while iterating slowly
		count := 0
		for it.Valid() {
			count++
			it.Next()
			// Small delay to simulate work
			select {
			case <-done:
				it.Close()
				return
			default:
			}
		}
		it.Close()
	})

	// Start a writer that will block until reader releases
	wg.Go(func() {
		// This write will block until the reader releases its RLock
		putEntry(sl, "new-key", 2, "new-val")
	})

	// Let things run briefly then signal done
	// Note: This test primarily verifies no deadlock or panic occurs
	go func() {
		// Give enough time for the goroutines to interact
		wg.Wait()
		close(done)
	}()

	// Wait with timeout
	select {
	case <-done:
		// Success - no deadlock
	case <-time.After(5 * time.Second):
		t.Fatal("Test timed out - possible deadlock")
	}
}

func TestIterator_SnapshotConsistency(t *testing.T) {
	sl := NewSkipList()

	// Insert initial data
	for i := range 10 {
		putEntry(sl, fmt.Sprintf("key-%02d", i), 1, "v1")
	}

	// Start an iterator (takes snapshot via RLock)
	it := sl.NewIterator()
	it.SeekToFirst()

	// Collect keys seen by iterator
	var seenKeys []string
	for it.Valid() {
		seenKeys = append(seenKeys, string(it.Key().UserKey))
		it.Next()
	}
	it.Close()

	// Verify we saw all 10 original keys
	if len(seenKeys) != 10 {
		t.Errorf("Expected 10 keys, got %d", len(seenKeys))
	}

	// Verify order
	for i, key := range seenKeys {
		expected := fmt.Sprintf("key-%02d", i)
		if key != expected {
			t.Errorf("Key %d: expected %q, got %q", i, expected, key)
		}
	}
}

// --- Benchmarks ---

func BenchmarkIterator_FullScan(b *testing.B) {
	counts := []int{100, 1000, 10000}
	for _, n := range counts {
		sl := NewSkipList()
		for i := range n {
			putEntry(sl, fmt.Sprintf("key-%08d", i), 1, "val")
		}
		b.Run(fmt.Sprintf("%d-entries", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				it := sl.NewIterator()
				for it.SeekToFirst(); it.Valid(); it.Next() {
					_ = it.Key()
				}
				it.Close()
			}
		})
	}
}

func BenchmarkIterator_Seek(b *testing.B) {
	const n = 1000
	sl := NewSkipList()
	for i := range n {
		putEntry(sl, fmt.Sprintf("key-%08d", i), 1, "val")
	}
	target := []byte("key-00000500") // middle key
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		it := sl.NewIterator()
		it.Seek(target)
		it.Close()
	}
}

func BenchmarkIterator_Next(b *testing.B) {
	const n = 1000
	sl := NewSkipList()
	for i := range n {
		putEntry(sl, fmt.Sprintf("key-%08d", i), 1, "val")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		it := sl.NewIterator()
		it.SeekToFirst()
		for it.Valid() {
			it.Next()
		}
		it.Close()
	}
}
