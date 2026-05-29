package sstable

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"sync"

	"github.com/aalhour/beachdb/internal/keys"
)

// Reader defines the SSTable reader struct.
type Reader struct {
	mu       sync.RWMutex
	file     *os.File
	fileSize int64
	footer   footer
	index    []indexEntry
}

// OpenReader reads an SSTable file and creates a Reader struct with
// the footer and index entries read, decoded and checksum-verified
func OpenReader(file *os.File) (*Reader, error) {
	// Validate file ptr and size
	if file == nil {
		return nil, ErrNilFile
	}

	info, err := file.Stat()
	if err != nil {
		return nil, ErrReadingFile
	}

	fileSize := info.Size()
	if fileSize < int64(footerSize) {
		return nil, ErrFileTooSmall
	}

	// Read the footer
	decodedFooter, err := readFooter(file)
	if err != nil {
		return nil, err
	}

	// Read index entries from footer's indexOffset
	indexEntries, err := readIndexEntries(file, decodedFooter)
	if err != nil {
		return nil, err
	}

	// Construct and return the reader
	reader := &Reader{
		file:     file,
		fileSize: fileSize,
		footer:   decodedFooter,
		index:    indexEntries,
	}

	return reader, nil
}

// Close closes the Reader struct
func (r *Reader) Close() error {
	if r == nil {
		return ErrReaderClosed
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		return ErrReaderClosed
	}

	// Close the file and mark reader as closed
	err := r.file.Close()
	r.file = nil

	if err != nil {
		return ErrReaderClosingFile
	}

	return nil
}

// FileSize returns the SSTable file size in bytes. It returns int64 to
// match the os file-size API (os.FileInfo.Size).
func (r *Reader) FileSize() int64 {
	return r.fileSize
}

// EntryCount returns the total number of key-value entries in the SSTable.
func (r *Reader) EntryCount() uint64 {
	return r.footer.entryCount
}

// DataBlockCount returns the nimber of data blocks in the SSTable.
func (r *Reader) DataBlockCount() uint32 {
	return r.footer.dataBlockCount
}

// IndexOffset returns the size of the index block in bytes.
func (r *Reader) IndexOffset() uint64 {
	return r.footer.indexOffset
}

// IndexSize returns the size of the index block in bytes.
func (r *Reader) IndexSize() uint32 {
	return r.footer.indexSize
}

// BlockInfo returns the last key, file offset, and size for data block i.
// Panics if i is out of range.
func (r *Reader) BlockInfo(i int) (lastKey keys.InternalKey, offset uint64, size uint32) {
	entry := r.index[i]
	return entry.lastKey, entry.offset, entry.size
}

// Get scans the SSTable for the given userkey + seqno and returns its
// value, if found; otherwise, error.
func (r *Reader) Get(userKey []byte, seqno uint64) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := len(r.index)
	if n == 0 {
		return nil, ErrKeyNotFound
	}

	// Binary search for the index block containing the key
	blockIndex := r.seekBlock(userKey)
	if blockIndex == n {
		return nil, ErrKeyNotFound
	}

	// Scan candidate blocks for the newest visible version of userKey.
	// A key may span adjacent blocks, so if we find the user key but
	// no version with seqno <= requested, continue to the next block.
	for blockIndex < n {
		index := r.index[blockIndex]
		blockData, err := r.readBlock(index.offset, index.size)
		if err != nil {
			return nil, err
		}

		entries, err := decodeBlockEntries(blockData)
		if err != nil {
			return nil, err
		}

		val, found, err := scanBlock(entries, userKey, seqno)
		if !found {
			return nil, ErrKeyNotFound
		}
		if err != nil || val != nil {
			return val, err
		}
		blockIndex++
	}

	return nil, ErrKeyNotFound
}

// seekBlock binary searches the index to find the first block
// whose lastKey >= the synthetic max key for userKey.
func (r *Reader) seekBlock(userKey []byte) int {
	// Construct the biggest internal key (max seqno) for
	// this user key
	synthetic := maxLookupKey(userKey)

	// Binary search the index array to get the index of the block
	// possibly containing the key
	return sort.Search(len(r.index), func(i int) bool {
		return r.index[i].lastKey.Compare(synthetic) >= 0
	})
}

// readBlock reads a data block at the given offset and size,
// verifies its checksum, and returns the payload bytes.
func (r *Reader) readBlock(offset uint64, size uint32) ([]byte, error) {
	// Use file.ReadAt which maps to `pread(2) on Unix, the operation
	// doesn't mutate the file's single shared seek position and is
	// goroutine safe
	blockData := make([]byte, size)
	n, err := r.file.ReadAt(blockData, int64(offset)) //nolint:gosec // G115: offset won't overflow int64

	// Verify err and number of bytes read
	if n != int(size) || err != nil {
		return nil, ErrCorruptBlock
	}

	blockPayload, err := verifyBlockChecksum(blockData)
	if err != nil {
		return nil, err
	}

	return blockPayload, nil
}

// loadBlockEntries loads the entries from a block at a given
// index returns its entries decoded
func (r *Reader) loadBlockEntries(blockIdx int) ([]blockEntry, error) {
	if blockIdx < 0 || blockIdx >= len(r.index) {
		return nil, fmt.Errorf("beachdb/sstable: invalid blockIdx %d", blockIdx)
	}

	index := r.index[blockIdx]
	data, err := r.readBlock(index.offset, index.size)
	if err != nil {
		return nil, err
	}

	entries, err := decodeBlockEntries(data)
	if err != nil {
		return nil, err
	}

	return entries, nil
}

// readFooter takes a file and returns a footer struct or error
// this function assumes that the file pointer was validated
func readFooter(file *os.File) (footer, error) {
	// Get file size to calculate footer offset
	fileInfo, err := file.Stat()
	if err != nil {
		return footer{}, fmt.Errorf("beachdb/sstable: %w", err)
	}
	fileSize := fileInfo.Size()
	offset := fileSize - int64(footerSize)

	// Use file.ReadAt which maps to `pread(2) on Unix, the operation
	// doesn't mutate the file's single shared seek position and is
	// goroutine safe
	footerPayload := make([]byte, footerSize)
	numBytesRead, err := file.ReadAt(footerPayload, offset)

	// Verify number of read bytes and err
	if numBytesRead != int(footerSize) || err != nil {
		return footer{}, ErrCorruptFooter
	}

	// Decode the byte buffer
	decodedFooter, err := decodeFooter(footerPayload)
	if err != nil {
		return footer{}, err
	}

	return decodedFooter, nil
}

// readIndexEntries takes a file and a decoded footer struct and reads the
// the full index bytes, returning an indexEntries slice or error
// this function assumes that the file pointer was validated
func readIndexEntries(file *os.File, decodedFooter footer) ([]indexEntry, error) {
	// Use file.ReadAt which maps to `pread(2) on Unix, the operation
	// doesn't mutate the file's single shared seek position and is
	// goroutine safe
	offset := int64(decodedFooter.indexOffset) //nolint:gosec // G115: offset won't overflow int64
	indexPayload := make([]byte, decodedFooter.indexSize)
	numBytesRead, err := file.ReadAt(indexPayload, offset)

	// Verify number of read bytes and err
	if numBytesRead != int(decodedFooter.indexSize) || err != nil {
		return nil, ErrCorruptIndex
	}

	return decodeIndexBlock(indexPayload)
}

// scanBlock scans decoded entries for the newest visible version of userKey.
// Returns (value, true, nil) on a visible put, (nil, true, ErrKeyDeleted)
// on a visible tombstone, (nil, true, nil) if the key is present but no
// version is visible yet, and (nil, false, nil) if the key is absent.
func scanBlock(entries []blockEntry, userKey []byte, seqno uint64) ([]byte, bool, error) {
	found := false
	for _, entry := range entries {
		if !bytes.Equal(entry.key.UserKey, userKey) {
			continue
		}
		found = true
		if entry.key.Seqno <= seqno {
			if entry.key.Kind == keys.InternalKeyKindDelete {
				return nil, true, ErrKeyDeleted
			}
			return slices.Clone(entry.value), true, nil
		}
	}
	return nil, found, nil
}

// maxLookupKey constructs a synthetic internal key with the maximum
// seqno so that it sorts before all real entries for the same user key.
func maxLookupKey(userKey []byte) keys.InternalKey {
	return keys.InternalKey{
		UserKey: userKey,
		Seqno:   math.MaxUint64,
		Kind:    keys.InternalKeyKindPut,
	}
}
