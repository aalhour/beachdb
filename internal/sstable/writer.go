package sstable

import (
	"fmt"
	"os"
	"sync"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// blockBuilder is used to build blocks in the SST file
type blockBuilder struct {
	buf        []byte
	entryCount uint32
	firstKey   keys.InternalKey
	lastKey    keys.InternalKey
	hasEntries bool
}

// indexEntry represents a single index added to
// the index block
type indexEntry struct {
	lastKey keys.InternalKey
	offset  uint64
	size    uint32
}

// newBlockBuilder creates a new blockBuilder struct.
func newBlockBuilder() *blockBuilder {
	return &blockBuilder{
		buf: make([]byte, 0, 1024),
	}
}

// Empty returns whether the block has entries or not
func (b *blockBuilder) Empty() bool {
	return !b.hasEntries
}

// Size returns the length of the block (in bytes)
func (b *blockBuilder) Size() int {
	return len(b.buf)
}

// EntryCount returns the number of entries in the interal buffer
func (b *blockBuilder) EntryCount() uint32 {
	return b.entryCount
}

// FirstKey returns the first key added
func (b *blockBuilder) FirstKey() keys.InternalKey {
	return b.firstKey
}

// LastKey returns the last key added
func (b *blockBuilder) LastKey() keys.InternalKey {
	return b.lastKey
}

// Add adds a key and value pair (encoded) to the internal buffer
func (b *blockBuilder) Add(key keys.InternalKey, value []byte) {
	// Entry encoding inside a block
	// [internal_key_len:4][internal_key_bytes][value_len:4][value_bytes]

	encoded := key.Encode()

	// Write directly into b.buf using a stack-allocated length prefix buffer
	// to avoid an intermediate heap allocation per entry.
	var lenBuf [4]byte
	coding.PutUint32(lenBuf[:], uint32(len(encoded))) //nolint:gosec // key length fits in uint32
	b.buf = append(b.buf, lenBuf[:]...)
	b.buf = append(b.buf, encoded...)
	coding.PutUint32(lenBuf[:], uint32(len(value))) //nolint:gosec // value length fits in uint32
	b.buf = append(b.buf, lenBuf[:]...)
	b.buf = append(b.buf, value...)

	// Update metadata
	if !b.hasEntries {
		b.firstKey = key
		b.hasEntries = true
	}
	b.lastKey = key
	b.entryCount++
}

// Finish calculates checksum of internal buffer, appends it at the end of
// buffer and returns the final byte array
func (b *blockBuilder) Finish() []byte {
	// First calculate the checksum (without the checksum placeholder)
	crc32 := checksum.CRC32C(b.buf)

	// Expand the buffer by 4 bytes in order to write the checksum correctly
	b.buf = append(b.buf, 0, 0, 0, 0)

	// Write the checksum and return it
	coding.PutUint32(b.buf[len(b.buf)-4:], crc32)
	return b.buf
}

// Reset clears the buffer and resets metadata for reuse.
// The backing array is retained to avoid a re-allocation on the next block.
func (b *blockBuilder) Reset() {
	b.entryCount = 0
	b.hasEntries = false
	b.buf = b.buf[:0]
	b.firstKey = keys.InternalKey{}
	b.lastKey = keys.InternalKey{}
}

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
		indexBlock := w.buildIndexBlock()
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

// buildIndexBlock encodes all index entries and appends a CRC32C trailer.
func (w *Writer) buildIndexBlock() []byte {
	// Encode each index entry: [lastKeyLen:4][lastKeyBytes][blockOffset:8][blockSize:4]

	// Encode all keys once to avoid double-encoding during size computation and writing.
	encodedKeys := make([][]byte, len(w.indexEntries))
	totalSize := int(checksumSize)
	for i, entry := range w.indexEntries {
		encodedKeys[i] = entry.lastKey.Encode()
		totalSize += 4 + len(encodedKeys[i]) + 8 + 4
	}

	// Write directly into buf using a stack-allocated tmp buffer for fixed-width fields,
	// avoiding a per-entry intermediate heap allocation.
	buf := make([]byte, 0, totalSize)
	var tmp [8]byte
	for i, entry := range w.indexEntries {
		coding.PutUint32(tmp[:4], uint32(len(encodedKeys[i]))) //nolint:gosec // key length fits in uint32
		buf = append(buf, tmp[:4]...)
		buf = append(buf, encodedKeys[i]...)
		coding.PutUint64(tmp[:], entry.offset)
		buf = append(buf, tmp[:]...)
		coding.PutUint32(tmp[:4], entry.size)
		buf = append(buf, tmp[:4]...)
	}

	// Append CRC32C trailer
	crc := checksum.CRC32C(buf)
	coding.PutUint32(tmp[:4], crc)
	buf = append(buf, tmp[:4]...)

	return buf
}

// encodedEntrySize returns the exact on-disk size of one entry in a data block.
// Format: [keyLen:4][encodedKey][valueLen:4][value]
// encodedKey = userKey + seqno(8) + kind(1), so its length is len(userKey) + 9.
func encodedEntrySize(key keys.InternalKey, value []byte) int {
	return 4 + len(key.UserKey) + 9 + 4 + len(value)
}
