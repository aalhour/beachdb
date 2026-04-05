package sstable

import (
	"bytes"

	"github.com/aalhour/beachdb/internal/keys"
)

// Iterator provides forward iteration over all entries in an SSTable.
// It is a two-level iterator: the outer level walks index entries (blocks),
// the inner level walks entries within each decoded block.
//
// Callers must call SeekToFirst or Seek before reading entries.
// After each positioning call, check Valid() before calling Key or Value.
// When Valid returns false, call Err to distinguish normal exhaustion
// (nil) from an I/O or corruption error.
type Iterator struct {
	reader   *Reader
	blockIdx int
	entries  []blockEntry
	entryIdx int
	valid    bool
	err      error
}

// NewIterator returns an iterator over all entries in the SSTable.
// The iterator is initially unpositioned; call SeekToFirst or Seek first.
func (r *Reader) NewIterator() *Iterator {
	return &Iterator{
		reader: r,
	}
}

// Valid reports whether the iterator is positioned at a valid entry.
func (it *Iterator) Valid() bool {
	return it.valid
}

// Err returns the error that caused the iterator to become invalid,
// or nil if the iterator was simply exhausted.
func (it *Iterator) Err() error {
	return it.err
}

// SeekToFirst positions the iterator at the first entry in the SSTable.
// On an empty table the iterator becomes invalid with a nil error.
func (it *Iterator) SeekToFirst() {
	// This method establishes validity and cannot guard
	// on `it.valid`, but we need to make sure that the
	// iterator is not closed already
	if it.reader == nil {
		it.valid = false
		it.err = ErrIteratorClosed
		return
	}

	it.blockIdx = 0

	if len(it.reader.index) == 0 {
		it.valid = false
		it.err = nil
		return
	}

	entries, err := it.reader.loadBlockEntries(it.blockIdx)
	if err != nil {
		it.valid = false
		it.err = err
		return
	}

	it.entryIdx = 0
	it.entries = entries
	it.valid = true
	it.err = nil
}

// Seek positions the iterator at the first entry whose UserKey >= target.
// If no such entry exists the iterator becomes invalid with a nil error.
func (it *Iterator) Seek(targetUserKey []byte) {
	// This method establishes validity and cannot guard
	// on `it.valid`, but we need to make sure that the
	// iterator is not closed already
	if it.reader == nil {
		it.valid = false
		it.err = ErrIteratorClosed
		return
	}

	blockIdx := it.reader.seekBlock(targetUserKey)
	if blockIdx == len(it.reader.index) {
		it.valid = false
		it.err = nil // No block was found - exhausion is not an err
		return
	}

	n := len(it.reader.index)

	for blockIdx < n {
		entries, err := it.reader.loadBlockEntries(blockIdx)
		if err != nil {
			it.valid = false
			it.err = err
			return
		}

		for entryIdx, entry := range entries {
			if bytes.Compare(entry.key.UserKey, targetUserKey) >= 0 {
				it.blockIdx = blockIdx
				it.entries = entries
				it.entryIdx = entryIdx
				it.valid = true
				it.err = nil
				return
			}
		}

		blockIdx++
	}

	// No entry with UserKey >= target exists
	it.valid = false
	it.err = nil
}

// Next advances the iterator to the next entry.
// When the current block is exhausted, the next block is loaded lazily.
func (it *Iterator) Next() {
	if !it.valid {
		return
	}

	it.entryIdx++
	if it.entryIdx < len(it.entries) {
		return
	}

	// Current block exhausted, advance to the next one
	it.blockIdx++
	it.entryIdx = 0

	if it.blockIdx >= len(it.reader.index) {
		it.valid = false
		return
	}

	entries, err := it.reader.loadBlockEntries(it.blockIdx)
	if err != nil {
		it.valid = false
		it.err = err
		return
	}

	it.entries = entries
}

// Key returns the internal key at the current iterator position.
// Only valid when Valid() returns true.
func (it *Iterator) Key() keys.InternalKey {
	if !it.valid {
		return keys.InternalKey{}
	}
	return it.entries[it.entryIdx].key
}

// Value returns the value at the current iterator position.
// The returned slice aliases internal iterator state — the caller must
// not mutate it and must copy if it needs to retain the value beyond
// the next Next/Seek/SeekToFirst/Close call.
// Only valid when Valid() returns true.
func (it *Iterator) Value() []byte {
	if !it.valid {
		return nil
	}
	return it.entries[it.entryIdx].value
}

// Close releases the iterator's resources. After Close, all
// positioning methods become no-ops and Valid returns false.
func (it *Iterator) Close() error {
	if it == nil || it.reader == nil {
		return ErrIteratorClosed
	}

	it.reader = nil
	it.entries = nil
	it.valid = false
	it.err = ErrIteratorClosed
	return nil
}
