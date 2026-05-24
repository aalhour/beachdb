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
	"github.com/aalhour/beachdb/internal/keys"
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
	closed bool              // Flag indicating whether db is closed or not
	seqno  uint64            // Monotonic sequence counter
	mem    memtable.Memtable // Memory table with recent writes
	immMem memtable.Memtable // frozen memtable being flushed; nil when idle
	ssts   []*sstable.Reader // Open SSTable readers, newest-last in slice
	wal    *wal.Writer       // Writer for the Write-Ahead Log (WAL) file

	// SSTable flush goroutine state
	nextSSTID   uint64        // Counter for SST file naming (new files)
	flushDoneCh chan struct{} // Channel for flushing coordination; closed when flush goroutine exits
	flushErr    error         // Last flush error
}

// Open initializes a DB struct and replays the WAL, if present.
func Open(dir string, opts ...Option) (*DB, error) {
	// Process configuration options
	cfg := applyOptions(opts)

	// Create directory for the database and fsync created directory entries.
	if err := mkdirAllAndSync(dir); err != nil {
		return nil, err
	}

	// Validate the memtable flush threshold
	if cfg.memtableFlushSize < 0 {
		return nil, ErrInvalidMemtableFlushSize
	}

	// Validate the SSTable block size
	if cfg.sstBlockSize < 0 {
		return nil, ErrInvalidSSTBlockSize
	}

	// Initialize the DB struct
	db := &DB{
		dir:               dir,
		syncOnWrite:       cfg.syncOnWrite,
		memtableFlushSize: cfg.memtableFlushSize,
		sstBlockSize:      cfg.sstBlockSize,
		closed:            false,
		mem:               memtable.NewSkipList(),
		seqno:             0,
	}

	// Init the concurrency `cond` field for write workloads
	db.cond = sync.NewCond(&db.mu)

	// Construct the WAL file path
	walFilePath := filepath.Join(dir, walFileName)

	// Replay the WAL if it exists
	err := replayWAL(db, walFilePath)
	if err != nil {
		return nil, err
	}

	// Create the WAL writer
	writer, err := wal.NewWriter(walFilePath)
	if err != nil {
		return nil, fmt.Errorf("beachdb: creating WAL writer: %w", err)
	}
	db.wal = writer

	// Sync the directory so the WAL file's directory entry reaches disk.
	// Without this, a crash could leave the WAL data on disk but the
	// directory unaware the file exists.
	if err := syncDir(dir); err != nil {
		// Best effort: close the writer before returning
		_ = writer.Close()
		return nil, err
	}

	// Discover SSTables and create readers for them
	sortedFileNames, nextSSTID, err := discoverSSTables(dir)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("beachdb: error discovering SSTables, %w", err)
	}

	// Iterate over discovered sstable files and open readers for them
	for _, fileName := range sortedFileNames {
		fullPath := filepath.Join(dir, fileName)
		sstableFile, err := os.Open(fullPath) //nolint:gosec // trusted dir + discovered filename
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("beachdb: opening SSTable %s: %w", fileName, err)
		}
		sstReader, err := sstable.OpenReader(sstableFile)
		if err != nil {
			_ = sstableFile.Close()
			_ = db.Close()
			return nil, fmt.Errorf("beachdb: reading SSTable %s: %w", fileName, err)
		}
		db.ssts = append(db.ssts, sstReader)
	}

	// Set the nextSSTID to point to the next free ID number
	db.nextSSTID = nextSSTID

	// Start the SSTable flushing goroutine only after Open succeeds.
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
	db.closed = true    // mark it as closed under the lock to reject new mutations
	db.cond.Broadcast() // wake flush goroutine so it sees closed=true and exists
	db.mu.Unlock()      // Release the lock before moving forward

	// Wait for flush goroutine OUTSIDE the lock.
	// The goroutine needs db.mu.Lock to finish its current flush.
	// Waiting while holding the lock would definitely deadlock.
	if db.flushDoneCh != nil {
		<-db.flushDoneCh
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

	// Mark it as closed
	db.wal = nil
	db.ssts = nil
	db.mem = nil
	db.immMem = nil

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
		sstPath := db.nextSSTPath()

		// Release the lock for I/O - this is where the work happens
		db.mu.Unlock()
		newSSTableReader, err := writeSSTableFn(sstPath, imm, db.sstBlockSize)

		// Re-acquire the lock to publish the results
		db.mu.Lock()

		if err != nil {
			db.flushErr = err
			db.cond.Broadcast()
			return
		}

		// Publish the new SSTable reader and clear the immutable memtable.
		if err := db.publishFlushedSSTLocked(newSSTableReader); err != nil {
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
func (db *DB) publishFlushedSSTLocked(sstReader *sstable.Reader) error {
	// FAILPOINT: sst_publish_error
	if err := crashhook.MaybeFault(crashhook.FaultSSTPublishError); err != nil {
		return fmt.Errorf("beachdb: publishing SSTable: %w", err)
	}

	db.flushErr = nil
	db.ssts = append(db.ssts, sstReader)
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

// Helper function for writing a memtable to an SSTable file on disk.
// blockSize controls the target data block size; 0 means use the sstable default.
func writeSSTable(path string, mem memtable.Memtable, blockSize int) (*sstable.Reader, error) {
	// FAILPOINT: sst_write_error
	if err := crashhook.MaybeFault(crashhook.FaultSSTWriteError); err != nil {
		return nil, fmt.Errorf("beachdb: writing SSTable: %w", err)
	}

	// Create the new sstable file
	sstFile, err := os.Create(path) //nolint:gosec // path constructed from trusted db.dir + formatted ID
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCreatingSSTFile, err)
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
		return nil, fmt.Errorf("beachdb: creating SSTable writer: %w", err)
	}

	// Iterate the immutable memtable and write entries to the SSTable
	iter := mem.NewIterator()
	iter.SeekToFirst()
	for iter.Valid() {
		if err := writer.Add(iter.Key(), iter.Value()); err != nil {
			_ = writer.Close()
			_ = iter.Close()
			_ = os.Remove(path) //nolint:gosec // path is constructed from the trusted DB directory
			return nil, fmt.Errorf("beachdb: writing entry to SSTable: %w", err)
		}
		iter.Next()
	}

	_ = iter.Close()
	if err = writer.Close(); err != nil {
		_ = os.Remove(path) //nolint:gosec // path is constructed from the trusted DB directory
		return nil, fmt.Errorf("beachdb: closing SSTable writer: %w", err)
	}

	// Sync parent directory so the new file's directory entry is durable
	if err = syncDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("beachdb: syncing directory after flush: %w", err)
	}

	// FAILPOINT: flush_after_file_sync
	crashhook.CrashIfArmed(crashhook.PointFlushAfterFileSync)

	// Re-open the file for reading
	sstFileReadMode, err := os.Open(path) //nolint:gosec // path constructed from trusted db.dir + formatted ID
	if err != nil {
		return nil, fmt.Errorf("beachdb: opening SSTable for reading: %w", err)
	}

	// Create a reader for the newest sstable
	sstReader, err := sstable.OpenReader(sstFileReadMode)
	if err != nil {
		_ = sstFileReadMode.Close()
		return nil, fmt.Errorf("beachdb: error reading newly flushed SSTable: %w", err)
	}

	return sstReader, nil
}

// Helper function for discovering SSTable files on disk
func discoverSSTables(dir string) ([]string, uint64, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("beachdb: reading directory: %w", err)
	}

	type sstableMeta struct {
		id   uint64
		name string
	}

	sstableFiles := make([]sstableMeta, 0, len(dirEntries))
	seenIDs := make(map[uint64]string, len(dirEntries))

	for _, entry := range dirEntries {
		if !entry.Type().IsRegular() {
			continue
		}

		fileName := entry.Name()
		if filepath.Ext(fileName) != sstableFileExt {
			continue
		}

		strID := strings.TrimSuffix(fileName, filepath.Ext(fileName))
		parsedID, err := strconv.ParseUint(strID, 10, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("beachdb: parsing SSTable ID %q: %w", fileName, err)
		}
		if existingName, exists := seenIDs[parsedID]; exists {
			return nil, 0, fmt.Errorf("beachdb: duplicate SSTable ID %d in %q and %q", parsedID, existingName, fileName)
		}

		seenIDs[parsedID] = fileName
		sstableFiles = append(sstableFiles, sstableMeta{
			id:   parsedID,
			name: fileName,
		})
	}

	slices.SortFunc(sstableFiles, func(left, right sstableMeta) int {
		switch {
		case left.id < right.id:
			return -1
		case left.id > right.id:
			return 1
		default:
			return strings.Compare(left.name, right.name)
		}
	})

	names := make([]string, len(sstableFiles))
	for i, file := range sstableFiles {
		names[i] = file.name
	}

	var nextID uint64
	if len(sstableFiles) > 0 {
		nextID = sstableFiles[len(sstableFiles)-1].id + 1
	}

	return names, nextID, nil
}

// Helper function for building an SSTable file name from a
// file ID number, e.g.: 1 --> 000001.sst
func buildSSTFileName(id uint64) string {
	return fmt.Sprintf("%0*d%s", sstableFileIDWidth, id, sstableFileExt)
}
