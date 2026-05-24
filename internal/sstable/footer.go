package sstable

// footer.go — Footer encoding and decoding.
//
// The footer is a fixed 40-byte record at EOF that bootstraps the reader:
//
//	[magic:8][version:4][index_offset:8][index_size:4][data_block_count:4][entry_count:8][checksum:4]
//
// See docs/formats/sstable.md for the full format specification.

import (
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

const (
	sstMagic   string = "BEACHSST"
	sstVersion uint32 = 1

	// Field sizes (in bytes).
	sstMagicSize          uint32 = 8
	sstVersionSize        uint32 = 4
	sstIndexOffsetSize    uint32 = 8
	sstIndexSizeSize      uint32 = 4
	sstDataBlockCountSize uint32 = 4
	sstEntryCountSize     uint32 = 8
	checksumSize          uint32 = 4

	// Field offsets within the footer.
	sstMagicOffset          uint32 = 0
	sstVersionOffset        uint32 = sstMagicOffset + sstMagicSize
	sstIndexOffsetOffset    uint32 = sstVersionOffset + sstVersionSize
	sstIndexSizeOffset      uint32 = sstIndexOffsetOffset + sstIndexOffsetSize
	sstDataBlockCountOffset uint32 = sstIndexSizeOffset + sstIndexSizeSize
	sstEntryCountOffset     uint32 = sstDataBlockCountOffset + sstDataBlockCountSize
	sstChecksumOffset       uint32 = sstEntryCountOffset + sstEntryCountSize

	// sstChecksumInputSize is the number of leading bytes covered by the footer checksum.
	sstChecksumInputSize uint32 = sstChecksumOffset

	// footerSize is the on-disk size of the footer in bytes.
	footerSize uint32 = sstChecksumOffset + checksumSize
)

// footer defines the values inside the footer block
type footer struct {
	indexOffset    uint64 // Where does the index block starts
	indexSize      uint32 // Size of the index block
	dataBlockCount uint32 // How many data blocks in this file
	entryCount     uint64 // How many entries are in this file
}

// newFooter constructs a footer from the given field values.
func newFooter(indexOffset uint64, indexSize uint32, dataBlockCount uint32, entryCount uint64) *footer {
	return &footer{
		indexOffset:    indexOffset,
		indexSize:      indexSize,
		dataBlockCount: dataBlockCount,
		entryCount:     entryCount,
	}
}

// encode encodes a footer struct, computes its checksum and
// returns a final byte array with all parts.
func (f *footer) encode() []byte {
	buf := make([]byte, footerSize)

	copy(buf[sstMagicOffset:sstMagicOffset+sstMagicSize], []byte(sstMagic))
	coding.PutUint32(buf[sstVersionOffset:sstVersionOffset+sstVersionSize], sstVersion)
	coding.PutUint64(buf[sstIndexOffsetOffset:sstIndexOffsetOffset+sstIndexOffsetSize], f.indexOffset)
	coding.PutUint32(buf[sstIndexSizeOffset:sstIndexSizeOffset+sstIndexSizeSize], f.indexSize)
	coding.PutUint32(buf[sstDataBlockCountOffset:sstDataBlockCountOffset+sstDataBlockCountSize], f.dataBlockCount)
	coding.PutUint64(buf[sstEntryCountOffset:sstEntryCountOffset+sstEntryCountSize], f.entryCount)

	crc32 := checksum.CRC32C(buf[:sstChecksumInputSize])
	coding.PutUint32(buf[sstChecksumOffset:sstChecksumOffset+checksumSize], crc32)

	return buf
}

// decodeFooter takes raw footer data bytes and decodes them into
// a footer struct, verifying magic, version, and checksum.
func decodeFooter(data []byte) (footer, error) {
	if len(data) != int(footerSize) {
		return footer{}, ErrCorruptFooter
	}

	if string(data[sstMagicOffset:sstMagicOffset+sstMagicSize]) != sstMagic {
		return footer{}, ErrBadMagic
	}

	version := coding.Uint32(data[sstVersionOffset : sstVersionOffset+sstVersionSize])
	if version != sstVersion {
		return footer{}, ErrUnsupportedVersion
	}

	stored := coding.Uint32(data[sstChecksumOffset : sstChecksumOffset+checksumSize])
	computed := checksum.CRC32C(data[:sstChecksumInputSize])
	if stored != computed {
		return footer{}, ErrCorruptFooter
	}

	f := footer{
		indexOffset:    coding.Uint64(data[sstIndexOffsetOffset : sstIndexOffsetOffset+sstIndexOffsetSize]),
		indexSize:      coding.Uint32(data[sstIndexSizeOffset : sstIndexSizeOffset+sstIndexSizeSize]),
		dataBlockCount: coding.Uint32(data[sstDataBlockCountOffset : sstDataBlockCountOffset+sstDataBlockCountSize]),
		entryCount:     coding.Uint64(data[sstEntryCountOffset : sstEntryCountOffset+sstEntryCountSize]),
	}

	return f, nil
}
