package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/aalhour/beachdb/internal/crashhook"
	"github.com/aalhour/beachdb/internal/fs"
	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/manifest"
	"github.com/aalhour/beachdb/internal/memtable"
	"github.com/aalhour/beachdb/internal/record"
	"github.com/aalhour/beachdb/internal/sstable"
	"github.com/aalhour/beachdb/internal/wal"
)

const (
	// walFileName specifies the name of the WAL file.
	walFileName = "beachdb.wal"

	// sstableFileExt specifies the extension of SSTable files.
	sstableFileExt = ".sst"

	// sstableFileIDWidth keeps lexicographic and numeric order aligned for all uint64 IDs.
	sstableFileIDWidth = 20

	// manifestFilePrefix specifies the file name prefix for MANIFEST files.
	manifestFilePrefix = "MANIFEST-"

	// manifestFileIDWidth keeps lexicographic and numeric order aligned for all uint64 IDs.
	manifestFileIDWidth = 6
)

var (
	// writeSSTableFn allows tests to inject flush-time failures.
	writeSSTableFn = writeSSTable

	// beforeWALSync allows tests to observe the durability boundary before fsync.
	beforeWALSync func()
)

// DB defines the database struct wrapping the public APIs.
type DB struct {
	// Immutable after Open()
	dir               string // Path on disk to write data into
	syncOnWrite       bool   // Option: Whether the DB should fsync writes or not
	memtableFlushSize int64  // Option: Flush threshold in bytes; 0 = no auto-flush
	sstBlockSize      int    // Option: SSTable block size in bytes; 0 = use sstable default

	// Concurrency
	// - Writers interact with the `cond` via `mu.Lock()`, `cond.Wait()`, `cond.Signal()`
	// - Readers use `mu.RLock()` and never touch the cond (unaffected)
	// - `cond.Wait()` atomically releases the write lock and sleeps,
	//   then reacquires it when signaled (no spinning, no race windows)
	mu   sync.RWMutex // protects all mutable state below
	cond *sync.Cond   // cond.L = &db.mu (write side only)

	// Mutable state (guarded by `mu`)
	closed   bool              // Flag indicating whether db is closed or not
	seqno    uint64            // Monotonic sequence counter
	logno    uint64            // TODO: new field
	mem      memtable.Memtable // Memory table with recent writes
	immMem   memtable.Memtable // frozen memtable being flushed; nil when idle
	ssts     []*sstable.Reader // Open SSTable readers, newest-last in slice
	wal      *wal.Writer       // Writer for the Write-Ahead Log (WAL) file
	manifest *manifest.Writer  // Active manifest writer
	version  *manifest.Version // Current in-memory version data (ssts metadata)

	// SSTable flush goroutine state
	nextSSTID   uint64        // Counter for SST file naming (new files)
	flushDoneCh chan struct{} // Channel for flushing coordination; closed when flush goroutine exits
	flushErr    error         // Last flush error
}

// Open initializes a DB struct, restoring state from MANIFEST + CURRENT
// and replaying any WAL records written after the last flush. A directory
// with no CURRENT is treated as a fresh database and bootstrapped from
// scratch (see openManifest for the full decision tree).
func Open(dir string, opts ...Option) (*DB, error) {
	cfg := applyOptions(opts)

	if err := fs.MkdirAllAndSync(dir); err != nil {
		return nil, err
	}
	if cfg.memtableFlushSize < 0 {
		return nil, ErrInvalidMemtableFlushSize
	}
	if cfg.sstBlockSize < 0 {
		return nil, ErrInvalidSSTBlockSize
	}

	db := &DB{
		dir:               dir,
		syncOnWrite:       cfg.syncOnWrite,
		memtableFlushSize: cfg.memtableFlushSize,
		sstBlockSize:      cfg.sstBlockSize,
		mem:               memtable.NewSkipList(),
	}
	db.cond = sync.NewCond(&db.mu)

	// Read CURRENT, replay manifest (or bootstrap fresh). On return,
	// db.version, db.manifest, db.ssts, db.nextSSTID, and db.seqno
	// are all installed from on-disk state.
	if err := openManifest(db); err != nil {
		return nil, err
	}

	walFilePath := filepath.Join(dir, walFileName)

	// WAL replay continues from db.seqno (the manifest's LastSequence).
	if err := replayWAL(db, walFilePath); err != nil {
		_ = db.Close()
		return nil, err
	}

	walWriter, err := wal.NewWriter(walFilePath)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("beachdb: creating WAL writer: %w", err)
	}
	db.wal = walWriter

	if db.memtableFlushSize > 0 {
		doneCh := make(chan struct{})
		db.flushDoneCh = doneCh
		go db.flushLoop(doneCh)
	}

	return db, nil
}

func (db *DB) Write(ctx context.Context, b *Batch) error {
	// Acquire write lock
	db.mu.Lock()
	defer db.mu.Unlock()

	// Check write pre-conditions
	if err := db.checkWritePreconditionsLocked(ctx); err != nil {
		return err
	}

	// Make sure the batch is neither nil nor empty
	if b == nil || b.Empty() {
		return nil
	}

	// Snapshot and encode once
	ops := b.snapshot()
	encoded := encodeOperations(ops)

	// Append the changes to the WAL file via wal.Writer
	if err := db.wal.Append(encoded); err != nil {
		return fmt.Errorf("beachdb: appending to WAL: %w", err)
	}

	// FAILPOINT: wal_after_append
	crashhook.CrashIfArmed(crashhook.PointWALAfterAppend)

	// Try to sync the WAL to disk if the option is set
	if err := db.syncWALLocked(); err != nil {
		return err
	}

	// Apply the Batch operations to Memtable
	db.applyOperations(ops)

	return db.maybeAutoFlushLocked()
}

// Get returns the value associated with the given key or returns an ErrKeyNotFound.
func (db *DB) Get(ctx context.Context, key []byte) (value []byte, err error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	// Check if the database was closed
	if db.closed {
		return nil, ErrDBClosed
	}

	// Check if the call was canceled
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("beachdb: Get call canceled: %w", err)
	}

	// Step #1: Check the active memtable
	value, found := db.mem.Get(key, db.seqno)
	if found {
		if value == nil {
			// Tombstone (latest update in memtable was a delete operation)
			return nil, ErrKeyNotFound
		}
		return value, nil
	}

	// Step #2: Check the immutable memtable (non-nil during flush)
	if db.immMem != nil {
		value, found = db.immMem.Get(key, db.seqno)
		if found {
			if value == nil {
				return nil, ErrKeyNotFound // Tombstone
			}
			return value, nil
		}
	}

	// Step #3: Scan SSTables, newest-first in reverse order, because
	// they are sorted lexicographically from oldest to newest (e.g.: 001 --> 123)
	for _, reader := range slices.Backward(db.ssts) {
		value, err := reader.Get(key, db.seqno)

		// Value found
		if err == nil {
			return value, nil
		}

		// Tombstone: key was explicitly deleted at this level, stop searching
		if errors.Is(err, sstable.ErrKeyDeleted) {
			return nil, ErrKeyNotFound
		}

		// Key absent from this SSTable, try the next one
		if errors.Is(err, sstable.ErrKeyNotFound) {
			continue
		}

		// Real error (corruption, I/O failure)
		return nil, fmt.Errorf("beachdb: reading SSTable: %w", err)
	}

	// Scanned all SSTables and found nothing, return an error.
	return nil, ErrKeyNotFound
}

// Put writes the key-value pair in the database.
func (db *DB) Put(ctx context.Context, key, value []byte) error {
	b := NewBatch()
	b.Put(key, value)
	return db.Write(ctx, b)
}

// Delete removes the key and its associated value from the database.
func (db *DB) Delete(ctx context.Context, key []byte) error {
	b := NewBatch()
	b.Delete(key)
	return db.Write(ctx, b)
}

// Flush writes the active memtable to a new SSTable and waits for the flush
// to complete before returning. It is a no-op if the memtable is empty.
//
// When auto-flush is enabled, Flush hands the memtable to the background
// goroutine and stalls until that specific flush finishes — it does not bypass
// or race with the background flush path. When auto-flush is disabled, the
// flush runs synchronously in the caller's goroutine.
func (db *DB) Flush() error {
	return db.flushMemtable()
}

// Close closes the database and frees allocated resources.
func (db *DB) Close() error {
	if db == nil {
		return ErrDBClosed
	}

	// Acquire lock to modify private state
	db.mu.Lock()
	if db.closed {
		db.mu.Unlock()
		return ErrDBClosed
	}
	db.closed = true          // mark it as closed under the lock to reject new mutations
	flushCh := db.flushDoneCh // snapshot under the lock — flushLoop's deferred cleanup nils this field
	db.cond.Broadcast()       // wake flush goroutine so it sees closed=true and exits
	db.mu.Unlock()            // Release the lock before moving forward

	// Wait for flush goroutine OUTSIDE the lock.
	// The goroutine needs db.mu.Lock to finish its current flush.
	// Waiting while holding the lock would definitely deadlock.
	// The snapshot above guards against a race with flushLoop's deferred
	// nil-write of db.flushDoneCh.
	if flushCh != nil {
		<-flushCh
	}

	// Final cleanup under lock
	db.mu.Lock()
	defer db.mu.Unlock()

	var firstError error

	// Close all SSTable readers before closing the WAL writer
	for _, reader := range db.ssts {
		if err := reader.Close(); err != nil && firstError == nil {
			firstError = err
		}
	}

	// Close the WAL writer
	if err := db.wal.Close(); err != nil && firstError == nil {
		firstError = err
	}

	// Close the manifest file writer
	if db.manifest != nil {
		if err := db.manifest.Close(); err != nil && firstError == nil {
			firstError = err
		}
		db.manifest = nil
	}

	// Mark it as closed
	db.wal = nil
	db.ssts = nil
	db.mem = nil
	db.immMem = nil
	db.manifest = nil

	if firstError != nil {
		return fmt.Errorf("beachdb: error closing DB: %w", firstError)
	}
	return nil
}

// applyBatch takes a batch struct and applies it to db.mem
func (db *DB) applyBatch(b *Batch) {
	if b == nil {
		return
	}

	// Apply the Batch operations to Memtable
	db.applyOperations(b.snapshot())
}

// checkWritePreconditionsLocked validates state required before a write can proceed.
// db.mu must already be held.
func (db *DB) checkWritePreconditionsLocked(ctx context.Context) error {
	switch {
	case db.closed:
		return ErrDBClosed
	case db.flushErr != nil:
		return db.flushErr
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("beachdb: Write call canceled: %w", err)
	}

	return nil
}

// applyOperations applies a frozen batch of operations to the active memtable.
func (db *DB) applyOperations(ops []operation) {
	for _, op := range ops {
		// Increase monotonic counter
		db.seqno++

		// Lookup correct internal key kind
		var kind byte
		switch op.opType {
		case OpTypePut:
			kind = keys.InternalKeyKindPut
		case OpTypeDelete:
			kind = keys.InternalKeyKindDelete
		}

		// Add key-value pair to the memtable
		internalKey := keys.InternalKey{
			UserKey: op.key,
			Seqno:   db.seqno,
			Kind:    kind,
		}

		db.mem.Put(internalKey, op.value)
	}
}

// syncWALLocked flushes and syncs the WAL when sync-on-write is enabled.
// db.mu must already be held.
func (db *DB) syncWALLocked() error {
	if !db.syncOnWrite {
		return nil
	}

	if beforeWALSync != nil {
		beforeWALSync()
	}

	// FAILPOINT: wal_sync_error
	if err := crashhook.MaybeFault(crashhook.FaultWALSyncError); err != nil {
		return fmt.Errorf("beachdb: syncing WAL: %w", err)
	}

	if err := db.wal.Sync(); err != nil {
		return fmt.Errorf("beachdb: syncing WAL: %w", err)
	}

	// FAILPOINT: wal_after_sync
	crashhook.CrashIfArmed(crashhook.PointWALAfterSync)

	return nil
}

// waitForFlushSlotLocked waits for any in-flight immutable memtable flush to finish.
// db.mu must already be held.
func (db *DB) waitForFlushSlotLocked() error {
	for db.immMem != nil {
		if db.flushErr != nil {
			return db.flushErr
		}

		db.cond.Wait()
		switch {
		case db.closed:
			return ErrDBClosed
		case db.flushErr != nil:
			return db.flushErr
		}
	}

	return nil
}

// maybeAutoFlushLocked rotates the active memtable when the flush threshold is reached.
// db.mu must already be held.
func (db *DB) maybeAutoFlushLocked() error {
	// Nothing to do unless auto-flush is enabled and the active memtable crossed the threshold.
	if db.memtableFlushSize == 0 || db.mem.Size() < db.memtableFlushSize {
		return nil
	}

	// Auto-flush is single-flight: wait until any prior immutable memtable finishes publishing.
	if err := db.waitForFlushSlotLocked(); err != nil {
		return err
	}

	// Freeze the current memtable for the flusher and install a fresh writable memtable.
	db.immMem = db.mem
	db.mem = memtable.NewSkipList()

	// Wake the background flusher now that there is immutable work ready to persist.
	db.cond.Signal()

	return nil
}

// replayWAL reads a WAL file, if it exists, and applies it to db.mem
func replayWAL(db *DB, walFilePath string) error {
	_, err := os.Stat(walFilePath) //nolint:gosec // path is constructed from the trusted DB directory
	if err != nil {
		// If WAL doesn't exist, it's a fresh database
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("beachdb: failed to open WAL for recovery: %w", err)
	}

	// WAL exists, create a WAL reader
	reader, err := wal.NewReader(walFilePath)
	if err != nil {
		return fmt.Errorf("beachdb: failed to open WAL for recovery: %w", err)
	}
	defer reader.Close()

	// Replay the WAL one record at a time
	for recordIdx := 0; ; recordIdx++ {
		payload, err := reader.Next()
		switch {
		case errors.Is(err, io.EOF):
			// Clean end
			// TODO: Log it as info
			return nil
		case errors.Is(err, record.ErrTruncated):
			// Incomplete write before crash, skip it
			// TODO: Log it as info
			if err := os.Truncate(walFilePath, reader.ValidOffset()); err != nil {
				return fmt.Errorf("beachdb: truncating WAL tail during recovery: %w", err)
			}
			return nil
		case err != nil:
			return fmt.Errorf("beachdb: WAL recovery failed at record %d: %w", recordIdx, err)
		}

		// Decode the payload a Batch and replay it
		batch, err := DecodeBatch(payload)
		if err != nil {
			return fmt.Errorf("beachdb: WAL recovery failed decoding batch %d: %w", recordIdx, err)
		}

		// Apply the Batch operations to Memtable
		db.applyOperations(batch.ops)
	}
}

// flushLoop serializes background memtable flushes until shutdown or the first flush failure.
func (db *DB) flushLoop(doneCh chan struct{}) {
	defer close(doneCh)

	db.mu.Lock()
	defer func() {
		if db.flushDoneCh == doneCh {
			db.flushDoneCh = nil
		}
		db.mu.Unlock()
	}()

	for {
		// Sleep until there is an immutable memtable to flush, or shutdown
		for db.immMem == nil && !db.closed {
			db.cond.Wait()
		}

		// If the database got closed in the meantime, return!
		if db.closed {
			return
		}

		// Grab what we need under the lock
		imm := db.immMem

		// A concurrent rotation can hand off an empty memtable: two writers
		// both pass the threshold check and queue on the flush slot, the
		// first installs a fresh memtable, and the second freezes it before
		// any write lands. Skip it — there is nothing to persist and an empty
		// flush would record a zero-range file in the manifest.
		if imm.Empty() {
			db.immMem = nil
			db.cond.Broadcast()
			continue
		}

		sstPath := db.nextSSTPath()

		// Release the lock for I/O - this is where the work happens
		db.mu.Unlock()
		flushResult, err := writeSSTableFn(sstPath, imm, db.sstBlockSize)

		// Re-acquire the lock to publish the results
		db.mu.Lock()

		if err != nil {
			db.flushErr = err
			db.cond.Broadcast()
			return
		}

		// Publish the new SSTable reader and clear the immutable memtable.
		if err := db.publishFlushedSSTLocked(flushResult); err != nil {
			db.flushErr = err
			db.cond.Broadcast()
			return
		}

		// Wake stalled writers and anyone waiting on flush completion
		db.cond.Broadcast()
	}
}

// flushMemtable is a synchronous helper function for writing the contents of `db.mem`
// to a new SST file and replaces the memtable with a new one
//
//nolint:gocognit // flush coordination is stateful and shared with background flushing
func (db *DB) flushMemtable() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return ErrDBClosed
	}
	if db.flushErr != nil {
		return db.flushErr
	}

	if db.mem.Empty() {
		return nil // Nothing to flush
	}

	// Wait for any in-progress flush to complete
	for db.immMem != nil {
		if db.flushErr != nil {
			return db.flushErr
		}
		db.cond.Wait()
		if db.closed {
			return ErrDBClosed
		}
		if db.flushErr != nil {
			return db.flushErr
		}
	}

	if db.flushDoneCh != nil {
		// Background goroutine is running - use it
		db.immMem = db.mem
		db.mem = memtable.NewSkipList()
		db.cond.Signal()

		// Wait for this flush to complete
		for db.immMem != nil && !db.closed {
			db.cond.Wait()
			if db.flushErr != nil {
				return db.flushErr
			}
		}
		if db.closed {
			return ErrDBClosed
		}

		return db.flushErr
	}

	// No goroutine - do it synchronously
	imm := db.mem
	db.immMem = imm
	db.mem = memtable.NewSkipList()
	sstPath := db.nextSSTPath()
	db.mu.Unlock() // Release the lock for I/O operations

	sstReader, err := writeSSTableFn(sstPath, imm, db.sstBlockSize)

	// Re-acquire the lock to publish the results
	db.mu.Lock()
	if err != nil {
		db.flushErr = err
		db.cond.Broadcast()
		return err
	}

	// Publish the new SSTable reader and clear the immutable memtable.
	if err := db.publishFlushedSSTLocked(sstReader); err != nil {
		db.flushErr = err
		db.cond.Broadcast()
		return err
	}

	db.cond.Broadcast()
	return nil
}

// publishFlushedSSTLocked publishes a successfully flushed SSTable under db.mu.
func (db *DB) publishFlushedSSTLocked(flushResult sstWriteResult) error {
	// FAILPOINT: sst_publish_error
	if err := crashhook.MaybeFault(crashhook.FaultSSTPublishError); err != nil {
		return fmt.Errorf("beachdb: publishing SSTable: %w", err)
	}

	// Create a new version edit and sync manifest with latest changes.
	versionEdit := manifest.VersionEdit{
		AddedFiles: []manifest.FileMetadata{
			{
				Level:       0,
				FileID:      db.nextSSTID,
				Size:        flushResult.size,
				SmallestKey: flushResult.smallest,
				LargestKey:  flushResult.largest,
			},
		},
		HasNextFileID:   true,
		NextFileID:      1 + db.nextSSTID,
		HasLastSequence: true,
		LastSequence:    db.seqno,
	}

	// The SSTable is already durable (writeSSTable synced the file and its
	// parent directory); the manifest edit has not been appended yet.
	// FAILPOINT: manifest_after_sst_sync
	crashhook.CrashIfArmed(crashhook.PointManifestAfterSSTSync)

	// Append the edit to the manifest --> this fsyncs the file + dir on disk!
	if err := db.manifest.Append(versionEdit.Encode()); err != nil {
		// The .sst file itself staying on disk is fine, it will be an orphan and will be
		// deleted on the next `Open()`. nextSSTID isn't bumped so the id/path is reused.
		_ = flushResult.reader.Close()
		return fmt.Errorf("beachdb: appending flush edit to manifest: %w", err)
	}

	// The manifest edit is durable; the in-memory version has not been
	// updated yet.
	// FAILPOINT: manifest_after_append
	crashhook.CrashIfArmed(crashhook.PointManifestAfterAppend)

	// Update current version with the edit and append the sst reader to db.ssts
	db.version = db.version.Apply(&versionEdit)
	db.flushErr = nil
	db.ssts = append(db.ssts, flushResult.reader)
	db.immMem = nil
	db.nextSSTID++

	// FAILPOINT: flush_after_publish
	crashhook.CrashIfArmed(crashhook.PointFlushAfterPublish)

	return nil
}

// nextSSTPath returns a full path for the SSTable file from
// the internal nextSSTID
func (db *DB) nextSSTPath() string {
	return filepath.Join(db.dir, buildSSTFileName(db.nextSSTID))
}

// openManifest reads the CURRENT pointer and dispatches to either the
// fresh-database bootstrap or the existing-manifest replay path. After
// it returns, db.version, db.manifest, db.nextSSTID, and db.seqno are
// installed and ready for use.
//
// Dispatch:
//
//	CURRENT missing                  → bootstrapFreshManifest
//	CURRENT exists but is empty      → hard error
//	CURRENT points at missing file   → hard error
//	manifest is corrupt mid-stream   → hard error
//	manifest tail truncated by crash → benign, replay tolerates and trims
//	CURRENT + manifest both intact   → replayExistingManifest
//
// "CURRENT missing" covers both the truly fresh directory and the case
// where a prior bootstrap crashed before installing CURRENT (leaving an
// orphan MANIFEST). CURRENT install is the final commit step of every
// manifest creation, so an orphan MANIFEST without a CURRENT pointing at
// it was never committed and the directory is effectively fresh.
func openManifest(db *DB) error {
	name, err := manifest.ReadCurrent(db.dir)
	switch {
	case errors.Is(err, manifest.ErrNoCurrentFile):
		return bootstrapFreshManifest(db)
	case err != nil:
		// CURRENT exists but is empty or otherwise unreadable.
		return fmt.Errorf("beachdb: reading CURRENT: %w", err)
	}

	return replayExistingManifest(db, name)
}

// bootstrapFreshManifest initializes the on-disk manifest state for a
// brand-new database directory. It creates MANIFEST-000001, appends a
// single VersionEdit carrying the initial counters (nextFileID=1,
// lastSequence=0, logNumber=0), and atomically installs CURRENT so the
// database is officially committed before Open returns.
//
// The CURRENT install is the commit barrier: until WriteCurrent succeeds,
// nothing on disk points at the new manifest, so a crash anywhere before
// that step leaves the directory looking fresh again on the next Open.
// A crash *after* CURRENT install leaves a valid, replayable database.
func bootstrapFreshManifest(db *DB) error {
	version := manifest.NewVersion(0)

	nextFileID := uint64(1)
	seqno := uint64(0)
	logno := uint64(0)

	// Pick the next-available MANIFEST ID rather than hardcoding 1. A prior
	// bootstrap may have crashed between manifest-create and CURRENT-install,
	// leaving an orphan MANIFEST-NNNNNN. Reusing that name with O_APPEND
	// would write our initial edit on top of the orphan's garbage bytes and
	// corrupt the manifest stream on the next Open.
	manifestID, err := nextAvailableManifestID(db.dir)
	if err != nil {
		return err
	}
	newManifestFileName := buildManifestFileName(manifestID)
	newManifestFilePath := filepath.Join(db.dir, newManifestFileName)
	manifestWriter, err := manifest.NewWriter(newManifestFilePath)
	if err != nil {
		return fmt.Errorf("beachdb: creating initial manifest: %w", err)
	}

	// Encode the initial counters. Has* flags must be true or Encode will
	// silently drop the fields and the on-disk record will be header-only.
	versionEdit := manifest.VersionEdit{
		HasNextFileID:   true,
		NextFileID:      nextFileID,
		HasLastSequence: true,
		LastSequence:    seqno,
		HasLogNumber:    true,
		LogNumber:       logno,
	}

	// Write the version edit to disk. On failure, close the writer and
	// remove the partial manifest so the directory still looks fresh on
	// the next Open.
	if err := manifestWriter.Append(versionEdit.Encode()); err != nil {
		_ = manifestWriter.Close()
		_ = os.Remove(newManifestFilePath)
		return fmt.Errorf("beachdb: writing initial manifest edit: %w", err)
	}

	// Apply to in-memory Version. Apply returns a new Version — assigning
	// the result is required, otherwise the change is dropped.
	version = version.Apply(&versionEdit)

	// Install CURRENT — the commit barrier. Until this succeeds, the
	// directory still looks fresh on the next Open and the orphan
	// MANIFEST-000001 will be picked up by a future bootstrap.
	if err := manifest.WriteCurrent(db.dir, newManifestFileName); err != nil {
		_ = manifestWriter.Close()
		_ = os.Remove(newManifestFilePath)
		return fmt.Errorf("beachdb: installing CURRENT: %w", err)
	}

	db.version = version
	db.manifest = manifestWriter
	db.seqno = seqno
	db.nextSSTID = nextFileID

	return nil
}

// replayExistingManifest opens the manifest named by CURRENT and replays
// every VersionEdit into a fresh Version, accumulating the file-number,
// sequence, and log-number counters along the way. After replay it opens
// readers for every SSTable referenced by the final Version and installs
// everything on db.
//
// Error policy follows the bootstrap decision tree:
//   - manifest file does not exist (CURRENT points at a missing file) →
//     hard error (corruption — the database promised this file existed)
//   - mid-stream corruption (bad checksum, bad magic, unknown tag,
//     truncated edit body, invalid InternalKey) → hard error
//   - trailing record.ErrTruncated (crash during write) → benign: truncate
//     the manifest file to the last validated boundary and continue
//   - referenced SSTable missing on disk → hard error
//
// manifestReplayResult holds the accumulated state from a manifest replay.
type manifestReplayResult struct {
	version     *manifest.Version
	nextFileID  uint64
	seqno       uint64
	logNumber   uint64
	validOffset int64
	tailTrimmed bool
}

// applyEditToReplay folds one edit into the running replay state. Returns
// an error if the edit re-adds a fileID already present in liveFiles
// (corrupt or engine-buggy manifest).
func applyEditToReplay(
	edit *manifest.VersionEdit,
	res *manifestReplayResult,
	liveFiles map[uint64]struct{},
	current string,
	sawNextFileID *bool,
) error {
	for _, fm := range edit.AddedFiles {
		if _, exists := liveFiles[fm.FileID]; exists {
			return fmt.Errorf("beachdb: manifest %q: duplicate AddFile for fileID %d", current, fm.FileID)
		}
		liveFiles[fm.FileID] = struct{}{}
	}
	for _, d := range edit.DeletedFiles {
		delete(liveFiles, d.FileID)
	}

	res.version = res.version.Apply(edit)
	if edit.HasNextFileID {
		res.nextFileID = edit.NextFileID
		*sawNextFileID = true
	}
	if edit.HasLastSequence {
		res.seqno = edit.LastSequence
	}
	if edit.HasLogNumber {
		res.logNumber = edit.LogNumber
	}
	return nil
}

// replayManifestStream reads every record from reader, applies each
// VersionEdit to a fresh Version, and accumulates counters. Trailing
// record.ErrTruncated is treated as benign (sets tailTrimmed); any other
// non-EOF error is mid-stream corruption and surfaces as an error.
// Duplicate AddFile for the same fileID is corruption.
func replayManifestStream(reader *manifest.Reader, current string) (manifestReplayResult, error) {
	res := manifestReplayResult{version: manifest.NewVersion(0)}
	// liveFiles tracks fileIDs currently in the Version so we can detect
	// duplicate AddFile edits at replay time.
	liveFiles := make(map[uint64]struct{})
	var sawNextFileID bool

	for {
		edit, err := reader.NextEdit()
		if errors.Is(err, io.EOF) || errors.Is(err, record.ErrTruncated) {
			res.validOffset = reader.ValidOffset()
			res.tailTrimmed = errors.Is(err, record.ErrTruncated)
			if !sawNextFileID {
				return res, fmt.Errorf("beachdb: manifest %q has no NextFileID counter — corrupt", current)
			}
			return res, nil
		}
		if err != nil {
			return res, fmt.Errorf("beachdb: replaying manifest %q: %w", current, err)
		}

		if err := applyEditToReplay(edit, &res, liveFiles, current, &sawNextFileID); err != nil {
			return res, err
		}
	}
}

// openSSTReadersForVersion opens an *sstable.Reader for every file in the
// given Version, in level-ascending order. Returns a hard error if any
// referenced SSTable file is missing or unreadable.
func openSSTReadersForVersion(dir string, version *manifest.Version) ([]*sstable.Reader, error) {
	readers := make([]*sstable.Reader, 0, len(version.AllFiles()))
	for _, fm := range version.AllFiles() {
		sstPath := filepath.Join(dir, buildSSTFileName(fm.FileID))
		//nolint:gosec // G304: path is built from the trusted DB directory + manifest-tracked file ID
		sstFile, err := os.Open(sstPath)
		if err != nil {
			return readers, fmt.Errorf("beachdb: opening SSTable %q referenced by manifest: %w", sstPath, err)
		}
		sstReader, err := sstable.OpenReader(sstFile)
		if err != nil {
			_ = sstFile.Close()
			return readers, fmt.Errorf("beachdb: reading SSTable %q: %w", sstPath, err)
		}
		readers = append(readers, sstReader)
	}
	return readers, nil
}

// cleanupOrphanSSTables removes canonical SSTable files that exist on disk
// but are not referenced by the manifest Version. Orphan cleanup is
// best-effort: the manifest is still the source of truth, so a deletion
// failure leaves a harmless file behind for a future Open to try again.
func cleanupOrphanSSTables(dir string, version *manifest.Version) {
	referenced := make(map[uint64]struct{})
	for _, fm := range version.AllFiles() {
		referenced[fm.FileID] = struct{}{}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileID, ok := parseSSTFileName(entry.Name())
		if !ok {
			continue
		}
		if _, exists := referenced[fileID]; exists {
			continue
		}

		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

func replayExistingManifest(db *DB, current string) error {
	manifestPath := filepath.Join(db.dir, current)

	reader, err := manifest.NewReader(manifestPath)
	if err != nil {
		// Includes os.ErrNotExist: CURRENT names a manifest that no longer
		// exists on disk — corruption, the database promised this file.
		return fmt.Errorf("beachdb: opening manifest %q: %w", current, err)
	}

	res, replayErr := replayManifestStream(reader, current)
	closeErr := reader.Close()
	if replayErr != nil {
		return replayErr
	}
	if closeErr != nil {
		return fmt.Errorf("beachdb: closing manifest reader: %w", closeErr)
	}

	if res.tailTrimmed {
		if err := os.Truncate(manifestPath, res.validOffset); err != nil {
			return fmt.Errorf("beachdb: truncating manifest tail at %d: %w", res.validOffset, err)
		}
	}

	cleanupOrphanSSTables(db.dir, res.version)

	ssts, err := openSSTReadersForVersion(db.dir, res.version)
	if err != nil {
		return err
	}

	// Open a Writer on the same manifest file. NewWriter uses O_APPEND,
	// so subsequent edits land at the (possibly truncated) EOF.
	writer, err := manifest.NewWriter(manifestPath)
	if err != nil {
		return fmt.Errorf("beachdb: opening manifest writer: %w", err)
	}

	db.version = res.version
	db.manifest = writer
	db.ssts = ssts
	db.nextSSTID = res.nextFileID
	db.seqno = res.seqno
	db.logno = res.logNumber

	return nil
}

// sstWriteResult carries the outputs of writing a memtable to an on-disk
// SSTable: the open reader plus the metadata needed to record the new
// file in a manifest VersionEdit.
type sstWriteResult struct {
	// reader is an open handle to the freshly written SSTable, ready to
	// serve reads.
	reader *sstable.Reader

	// smallest is the smallest InternalKey in the SSTable (first key in
	// sorted order).
	smallest keys.InternalKey

	// largest is the largest InternalKey in the SSTable (last key in
	// sorted order).
	largest keys.InternalKey

	// size is the SSTable file size in bytes.
	size uint64
}

// writeMemtableEntries adds every entry of mem to writer in ascending
// InternalKey order, returning the smallest and largest keys written. The
// memtable yields keys in sorted order, so the first key is the smallest and
// the last is the largest. It returns ErrFlushEmptyMemtable when the memtable
// has no entries.
func writeMemtableEntries(
	writer *sstable.Writer,
	mem memtable.Memtable,
) (smallest, largest keys.InternalKey, err error) {
	iter := mem.NewIterator()
	iter.SeekToFirst()

	first := true
	for iter.Valid() {
		key := iter.Key()
		if addErr := writer.Add(key, iter.Value()); addErr != nil {
			_ = iter.Close()
			return smallest, largest, fmt.Errorf("beachdb: writing entry to SSTable: %w", addErr)
		}
		if first {
			smallest = key
			first = false
		}
		largest = key
		iter.Next()
	}
	_ = iter.Close()

	if first {
		return smallest, largest, ErrFlushEmptyMemtable
	}
	return smallest, largest, nil
}

// Helper function for writing a memtable to an SSTable file on disk.
// blockSize controls the target data block size; 0 means use the sstable default.
func writeSSTable(path string, mem memtable.Memtable, blockSize int) (sstWriteResult, error) {
	// FAILPOINT: sst_write_error
	if err := crashhook.MaybeFault(crashhook.FaultSSTWriteError); err != nil {
		return sstWriteResult{}, fmt.Errorf("beachdb: writing SSTable: %w", err)
	}

	// Create the new sstable file
	sstFile, err := os.Create(path) //nolint:gosec // path constructed from trusted db.dir + formatted ID
	if err != nil {
		return sstWriteResult{}, fmt.Errorf("%w: %w", ErrCreatingSSTFile, err)
	}
	defer sstFile.Close()

	// Build writer options — always sync, add block size if configured
	writerOpts := []sstable.WriterOption{sstable.WithSync(true)}
	if blockSize > 0 {
		writerOpts = append(writerOpts, sstable.WithBlockSize(blockSize))
	}

	// Create the sstable writer
	writer, err := sstable.NewWriter(sstFile, writerOpts...)
	if err != nil {
		_ = os.Remove(path) //nolint:gosec // path is constructed from the trusted DB directory
		return sstWriteResult{}, fmt.Errorf("beachdb: creating SSTable writer: %w", err)
	}

	// Write every memtable entry to the SSTable, capturing the key range.
	// An empty memtable is rejected (it would record a zero-value key range
	// in the manifest).
	smallestKey, largestKey, err := writeMemtableEntries(writer, mem)
	if err != nil {
		_ = writer.Close()
		_ = os.Remove(path) //nolint:gosec // path is constructed from the trusted DB directory
		return sstWriteResult{}, err
	}

	if err = writer.Close(); err != nil {
		_ = os.Remove(path) //nolint:gosec // path is constructed from the trusted DB directory
		return sstWriteResult{}, fmt.Errorf("beachdb: closing SSTable writer: %w", err)
	}

	// Sync parent directory so the new file's directory entry is durable
	if err = fs.SyncDir(filepath.Dir(path)); err != nil {
		return sstWriteResult{}, fmt.Errorf("beachdb: syncing directory after flush: %w", err)
	}

	// FAILPOINT: flush_after_file_sync
	crashhook.CrashIfArmed(crashhook.PointFlushAfterFileSync)

	// Re-open the file for reading
	sstFileReadMode, err := os.Open(path) //nolint:gosec // path constructed from trusted db.dir + formatted ID
	if err != nil {
		return sstWriteResult{}, fmt.Errorf("beachdb: opening SSTable for reading: %w", err)
	}

	// Create a reader for the newest sstable
	sstReader, err := sstable.OpenReader(sstFileReadMode)
	if err != nil {
		_ = sstFileReadMode.Close()
		return sstWriteResult{}, fmt.Errorf("beachdb: error reading newly flushed SSTable: %w", err)
	}

	fileSize := sstReader.FileSize()
	if fileSize < 0 {
		_ = sstReader.Close()
		return sstWriteResult{}, fmt.Errorf("beachdb: SSTable %q reports negative size %d", path, fileSize)
	}

	result := sstWriteResult{
		reader:   sstReader,
		smallest: smallestKey,
		largest:  largestKey,
		size:     uint64(fileSize),
	}
	return result, nil
}

// buildSSTFileName builds an SSTable file name from a file ID,
// e.g.: 1 --> 000001.sst
func buildSSTFileName(id uint64) string {
	return fmt.Sprintf("%0*d%s", sstableFileIDWidth, id, sstableFileExt)
}

// parseSSTFileName extracts the file ID from a canonical BeachDB SSTable
// filename. Non-canonical names are ignored by orphan cleanup.
func parseSSTFileName(name string) (uint64, bool) {
	if filepath.Ext(name) != sstableFileExt {
		return 0, false
	}

	idStr := strings.TrimSuffix(name, sstableFileExt)
	if len(idStr) != sstableFileIDWidth {
		return 0, false
	}
	for _, ch := range idStr {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}

	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// buildManifestFileName builds a MANIFEST filename from a file ID,
// e.g. 1 → "MANIFEST-000001".
func buildManifestFileName(id uint64) string {
	return fmt.Sprintf("%s%0*d", manifestFilePrefix, manifestFileIDWidth, id)
}

// nextAvailableManifestID returns the smallest MANIFEST file ID not yet
// present in dir. Used by bootstrap to avoid colliding with an orphan
// MANIFEST file left by a prior crashed bootstrap. Returns 1 if no
// MANIFEST files exist.
func nextAvailableManifestID(dir string) (uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("beachdb: scanning dir for MANIFEST files: %w", err)
	}
	var maxID uint64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, manifestFilePrefix) {
			continue
		}
		idStr := strings.TrimPrefix(name, manifestFilePrefix)
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			continue
		}
		if id > maxID {
			maxID = id
		}
	}
	return maxID + 1, nil
}
