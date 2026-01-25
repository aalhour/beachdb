package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestDBOpen(t *testing.T) {
	tests := []struct {
		name    string
		opts    []Option
		wantErr bool
	}{
		{
			name:    "open with default options",
			opts:    nil,
			wantErr: false,
		},
		{
			name:    "open with sync enabled",
			opts:    []Option{WithSync(true)},
			wantErr: false,
		},
		{
			name:    "open with sync disabled",
			opts:    []Option{WithSync(false)},
			wantErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Create temp dir
			dir := t.TempDir()

			db, err := Open(dir, test.opts...)

			if test.wantErr && err == nil {
				t.Fatal("expected error but got none")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !test.wantErr {
				if db == nil {
					t.Fatal("expected non-nil DB but got nil")
				}

				// Verify directory was created
				if _, err := os.Stat(dir); os.IsNotExist(err) {
					t.Errorf("directory %s was not created", dir)
				}

				// Verify WAL file was created
				walPath := filepath.Join(dir, "beachdb.wal")
				if _, err := os.Stat(walPath); os.IsNotExist(err) {
					t.Errorf("WAL file %s was not created", walPath)
				}

				// Clean up
				if db.wal != nil {
					db.Close()
				}
			}
		})
	}
}

func TestDBPutGet(t *testing.T) {
	tests := []struct {
		name    string
		key     []byte
		value   []byte
		wantErr bool
	}{
		{
			name:    "simple key-value",
			key:     []byte("name"),
			value:   []byte("alice"),
			wantErr: false,
		},
		{
			name:    "empty key",
			key:     []byte{},
			value:   []byte("value"),
			wantErr: false,
		},
		{
			name:    "empty value",
			key:     []byte("key"),
			value:   []byte{},
			wantErr: false,
		},
		{
			name:    "binary data",
			key:     []byte{0x00, 0x01, 0x02},
			value:   []byte{0xFF, 0xFE, 0xFD},
			wantErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir)
			if err != nil {
				t.Fatalf("failed to open db: %v", err)
			}
			defer db.Close()

			ctx := context.Background()

			// Put the key-value pair
			err = db.Put(ctx, test.key, test.value)
			if test.wantErr && err == nil {
				t.Fatal("expected error but got none")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error on Put: %v", err)
			}

			// Get the value back
			got, err := db.Get(ctx, test.key)
			if err != nil {
				t.Fatalf("unexpected error on Get: %v", err)
			}

			if !slices.Equal(got, test.value) {
				t.Errorf("expected value %v, got %v", test.value, got)
			}
		})
	}
}

func TestDBGetKeyNotFound(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Try to get a non-existent key
	_, err = db.Get(ctx, []byte("nonexistent"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestDBDelete(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Put a key-value pair
	key := []byte("name")
	value := []byte("alice")
	err = db.Put(ctx, key, value)
	if err != nil {
		t.Fatalf("failed to put: %v", err)
	}

	// Verify it exists
	got, err := db.Get(ctx, key)
	if err != nil {
		t.Fatalf("failed to get: %v", err)
	}
	if !slices.Equal(got, value) {
		t.Errorf("expected value %v, got %v", value, got)
	}

	// Delete the key
	err = db.Delete(ctx, key)
	if err != nil {
		t.Fatalf("failed to delete: %v", err)
	}

	// Verify it's gone
	_, err = db.Get(ctx, key)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

func TestDBWrite(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Create a batch with multiple operations
	batch := NewBatch()
	batch.Put([]byte("key1"), []byte("value1"))
	batch.Put([]byte("key2"), []byte("value2"))
	batch.Delete([]byte("key3"))

	// Write the batch
	err = db.Write(ctx, batch)
	if err != nil {
		t.Fatalf("failed to write batch: %v", err)
	}

	// Verify key1
	got1, err := db.Get(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("failed to get key1: %v", err)
	}
	if !slices.Equal(got1, []byte("value1")) {
		t.Errorf("expected value1, got %v", got1)
	}

	// Verify key2
	got2, err := db.Get(ctx, []byte("key2"))
	if err != nil {
		t.Fatalf("failed to get key2: %v", err)
	}
	if !slices.Equal(got2, []byte("value2")) {
		t.Errorf("expected value2, got %v", got2)
	}

	// Verify key3 doesn't exist (was deleted)
	_, err = db.Get(ctx, []byte("key3"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound for key3, got %v", err)
	}
}

func TestDBClose(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	// Close the database
	err = db.Close()
	if err != nil {
		t.Fatalf("failed to close db: %v", err)
	}

	// Second close should return error
	err = db.Close()
	if !errors.Is(err, ErrDBClosed) {
		t.Errorf("expected ErrDBClosed on second close, got %v", err)
	}
}

func TestDBCloseNilDB(t *testing.T) {
	var db *DB
	err := db.Close()
	if !errors.Is(err, ErrDBClosed) {
		t.Errorf("expected ErrDBClosed for nil DB, got %v", err)
	}
}

func TestDBContextCancellation(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// Test with already canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Put should fail with context error
	err = db.Put(ctx, []byte("key"), []byte("value"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Get should fail with context error
	_, err = db.Get(ctx, []byte("key"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Delete should fail with context error
	err = db.Delete(ctx, []byte("key"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	// Write should fail with context error
	batch := NewBatch()
	batch.Put([]byte("key"), []byte("value"))
	err = db.Write(ctx, batch)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestDBContextDeadline(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// Test with expired deadline
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	// Put should fail with deadline exceeded
	err = db.Put(ctx, []byte("key"), []byte("value"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestDBConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	const numGoroutines = 10
	const numOpsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Launch multiple goroutines that write concurrently
	for i := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			for j := range numOpsPerGoroutine {
				key := []byte("key_" + string(rune(id)) + "_" + string(rune(j)))
				value := []byte("value_" + string(rune(id)) + "_" + string(rune(j)))
				if err := db.Put(ctx, key, value); err != nil {
					t.Errorf("goroutine %d: failed to put: %v", id, err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify that all writes succeeded by checking a few keys
	for i := range numGoroutines {
		key := []byte("key_" + string(rune(i)) + "_" + string(rune(0)))
		_, err := db.Get(ctx, key)
		if err != nil {
			t.Errorf("failed to get key for goroutine %d: %v", i, err)
		}
	}
}

func TestDBConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Write some initial data
	for i := range 100 {
		key := []byte("key_" + string(rune(i)))
		value := []byte("value_" + string(rune(i)))
		if err := db.Put(ctx, key, value); err != nil {
			t.Fatalf("failed to put initial data: %v", err)
		}
	}

	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Launch multiple goroutines that read concurrently
	for range numGoroutines {
		go func() {
			defer wg.Done()
			for j := range 100 {
				key := []byte("key_" + string(rune(j)))
				_, err := db.Get(ctx, key)
				if err != nil {
					t.Errorf("failed to get key: %v", err)
				}
			}
		}()
	}

	wg.Wait()
}

func TestDBApplyBatch(t *testing.T) {
	tests := []struct {
		name     string
		batch    *Batch
		expected map[string][]byte
	}{
		{
			name:     "nil batch",
			batch:    nil,
			expected: map[string][]byte{},
		},
		{
			name:     "empty batch",
			batch:    NewBatch(),
			expected: map[string][]byte{},
		},
		{
			name: "batch with puts",
			batch: func() *Batch {
				b := NewBatch()
				b.Put([]byte("key1"), []byte("value1"))
				b.Put([]byte("key2"), []byte("value2"))
				return b
			}(),
			expected: map[string][]byte{
				"key1": []byte("value1"),
				"key2": []byte("value2"),
			},
		},
		{
			name: "batch with deletes",
			batch: func() *Batch {
				b := NewBatch()
				b.Delete([]byte("key1"))
				return b
			}(),
			expected: map[string][]byte{},
		},
		{
			name: "batch with put then delete same key",
			batch: func() *Batch {
				b := NewBatch()
				b.Put([]byte("key1"), []byte("value1"))
				b.Delete([]byte("key1"))
				return b
			}(),
			expected: map[string][]byte{},
		},
		{
			name: "batch with mixed operations",
			batch: func() *Batch {
				b := NewBatch()
				b.Put([]byte("key1"), []byte("value1"))
				b.Put([]byte("key2"), []byte("value2"))
				b.Delete([]byte("key1"))
				b.Put([]byte("key3"), []byte("value3"))
				return b
			}(),
			expected: map[string][]byte{
				"key2": []byte("value2"),
				"key3": []byte("value3"),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := &DB{mem: make(map[string][]byte)}
			db.applyBatch(test.batch)

			// Check that the in-memory map matches expected
			if len(db.mem) != len(test.expected) {
				t.Errorf("expected map size %d, got %d", len(test.expected), len(db.mem))
			}

			for key, expectedValue := range test.expected {
				gotValue, ok := db.mem[key]
				if !ok {
					t.Errorf("expected key %q to exist in map", key)
					continue
				}
				if !slices.Equal(gotValue, expectedValue) {
					t.Errorf("for key %q: expected value %v, got %v", key, expectedValue, gotValue)
				}
			}

			// Check that no unexpected keys exist
			for key := range db.mem {
				if _, ok := test.expected[key]; !ok {
					t.Errorf("unexpected key %q in map", key)
				}
			}
		})
	}
}

func TestDBWithSyncOption(t *testing.T) {
	tests := []struct {
		name       string
		opts       []Option
		expectSync bool
	}{
		{
			name:       "default should sync",
			opts:       nil,
			expectSync: true,
		},
		{
			name:       "explicit sync true",
			opts:       []Option{WithSync(true)},
			expectSync: true,
		},
		{
			name:       "explicit sync false",
			opts:       []Option{WithSync(false)},
			expectSync: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir, test.opts...)
			if err != nil {
				t.Fatalf("failed to open db: %v", err)
			}
			defer db.Close()

			if db.syncOnWrite != test.expectSync {
				t.Errorf("expected syncOnWrite=%v, got %v", test.expectSync, db.syncOnWrite)
			}
		})
	}
}

func TestDBOverwriteValue(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	key := []byte("name")

	// Put initial value
	err = db.Put(ctx, key, []byte("alice"))
	if err != nil {
		t.Fatalf("failed to put initial value: %v", err)
	}

	// Verify initial value
	got, err := db.Get(ctx, key)
	if err != nil {
		t.Fatalf("failed to get initial value: %v", err)
	}
	if !slices.Equal(got, []byte("alice")) {
		t.Errorf("expected alice, got %v", got)
	}

	// Overwrite with new value
	err = db.Put(ctx, key, []byte("bob"))
	if err != nil {
		t.Fatalf("failed to overwrite value: %v", err)
	}

	// Verify new value
	got, err = db.Get(ctx, key)
	if err != nil {
		t.Fatalf("failed to get new value: %v", err)
	}
	if !slices.Equal(got, []byte("bob")) {
		t.Errorf("expected bob, got %v", got)
	}
}

func TestDBPutDeleteSequence(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	key := []byte("key")

	// Put, delete, put again cycle
	err = db.Put(ctx, key, []byte("value1"))
	if err != nil {
		t.Fatalf("failed first put: %v", err)
	}

	err = db.Delete(ctx, key)
	if err != nil {
		t.Fatalf("failed delete: %v", err)
	}

	// After delete, key should not exist
	_, err = db.Get(ctx, key)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound after delete, got %v", err)
	}

	// Put again with different value
	err = db.Put(ctx, key, []byte("value2"))
	if err != nil {
		t.Fatalf("failed second put: %v", err)
	}

	// Should get the new value
	got, err := db.Get(ctx, key)
	if err != nil {
		t.Fatalf("failed to get after re-put: %v", err)
	}
	if !slices.Equal(got, []byte("value2")) {
		t.Errorf("expected value2, got %v", got)
	}
}

func TestDBWALReplay(t *testing.T) {
	dir := t.TempDir()

	// Open database and write some data
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	ctx := context.Background()

	// Write some data
	testData := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	for k, v := range testData {
		err := db.Put(ctx, []byte(k), []byte(v))
		if err != nil {
			t.Fatalf("failed to put %s: %v", k, err)
		}
	}

	// Delete one key
	err = db.Delete(ctx, []byte("key2"))
	if err != nil {
		t.Fatalf("failed to delete key2: %v", err)
	}

	// Close the database
	err = db.Close()
	if err != nil {
		t.Fatalf("failed to close db: %v", err)
	}

	// Reopen the database - should replay WAL
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	defer db2.Close()

	// Verify key1 exists and has correct value
	val1, err := db2.Get(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("failed to get key1 after replay: %v", err)
	}
	if !slices.Equal(val1, []byte("value1")) {
		t.Errorf("expected value1, got %v", val1)
	}

	// Verify key2 was deleted
	_, err = db2.Get(ctx, []byte("key2"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound for key2, got %v", err)
	}

	// Verify key3 exists and has correct value
	val3, err := db2.Get(ctx, []byte("key3"))
	if err != nil {
		t.Fatalf("failed to get key3 after replay: %v", err)
	}
	if !slices.Equal(val3, []byte("value3")) {
		t.Errorf("expected value3, got %v", val3)
	}
}

func TestDBOpenNoExistingWAL(t *testing.T) {
	// Test opening a database when no WAL exists yet
	dir := t.TempDir()

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db with no existing WAL: %v", err)
	}
	defer db.Close()

	// Should be able to write data
	ctx := context.Background()
	err = db.Put(ctx, []byte("key"), []byte("value"))
	if err != nil {
		t.Fatalf("failed to put after opening fresh db: %v", err)
	}

	// Should be able to read it back
	got, err := db.Get(ctx, []byte("key"))
	if err != nil {
		t.Fatalf("failed to get after put: %v", err)
	}
	if !slices.Equal(got, []byte("value")) {
		t.Errorf("expected value, got %v", got)
	}
}
