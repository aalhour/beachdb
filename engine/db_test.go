package engine

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aalhour/beachdb/internal/memtable"
	"github.com/aalhour/beachdb/internal/testutil"
)

func TestDB_Open(t *testing.T) {
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

func TestDB_PutGet(t *testing.T) {
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

func TestDB_GetKeyNotFound(t *testing.T) {
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

func TestDB_Delete(t *testing.T) {
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

func TestDB_Write(t *testing.T) {
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

func TestDB_Close(t *testing.T) {
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

func TestDB_CloseNilDB(t *testing.T) {
	var db *DB
	err := db.Close()
	if !errors.Is(err, ErrDBClosed) {
		t.Errorf("expected ErrDBClosed for nil DB, got %v", err)
	}
}

func TestDB_ContextCancellation(t *testing.T) {
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

func TestDB_ContextDeadline(t *testing.T) {
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

func TestDB_ConcurrentWrites(t *testing.T) {
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
				//nolint:gosec // G115: test code with small bounded values
				key := []byte("key_" + string(rune(id)) + "_" + string(rune(j)))
				//nolint:gosec // G115: test code with small bounded values
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
		//nolint:gosec // G115: test code with small bounded values
		key := []byte("key_" + string(rune(i)) + "_" + string(rune(0)))
		_, err := db.Get(ctx, key)
		if err != nil {
			t.Errorf("failed to get key for goroutine %d: %v", i, err)
		}
	}
}

func TestDB_ConcurrentReads(t *testing.T) {
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

func TestDB_ApplyBatch(t *testing.T) {
	// Test struct: expected keys that should exist, and keys that should NOT exist
	tests := []struct {
		name        string
		batch       *Batch
		expectExist map[string][]byte // keys that should exist with these values
		expectGone  []string          // keys that should NOT exist (deleted)
	}{
		{
			name:        "nil batch",
			batch:       nil,
			expectExist: map[string][]byte{},
			expectGone:  []string{},
		},
		{
			name:        "empty batch",
			batch:       NewBatch(),
			expectExist: map[string][]byte{},
			expectGone:  []string{},
		},
		{
			name: "batch with puts",
			batch: func() *Batch {
				b := NewBatch()
				b.Put([]byte("key1"), []byte("value1"))
				b.Put([]byte("key2"), []byte("value2"))
				return b
			}(),
			expectExist: map[string][]byte{
				"key1": []byte("value1"),
				"key2": []byte("value2"),
			},
			expectGone: []string{},
		},
		{
			name: "batch with deletes on non-existent key",
			batch: func() *Batch {
				b := NewBatch()
				b.Delete([]byte("key1"))
				return b
			}(),
			expectExist: map[string][]byte{},
			expectGone:  []string{"key1"},
		},
		{
			name: "batch with put then delete same key",
			batch: func() *Batch {
				b := NewBatch()
				b.Put([]byte("key1"), []byte("value1"))
				b.Delete([]byte("key1"))
				return b
			}(),
			expectExist: map[string][]byte{},
			expectGone:  []string{"key1"}, // tombstone hides the put
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
			expectExist: map[string][]byte{
				"key2": []byte("value2"),
				"key3": []byte("value3"),
			},
			expectGone: []string{"key1"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := &DB{mem: memtable.NewSkipList(), seqno: 0}
			db.applyBatch(test.batch)

			// Verify keys that should exist
			for key, expectedValue := range test.expectExist {
				gotValue, found := db.mem.Get([]byte(key), db.seqno)
				if !found {
					t.Errorf("expected key %q to exist", key)
					continue
				}
				if !slices.Equal(gotValue, expectedValue) {
					t.Errorf("key %q: expected value %v, got %v", key, expectedValue, gotValue)
				}
			}

			// Verify keys that should NOT exist (deleted or never added)
			for _, key := range test.expectGone {
				_, found := db.mem.Get([]byte(key), db.seqno)
				if found {
					t.Errorf("key %q should not exist (was deleted or never added)", key)
				}
			}
		})
	}
}

func TestDB_WithSyncOption(t *testing.T) {
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

func TestDB_OverwriteValue(t *testing.T) {
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

func TestDB_PutDeleteSequence(t *testing.T) {
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

func TestDB_SeqnoIncrement(t *testing.T) {
	// Verify that each write operation gets a unique, incrementing sequence number.
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Initial seqno should be 0
	if db.seqno != 0 {
		t.Errorf("initial seqno = %d, want 0", db.seqno)
	}

	// First Put: seqno becomes 1
	err = db.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put 1 failed: %v", err)
	}
	if db.seqno != 1 {
		t.Errorf("after Put 1: seqno = %d, want 1", db.seqno)
	}

	// Second Put: seqno becomes 2
	err = db.Put(ctx, []byte("key2"), []byte("value2"))
	if err != nil {
		t.Fatalf("Put 2 failed: %v", err)
	}
	if db.seqno != 2 {
		t.Errorf("after Put 2: seqno = %d, want 2", db.seqno)
	}

	// Delete: seqno becomes 3
	err = db.Delete(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if db.seqno != 3 {
		t.Errorf("after Delete: seqno = %d, want 3", db.seqno)
	}

	// Batch with 3 ops: seqno becomes 6
	batch := NewBatch()
	batch.Put([]byte("key3"), []byte("value3"))
	batch.Put([]byte("key4"), []byte("value4"))
	batch.Delete([]byte("key2"))
	err = db.Write(ctx, batch)
	if err != nil {
		t.Fatalf("Write batch failed: %v", err)
	}
	if db.seqno != 6 {
		t.Errorf("after batch (3 ops): seqno = %d, want 6", db.seqno)
	}
}

func TestDB_SeqnoPreservedAfterReplay(t *testing.T) {
	// Verify that seqno is correctly restored after WAL replay.
	dir := t.TempDir()
	ctx := context.Background()

	// Open and write some data
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	// Write 5 operations
	for i := range 5 {
		err = db.Put(ctx, []byte("key"), []byte("value"))
		if err != nil {
			t.Fatalf("Put %d failed: %v", i, err)
		}
	}
	seqnoBeforeClose := db.seqno
	if seqnoBeforeClose != 5 {
		t.Errorf("seqno before close = %d, want 5", seqnoBeforeClose)
	}

	// Close and reopen
	err = db.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	defer db2.Close()

	// After replay, seqno should be restored
	if db2.seqno != seqnoBeforeClose {
		t.Errorf("seqno after replay = %d, want %d", db2.seqno, seqnoBeforeClose)
	}

	// New writes should continue from where we left off
	err = db2.Put(ctx, []byte("key"), []byte("value"))
	if err != nil {
		t.Fatalf("Put after reopen failed: %v", err)
	}
	if db2.seqno != 6 {
		t.Errorf("seqno after new write = %d, want 6", db2.seqno)
	}
}

func TestDB_WALReplay(t *testing.T) {
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

func TestDB_OpenNoExistingWAL(t *testing.T) {
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

func TestDB_CrashLoop(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Reference model: tracks expected state
	model := testutil.NewModel()

	// Seed for reproducibility (using math/rand/v2)
	//nolint:gosec // G404: Test code doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(42, 1024))

	const numCycles = 100

	// Simulate 100 crash-and-recovery cycles
	for i := range numCycles {
		// Open DB
		db, err := Open(dir)
		if err != nil {
			t.Fatalf("cycle %d: failed to open: %v", i, err)
		}

		// Generate random operations
		batch := NewBatch()
		for range 10 {
			key := testutil.RandKey(rng, 32)

			if rng.Float64() < 0.7 {
				// 70% chance: Put
				value := testutil.RandValue(rng, 128)
				batch.Put(key, value)
				model.Put(key, value)
			} else {
				// 30% chance: Delete
				batch.Delete(key)
				model.Delete(key)
			}
		}

		// Write batch (durable write)
		if err := db.Write(ctx, batch); err != nil {
			t.Fatalf("cycle %d: write failed: %v", i, err)
		}

		// Close DB (simulates crash after successful write)
		if err := db.Close(); err != nil {
			t.Fatalf("cycle %d: close failed: %v", i, err)
		}
	}

	// Final recovery: open one last time
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("final open failed: %v", err)
	}
	defer db.Close()

	// Verify: for every key in model, DB should have same value (or both not found)
	modelKeys := model.Keys()
	t.Logf("Verifying %d unique keys after %d crash cycles", len(modelKeys), numCycles)

	for _, key := range modelKeys {
		expectedValue, modelHasKey := model.Get(key)
		actualValue, dbErr := db.Get(ctx, key)

		if !modelHasKey {
			// Key was deleted in model (tombstoned) — DB should also return not found
			if dbErr == nil {
				t.Errorf("key %q: deleted in model but found in DB with value %x", key, actualValue)
			} else if !errors.Is(dbErr, ErrKeyNotFound) {
				t.Errorf("key %q: deleted in model, DB returned unexpected error: %v", key, dbErr)
			}
			continue
		}

		// Key exists in model — DB should have same value
		if dbErr != nil {
			t.Errorf("key %q: expected in DB but got error: %v", key, dbErr)
			continue
		}

		if !slices.Equal(expectedValue, actualValue) {
			t.Errorf("key %q: value mismatch\n  expected: %x\n  got:      %x",
				key, expectedValue, actualValue)
		}
	}

	// Also verify DB doesn't have extra keys
	// (This requires iterating DB, which v1 doesn't support — skip for now)
}

func TestDB_OverwriteReplay(t *testing.T) {
	// Explicitly test that overwriting a key survives crash/replay
	dir := t.TempDir()
	ctx := context.Background()

	// Phase 1: Write initial value
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	key := []byte("overwrite-test-key")
	value1 := []byte("first-value")
	value2 := []byte("second-value")
	value3 := []byte("third-value-final")

	if err := db.Put(ctx, key, value1); err != nil {
		t.Fatalf("Put value1 failed: %v", err)
	}

	// Phase 2: Overwrite with second value, close and reopen
	if err := db.Put(ctx, key, value2); err != nil {
		t.Fatalf("Put value2 failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close after value2 failed: %v", err)
	}

	db, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen after value2 failed: %v", err)
	}

	// Verify value2 is there
	got, err := db.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after reopen 1 failed: %v", err)
	}
	if !slices.Equal(got, value2) {
		t.Errorf("after reopen 1: expected %q, got %q", value2, got)
	}

	// Phase 3: Overwrite again, close and reopen
	if err := db.Put(ctx, key, value3); err != nil {
		t.Fatalf("Put value3 failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close after value3 failed: %v", err)
	}

	db, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen after value3 failed: %v", err)
	}
	defer db.Close()

	// Verify final value is there
	got, err = db.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after reopen 2 failed: %v", err)
	}
	if !slices.Equal(got, value3) {
		t.Errorf("after reopen 2: expected %q, got %q", value3, got)
	}
}

func TestDB_CrashRecovery_WithMemtable(t *testing.T) {
	// Comprehensive crash recovery test covering all memtable semantics:
	// - Write data, close, reopen → data is there
	// - Sequence numbers are restored from WAL
	// - Overwrites replay correctly
	// - Deletes replay correctly
	dir := t.TempDir()
	ctx := context.Background()

	// Phase 1: Initial writes with overwrites and deletes
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	// Write key1 with multiple overwrites
	if err := db.Put(ctx, []byte("key1"), []byte("v1-initial")); err != nil {
		t.Fatalf("Put key1 v1 failed: %v", err)
	}
	if err := db.Put(ctx, []byte("key1"), []byte("v1-overwrite")); err != nil {
		t.Fatalf("Put key1 v2 failed: %v", err)
	}

	// Write key2, then delete it
	if err := db.Put(ctx, []byte("key2"), []byte("v2-will-delete")); err != nil {
		t.Fatalf("Put key2 failed: %v", err)
	}
	if err := db.Delete(ctx, []byte("key2")); err != nil {
		t.Fatalf("Delete key2 failed: %v", err)
	}

	// Write key3 normally
	if err := db.Put(ctx, []byte("key3"), []byte("v3-normal")); err != nil {
		t.Fatalf("Put key3 failed: %v", err)
	}

	// Write key4, delete, then resurrect
	if err := db.Put(ctx, []byte("key4"), []byte("v4-first")); err != nil {
		t.Fatalf("Put key4 v1 failed: %v", err)
	}
	if err := db.Delete(ctx, []byte("key4")); err != nil {
		t.Fatalf("Delete key4 failed: %v", err)
	}
	if err := db.Put(ctx, []byte("key4"), []byte("v4-resurrected")); err != nil {
		t.Fatalf("Put key4 v2 failed: %v", err)
	}

	seqnoBeforeClose := db.seqno
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Phase 2: Reopen and verify all state is restored
	db, err = Open(dir)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer db.Close()

	// Verify sequence number restored
	if db.seqno != seqnoBeforeClose {
		t.Errorf("seqno after replay = %d, want %d", db.seqno, seqnoBeforeClose)
	}

	// Verify key1: should have overwritten value
	got, err := db.Get(ctx, []byte("key1"))
	if err != nil {
		t.Errorf("Get key1 failed: %v", err)
	} else if !slices.Equal(got, []byte("v1-overwrite")) {
		t.Errorf("key1: expected %q, got %q", "v1-overwrite", got)
	}

	// Verify key2: should be deleted
	_, err = db.Get(ctx, []byte("key2"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("key2 should be deleted, got err=%v", err)
	}

	// Verify key3: should exist normally
	got, err = db.Get(ctx, []byte("key3"))
	if err != nil {
		t.Errorf("Get key3 failed: %v", err)
	} else if !slices.Equal(got, []byte("v3-normal")) {
		t.Errorf("key3: expected %q, got %q", "v3-normal", got)
	}

	// Verify key4: should be resurrected
	got, err = db.Get(ctx, []byte("key4"))
	if err != nil {
		t.Errorf("Get key4 failed: %v", err)
	} else if !slices.Equal(got, []byte("v4-resurrected")) {
		t.Errorf("key4: expected %q, got %q", "v4-resurrected", got)
	}

	// Verify new writes get correct seqno
	if err := db.Put(ctx, []byte("key5"), []byte("v5-new")); err != nil {
		t.Fatalf("Put key5 failed: %v", err)
	}
	if db.seqno != seqnoBeforeClose+1 {
		t.Errorf("seqno after new write = %d, want %d", db.seqno, seqnoBeforeClose+1)
	}
}
