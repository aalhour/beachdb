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
// # Writer
//
// [Writer] accepts sorted key-value entries and produces an SSTable file.
// It manages block rotation (flushing a block when it exceeds the target
// size), builds the index, and writes the footer. The caller is responsible
// for supplying entries in internal-key order.
//
// # Reader
//
// [Reader] opens an existing SSTable, validates its footer and index, and
// supports point lookups via [Reader.Get] and forward scans via [Iterator].
// Data blocks are loaded lazily on demand; the index is loaded eagerly at
// open time. The reader is safe for concurrent use by multiple goroutines.
//
// # Iterator
//
// [Iterator] walks all entries in internal-key order across data blocks.
// It does not collapse versions of the same user key or filter tombstones;
// higher layers handle snapshot filtering and merge semantics.
//
// # Current simplifications
//
// The following features are intentionally omitted from the current format
// and will be added in future versions:
//
//   - No compression (per-block compression)
//   - No bloom filters (per-SSTable key filter)
//   - No manifest integration (SSTable lifecycle tracking)
//   - No compaction (merging old SSTables into new ones)
//   - No block cache (caching frequently-read blocks in memory)
//   - No prefix compression or restart arrays
//
// See docs/formats/sstable.md for the full format specification.
package sstable
