package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

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
		{0, "000000.sst"},
		{1, "000001.sst"},
		{42, "000042.sst"},
		{999999, "999999.sst"},
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
	sstPath := filepath.Join(dir, "000000.sst")
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

	// Flush with no data — should succeed and produce a valid (empty) SSTable
	if err := db.flushMemtable(); err != nil {
		t.Fatalf("flushMemtable on empty: %v", err)
	}

	// The SST file should exist
	sstPath := filepath.Join(dir, "000000.sst")
	if _, err := os.Stat(sstPath); err != nil {
		t.Fatalf("SST file not found: %v", err)
	}

	// Reader should be published
	if len(db.ssts) != 1 {
		t.Fatalf("expected 1 reader, got %d", len(db.ssts))
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

// --- Task 3.3/3.4: Get reads from SSTables + flush integration ---

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
