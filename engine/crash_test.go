package engine

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aalhour/beachdb/internal/testutil"
)

// TestDB_CrashRecovery_TruncatedWAL tests recovery from a truncated WAL file.
// This simulates a crash mid-write where the WAL record is incomplete.
func TestDB_CrashRecovery_TruncatedWAL(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Write some data
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	testData := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	for k, v := range testData {
		if err := db.Put(ctx, []byte(k), []byte(v)); err != nil {
			t.Fatalf("failed to put %s: %v", k, err)
		}
	}

	db.Close()

	// Truncate the WAL file (simulate crash mid-write)
	walPath := filepath.Join(dir, walFileName)
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("failed to stat WAL: %v", err)
	}

	// Remove last 10 bytes (incomplete record)
	if err := os.Truncate(walPath, info.Size()-10); err != nil {
		t.Fatalf("failed to truncate WAL: %v", err)
	}

	// Reopen - should handle truncation gracefully
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to reopen after truncation: %v", err)
	}
	defer db2.Close()

	// Verify data (may have lost the last write)
	// We should recover at least the first 2 keys
	recoveredCount := 0
	for k, v := range testData {
		got, err := db2.Get(ctx, []byte(k))
		if errors.Is(err, ErrKeyNotFound) {
			t.Logf("key %s not recovered (acceptable after truncation)", k)
			continue
		}
		if err != nil {
			t.Errorf("unexpected error getting %s: %v", k, err)
			continue
		}
		if string(got) != v {
			t.Errorf("key %s: expected %q, got %q", k, v, got)
		}
		recoveredCount++
	}

	if recoveredCount == 0 {
		t.Error("no data recovered after truncation")
	}

	t.Logf("recovered %d/%d keys after WAL truncation", recoveredCount, len(testData))
}

// TestDB_CrashRecovery_CorruptedWAL tests recovery from a corrupted WAL file.
// This simulates data corruption (bit flips, etc.)
func TestDB_CrashRecovery_CorruptedWAL(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Write some data
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	testData := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}

	for k, v := range testData {
		if err := db.Put(ctx, []byte(k), []byte(v)); err != nil {
			t.Fatalf("failed to put %s: %v", k, err)
		}
	}

	db.Close()

	// Corrupt the WAL file (flip some bits in the middle)
	walPath := filepath.Join(dir, walFileName)
	//nolint:gosec // G304: Test code with controlled path
	data, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("failed to read WAL: %v", err)
	}

	if len(data) > 50 {
		// Flip bits in the middle of the file
		data[len(data)/2] ^= 0xFF
		data[len(data)/2+1] ^= 0xFF

		if err := os.WriteFile(walPath, data, 0644); err != nil { //nolint:gosec
			t.Fatalf("failed to write corrupted WAL: %v", err)
		}
	}

	// Reopen - should handle corruption gracefully
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to reopen after corruption: %v", err)
	}
	defer db2.Close()

	// Verify data (may have lost data after corruption point)
	recoveredCount := 0
	for k, v := range testData {
		got, err := db2.Get(ctx, []byte(k))
		if errors.Is(err, ErrKeyNotFound) {
			t.Logf("key %s not recovered (acceptable after corruption)", k)
			continue
		}
		if err != nil {
			t.Errorf("unexpected error getting %s: %v", k, err)
			continue
		}
		if string(got) != v {
			t.Errorf("key %s: expected %q, got %q", k, v, got)
		}
		recoveredCount++
	}

	t.Logf("recovered %d/%d keys after WAL corruption", recoveredCount, len(testData))
}

// TestDB_CrashRecovery_RandomizedStress performs randomized crash-and-recover cycles.
// This is closer to a true crash simulation - we repeatedly:
// 1. Write random data
// 2. "Crash" by truncating/corrupting the WAL randomly
// 3. Verify we can still recover
func TestDB_CrashRecovery_RandomizedStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	dir := t.TempDir()
	ctx := context.Background()
	//nolint:gosec // G404: Test code doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(42, 1024))

	// Track what we successfully committed
	model := testutil.NewModel()

	const numCycles = 50

	for cycle := range numCycles {
		// Open DB
		db, err := Open(dir)
		if err != nil {
			t.Fatalf("cycle %d: failed to open: %v", cycle, err)
		}

		// Write some random data
		numWrites := 5 + rng.IntN(10) // 5-15 writes
		//nolint:intrange // Need loop variable for error message
		for i := range numWrites {
			key := testutil.RandKey(rng, 32)
			value := testutil.RandValue(rng, 128)

			if err := db.Put(ctx, key, value); err != nil {
				t.Fatalf("cycle %d: write %d failed: %v", cycle, i, err)
			}

			// Track in model
			model.Put(key, value)
		}

		// Close cleanly
		if err := db.Close(); err != nil {
			t.Fatalf("cycle %d: close failed: %v", cycle, err)
		}

		// Randomly corrupt or truncate the WAL (50% chance)
		//nolint:nestif // Test code with clear logic despite nesting
		if rng.Float64() < 0.5 {
			walPath := filepath.Join(dir, walFileName)
			info, err := os.Stat(walPath)
			if err != nil {
				continue // WAL might not exist yet
			}

			if rng.Float64() < 0.5 {
				// Truncate
				truncateBy := rng.Int64N(info.Size()/4 + 1)
				newSize := max(0, info.Size()-truncateBy)
				os.Truncate(walPath, newSize)
				t.Logf("cycle %d: truncated WAL by %d bytes", cycle, truncateBy)
			} else {
				// Corrupt
				//nolint:gosec // G304: Test code with controlled path
				data, _ := os.ReadFile(walPath)
				if len(data) > 10 {
					corruptOffset := rng.IntN(len(data) - 10)
					data[corruptOffset] ^= 0xFF
					os.WriteFile(walPath, data, 0644) //nolint:gosec
					t.Logf("cycle %d: corrupted WAL at offset %d", cycle, corruptOffset)
				}
			}
		}
	}

	// Final recovery - open one last time and verify
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("final open failed: %v", err)
	}
	defer db.Close()

	// Verify that we can at least open and read
	// (We may have lost some data due to corruption, that's expected)
	recoveredKeys := 0
	for _, key := range model.Keys() {
		_, err := db.Get(ctx, key)
		if err == nil {
			recoveredKeys++
		}
	}

	t.Logf("recovered %d keys after %d crash cycles", recoveredKeys, numCycles)

	// We should recover at least some data (unless we got very unlucky)
	if model.Len() > 10 && recoveredKeys == 0 {
		t.Error("recovered 0 keys after writing many - something is wrong")
	}
}

// TestDB_CrashRecovery_EmptyWALAfterTruncation tests that we handle
// a completely empty WAL file (e.g., truncated to 0 bytes)
func TestDB_CrashRecovery_EmptyWALAfterTruncation(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Write some data
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	if err := db.Put(ctx, []byte("key"), []byte("value")); err != nil {
		t.Fatalf("failed to put: %v", err)
	}

	db.Close()

	// Truncate WAL to 0 bytes (catastrophic crash)
	walPath := filepath.Join(dir, walFileName)
	if err := os.Truncate(walPath, 0); err != nil {
		t.Fatalf("failed to truncate WAL: %v", err)
	}

	// Reopen - should handle empty WAL
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to reopen after empty WAL: %v", err)
	}
	defer db2.Close()

	// Data is lost (expected), but DB should be operational
	_, err = db2.Get(ctx, []byte("key"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound after WAL truncation, got %v", err)
	}

	// Should be able to write new data
	if err := db2.Put(ctx, []byte("newkey"), []byte("newvalue")); err != nil {
		t.Errorf("failed to write after recovery: %v", err)
	}

	got, err := db2.Get(ctx, []byte("newkey"))
	if err != nil {
		t.Errorf("failed to read after recovery: %v", err)
	}
	if !slices.Equal(got, []byte("newvalue")) {
		t.Errorf("wrong value after recovery: got %q", got)
	}
}
