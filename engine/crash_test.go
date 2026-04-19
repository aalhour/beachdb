package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aalhour/beachdb/internal/testutil"
	"github.com/aalhour/beachdb/internal/wal"
)

// TestDB_CrashRecovery_TruncatedWAL tests recovery from a truncated WAL file.
// This simulates a crash mid-write where the WAL record is incomplete.
func TestDB_CrashRecovery_TruncatedWAL(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Write ordered data so recovery expectations are deterministic.
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	type kv struct {
		key string
		val string
	}
	testData := []kv{
		{key: "key-001", val: "value-001"},
		{key: "key-002", val: "value-002"},
		{key: "key-003", val: "value-003"},
		{key: "key-004", val: "value-004"},
		{key: "key-005", val: "value-005"},
	}

	for _, item := range testData {
		if err := db.Put(ctx, []byte(item.key), []byte(item.val)); err != nil {
			t.Fatalf("failed to put %s: %v", item.key, err)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}

	// Truncate the WAL file by one byte to ensure the final record is torn.
	walPath := filepath.Join(dir, walFileName)
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("failed to stat WAL: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("expected non-empty WAL")
	}

	if err := os.Truncate(walPath, info.Size()-1); err != nil {
		t.Fatalf("failed to truncate WAL: %v", err)
	}

	// Pre-compute the expected repair offset from the truncated file.
	reader, err := wal.NewReader(walPath)
	if err != nil {
		t.Fatalf("failed to open WAL reader: %v", err)
	}
	defer reader.Close()

	for {
		_, err = reader.Next()
		if errors.Is(err, wal.ErrTruncated) {
			break
		}
		if errors.Is(err, io.EOF) {
			t.Fatal("expected truncated WAL to end with ErrTruncated")
		}
		if err != nil {
			t.Fatalf("unexpected WAL read error: %v", err)
		}
	}
	expectedValidOffset := reader.ValidOffset()
	if expectedValidOffset <= 0 {
		t.Fatalf("expected positive valid offset, got %d", expectedValidOffset)
	}

	// Reopen - should handle truncation gracefully
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("failed to reopen after truncation: %v", err)
	}
	defer db2.Close()

	// Verify that replay physically repaired WAL to the valid offset.
	repairedInfo, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("failed to stat repaired WAL: %v", err)
	}
	if repairedInfo.Size() != expectedValidOffset {
		t.Fatalf("repaired WAL size = %d, want %d", repairedInfo.Size(), expectedValidOffset)
	}

	// Verify deterministic recovery: all but the torn final write are present.
	for i := range len(testData) - 1 {
		got, err := db2.Get(ctx, []byte(testData[i].key))
		if err != nil {
			t.Fatalf("expected %s to be recovered: %v", testData[i].key, err)
		}
		if string(got) != testData[i].val {
			t.Fatalf("value for %s = %q, want %q", testData[i].key, got, testData[i].val)
		}
	}

	last := testData[len(testData)-1]
	_, err = db2.Get(ctx, []byte(last.key))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected torn final write %s to be absent, got %v", last.key, err)
	}
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

	for i := range 5 {
		key := fmt.Appendf(nil, "key-%03d", i)
		val := fmt.Appendf(nil, "value-%03d", i)
		if err := db.Put(ctx, key, val); err != nil {
			t.Fatalf("failed to put %s: %v", key, err)
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

	// Reopen - corruption should fail fast
	db2, err := Open(dir)
	if err == nil {
		db2.Close()
		t.Fatal("expected reopen after corruption to fail")
	}
	if !errors.Is(err, wal.ErrChecksum) && !errors.Is(err, wal.ErrBadMagic) && !errors.Is(err, ErrCorruptBatch) {
		t.Fatalf("expected WAL corruption error, got %v", err)
	}
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

		// Randomly truncate the WAL (simulate torn last record)
		//nolint:nestif // Test code with clear logic despite nesting
		if rng.Float64() < 0.5 {
			walPath := filepath.Join(dir, walFileName)
			info, err := os.Stat(walPath)
			if err != nil {
				continue // WAL might not exist yet
			}

			truncateBy := rng.Int64N(info.Size()/4 + 1)
			newSize := max(0, info.Size()-truncateBy)
			os.Truncate(walPath, newSize)
			t.Logf("cycle %d: truncated WAL by %d bytes", cycle, truncateBy)
		}
	}

	// Final recovery - truncated tails are ignored, so open should still succeed.
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
