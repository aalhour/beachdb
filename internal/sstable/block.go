package sstable

// block.go — Data block encoding, decoding and building.
//
// A data block stores a sequence of sorted key-value entries followed by a
// CRC32C checksum trailer:
//
//	[entry 0][entry 1]...[entry N][checksum:4]
//
// Each entry is encoded as:
//
//	[internal_key_len:4][internal_key_bytes][value_len:4][value_bytes]
//
// See docs/formats/sstable.md for the full format specification.

import (
	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// blockEntry defines a convenience struct for holding
// a decoded entry from a block that was read from disk
type blockEntry struct {
	key   keys.InternalKey
	value []byte
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

// blockBuilder is used to build blocks in the SST file
type blockBuilder struct {
	buf        []byte
	entryCount uint32
	firstKey   keys.InternalKey
	lastKey    keys.InternalKey
	hasEntries bool
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

// encodedEntrySize returns the exact on-disk size of one entry in a data block.
// Format: [keyLen:4][encodedKey][valueLen:4][value]
// encodedKey = userKey + seqno(8) + kind(1), so its length is len(userKey) + 9.
func encodedEntrySize(key keys.InternalKey, value []byte) int {
	return 4 + len(key.UserKey) + 9 + 4 + len(value)
}
