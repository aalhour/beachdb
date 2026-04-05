package sstable

// index.go — Index block encoding and decoding.
//
// The index block contains one entry per data block, mapping each block's
// last internal key to its file offset and size:
//
//	[last_internal_key_len:4][last_internal_key_bytes][block_offset:8][block_size:4]
//
// The full index block is checksummed like a data block:
//
//	[index entry 0][index entry 1]...[index entry N][checksum:4]
//
// See docs/formats/sstable.md for the full format specification.

import (
	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// indexEntry represents a single index added to
// the index block
type indexEntry struct {
	lastKey keys.InternalKey
	offset  uint64
	size    uint32
}

// buildIndexBlock encodes all index entries and appends a CRC32C trailer.
func buildIndexBlock(entries []indexEntry) []byte {
	// Encode each index entry: [lastKeyLen:4][lastKeyBytes][blockOffset:8][blockSize:4]

	// Encode all keys once to avoid double-encoding during size computation and writing.
	encodedKeys := make([][]byte, len(entries))
	totalSize := int(checksumSize)
	for i, entry := range entries {
		encodedKeys[i] = entry.lastKey.Encode()
		totalSize += 4 + len(encodedKeys[i]) + 8 + 4
	}

	// Write directly into buf using a stack-allocated tmp buffer for fixed-width fields,
	// avoiding a per-entry intermediate heap allocation.
	buf := make([]byte, 0, totalSize)
	var tmp [8]byte
	for i, entry := range entries {
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

// decodeIndexBlock takes an index block binary data payload
// verifies the checksum at the end and then decodes it
// according the wire format into an array if indexEntries
func decodeIndexBlock(payload []byte) ([]indexEntry, error) {
	// Validate the CRC32 checksum
	n := len(payload)
	m := int(checksumSize)

	if n < m {
		return nil, ErrCorruptIndex
	}

	expectedCRC32 := coding.Uint32(payload[n-m:])
	calculatedCRC32 := checksum.CRC32C(payload[:n-m])
	if calculatedCRC32 != expectedCRC32 {
		return nil, ErrIndexChecksumMismatch
	}

	var indexEntries []indexEntry
	br := coding.NewByteReader(payload[:n-m])

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
