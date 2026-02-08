// Package checksum provides CRC32C checksum utilities for data integrity verification.
package checksum

import (
	"hash/crc32"
)

var crc32Table = crc32.MakeTable(crc32.Castagnoli)

// CRC32C returns the CRC-32 checksum of data
func CRC32C(data []byte) uint32 {
	return crc32.Checksum(data, crc32Table)
}
