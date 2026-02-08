package testutil

import (
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

	if m.data == nil {
		t.Error("new model should have initialized data map")
	}
}

func TestModel_Put_Get(t *testing.T) {
	m := NewModel()

	key := []byte("name")
	value := []byte("alice")

	// Put a key-value pair
	m.Put(key, value)

	// Get it back
	got, ok := m.Get(key)
	if !ok {
		t.Fatal("Get returned ok=false for existing key")
	}

	if !slices.Equal(got, value) {
		t.Errorf("expected value %q, got %q", value, got)
	}

	// Check length
	if m.Len() != 1 {
		t.Errorf("expected length 1, got %d", m.Len())
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

	// Put the value
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

	// Put a value
	m.Put(key, value)

	// Verify it exists
	_, ok := m.Get(key)
	if !ok {
		t.Fatal("key should exist before delete")
	}

	if m.Len() != 1 {
		t.Errorf("expected length 1 before delete, got %d", m.Len())
	}

	// Delete it
	m.Delete(key)

	// Verify it's gone
	_, ok = m.Get(key)
	if ok {
		t.Error("key should not exist after delete")
	}

	if m.Len() != 0 {
		t.Errorf("expected length 0 after delete, got %d", m.Len())
	}
}

func TestModel_Delete_NonExistent(t *testing.T) {
	m := NewModel()

	// Deleting non-existent key should not panic
	m.Delete([]byte("nonexistent"))

	if m.Len() != 0 {
		t.Errorf("expected length 0, got %d", m.Len())
	}
}

func TestModel_Overwrite(t *testing.T) {
	m := NewModel()

	key := []byte("key")
	value1 := []byte("value1")
	value2 := []byte("value2")

	// Put first value
	m.Put(key, value1)

	got, _ := m.Get(key)
	if !slices.Equal(got, value1) {
		t.Errorf("expected %q, got %q", value1, got)
	}

	// Overwrite with second value
	m.Put(key, value2)

	got, _ = m.Get(key)
	if !slices.Equal(got, value2) {
		t.Errorf("expected %q, got %q", value2, got)
	}

	// Length should still be 1
	if m.Len() != 1 {
		t.Errorf("expected length 1 after overwrite, got %d", m.Len())
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
		t.Errorf("expected 3 keys, got %d", len(keys))
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

func TestModel_EmptyKeyValue(t *testing.T) {
	m := NewModel()

	t.Run("empty key", func(t *testing.T) {
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
		t.Errorf("expected length 3, got %d", m.Len())
	}

	m.Delete([]byte("b"))

	if m.Len() != 2 {
		t.Errorf("expected length 2 after delete, got %d", m.Len())
	}

	m.Put([]byte("d"), []byte("4"))
	m.Put([]byte("a"), []byte("updated"))

	if m.Len() != 3 {
		t.Errorf("expected length 3, got %d", m.Len())
	}

	// Verify final state
	got, _ := m.Get([]byte("a"))
	if string(got) != "updated" {
		t.Errorf("expected 'updated', got %q", got)
	}

	_, ok := m.Get([]byte("b"))
	if ok {
		t.Error("key 'b' should not exist")
	}

	got, _ = m.Get([]byte("c"))
	if string(got) != "3" {
		t.Errorf("expected '3', got %q", got)
	}

	got, _ = m.Get([]byte("d"))
	if string(got) != "4" {
		t.Errorf("expected '4', got %q", got)
	}
}
