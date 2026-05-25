package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aalhour/beachdb/internal/memtable"
	"github.com/aalhour/beachdb/internal/sstable"
)

// --- DB state for SSTables ---

// Spec: "fresh DB, verify len(db.ssts) == 0"
func TestDB_OpenWithNoSSTables(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if len(db.ssts) != 0 {
		t.Fatalf("expected 0 SSTable readers on fresh DB, got %d", len(db.ssts))
	}
	if db.nextSSTID != 0 {
		t.Fatalf("expected nextSSTID=0 on fresh DB, got %d", db.nextSSTID)
	}
}

// Spec: "create SST files manually, open DB, verify they are loaded"
func TestDB_DiscoverSSTables(t *testing.T) {
	dir := t.TempDir()

	// Create a DB, write entries, flush to produce SSTable files
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	for i := range 10 {
		if err := db.Put(ctx, fmt.Appendf(nil, "key-%04d", i), fmt.Appendf(nil, "val-%04d", i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	db.Close()

	// Verify the SST file exists on disk
	matches, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 SST file, got %d", len(matches))
	}

	// Re-open the DB — it should discover the SSTable
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer db2.Close()

	if len(db2.ssts) != 1 {
		t.Fatalf("expected 1 SSTable reader after re-open, got %d", len(db2.ssts))
	}
	if db2.nextSSTID != 1 {
		t.Fatalf("expected nextSSTID=1, got %d", db2.nextSSTID)
	}
}

func TestDB_DiscoverMultipleSSTables(t *testing.T) {
	dir := t.TempDir()

	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Flush 3 times to produce 3 SSTable files
	for batch := range 3 {
		for i := range 5 {
			key := fmt.Appendf(nil, "batch%d-key%d", batch, i)
			val := fmt.Appendf(nil, "batch%d-val%d", batch, i)
			if err := db.Put(ctx, key, val); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
		if err := db.flushMemtable(); err != nil {
			t.Fatalf("flush %d: %v", batch, err)
		}
	}
	db.Close()

	// Verify 3 SST files on disk
	matches, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("expected 3 SST files, got %d", len(matches))
	}

	// Re-open and verify
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer db2.Close()

	if len(db2.ssts) != 3 {
		t.Fatalf("expected 3 SSTable readers, got %d", len(db2.ssts))
	}
	if db2.nextSSTID != 3 {
		t.Fatalf("expected nextSSTID=3, got %d", db2.nextSSTID)
	}
}

// Non-SST files in the directory should be ignored
func TestDB_DiscoverIgnoresNonSSTFiles(t *testing.T) {
	dir := t.TempDir()

	// Create some non-SST files
	for _, name := range []string{"notes.txt", "backup.bak", "data.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	files, nextID, err := discoverSSTables(dir)
	if err != nil {
		t.Fatalf("discoverSSTables: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 SST files, got %d: %v", len(files), files)
	}
	if nextID != 0 {
		t.Fatalf("expected nextID=0, got %d", nextID)
	}
}

// buildSSTFileName produces the expected zero-padded format
func TestBuildSSTFileName(t *testing.T) {
	tests := []struct {
		id   uint64
		want string
	}{
		{0, "00000000000000000000.sst"},
		{1, "00000000000000000001.sst"},
		{42, "00000000000000000042.sst"},
		{999999, "00000000000000999999.sst"},
	}
	for _, tt := range tests {
		got := buildSSTFileName(tt.id)
		if got != tt.want {
			t.Errorf("buildSSTFileName(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}
}

// Close should close all SSTable readers
func TestDB_CloseClosesSSTReaders(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := db.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if len(db.ssts) != 1 {
		t.Fatalf("expected 1 reader, got %d", len(db.ssts))
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if db.ssts != nil {
		t.Fatal("expected db.ssts to be nil after Close")
	}
}

// TestDB_Close_FlushDoneCh_NoRace is a regression guard against an unprotected
// read of db.flushDoneCh in Close. After Close releases db.mu (so flushLoop can
// finish its current flush), Close reads db.flushDoneCh outside any lock,
// while flushLoop's deferred cleanup writes the same field under db.mu. The
// race detector flags the mismatched synchronization on every run.
//
// Loop to give the detector multiple Open/Close cycles. A single iteration
// is usually enough — go's race detector tracks happens-before, not actual
// memory collisions — but a small loop hardens against future refactors that
// could narrow the window.
//
// Run with: go test -race ./engine/ -run=TestDB_Close_FlushDoneCh_NoRace
func TestDB_Close_FlushDoneCh_NoRace(t *testing.T) {
	for range 20 {
		dir := t.TempDir()
		db, err := Open(dir, WithSync(false), WithMemtableFlushSize(512))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// --- Flush path ---

// Spec: "put entries, flush, verify SST file exists"
func TestFlush_ProducesValidSSTable(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	entries := map[string]string{
		"alpha": "v-alpha",
		"beta":  "v-beta",
		"gamma": "v-gamma",
	}
	for k, v := range entries {
		if err := db.Put(ctx, []byte(k), []byte(v)); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}

	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flushMemtable: %v", err)
	}

	// SST file should exist on disk
	sstPath := filepath.Join(dir, buildSSTFileName(0))
	if _, err := os.Stat(sstPath); err != nil {
		t.Fatalf("SST file not found: %v", err)
	}

	// A reader should be published in db.ssts
	if len(db.ssts) != 1 {
		t.Fatalf("expected 1 SSTable reader, got %d", len(db.ssts))
	}

	// Verify the SSTable is valid by opening it independently
	f, err := os.Open(sstPath) //nolint:gosec // test with known temp path
	if err != nil {
		t.Fatalf("os.Open: %v", err)
	}
	defer f.Close()
	r, err := sstable.OpenReader(f)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	// Iterate and verify all entries are present
	it := r.NewIterator()
	count := 0
	it.SeekToFirst()
	for it.Valid() {
		count++
		it.Next()
	}
	if count != len(entries) {
		t.Fatalf("SSTable has %d entries, want %d", count, len(entries))
	}
}

// Spec: "flush an empty memtable, verify behavior is reasonable"
func TestFlush_EmptyMemtable(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Flush with no data — should succeed as a no-op
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flushMemtable on empty: %v", err)
	}

	// No SST should be produced for an empty memtable
	if len(db.ssts) != 0 {
		t.Fatalf("expected 0 readers for empty flush, got %d", len(db.ssts))
	}
}

// Memtable is reset to empty after flush
func TestFlush_MemtableResetAfterFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Put(ctx, []byte("key"), []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// The memtable should be empty (new skiplist)
	_, found := db.mem.Get([]byte("key"), db.seqno)
	if found {
		t.Fatal("expected memtable to be empty after flush, but key was found")
	}
}

// nextSSTID increments after each flush
func TestFlush_IncrementsSSTID(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	for i := range 3 {
		if err := db.Put(ctx, fmt.Appendf(nil, "k%d", i), []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := db.flushMemtable(); err != nil {
			t.Fatalf("flush %d: %v", i, err)
		}
	}

	if db.nextSSTID != 3 {
		t.Fatalf("expected nextSSTID=3 after 3 flushes, got %d", db.nextSSTID)
	}
	if len(db.ssts) != 3 {
		t.Fatalf("expected 3 readers, got %d", len(db.ssts))
	}

	// Verify filenames on disk
	for i := range 3 {
		sstPath := filepath.Join(dir, buildSSTFileName(uint64(i)))
		if _, err := os.Stat(sstPath); err != nil {
			t.Fatalf("expected %s to exist: %v", sstPath, err)
		}
	}
}

// WAL should not be deleted or modified by flush
func TestFlush_WALIsPreserved(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Put(ctx, []byte("key"), []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	walPath := filepath.Join(dir, walFileName)
	walInfoBefore, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("WAL stat before flush: %v", err)
	}

	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	walInfoAfter, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("WAL stat after flush: %v", err)
	}

	// WAL file should still exist with same size (not truncated/deleted)
	if walInfoAfter.Size() != walInfoBefore.Size() {
		t.Fatalf("WAL size changed: before=%d, after=%d", walInfoBefore.Size(), walInfoAfter.Size())
	}
}

// Writes can continue after a flush
func TestFlush_WritesAfterFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Write, flush, write more
	if err := db.Put(ctx, []byte("before"), []byte("v1")); err != nil {
		t.Fatalf("Put before flush: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := db.Put(ctx, []byte("after"), []byte("v2")); err != nil {
		t.Fatalf("Put after flush: %v", err)
	}

	// "after" should be in the new memtable
	val, found := db.mem.Get([]byte("after"), db.seqno)
	if !found {
		t.Fatal("expected 'after' in memtable")
	}
	if !slices.Equal(val, []byte("v2")) {
		t.Fatalf("expected 'v2', got %q", val)
	}
}

func TestFlush_SynchronousFailurePreservesReadableData(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Put(ctx, []byte("key"), []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	flushFailure := errors.New("forced flush failure")
	prevWriteSSTableFn := writeSSTableFn
	writeSSTableFn = func(string, memtable.Memtable, int) (*sstable.Reader, error) {
		return nil, flushFailure
	}
	t.Cleanup(func() {
		writeSSTableFn = prevWriteSSTableFn
	})

	err = db.flushMemtable()
	if !errors.Is(err, flushFailure) {
		t.Fatalf("expected flush failure, got %v", err)
	}

	got, err := db.Get(ctx, []byte("key"))
	if err != nil {
		t.Fatalf("expected key to remain readable after failed flush, got %v", err)
	}
	if !slices.Equal(got, []byte("value")) {
		t.Fatalf("Get(key) = %q, want %q", got, "value")
	}

	if len(db.ssts) != 0 {
		t.Fatalf("expected no SSTs to be published on failed flush, got %d", len(db.ssts))
	}
	if db.nextSSTID != 0 {
		t.Fatalf("expected nextSSTID to stay at 0 after failed flush, got %d", db.nextSSTID)
	}
	if db.immMem == nil {
		t.Fatal("expected failed synchronous flush to keep the frozen memtable readable")
	}

	if err := db.Put(ctx, []byte("later"), []byte("value")); !errors.Is(err, flushFailure) {
		t.Fatalf("expected subsequent writes to fail with flush error, got %v", err)
	}
}

// Multiple flushes produce distinct, valid SSTable files
func TestFlush_MultipleFlushes(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	totalEntries := 0

	for batch := range 5 {
		n := (batch + 1) * 3
		for i := range n {
			key := fmt.Appendf(nil, "b%d-k%04d", batch, i)
			if err := db.Put(ctx, key, []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
		totalEntries += n

		if err := db.flushMemtable(); err != nil {
			t.Fatalf("flush %d: %v", batch, err)
		}
	}

	if len(db.ssts) != 5 {
		t.Fatalf("expected 5 readers, got %d", len(db.ssts))
	}

	// Each SST should be independently openable
	for i := range 5 {
		path := filepath.Join(dir, buildSSTFileName(uint64(i)))
		f, err := os.Open(path) //nolint:gosec // test with known temp path
		if err != nil {
			t.Fatalf("Open SST %d: %v", i, err)
		}
		r, err := sstable.OpenReader(f)
		if err != nil {
			f.Close()
			t.Fatalf("OpenReader SST %d: %v", i, err)
		}
		r.Close()
	}
}

// Flush should not panic on a closed DB
func TestFlush_OnClosedDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()

	// Should not panic — the function accesses db.mem, db.nextSSTID, etc.
	// under the lock; with db closed, it should fail gracefully or produce
	// an empty SSTable. We just verify no panic.
	_ = db.flushMemtable()
}

// --- discoverSSTables unit tests ---

func TestDiscoverSSTables_SortOrder(t *testing.T) {
	dir := t.TempDir()

	// Create SST files out of order
	names := []string{"000003.sst", "000001.sst", "000002.sst"}
	for _, name := range names {
		path := filepath.Join(dir, name)
		// Write a valid (empty) SSTable
		f, err := os.Create(path) //nolint:gosec // test with known temp path
		if err != nil {
			t.Fatal(err)
		}
		w, err := sstable.NewWriter(f, sstable.WithSync(false))
		if err != nil {
			t.Fatal(err)
		}
		w.Close()
	}

	files, nextID, err := discoverSSTables(dir)
	if err != nil {
		t.Fatalf("discoverSSTables: %v", err)
	}

	// Should be sorted ASC
	expected := []string{"000001.sst", "000002.sst", "000003.sst"}
	if !slices.Equal(files, expected) {
		t.Fatalf("expected %v, got %v", expected, files)
	}
	if nextID != 4 {
		t.Fatalf("expected nextID=4, got %d", nextID)
	}
}

func TestDiscoverSSTables_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	files, nextID, err := discoverSSTables(dir)
	if err != nil {
		t.Fatalf("discoverSSTables: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
	if nextID != 0 {
		t.Fatalf("expected nextID=0, got %d", nextID)
	}
}

func TestDiscoverSSTables_RejectsMalformedName(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "not-a-number.sst"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := discoverSSTables(dir)
	if err == nil {
		t.Fatal("expected malformed SSTable name to fail discovery")
	}
}

func TestDiscoverSSTables_SortsByNumericID(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"10.sst", "2.sst", "000001.sst"} {
		path := filepath.Join(dir, name)
		f, err := os.Create(path) //nolint:gosec // test with known temp path
		if err != nil {
			t.Fatal(err)
		}
		w, err := sstable.NewWriter(f, sstable.WithSync(false))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	files, nextID, err := discoverSSTables(dir)
	if err != nil {
		t.Fatalf("discoverSSTables: %v", err)
	}

	expected := []string{"000001.sst", "2.sst", "10.sst"}
	if !slices.Equal(files, expected) {
		t.Fatalf("expected %v, got %v", expected, files)
	}
	if nextID != 11 {
		t.Fatalf("expected nextID=11, got %d", nextID)
	}
}

func TestDiscoverSSTables_RejectsDuplicateNumericID(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"1.sst", "000001.sst"} {
		path := filepath.Join(dir, name)
		f, err := os.Create(path) //nolint:gosec // test with known temp path
		if err != nil {
			t.Fatal(err)
		}
		w, err := sstable.NewWriter(f, sstable.WithSync(false))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := discoverSSTables(dir)
	if err == nil {
		t.Fatal("expected duplicate numeric SSTable IDs to fail discovery")
	}
}

// --- Get reads from SSTables + flush integration ---

// Basic: Get falls through memtable miss to SSTable
func TestDB_WriteFlushRead(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	for i := range 10 {
		key := fmt.Appendf(nil, "key-%04d", i)
		val := fmt.Appendf(nil, "val-%04d", i)
		if err := db.Put(ctx, key, val); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// All keys are now in SSTable only (memtable was reset)
	for i := range 10 {
		key := fmt.Appendf(nil, "key-%04d", i)
		want := fmt.Appendf(nil, "val-%04d", i)
		got, err := db.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Get(%s) = %q, want %q", key, got, want)
		}
	}
}

// Both pre-flush (SSTable) and post-flush (memtable) keys are readable
func TestDB_WriteFlushWriteRead(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Batch 1: write + flush → lands in SSTable
	if err := db.Put(ctx, []byte("flushed"), []byte("from-sst")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Batch 2: write after flush → lives in memtable
	if err := db.Put(ctx, []byte("inmem"), []byte("from-mem")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Both should be readable
	got, err := db.Get(ctx, []byte("flushed"))
	if err != nil {
		t.Fatalf("Get(flushed): %v", err)
	}
	if !slices.Equal(got, []byte("from-sst")) {
		t.Fatalf("Get(flushed) = %q, want %q", got, "from-sst")
	}

	got, err = db.Get(ctx, []byte("inmem"))
	if err != nil {
		t.Fatalf("Get(inmem): %v", err)
	}
	if !slices.Equal(got, []byte("from-mem")) {
		t.Fatalf("Get(inmem) = %q, want %q", got, "from-mem")
	}
}

// Memtable value wins over SSTable value for the same key
func TestDB_MemtableShadowsSSTable(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	if err := db.Put(ctx, []byte("foo"), []byte("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Overwrite in memtable
	if err := db.Put(ctx, []byte("foo"), []byte("new")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := db.Get(ctx, []byte("foo"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !slices.Equal(got, []byte("new")) {
		t.Fatalf("Get(foo) = %q, want %q (memtable should shadow SSTable)", got, "new")
	}
}

// Tombstone in memtable hides value in SSTable
func TestDB_DeleteAfterFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	if err := db.Put(ctx, []byte("doomed"), []byte("alive")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Delete in memtable should shadow the SSTable value
	if err := db.Delete(ctx, []byte("doomed")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = db.Get(ctx, []byte("doomed"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after delete, got %v", err)
	}
}

// Newer SSTable value wins over older SSTable value
func TestDB_NewerSSTShadowsOlder(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// First flush: foo=v1
	if err := db.Put(ctx, []byte("foo"), []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush 1: %v", err)
	}

	// Second flush: foo=v2
	if err := db.Put(ctx, []byte("foo"), []byte("v2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush 2: %v", err)
	}

	got, err := db.Get(ctx, []byte("foo"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !slices.Equal(got, []byte("v2")) {
		t.Fatalf("Get(foo) = %q, want %q (newer SST should win)", got, "v2")
	}
}

// Tombstone in newer SSTable hides value in older SSTable
func TestDB_DeleteInSSTShadowsOlderSST(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// First flush: key exists
	if err := db.Put(ctx, []byte("ephemeral"), []byte("here")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush 1: %v", err)
	}

	// Second flush: key deleted
	if err := db.Delete(ctx, []byte("ephemeral")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush 2: %v", err)
	}

	_, err = db.Get(ctx, []byte("ephemeral"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound (tombstone in SST2 hides SST1), got %v", err)
	}
}

// Close + reopen, data readable from discovered SSTs
func TestDB_ReopenLoadsSSTables(t *testing.T) {
	dir := t.TempDir()

	// Write, flush, close
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	for i := range 20 {
		key := fmt.Appendf(nil, "persist-%04d", i)
		val := fmt.Appendf(nil, "value-%04d", i)
		if err := db.Put(ctx, key, val); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify all keys are readable
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer db2.Close()

	for i := range 20 {
		key := fmt.Appendf(nil, "persist-%04d", i)
		want := fmt.Appendf(nil, "value-%04d", i)
		got, err := db2.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%s) after reopen: %v", key, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Get(%s) = %q, want %q", key, got, want)
		}
	}
}

// --- Benchmarks ---

func BenchmarkFlush(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"100-entries", 100},
		{"1000-entries", 1000},
		{"10000-entries", 10000},
	}
	for _, s := range counts {
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				b.StopTimer()
				dir := b.TempDir()
				db, err := Open(dir, WithSync(false))
				if err != nil {
					b.Fatalf("Open: %v", err)
				}
				ctx := context.Background()
				for i := range s.count {
					key := fmt.Appendf(nil, "key-%08d", i)
					if err := db.Put(ctx, key, []byte("value-data-here")); err != nil {
						b.Fatalf("Put: %v", err)
					}
				}
				b.StartTimer()

				if err := db.flushMemtable(); err != nil {
					b.Fatalf("flush: %v", err)
				}

				b.StopTimer()
				db.Close()
				b.StartTimer()
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Auto-flush tests
// ---------------------------------------------------------------------------

// countSSTFiles returns the number of .sst files in dir.
func countSSTFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sst" {
			n++
		}
	}
	return n
}

// writeNBytes writes enough key-value pairs to reach at least n bytes in the memtable.
func writeNBytes(ctx context.Context, t *testing.T, db *DB, n int) {
	t.Helper()
	written := 0
	i := 0
	for written < n {
		key := fmt.Appendf(nil, "key-%06d", i)
		val := fmt.Appendf(nil, "value-%06d", i)
		if err := db.Put(ctx, key, val); err != nil {
			t.Fatalf("Put: %v", err)
		}
		// Rough estimate; internal key overhead is ~9 bytes + 8 bytes framing in skiplist
		written += len(key) + len(val) + 20
		i++
	}
}

// 1. TriggersOnThreshold — write enough data, close, verify SST files exist.
func TestDB_AutoFlush_TriggersOnThreshold(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(512))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	writeNBytes(ctx, t, db, 1024)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := countSSTFiles(t, dir); n < 1 {
		t.Fatalf("expected at least 1 SST file, got %d", n)
	}
}

// 2. DataReadableAfterFlush — trigger flush, close, reopen, verify data.
func TestDB_AutoFlush_DataReadableAfterFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(512))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keys := make([]string, 0, 50)
	for i := range 50 {
		k := fmt.Sprintf("key-%06d", i)
		v := fmt.Sprintf("value-%06d", i)
		if err := db.Put(ctx, []byte(k), []byte(v)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		keys = append(keys, k)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify all keys
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	for _, k := range keys {
		val, err := db2.Get(ctx, []byte(k))
		if err != nil {
			t.Fatalf("Get(%s): %v", k, err)
		}
		expected := "value-" + k[4:] // "key-000001" -> "value-000001"
		if string(val) != expected {
			t.Fatalf("Get(%s) = %q, want %q", k, val, expected)
		}
	}
}

// 3. DisabledWhenZero — no auto-flush when threshold is 0.
func TestDB_AutoFlush_DisabledWhenZero(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	writeNBytes(ctx, t, db, 2048)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := countSSTFiles(t, dir); n != 0 {
		t.Fatalf("expected 0 SST files with flush disabled, got %d", n)
	}
}

// 4. MultipleFlushes — trigger 2+ flushes, verify multiple SST files and data.
func TestDB_AutoFlush_MultipleFlushes(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(256))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	allKeys := make([]string, 0, 100)
	for i := range 100 {
		k := fmt.Sprintf("key-%06d", i)
		v := fmt.Sprintf("value-%06d", i)
		if err := db.Put(ctx, []byte(k), []byte(v)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		allKeys = append(allKeys, k)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if n := countSSTFiles(t, dir); n < 2 {
		t.Fatalf("expected at least 2 SST files, got %d", n)
	}

	// Reopen and verify all data
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	for _, k := range allKeys {
		val, err := db2.Get(ctx, []byte(k))
		if err != nil {
			t.Fatalf("Get(%s): %v", k, err)
		}
		expected := "value-" + k[4:]
		if string(val) != expected {
			t.Fatalf("Get(%s) = %q, want %q", k, val, expected)
		}
	}
}

// 5. ReopenAfterAutoFlush — data recovered from SSTables, not just WAL.
func TestDB_AutoFlush_ReopenAfterAutoFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(512))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	writeNBytes(ctx, t, db, 1024)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify SST files exist before reopen
	sstCount := countSSTFiles(t, dir)
	if sstCount < 1 {
		t.Fatalf("expected SST files after flush, got %d", sstCount)
	}

	// Reopen — data should load from SSTables
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	if len(db2.ssts) < 1 {
		t.Fatalf("expected SSTable readers after reopen, got %d", len(db2.ssts))
	}

	// Spot-check a key
	val, err := db2.Get(ctx, []byte("key-000000"))
	if err != nil {
		t.Fatalf("Get(key-000000): %v", err)
	}
	if string(val) != "value-000000" {
		t.Fatalf("Get(key-000000) = %q, want %q", val, "value-000000")
	}
}

// 6. DeletesVisibleAfterFlush — tombstone propagation across flush boundaries.
func TestDB_AutoFlush_DeletesVisibleAfterFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(256))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Write a key and enough data to trigger a flush
	if err := db.Put(ctx, []byte("target"), []byte("alive")); err != nil {
		t.Fatal(err)
	}
	writeNBytes(ctx, t, db, 512) // trigger flush

	// Delete the key and trigger another flush
	if err := db.Delete(ctx, []byte("target")); err != nil {
		t.Fatal(err)
	}
	writeNBytes(ctx, t, db, 512) // trigger another flush

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify the key is deleted
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	_, err = db2.Get(ctx, []byte("target"))
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for deleted key, got %v", err)
	}
}

// 7. ImmutableMemtableVisible — data in immMem is readable during flush.
func TestDB_AutoFlush_ImmutableMemtableVisible(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(256))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Write a known key
	if err := db.Put(ctx, []byte("visible-key"), []byte("visible-val")); err != nil {
		t.Fatal(err)
	}

	// Write enough to trigger the swap (key moves to immMem)
	writeNBytes(ctx, t, db, 512)

	// The key should still be readable (either from immMem or SSTables)
	val, err := db.Get(ctx, []byte("visible-key"))
	if err != nil {
		t.Fatalf("Get(visible-key): %v", err)
	}
	if string(val) != "visible-val" {
		t.Fatalf("Get(visible-key) = %q, want %q", val, "visible-val")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// 8. CloseWaitsForFlush — trigger flush, close immediately, verify SST on disk.
func TestDB_AutoFlush_CloseWaitsForFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(256))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	writeNBytes(ctx, t, db, 512)

	// Close immediately — should block until flush completes
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// SST file must exist on disk
	if n := countSSTFiles(t, dir); n < 1 {
		t.Fatalf("expected at least 1 SST file after close, got %d", n)
	}
}

func TestDB_AutoFlush_FailureStopsFurtherWrites(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(1))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	flushFailure := errors.New("forced auto-flush failure")
	prevWriteSSTableFn := writeSSTableFn
	writeSSTableFn = func(string, memtable.Memtable, int) (*sstable.Reader, error) {
		return nil, flushFailure
	}
	t.Cleanup(func() {
		writeSSTableFn = prevWriteSSTableFn
	})

	ctx := context.Background()
	if err := db.Put(ctx, []byte("key"), []byte("value")); err != nil {
		t.Fatalf("initial Put: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		db.mu.RLock()
		err = db.flushErr
		done := db.flushDoneCh == nil
		db.mu.RUnlock()
		if errors.Is(err, flushFailure) && done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !errors.Is(err, flushFailure) {
		t.Fatalf("expected auto-flush failure to be recorded, got %v", err)
	}

	got, err := db.Get(ctx, []byte("key"))
	if err != nil {
		t.Fatalf("expected key to remain readable after failed auto-flush, got %v", err)
	}
	if !slices.Equal(got, []byte("value")) {
		t.Fatalf("Get(key) = %q, want %q", got, "value")
	}

	if err := db.Put(ctx, []byte("later"), []byte("value")); !errors.Is(err, flushFailure) {
		t.Fatalf("expected writes after auto-flush failure to fail, got %v", err)
	}

	if err := db.Flush(); !errors.Is(err, flushFailure) {
		t.Fatalf("expected Flush after auto-flush failure to return flush error, got %v", err)
	}
}

// 9. WriteStall — concurrent writers with tiny threshold, no data loss, no races.
//
// Uses concurrent writers only.
func TestDB_AutoFlush_WriteStall(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(256))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const numGoroutines = 4
	const writesPerGoroutine = 50

	var wg sync.WaitGroup
	errs := make(chan error, numGoroutines*writesPerGoroutine)

	for g := range numGoroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range writesPerGoroutine {
				k := fmt.Appendf(nil, "g%d-key-%06d", id, i)
				v := fmt.Appendf(nil, "g%d-val-%06d", id, i)
				if err := db.Put(ctx, k, v); err != nil {
					errs <- fmt.Errorf("goroutine %d, write %d: %w", id, i, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent write error: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify all data
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	for g := range numGoroutines {
		for i := range writesPerGoroutine {
			k := fmt.Sprintf("g%d-key-%06d", g, i)
			v := fmt.Sprintf("g%d-val-%06d", g, i)
			got, err := db2.Get(ctx, []byte(k))
			if err != nil {
				t.Fatalf("Get(%s): %v", k, err)
			}
			if string(got) != v {
				t.Fatalf("Get(%s) = %q, want %q", k, got, v)
			}
		}
	}
}

// 10. ConcurrentReadWrite — concurrent readers and writers while flushes happen.
//
// Verifies that Get() never sees an error other than ErrKeyNotFound for
// keys that haven't been written yet, and that once a key is written it
// eventually becomes readable. Run with -race.
func TestDB_AutoFlush_ConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false), WithMemtableFlushSize(256))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const numWriters = 3
	const numReaders = 3
	const writesPerWriter = 40

	var wg sync.WaitGroup
	errs := make(chan error, (numWriters+numReaders)*writesPerWriter)

	// Writers: each goroutine writes its own key space
	for w := range numWriters {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range writesPerWriter {
				k := fmt.Appendf(nil, "w%d-key-%06d", id, i)
				v := fmt.Appendf(nil, "w%d-val-%06d", id, i)
				if err := db.Put(ctx, k, v); err != nil {
					errs <- fmt.Errorf("writer %d, put %d: %w", id, i, err)
					return
				}
			}
		}(w)
	}

	// Readers: continuously read keys from writer 0's space.
	// Keys may not exist yet (ErrKeyNotFound is OK), but any other error is a bug.
	for r := range numReaders {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range writesPerWriter {
				k := fmt.Appendf(nil, "w0-key-%06d", i)
				_, err := db.Get(ctx, k)
				if err != nil && !errors.Is(err, ErrKeyNotFound) {
					errs <- fmt.Errorf("reader %d, get %d: %w", id, i, err)
					return
				}
			}
		}(r)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent read/write error: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen and verify all writer data is present
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	for w := range numWriters {
		for i := range writesPerWriter {
			k := fmt.Sprintf("w%d-key-%06d", w, i)
			v := fmt.Sprintf("w%d-val-%06d", w, i)
			got, err := db2.Get(ctx, []byte(k))
			if err != nil {
				t.Fatalf("Get(%s): %v", k, err)
			}
			if string(got) != v {
				t.Fatalf("Get(%s) = %q, want %q", k, got, v)
			}
		}
	}
}
