package testutil

import (
	"math"
	"slices"
	"testing"
)

func TestNewModel(t *testing.T) {
	m := NewModel()

	if m == nil {
		t.Fatal("NewModel returned nil")
	}

	if m.Len() != 0 {
		t.Errorf("new model should have length 0, got %d", m.Len())
	}

	if m.entries == nil {
		t.Error("new model should have initialized entries slice")
	}
}

func TestModel_Put_Get(t *testing.T) {
	m := NewModel()

	key := []byte("name")
	value := []byte("alice")

	m.Put(key, value)

	got, ok := m.Get(key)
	if !ok {
		t.Fatal("Get returned ok=false for existing key")
	}

	if !slices.Equal(got, value) {
		t.Errorf("expected value %q, got %q", value, got)
	}

	// Len returns total entries
	if m.Len() != 1 {
		t.Errorf("expected 1 entry, got %d", m.Len())
	}
}

func TestModel_Get_NotFound(t *testing.T) {
	m := NewModel()

	got, ok := m.Get([]byte("nonexistent"))
	if ok {
		t.Error("Get returned ok=true for non-existent key")
	}

	if got != nil {
		t.Errorf("Get should return nil for non-existent key, got %v", got)
	}
}

func TestModel_Put_ValueCopied(t *testing.T) {
	m := NewModel()

	key := []byte("key")
	value := []byte("original")

	m.Put(key, value)

	// Mutate the original value
	value[0] = 'X'

	// Get should return the original value, not the mutated one
	got, ok := m.Get(key)
	if !ok {
		t.Fatal("Get returned ok=false")
	}

	if got[0] == 'X' {
		t.Error("Put did not copy the value; external mutation affected stored value")
	}

	if string(got) != "original" {
		t.Errorf("expected 'original', got %q", got)
	}
}

func TestModel_Get_ValueCopied(t *testing.T) {
	m := NewModel()

	key := []byte("key")
	value := []byte("value")

	m.Put(key, value)

	// Get the value
	got1, _ := m.Get(key)

	// Mutate the returned value
	got1[0] = 'X'

	// Get again - should return original value, not mutated one
	got2, ok := m.Get(key)
	if !ok {
		t.Fatal("Get returned ok=false")
	}

	if got2[0] == 'X' {
		t.Error("Get did not copy the value; external mutation affected stored value")
	}

	if string(got2) != "value" {
		t.Errorf("expected 'value', got %q", got2)
	}
}

func TestModel_Delete(t *testing.T) {
	m := NewModel()

	key := []byte("key")
	value := []byte("value")

	m.Put(key, value)

	// Verify it exists
	_, ok := m.Get(key)
	if !ok {
		t.Fatal("key should exist before delete")
	}

	if m.Len() != 1 {
		t.Errorf("expected 1 entry before delete, got %d", m.Len())
	}

	// Delete adds a tombstone
	m.Delete(key)

	// Key should be found as a tombstone (nil value, found=true)
	val, ok := m.Get(key)
	if !ok || val != nil {
		t.Errorf("expected (nil, true) for tombstone, got (%q, %v)", val, ok)
	}

	// But Len() counts all entries (put + tombstone)
	if m.Len() != 2 {
		t.Errorf("expected 2 entries after delete (put + tombstone), got %d", m.Len())
	}
}

func TestModel_Delete_NonExistent(t *testing.T) {
	m := NewModel()

	// Deleting non-existent key adds a tombstone
	m.Delete([]byte("nonexistent"))

	// Tombstone is still an entry
	if m.Len() != 1 {
		t.Errorf("expected 1 entry (tombstone), got %d", m.Len())
	}

	// Key should be found as a tombstone
	val, ok := m.Get([]byte("nonexistent"))
	if !ok || val != nil {
		t.Errorf("expected (nil, true) for tombstone, got (%q, %v)", val, ok)
	}
}

func TestModel_Overwrite(t *testing.T) {
	m := NewModel()

	key := []byte("key")
	value1 := []byte("value1")
	value2 := []byte("value2")

	m.Put(key, value1)

	got, _ := m.Get(key)
	if !slices.Equal(got, value1) {
		t.Errorf("expected %q, got %q", value1, got)
	}

	// "Overwrite" adds a new entry with higher seqno
	m.Put(key, value2)

	got, _ = m.Get(key)
	if !slices.Equal(got, value2) {
		t.Errorf("expected %q, got %q", value2, got)
	}

	// Len counts all entries (both versions exist)
	if m.Len() != 2 {
		t.Errorf("expected 2 entries after overwrite, got %d", m.Len())
	}
}

func TestModel_Keys(t *testing.T) {
	m := NewModel()

	// Empty model should return empty slice
	keys := m.Keys()
	if len(keys) != 0 {
		t.Errorf("empty model should return 0 keys, got %d", len(keys))
	}

	// Add some keys
	m.Put([]byte("key1"), []byte("value1"))
	m.Put([]byte("key2"), []byte("value2"))
	m.Put([]byte("key3"), []byte("value3"))

	keys = m.Keys()
	if len(keys) != 3 {
		t.Errorf("expected 3 unique keys, got %d", len(keys))
	}

	// Verify all keys are present (order doesn't matter)
	keyStrs := make(map[string]bool)
	for _, k := range keys {
		keyStrs[string(k)] = true
	}

	expectedKeys := []string{"key1", "key2", "key3"}
	for _, expected := range expectedKeys {
		if !keyStrs[expected] {
			t.Errorf("expected key %q not found in Keys()", expected)
		}
	}
}

func TestModel_Keys_Unique(t *testing.T) {
	m := NewModel()

	// Add multiple versions of same key
	m.Put([]byte("key"), []byte("v1"))
	m.Put([]byte("key"), []byte("v2"))
	m.Put([]byte("key"), []byte("v3"))

	keys := m.Keys()
	if len(keys) != 1 {
		t.Errorf("Keys() should return unique keys; expected 1, got %d", len(keys))
	}

	if string(keys[0]) != "key" {
		t.Errorf("expected key 'key', got %q", keys[0])
	}

	// But Len() counts all entries
	if m.Len() != 3 {
		t.Errorf("expected 3 entries, got %d", m.Len())
	}
}

func TestModel_EmptyKeyValue(t *testing.T) {
	t.Run("empty key", func(t *testing.T) {
		m := NewModel()
		m.Put([]byte{}, []byte("value"))

		got, ok := m.Get([]byte{})
		if !ok {
			t.Error("should be able to use empty key")
		}
		if string(got) != "value" {
			t.Errorf("expected 'value', got %q", got)
		}
	})

	t.Run("empty value", func(t *testing.T) {
		m := NewModel()
		m.Put([]byte("key"), []byte{})

		got, ok := m.Get([]byte("key"))
		if !ok {
			t.Error("should be able to use empty value")
		}
		if len(got) != 0 {
			t.Errorf("expected empty value, got %v", got)
		}
	})

	t.Run("empty key and value", func(t *testing.T) {
		m := NewModel()
		m.Put([]byte{}, []byte{})

		got, ok := m.Get([]byte{})
		if !ok {
			t.Error("should be able to use empty key and value")
		}
		if len(got) != 0 {
			t.Errorf("expected empty value, got %v", got)
		}
	})
}

func TestModel_BinaryData(t *testing.T) {
	m := NewModel()

	// Binary key and value with null bytes
	key := []byte{0x00, 0x01, 0xFF, 0xFE}
	value := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	m.Put(key, value)

	got, ok := m.Get(key)
	if !ok {
		t.Fatal("Get returned ok=false for binary key")
	}

	if !slices.Equal(got, value) {
		t.Errorf("binary value mismatch: expected %v, got %v", value, got)
	}
}

func TestModel_MultipleOperations(t *testing.T) {
	m := NewModel()

	// Simulate a complex sequence
	m.Put([]byte("a"), []byte("1"))
	m.Put([]byte("b"), []byte("2"))
	m.Put([]byte("c"), []byte("3"))

	if m.Len() != 3 {
		t.Errorf("expected 3 entries, got %d", m.Len())
	}

	m.Delete([]byte("b")) // Adds tombstone

	if m.Len() != 4 {
		t.Errorf("expected 4 entries after delete, got %d", m.Len())
	}

	m.Put([]byte("d"), []byte("4"))
	m.Put([]byte("a"), []byte("updated")) // Second version of "a"

	if m.Len() != 6 {
		t.Errorf("expected 6 entries, got %d", m.Len())
	}

	// Verify final visible state
	got, _ := m.Get([]byte("a"))
	if string(got) != "updated" {
		t.Errorf("expected 'updated', got %q", got)
	}

	val, ok := m.Get([]byte("b"))
	if !ok || val != nil {
		t.Errorf("key 'b': expected (nil, true) for tombstone, got (%q, %v)", val, ok)
	}

	got, _ = m.Get([]byte("c"))
	if string(got) != "3" {
		t.Errorf("expected '3', got %q", got)
	}

	got, _ = m.Get([]byte("d"))
	if string(got) != "4" {
		t.Errorf("expected '4', got %q", got)
	}

	// Keys should return unique visible keys (excluding tombstoned)
	// Note: Keys() returns all unique keys that have entries, even if tombstoned
	keys := m.Keys()
	if len(keys) != 4 {
		t.Errorf("expected 4 unique keys, got %d", len(keys))
	}
}

// --- MVCC / Seqno-aware tests ---

func TestModel_PutAt_GetAt(t *testing.T) {
	m := NewModel()

	key := []byte("key")

	// Write at seqno 1
	m.PutAt(key, []byte("v1"), 1)

	// Write at seqno 3
	m.PutAt(key, []byte("v3"), 3)

	// Write at seqno 2 (out of order, simulating concurrent writes)
	m.PutAt(key, []byte("v2"), 2)

	// GetAt seqno 1 should return v1
	got, ok := m.GetAt(key, 1)
	if !ok {
		t.Fatal("GetAt(1) should find v1")
	}
	if string(got) != "v1" {
		t.Errorf("GetAt(1): expected 'v1', got %q", got)
	}

	// GetAt seqno 2 should return v2 (newest <= 2)
	got, ok = m.GetAt(key, 2)
	if !ok {
		t.Fatal("GetAt(2) should find v2")
	}
	if string(got) != "v2" {
		t.Errorf("GetAt(2): expected 'v2', got %q", got)
	}

	// GetAt seqno 3 should return v3
	got, ok = m.GetAt(key, 3)
	if !ok {
		t.Fatal("GetAt(3) should find v3")
	}
	if string(got) != "v3" {
		t.Errorf("GetAt(3): expected 'v3', got %q", got)
	}

	// GetAt seqno 100 should still return v3 (newest <= 100)
	got, ok = m.GetAt(key, 100)
	if !ok {
		t.Fatal("GetAt(100) should find v3")
	}
	if string(got) != "v3" {
		t.Errorf("GetAt(100): expected 'v3', got %q", got)
	}

	// GetAt seqno 0 should find nothing
	_, ok = m.GetAt(key, 0)
	if ok {
		t.Error("GetAt(0) should not find anything")
	}
}

func TestModel_DeleteAt_GetAt(t *testing.T) {
	m := NewModel()

	key := []byte("key")

	// Write at seqno 1
	m.PutAt(key, []byte("v1"), 1)

	// Delete at seqno 2
	m.DeleteAt(key, 2)

	// Write again at seqno 3 (resurrection)
	m.PutAt(key, []byte("v3"), 3)

	// GetAt seqno 1 should return v1
	got, ok := m.GetAt(key, 1)
	if !ok {
		t.Fatal("GetAt(1) should find v1")
	}
	if string(got) != "v1" {
		t.Errorf("GetAt(1): expected 'v1', got %q", got)
	}

	// GetAt seqno 2 should return (nil, true) — tombstone is a definitive answer
	got, ok = m.GetAt(key, 2)
	if !ok {
		t.Error("GetAt(2) should return found=true for tombstone")
	}
	if got != nil {
		t.Errorf("GetAt(2) should return nil value for tombstone, got %q", got)
	}

	// GetAt seqno 3 should return v3 (resurrected)
	got, ok = m.GetAt(key, 3)
	if !ok {
		t.Fatal("GetAt(3) should find v3")
	}
	if string(got) != "v3" {
		t.Errorf("GetAt(3): expected 'v3', got %q", got)
	}
}

func TestModel_GetAt_SeqnoVisibility(t *testing.T) {
	m := NewModel()

	// Simulate writes at specific seqnos
	m.PutAt([]byte("a"), []byte("a1"), 1)
	m.PutAt([]byte("b"), []byte("b2"), 2)
	m.PutAt([]byte("a"), []byte("a3"), 3)
	m.DeleteAt([]byte("b"), 4)
	m.PutAt([]byte("c"), []byte("c5"), 5)

	tests := []struct {
		key   string
		seqno uint64
		want  string
		found bool
	}{
		{"a", 0, "", false},
		{"a", 1, "a1", true},
		{"a", 2, "a1", true},
		{"a", 3, "a3", true},
		{"a", 100, "a3", true},
		{"b", 1, "", false},
		{"b", 2, "b2", true},
		{"b", 3, "b2", true},
		{"b", 4, "", true}, // tombstone: found=true, value=nil
		{"b", 100, "", true},
		{"c", 4, "", false},
		{"c", 5, "c5", true},
	}

	for _, tc := range tests {
		got, ok := m.GetAt([]byte(tc.key), tc.seqno)
		if ok != tc.found {
			t.Errorf("GetAt(%q, %d): found=%v, want %v", tc.key, tc.seqno, ok, tc.found)
			continue
		}
		if tc.found && string(got) != tc.want {
			t.Errorf("GetAt(%q, %d): got %q, want %q", tc.key, tc.seqno, got, tc.want)
		}
	}
}

func TestModel_Put_UsesMaxSeqno(t *testing.T) {
	m := NewModel()

	// Put without seqno should use MaxUint64
	m.Put([]byte("key"), []byte("value"))

	// Verify the entry has MaxUint64 seqno
	if m.entries[0].seqno != math.MaxUint64 {
		t.Errorf("Put should use MaxUint64 seqno, got %d", m.entries[0].seqno)
	}

	// Should be visible via Get() which uses MaxUint64
	got, ok := m.Get([]byte("key"))
	if !ok {
		t.Error("Put should be visible via Get()")
	}
	if string(got) != "value" {
		t.Errorf("expected 'value', got %q", got)
	}

	// Should NOT be visible at small seqno (MaxUint64 > 1)
	_, ok = m.GetAt([]byte("key"), 1)
	if ok {
		t.Error("Put with MaxUint64 seqno should NOT be visible at seqno 1")
	}

	// Should be visible at MaxUint64
	got, ok = m.GetAt([]byte("key"), math.MaxUint64)
	if !ok {
		t.Error("Put should be visible at seqno MaxUint64")
	}
	if string(got) != "value" {
		t.Errorf("expected 'value', got %q", got)
	}
}

func TestModel_Delete_UsesMaxSeqno(t *testing.T) {
	m := NewModel()

	m.PutAt([]byte("key"), []byte("value"), 1)
	m.Delete([]byte("key"))

	// Verify the tombstone has MaxUint64 seqno
	if m.entries[1].seqno != math.MaxUint64 {
		t.Errorf("Delete should use MaxUint64 seqno, got %d", m.entries[1].seqno)
	}

	// At seqno 1, only the put is visible (tombstone at MaxUint64 is "too new")
	got, ok := m.GetAt([]byte("key"), 1)
	if !ok {
		t.Error("key should be visible at seqno 1 (before tombstone)")
	}
	if string(got) != "value" {
		t.Errorf("expected 'value', got %q", got)
	}

	// Via Get() (which uses MaxUint64), the tombstone wins — returns (nil, true)
	val, ok := m.Get([]byte("key"))
	if !ok || val != nil {
		t.Errorf("expected (nil, true) for tombstone via Get(), got (%q, %v)", val, ok)
	}
}
