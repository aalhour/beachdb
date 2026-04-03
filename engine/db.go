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

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/memtable"
	"github.com/aalhour/beachdb/internal/sstable"
	"github.com/aalhour/beachdb/internal/wal"
)

const (
	// walFileName specifies the name of the WAL file.
	walFileName = "beachdb.wal"

	// sstableFileExt specifies the extension of SSTable files.
	sstableFileExt = ".sst"
)

// DB defines the database struct wrapping the public APIs.
type DB struct {
	closed      bool              // Flag indicating whether db is closed or not
	mu          sync.RWMutex      // Synchronization for safe concurrency.
	dir         string            // Path on disk to write data into.
	wal         *wal.Writer       // Writer for WAL file.
	mem         memtable.Memtable // Memory table data structure
	seqno       uint64            // Monotonic sequence counter
	ssts        []*sstable.Reader // Open SSTable readers, newest-first
	nextSSTID   uint64            // Counter for SST file naming (new files)
	syncOnWrite bool              // Option: Whether the DB should fsync writes or not.
}

// Open initializes a DB struct and replays the WAL, if present.
func Open(dir string, opts ...Option) (*DB, error) {
	// Process configuration options
	cfg := applyOptions(opts)

	// Create directory for the database and fsync created directory entries.
	if err := mkdirAllAndSync(dir); err != nil {
		return nil, err
	}

	// Initialize the DB struct
	db := &DB{
		closed:      false,
		dir:         dir,
		mem:         memtable.NewSkipList(),
		seqno:       0,
		syncOnWrite: cfg.syncOnWrite,
	}

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
			_ = db.Close()
			return nil, fmt.Errorf("beachdb: reading SSTable %s: %w", fileName, err)
		}
		db.ssts = append(db.ssts, sstReader)
	}
	db.nextSSTID = nextSSTID

	return db, nil
}

func (db *DB) Write(ctx context.Context, b *Batch) error {
	// Acquire write lock
	db.mu.Lock()
	defer db.mu.Unlock()

	// Check if the database was closed
	if db.closed {
		return ErrDBClosed
	}

	// Check if already canceled before doing any work
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("beachdb: Write call canceled: %w", err)
	}

	// Encode the Batch and append it to the WAL
	if b == nil {
		return nil
	}
	encoded := b.Encode()

	err := db.wal.Append(encoded)
	if err != nil {
		return fmt.Errorf("beachdb: appending to WAL: %w", err)
	}

	// Check whether we should fsync the WAL on write or not
	if db.syncOnWrite {
		// fsync can be slow - check context before the call
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("beachdb: Write call canceled: %w", err)
		}

		err = db.wal.Sync()
		if err != nil {
			return fmt.Errorf("beachdb: syncing WAL: %w", err)
		}
	}

	db.applyBatch(b)

	return nil
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

	// Search the memtable
	value, found := db.mem.Get(key, db.seqno)

	// Check if the key was found in the membtable
	if found {
		if value == nil {
			// Tombestone (latest update in memtable was a delete operation)
			return nil, ErrKeyNotFound
		}
		return value, nil
	}

	// Otherwise, search for key in SSTables in reverse order, because
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

// Close closes the database and frees allocated resources.
func (db *DB) Close() error {
	// Check nil receiver
	if db == nil {
		return ErrDBClosed
	}

	// Acquire lock to modify private state
	db.mu.Lock()
	defer db.mu.Unlock()

	// Check if the database was closed
	if db.closed {
		return ErrDBClosed
	}

	var firstError error

	// Close all SSTable readers before closing the WAL writer
	for _, reader := range db.ssts {
		err := reader.Close()
		if err != nil && firstError == nil {
			firstError = err
		}
	}

	// Close the WAL writer
	err := db.wal.Close()
	if err != nil && firstError == nil {
		firstError = err
	}

	// Mark it as closed
	db.ssts = nil
	db.wal = nil
	db.mem = nil
	db.closed = true

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

	for _, op := range b.ops {
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

// replayWAL reads a WAL file, if it exists, and applies it to db.mem
func replayWAL(db *DB, walFilePath string) error {
	_, err := os.Stat(walFilePath)
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
	for {
		payload, err := reader.Next()
		if errors.Is(err, io.EOF) {
			// Clean end
			// TODO: Log it as info
			break
		} else if errors.Is(err, wal.ErrTruncated) {
			// Incomplete write before crash, skip it
			// TODO: Log it as info
			break
		} else if err != nil {
			// IO errors, we should log them as warn
			// TODO: Log as warn
			break
		}

		// Decode the payload a Batch and replay it
		batch, err := DecodeBatch(payload)
		if err != nil {
			// Corrupted batch - skip it
			continue
		}
		db.applyBatch(batch)
	}

	return nil
}

// flushMemtable writes the contents in db.mem to a new SST file and replaces the
// memtable with a new one
func (db *DB) flushMemtable() error {
	// -----------------------------------
	// Phase 1: swap mutable under a lock
	// -----------------------------------
	db.mu.Lock()

	// Check if db was closed
	if db.closed {
		db.mu.Unlock()
		return ErrDBClosed
	}

	// Grab a pointer to the previous db.mem
	immutableMem := db.mem
	// Replace the memtable with a new one
	db.mem = memtable.NewSkipList()
	// Get path of next sstable
	sstPath := db.nextSSTPath()
	// Increment next SSTID (for future flushes)
	db.nextSSTID++
	db.mu.Unlock() // release!

	// ----------------------------------------------
	// Phase 2: operate over (old) immutable memtable
	// 			doesn't require locking
	// ----------------------------------------------
	// Create the new sstable file
	sstFile, err := os.Create(sstPath) //nolint:gosec // path constructed from trusted db.dir + formatted ID
	if err != nil {
		return ErrCreatingSSTFile
	}
	defer sstFile.Close()

	// Create the sstable writer
	writer, err := sstable.NewWriter(sstFile, sstable.WithSync(true))
	if err != nil {
		_ = os.Remove(sstPath)
		return fmt.Errorf("beachdb: creating SSTable writer: %w", err)
	}

	// Iterate the immutable memtable and write entries to the SSTable
	iter := immutableMem.NewIterator()
	iter.SeekToFirst()
	for iter.Valid() {
		if err := writer.Add(iter.Key(), iter.Value()); err != nil {
			_ = writer.Close()
			_ = iter.Close()
			_ = os.Remove(sstPath)
			return fmt.Errorf("beachdb: writing entry to SSTable: %w", err)
		}
		iter.Next()
	}

	_ = iter.Close()
	if err = writer.Close(); err != nil {
		_ = os.Remove(sstPath)
		return fmt.Errorf("beachdb: closing SSTable writer: %w", err)
	}

	// Sync parent directory so the new file's directory entry is durable
	if err = syncDir(db.dir); err != nil {
		return fmt.Errorf("beachdb: syncing directory after flush: %w", err)
	}

	// Re-open the file for reading
	sstFileReadMode, err := os.Open(sstPath) //nolint:gosec // path constructed from trusted db.dir + formatted ID
	if err != nil {
		return fmt.Errorf("beachdb: opening SSTable for reading: %w", err)
	}
	sstReader, err := sstable.OpenReader(sstFileReadMode)
	if err != nil {
		return fmt.Errorf("beachdb: reading flushed SSTable: %w", err)
	}

	// -------------------------------------------------------
	// Phase 3 — Publish the new sstable reader (under a lock)
	// -------------------------------------------------------
	db.mu.Lock()
	db.ssts = append(db.ssts, sstReader)
	db.mu.Unlock()

	// Synccess! :>
	return nil
}

// nextSSTPath returns a full path for the SSTable file from
// the internal nextSSTID
func (db *DB) nextSSTPath() string {
	return filepath.Join(db.dir, buildSSTFileName(db.nextSSTID))
}

// Helper function for discovering SSTable files on disk
func discoverSSTables(dir string) ([]string, uint64, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("beachdb: reading directory: %w", err)
	}

	sstableFiles := make([]string, 0, len(dirEntries))

	for _, entry := range dirEntries {
		if !entry.Type().IsRegular() {
			continue
		}

		fileName := entry.Name()
		if filepath.Ext(fileName) != sstableFileExt {
			continue
		}

		sstableFiles = append(sstableFiles, fileName)
	}

	slices.Sort(sstableFiles)

	var maxID uint64
	n := len(sstableFiles)
	if n > 0 {
		biggestName := sstableFiles[n-1]
		strID := strings.TrimSuffix(biggestName, filepath.Ext(biggestName))

		parsedID, err := strconv.ParseUint(strID, 10, 64)
		if err != nil {
			return sstableFiles, 0, fmt.Errorf("beachdb: parsing SSTable ID %q: %w", biggestName, err)
		}

		maxID = parsedID + 1
	}

	return sstableFiles, maxID, nil
}

// Helper function for building an SSTable file name from a
// file ID number, e.g.: 1 --> 000001.sst
func buildSSTFileName(id uint64) string {
	return fmt.Sprintf("%06d.sst", id)
}
