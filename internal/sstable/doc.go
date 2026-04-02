// Package sstable implements the SSTable (Sorted String Table) format for
// BeachDB's LSM storage engine.
//
// An SSTable is an immutable, sorted key-value file written once during a
// memtable flush and read many times for point lookups and forward scans.
//
// # File layout
//
//	[data block 0][data block 1]...[data block N][index block][footer]
//
// The reader bootstraps from a fixed-size footer at EOF, loads the index
// block into memory, and lazily reads data blocks as needed.
//
// All multi-byte integers are big-endian. Data blocks and the index block
// carry a CRC32C checksum trailer. The footer has its own CRC32C checksum.
//
// See docs/formats/sstable.md for the full format specification.
package sstable
