package manifest

import (
	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/record"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// VersionEdit field tags for the manifest TLV encoding (v1).
//
// v1 hard-errors on unknown tags. No reserved slots: future fields
// (compaction pointers, snapshots, table catalogs, per-table tracking)
// will ship with a manifest format version bump, not a v1 tag insertion.
// See docs/formats/manifest.md for the full wire format and rationale.
const (
	tagAddFile      byte = 0x01 // [level u32][fileID u64][size u64][smallestKey uint32 len+bytes][largestKey uint32 len+bytes]
	tagDeleteFile   byte = 0x02 // [level u32][fileID u64]
	tagNextFileID   byte = 0x03 // [value u64]
	tagLastSequence byte = 0x04 // [value u64]
	tagLogNumber    byte = 0x05 // [value u64]
)

// FileMetadata represents metadata about an SSTable file referenced by the manifest.
type FileMetadata struct {
	// Level is the LSM level the file lives at.
	Level uint32

	// FileID is the unique identifier for the SSTable file (matches the on-disk filename).
	FileID uint64

	// Size is the size of the SSTable file in bytes.
	Size uint64

	// SmallestKey is the smallest internal key contained in the file (inclusive).
	SmallestKey keys.InternalKey

	// LargestKey is the largest internal key contained in the file (inclusive).
	LargestKey keys.InternalKey
}

// DeletedFile identifies an SSTable file that is being removed from the file set.
type DeletedFile struct {
	// Level is the LSM level the file lived at before deletion.
	Level uint32

	// FileID is the unique identifier of the SSTable file being deleted.
	FileID uint64
}

// VersionEdit represents one atomic change to the database's file set.
// Each field is optional: only set fields are encoded on disk, and a decoder
// rebuilds the Version by applying a sequence of edits in order.
type VersionEdit struct {
	// AddedFiles are SSTables that this edit introduces into the file set.
	AddedFiles []FileMetadata

	// DeletedFiles are SSTables that this edit removes from the file set.
	DeletedFiles []DeletedFile

	// HasNextFileID indicates whether NextFileID carries a meaningful value
	// in this edit. Required because uint64 has no sentinel for "unset".
	HasNextFileID bool

	// NextFileID is the next SSTable file number the database will allocate.
	NextFileID uint64

	// HasLastSeqNo indicates whether LastSeqNo carries a meaningful value in this edit.
	HasLastSeqNo bool

	// LastSeqNo is the highest sequence number written by the database so far.
	LastSeqNo uint64

	// HasLogNo indicates whether LogNo carries a meaningful value in this edit.
	HasLogNo bool

	// LogNo is the WAL file's log number that this edit is associated with.
	LogNo uint64
}

// Encode serializes the batch operations to a byte slice.
// The encoding is deterministic: the same batch always produces the same bytes.
func (e *VersionEdit) Encode() []byte {
	buf := make([]byte, 0, 10)

	offset := 0

	// Next file ID tag
	if e.HasNextFileID {
		buf[offset] = tagNextFileID
		offset++
		coding.PutUint64(buf[offset:], e.NextFileID)
		offset += 8
	}

	// Last sequence number tag
	if e.HasLastSeqNo {
		buf[offset] = tagLastSequence
		offset++
		coding.PutUint64(buf[offset:], e.LastSeqNo)
		offset += 8
	}

	// Log number tag
	if e.HasLogNo {
		buf[offset] = tagLogNumber
		offset++
		coding.PutUint64(buf[offset:], e.LogNo)
		offset += 8
	}

	// Deleted files tags
	// Frame: [tag byte][level u32][fileID u64]
	for _, deletedFile := range e.DeletedFiles {
		buf[offset] = tagDeleteFile
		offset++
		coding.PutUint32(buf[offset:], deletedFile.Level)
		offset += 4
		coding.PutUint64(buf[offset:], deletedFile.FileID)
		offset += 8
	}

	// Added files tags
	// Frame: [tag byte][level u32][fileID u64][size u64][smallestKey uint32 len+bytes][largestKey uint32 len+bytes]
	for _, addedFile := range e.AddedFiles {
		buf[offset] = tagAddFile
		offset++
		coding.PutUint32(buf[offset:], addedFile.Level)
		offset += 4
		coding.PutUint64(buf[offset:], addedFile.FileID)
		offset += 8
		coding.PutUint64(buf[offset:], addedFile.Size)
		offset += 8

		// Encode the smallest key + get it's length
		smallestKeyBytes := addedFile.SmallestKey.Encode()
		offset = writeLengthPrefixedBytes(buf, offset, smallestKeyBytes)

		// Encode the largest key + get it's length
		largestKeyBytes := addedFile.LargestKey.Encode()
		offset = writeLengthPrefixedBytes(buf, offset, largestKeyBytes)
	}

	return buf
}

// DecodeVersionEdit decodes a byte slice into a VersionEdit.
// Returns an error if the data is malformed, truncated, or contains invalid operations.
func DecodeVersionEdit(data []byte) (*VersionEdit, error) {
	r := coding.NewByteReader(data)

	edit := &VersionEdit{}

	for r.Remaining() > 0 {
		tag, err := r.ReadByte()
		if err != nil {
			return nil, record.ErrTruncated
		}
		switch tag {
		case tagNextFileID:
			nextFileID, err := r.ReadUint64()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			edit.NextFileID = nextFileID
			edit.HasNextFileID = true
		case tagLastSequence:
			lastSeqNo, err := r.ReadUint64()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			edit.LastSeqNo = lastSeqNo
			edit.HasLastSeqNo = true
		case tagLogNumber:
			logNo, err := r.ReadUint64()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			edit.LogNo = logNo
			edit.HasLogNo = true
		case tagDeleteFile:
			level, err := r.ReadUint32()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			fileID, err := r.ReadUint64()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			edit.DeletedFiles = append(edit.DeletedFiles, DeletedFile{
				Level:  level,
				FileID: fileID,
			})
		case tagAddFile:
			// read level + fileID + size + two length-prefixed keys → append
			level, err := r.ReadUint32()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			fileID, err := r.ReadUint64()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			fileSize, err := r.ReadUint64()
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			smallestKeyBytes, err := readLengthPrefixedBytes(r)
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			largestKeyBytes, err := readLengthPrefixedBytes(r)
			if err != nil {
				return &VersionEdit{}, record.ErrCorruptRecord
			}
			smallestKey, err := keys.DecodeInternalKey(smallestKeyBytes)
			if err != nil {
				return &VersionEdit{}, err
			}
			largestKey, err := keys.DecodeInternalKey(largestKeyBytes)
			if err != nil {
				return &VersionEdit{}, err
			}
			edit.AddedFiles = append(edit.AddedFiles, FileMetadata{
				Level:       level,
				FileID:      fileID,
				Size:        fileSize,
				SmallestKey: smallestKey,
				LargestKey:  largestKey,
			})
		default:
			return &VersionEdit{}, ErrUnknownTag
		}
	}
	return edit, nil
}

// writeLengthPrefixedBytes writes b as a length-prefixed byte slice into buf
// starting at offset. Layout: [uint32 length big-endian][b...]. Returns the
// new offset positioned immediately after the written bytes.
func writeLengthPrefixedBytes(buf []byte, offset int, b []byte) int {
	coding.PutUint32(buf[offset:], uint32(len(b)))
	offset += 4
	copy(buf[offset:], b)
	return offset + len(b)
}

// readLengthPrefixedBytes reads a length-prefixed byte slice from r and returns
// an independent copy of the payload. Returns ErrTruncated if either the length
// prefix or the payload bytes are unavailable.
func readLengthPrefixedBytes(r *coding.ByteReader) ([]byte, error) {
	n, err := r.ReadUint32()
	if err != nil {
		return nil, record.ErrTruncated
	}
	b, err := r.ReadBytes(int(n))
	if err != nil {
		return nil, record.ErrTruncated
	}
	out := make([]byte, n)
	copy(out, b)
	return out, nil
}
