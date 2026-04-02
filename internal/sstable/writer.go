package sstable

import (
	"fmt"
	"os"
	"sync"

	"github.com/aalhour/beachdb/internal/keys"
)

// Writer implements the SSTable writer type
type Writer struct {
	mu             sync.Mutex
	file           *os.File
	currentBlock   *blockBuilder
	indexEntries   []indexEntry
	offset         uint64
	entryCount     uint64
	dataBlockCount uint32
	lastKey        keys.InternalKey
	hasEntries     bool
	closed         bool

	// Options
	syncOnClose     bool
	targetBlockSize int
}

// NewWriter creates a new Writer struct.
func NewWriter(file *os.File, opts ...WriterOption) (*Writer, error) {
	cfg := applyOptions(opts)

	// Validate file and options
	if file == nil {
		return nil, ErrNilFile
	}

	if cfg.blockSize <= 0 {
		return nil, ErrInvalidBlockSize
	}

	blockBuilder := newBlockBuilder()

	writer := &Writer{
		file:            file,
		currentBlock:    blockBuilder,
		syncOnClose:     cfg.syncOnClose,
		targetBlockSize: cfg.blockSize,
	}

	return writer, nil
}

// Add inserts the key-value pair to the writer's state.
func (w *Writer) Add(key keys.InternalKey, value []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrWriterClosed
	}

	// New key must be strictly larger than the last key added
	if w.hasEntries && w.lastKey.Compare(key) >= 0 {
		return ErrOutOfOrderKey
	}

	// Check if the key would overflow the current block, if and
	// only if the current block has other entries.
	// However, if the block is empty we allow a large entry that
	// would overflow the block to have its own entry block (simpler design)
	if !w.currentBlock.Empty() {
		estimatedSize := encodedEntrySize(key, value)
		currBlockSize := w.currentBlock.Size() + estimatedSize
		if currBlockSize > w.targetBlockSize {
			// Flush the current block to disk and reset the builder
			if err := w.flushCurrentBlock(); err != nil {
				return err
			}
		}
	}

	// Add the entry to the current block
	w.currentBlock.Add(key, value)

	// Update metadata
	w.lastKey = key
	w.hasEntries = true
	w.entryCount++

	return nil
}

// Close closes the writer and syncs the file based on options.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrWriterClosed
	}

	var firstErr error

	// Flush the last partial data block if it has entries
	if w.currentBlock.hasEntries {
		if err := w.flushCurrentBlock(); err != nil {
			firstErr = err
		}
	}

	// Write the index block
	if firstErr == nil {
		indexBlock := buildIndexBlock(w.indexEntries)
		indexOffset := w.offset
		indexSize := uint32(len(indexBlock)) //nolint:gosec // index block size fits in uint32

		if _, err := w.file.Write(indexBlock); err != nil {
			firstErr = err
		}

		// Write the footer
		if firstErr == nil {
			f := newFooter(indexOffset, indexSize, w.dataBlockCount, w.entryCount)
			if _, err := w.file.Write(f.encode()); err != nil {
				firstErr = err
			}
		}
	}

	// Sync if configured
	if firstErr == nil && w.syncOnClose {
		if err := w.file.Sync(); err != nil {
			firstErr = err
		}
	}

	// Always mark closed, even on error
	w.closed = true

	return firstErr
}

// flushCurrentBlock flushes the current block to disk
func (w *Writer) flushCurrentBlock() error {
	// No need to lock the function since this is an
	// internal helper for Add()
	blockBytes := w.currentBlock.Finish()

	// Write the data to the file
	// We do *NOT* call w.file.Sync() here, see `syncOnClose`
	// option's behavior in Close() function
	_, err := w.file.Write(blockBytes)
	if err != nil {
		return fmt.Errorf("beachdb/sstable: writing block: %w", err)
	}

	// Record an index entry
	newIndex := indexEntry{
		lastKey: w.currentBlock.LastKey(),
		offset:  w.offset,
		size:    uint32(len(blockBytes)), //nolint:gosec // block size fits in uint32
	}
	w.indexEntries = append(w.indexEntries, newIndex)

	// Update metadata
	w.offset += uint64(len(blockBytes))
	w.dataBlockCount++

	// Reset the current block builder
	w.currentBlock.Reset()

	return nil
}
