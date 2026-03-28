package sstable

import (
	"os"
	"sync"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// blockBuilder is used to build blocks in the SST file
type blockBuilder struct {
	mu         sync.RWMutex
	buf        []byte
	entryCount uint32
	firstKey   keys.InternalKey
	lastKey    keys.InternalKey
	hasEntries bool
}

// indexEntry represents a single index added to the footer of an SST
type indexEntry struct {
	lastKey keys.InternalKey
	offset  uint64
	size    uint32
}

// newBlockBuilder creates a new blockBuilder struct.
func newBlockBuilder() *blockBuilder {
	return &blockBuilder{
		buf: make([]byte, 1024),
	}
}

func (b *blockBuilder) Empty() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.hasEntries == false
}

func (b *blockBuilder) Size() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return len(b.buf)
}

func (b *blockBuilder) EntryCount() uint32 {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.entryCount
}

func (b *blockBuilder) FirstKey() keys.InternalKey {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.firstKey
}

func (b *blockBuilder) LastKey() keys.InternalKey {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.lastKey
}

func (b *blockBuilder) Add(key keys.InternalKey, value []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Entry encoding inside a block
	// [internal_key_len:4][internal_key_bytes][value_len:4][value_bytes]

	encoded := key.Encode()

	// Entry size:
	// 4 bytes: for storing the length of the key
	// + length of the key (in bytes)
	// + 4 bytes: for storing the length of the value
	// + length of the value (in bytes)
	entrySize := 4 + len(encoded) + 4 + len(value)

	// Create a buffer for this entry alone
	buf := make([]byte, entrySize)
	offset := 0
	coding.PutUint32(buf[offset:], uint32(len(encoded)))
	offset += 4
	copy(buf[offset:], encoded)
	offset += len(encoded)
	coding.PutUint32(buf[offset:], uint32(len(value)))
	offset += 4
	copy(buf[offset:], value)

	// Append the entry's buffer to the blockBuilder's buffer
	b.buf = append(b.buf, buf...)

	// Update metadata
	if !b.hasEntries {
		b.firstKey = key
		b.hasEntries = true
	}
	b.lastKey = key
	b.entryCount += 1
}

func (b *blockBuilder) Finish() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	// First calculate the checksum (without the checksum placeholder)
	crc32 := checksum.CRC32C(b.buf)

	// Expand the buffer by 4 bytes in order to write the checksum correctly
	b.buf = append(b.buf, 0, 0, 0, 0)

	// Write the checksum and return it
	coding.PutUint32(b.buf[len(b.buf)-4:], crc32)
	return b.buf
}

func (b *blockBuilder) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.entryCount = 0
	b.hasEntries = false
	b.buf = make([]byte, 1024)
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

	// New key must be strictly larger than the last seen key
	if w.lastKey.Compare(key) >= 0 {
		return ErrOutOfOrderKey
	}

	// Check if the key would overflow the current block, if and
	// only if the current block has other entries.
	// However, if the block is empty we allow a large entry that
	// would overflow the block to have its own entry block (simpler design)
	if w.currentBlock.hasEntries {
		estimatedSize := estimatedEntrySize(key, value)
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
	w.entryCount += 1

	return nil
}

// Close closes the writer and syncs the file based on options.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrWriterClosed
	}

	// Check if current block has entries and if yes, flush it!
	if w.currentBlock.hasEntries {
		if err := w.flushCurrentBlock(); err != nil {
			return err
		}
	}

	indexBlock := w.buildIndexBlock()
	w.closed = true

	return nil
}

// flushCurrentBlock flushes the current block to disk
func (w *Writer) flushCurrentBlock() error {
	// No need to lock the function since this is an
	// internal helper for Add()
	blockBytes := w.currentBlock.Finish()

	// Write the data to the file
	_, err := w.file.Write(blockBytes)
	if err != nil {
		return err
	}
	w.file.Sync()

	// Record an index entry
	newIndex := indexEntry{
		lastKey: w.currentBlock.LastKey(),
		offset:  w.offset,
		size:    uint32(len(blockBytes)),
	}
	w.indexEntries = append(w.indexEntries, newIndex)

	// Update metadata
	w.offset = uint64(len(blockBytes))
	w.dataBlockCount += 1

	// Reset the current block builder
	w.currentBlock.Reset()

	return nil
}

func (w *Writer) buildIndexBlock() []byte {
	return nil
}

// estimatedEntrySize returns an approximation of the key + value sizes in the block
func estimatedEntrySize(key keys.InternalKey, value []byte) int {
	// 4 (key len prefix) + userKey + 8 (seqno) + 1 (kind) + 4 (value len prefix) + value
	return 4 + len(key.UserKey) + 9 + 4 + len(value)
}
