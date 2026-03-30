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
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// blockEntry defines a convenience struct for holding
// a decoded entry from a block read from disk
type blockEntry struct {
	key   keys.InternalKey
	value []byte
}

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

	// Validate the CRC32 checksum
	n := len(indexPayload)
	expectedCRC32 := coding.Uint32(indexPayload[n-4:])
	calculatedCRC32 := checksum.CRC32C(indexPayload[:n-4])
	if calculatedCRC32 != expectedCRC32 {
		return nil, ErrIndexChecksumMismatch
	}

	var indexEntries []indexEntry
	br := coding.NewByteReader(indexPayload[:n-4])

	for br.Remaining() > 0 {
		keyLen, err := br.ReadUint32()
		if err != nil {
			return nil, ErrCorruptIndex
		}

		keyBytes, err := br.ReadBytes(int(keyLen))
		if err != nil {
			return nil, ErrCorruptIndex
		}

		key, err := keys.DecodeInternalKey(keyBytes)
		if err != nil {
			return nil, ErrCorruptIndex
		}

		offset, err := br.ReadUint64()
		if err != nil {
			return nil, ErrCorruptIndex
		}

		size, err := br.ReadUint32()
		if err != nil {
			return nil, ErrCorruptIndex
		}

		indexEntries = append(indexEntries, indexEntry{
			lastKey: key,
			offset:  offset,
			size:    size,
		})
	}

	return indexEntries, nil
}

// verifyBlockChecksum takes a block's raw bytes, verifies its
// checksum and returns only the block's data bytes (minus the checksum)
func verifyBlockChecksum(raw []byte) ([]byte, error) {
	n := len(raw)

	// Validate length
	if n < int(checksumSize) {
		return nil, ErrCorruptBlock
	}

	// Validate checksum
	payload := raw[:n-int(checksumSize)]
	storedChecksum := coding.Uint32(raw[n-int(checksumSize):])
	calculatedChecksum := checksum.CRC32C(payload)
	if storedChecksum != calculatedChecksum {
		return nil, ErrBlockChecksumMismatch
	}

	return payload, nil
}

// decodeBlockEntries takes a block byte payload and decodes each
// key-value entry in it, returning a list of block entries or error
func decodeBlockEntries(payload []byte) ([]blockEntry, error) {
	br := coding.NewByteReader(payload)
	var entries []blockEntry

	for br.Remaining() > 0 {
		// Decode the bytes a segment at a time
		keyLen, err := br.ReadUint32()
		if err != nil {
			return nil, ErrCorruptBlock
		}

		keyBytes, err := br.ReadBytes(int(keyLen))
		if err != nil {
			return nil, ErrCorruptBlock
		}

		key, err := keys.DecodeInternalKey(keyBytes)
		if err != nil {
			return nil, ErrCorruptBlock
		}

		valueLen, err := br.ReadUint32()
		if err != nil {
			return nil, ErrCorruptBlock
		}

		valueBytes, err := br.ReadBytes(int(valueLen))
		if err != nil {
			return nil, ErrCorruptBlock
		}

		entries = append(entries, blockEntry{
			key:   key,
			value: valueBytes,
		})
	}

	return entries, nil
}

// scanBlock scans decoded entries for the newest visible version of userKey.
// Returns (value, true, nil) on a visible put, (nil, true, ErrKeyNotFound)
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
				return nil, true, ErrKeyNotFound
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
