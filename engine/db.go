package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/memtable"
	"github.com/aalhour/beachdb/internal/wal"
)

var (
	// walFileName specifies the name of the WAL file
	walFileName = "beachdb.wal"
)

// DB defines the database struct wrapping the public APIs.
type DB struct {
	mu          sync.RWMutex      // synchronization for safe concurrency.
	dir         string            // path on disk to write data into.
	wal         *wal.Writer       // Writer for WAL file.
	mem         memtable.Memtable // Memory table data structure
	seqno       uint64            // Monotonic sequence counter
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
		return nil, err
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

	return db, nil
}

func (db *DB) Write(ctx context.Context, b *Batch) error {
	// Acquire write lock
	db.mu.Lock()
	defer db.mu.Unlock()

	// Check if already canceled before doing any work
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("beachdb: Write call canceled: %w", err)
	}

	// Check if the database was closed
	if db.wal == nil {
		return ErrDBClosed
	}

	// Encode the Batch and append it to the WAL
	if b == nil {
		return nil
	}
	encoded := b.Encode()

	err := db.wal.Append(encoded)
	if err != nil {
		return err
	}

	// Check whether we should fsync the WAL on write or not
	if db.syncOnWrite {
		// fsync can be slow - check context before the call
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("beachdb: Write call canceled: %w", err)
		}

		err = db.wal.Sync()
		if err != nil {
			return err
		}
	}

	db.applyBatch(b)

	return nil
}

// Get returns the value associated with the given key or returns an ErrKeyNotFound.
func (db *DB) Get(ctx context.Context, key []byte) (value []byte, err error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	// Check if the call was canceled
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("beachdb: Get call canceled: %w", err)
	}

	value, found := db.mem.Get(key, db.seqno)
	if !found {
		return nil, ErrKeyNotFound
	}
	return value, nil
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

	// Check if already closed or not
	if db.wal == nil {
		return ErrDBClosed
	}

	// Close the WAL writer
	err := db.wal.Close()

	// Mark it as closed
	db.wal = nil
	db.mem = nil

	return err
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
