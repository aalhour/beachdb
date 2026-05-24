package manifest

import "github.com/aalhour/beachdb/internal/keys"

// FileMetadata represents metadata about an SSTable file.
type FileMetadata struct {
	Level       int
	FileID      uint64
	Size        uint64
	SmallestKey keys.InternalKey
	LargestKey  keys.InternalKey
}

// VersionEdit represents one atomic change to the database's file set.
type VersionEdit struct {
	AddedFiles   []FileMetadata
	DeletedFiles []struct {
		Level  int
		FileID uint64
	}
	HasNextFileID bool
	NextFileID    uint64 // Next SSTable file number
	HasLastSeqNo  bool
	LastSeqNo     uint64 // DB's seqno
	HasLogNo      bool
	LogNo         uint64 // WAL file's log number
}

// Encode serializes the batch operations to a byte slice.
// The encoding is deterministic: the same batch always produces the same bytes.
func (e *VersionEdit) Encode() []byte {
	return nil
}

// DecodeVersionEdit decodes a byte slice into a VersionEdit.
// Returns an error if the data is malformed, truncated, or contains invalid operations.
func DecodeVersionEdit(_ []byte) (*VersionEdit, error) {
	return &VersionEdit{}, nil
}
