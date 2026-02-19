package memtable

import "github.com/aalhour/beachdb/internal/keys"

// Iterator provides forward iteration over memtable entries in sorted order.
type Iterator interface {
	SeekToFirst()          // Position at first key
	Seek(target []byte)    // Position at first key >= target
	Valid() bool           // True if positioned at valid entry
	Next()                 // Advance to next key
	Key() keys.InternalKey // Current key - only valid if Valid() == true
	Value() []byte         // Current value - only valid if Valid() == true
	Close() error          // Release resources
}
