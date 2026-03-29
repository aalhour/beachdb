package sstable

import (
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

const (
	footerSize          = 40
	sstMagic            = "BEACHSST"
	sstVersion   uint32 = 1
	checksumSize        = 4
)

// footer defines the values inside the footer block
type footer struct {
	indexOffset    uint64 // Where does the index block starts
	indexSize      uint32 // Size of the index block
	dataBlockCount uint32 // How many data blocks in this file
	entryCount     uint64 // How many entries are in this file
}

func newFooter(indexOffset uint64, indexSize uint32, dataBlockCount uint32, entryCount uint64) *footer {
	return &footer{
		indexOffset:    indexOffset,
		indexSize:      indexSize,
		dataBlockCount: dataBlockCount,
		entryCount:     entryCount,
	}
}

func (f *footer) Encode() []byte {
	// Footer format
	// [magic:8][version:4][indexOffset:8][indexSize:4][dataBlockCount:4][entryCount:8][checksum:4]

	buf := make([]byte, footerSize)

	// Write all the fields to buffer
	offset := 0
	copy(buf[offset:], []byte(sstMagic))
	offset += 8
	coding.PutUint32(buf[offset:], sstVersion)
	offset += 4
	coding.PutUint64(buf[offset:], f.indexOffset)
	offset += 8
	coding.PutUint32(buf[offset:], f.indexSize)
	offset += 4
	coding.PutUint32(buf[offset:], f.dataBlockCount)
	offset += 4
	coding.PutUint64(buf[offset:], f.entryCount)
	offset += 8

	// Calculate the checksum of buffer and append it at the end
	crc32 := checksum.CRC32C(buf[:offset])
	coding.PutUint32(buf[offset:], crc32)

	return buf
}

func decodeFooter(data []byte) (footer, error) {
	if len(data) != footerSize {
		return footer{}, ErrCorruptFooter
	}

	// Verify magic
	if string(data[0:8]) != sstMagic {
		return footer{}, ErrBadMagic
	}

	// Verify version
	version := coding.Uint32(data[8:])
	if version != sstVersion {
		return footer{}, ErrUnsupportedVersion
	}

	// Verify checksum: CRC32C of bytes [0:36] must match bytes [36:40]
	stored := coding.Uint32(data[36:])
	computed := checksum.CRC32C(data[:36])
	if stored != computed {
		return footer{}, ErrCorruptFooter
	}

	// Parse fields
	f := footer{
		indexOffset:    coding.Uint64(data[12:]),
		indexSize:      coding.Uint32(data[20:]),
		dataBlockCount: coding.Uint32(data[24:]),
		entryCount:     coding.Uint64(data[28:]),
	}

	return f, nil
}
