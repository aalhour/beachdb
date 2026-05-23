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

type VersionEdit struct {
	AddedFiles   []FileMetadata
	DeletedFiles []struct {
		Level  int
		FileId uint64
	}
	HasNextFileID bool
	NextFileID    uint64 // Next SSTable file number
	HasLastSeqNo  bool
	LastSeqNo     uint64 // DB's seqno
	HasLogNo      bool
	LogNo         uint64 // WAL file's log number
}

func (e *VersionEdit) Encode() []byte {
	return nil
}

func DecodeVersionEdit(data []byte) (*VersionEdit, error) {
	return &VersionEdit{}, nil
}
