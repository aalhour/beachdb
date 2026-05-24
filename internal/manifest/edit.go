package manifest

import (
	"fmt"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// VersionEdit field tags for the manifest TLV encoding (v1).
//
// v1 hard-errors on unknown tags. No reserved slots: future fields
// (compaction pointers, snapshots, table catalogs, per-table tracking)
// will ship with a manifest format version bump, not a v1 tag insertion.
// See docs/formats/manifest.md for the full wire format and rationale.
// Wire format per tag — see docs/formats/manifest.md.
//
//	tagAddFile      [level u32][fileID u64][size u64][smallestKey len+bytes][largestKey len+bytes]
//	tagDeleteFile   [level u32][fileID u64]
//	tagNextFileID   [value u64]
//	tagLastSequence [value u64]
//	tagLogNumber    [value u64]
const (
	tagAddFile      byte = 0x01
	tagDeleteFile   byte = 0x02
	tagNextFileID   byte = 0x03
	tagLastSequence byte = 0x04
	tagLogNumber    byte = 0x05
)

// maxLengthPrefixedField caps the size of any length-prefixed byte field
// accepted by DecodeVersionEdit (in v1, the smallest and largest internal
// keys in an AddFile record). 64 KiB is generous for an internal key
// (user keys are typically well under 1 KiB plus 9 bytes for seqno + kind)
// and blocks corruption-driven multi-GiB allocations from a bogus uint32
// length prefix.
const maxLengthPrefixedField uint32 = 1 << 16

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

	// HasLastSequence indicates whether LastSequence carries a meaningful value in this edit.
	HasLastSequence bool

	// LastSequence is the highest sequence number written by the database so far.
	LastSequence uint64

	// HasLogNumber indicates whether LogNumber carries a meaningful value in this edit.
	HasLogNumber bool

	// LogNumber is the WAL file's log number that this edit is associated with.
	LogNumber uint64
}

// addedFileBytes caches the pre-encoded internal-key bytes for one AddedFile
// so the size pass and write pass don't both call InternalKey.Encode().
type addedFileBytes struct {
	smallest []byte
	largest  []byte
}

// Encode serializes the VersionEdit fields to a byte slice in deterministic
// TLV order: counters (NextFileID, LastSequence, LogNumber), then DeletedFiles
// in input order, then AddedFiles in input order. Same convention as LevelDB.
// The same VersionEdit value always encodes to the same byte sequence.
func (e *VersionEdit) Encode() []byte {
	addedBytes := make([]addedFileBytes, len(e.AddedFiles))
	for i := range e.AddedFiles {
		addedBytes[i].smallest = e.AddedFiles[i].SmallestKey.Encode()
		addedBytes[i].largest = e.AddedFiles[i].LargestKey.Encode()
	}

	buf := make([]byte, e.encodedSize(addedBytes))
	offset := 0
	offset = writeCounterTag(buf, offset, tagNextFileID, e.HasNextFileID, e.NextFileID)
	offset = writeCounterTag(buf, offset, tagLastSequence, e.HasLastSequence, e.LastSequence)
	offset = writeCounterTag(buf, offset, tagLogNumber, e.HasLogNumber, e.LogNumber)
	for _, d := range e.DeletedFiles {
		offset = writeDeletedFile(buf, offset, d)
	}
	for i, a := range e.AddedFiles {
		offset = writeAddedFile(buf, offset, a, addedBytes[i])
	}
	return buf
}

// encodedSize returns the exact byte size Encode will produce for this edit
// given the pre-encoded internal-key bytes.
func (e *VersionEdit) encodedSize(addedBytes []addedFileBytes) int {
	size := 0
	if e.HasNextFileID {
		size += 1 + 8
	}
	if e.HasLastSequence {
		size += 1 + 8
	}
	if e.HasLogNumber {
		size += 1 + 8
	}
	size += len(e.DeletedFiles) * (1 + 4 + 8)
	for i := range e.AddedFiles {
		size += 1 + 4 + 8 + 8
		size += 4 + len(addedBytes[i].smallest) + 4 + len(addedBytes[i].largest)
	}
	return size
}

// DecodeVersionEdit decodes a byte slice into a VersionEdit.
// The input must be the validated payload of a manifest record — the framing
// layer has already checked checksum and length, so any short read here means
// the body itself is malformed (returns ErrTruncatedEdit). An unknown tag
// returns ErrUnknownTag (v1 hard-error policy).
func DecodeVersionEdit(data []byte) (*VersionEdit, error) {
	r := coding.NewByteReader(data)
	edit := &VersionEdit{}

	for r.Remaining() > 0 {
		tag, err := r.ReadByte()
		if err != nil {
			return nil, ErrTruncatedEdit
		}
		if err := decodeTag(r, edit, tag); err != nil {
			return nil, err
		}
	}

	return edit, nil
}

// writeCounterTag writes [tag][uint64] for one of the counter tags
// (NextFileID, LastSequence, LogNumber) when has is true. Returns the new offset.
func writeCounterTag(buf []byte, offset int, tag byte, has bool, value uint64) int {
	if !has {
		return offset
	}
	buf[offset] = tag
	offset++
	coding.PutUint64(buf[offset:], value)
	return offset + 8
}

// writeDeletedFile writes [tagDeleteFile][level u32][fileID u64] and returns the new offset.
func writeDeletedFile(buf []byte, offset int, d DeletedFile) int {
	buf[offset] = tagDeleteFile
	offset++
	coding.PutUint32(buf[offset:], d.Level)
	offset += 4
	coding.PutUint64(buf[offset:], d.FileID)
	return offset + 8
}

// writeAddedFile writes the full AddedFile TLV record and returns the new offset.
// Layout: [tagAddFile][level u32][fileID u64][size u64][smallestKey len+bytes][largestKey len+bytes].
func writeAddedFile(buf []byte, offset int, a FileMetadata, keys addedFileBytes) int {
	buf[offset] = tagAddFile
	offset++
	coding.PutUint32(buf[offset:], a.Level)
	offset += 4
	coding.PutUint64(buf[offset:], a.FileID)
	offset += 8
	coding.PutUint64(buf[offset:], a.Size)
	offset += 8
	offset = writeLengthPrefixedBytes(buf, offset, keys.smallest)
	offset = writeLengthPrefixedBytes(buf, offset, keys.largest)
	return offset
}

// writeLengthPrefixedBytes writes b as a length-prefixed byte slice into buf
// starting at offset. Layout: [uint32 length big-endian][b...]. Returns the
// new offset positioned immediately after the written bytes.
func writeLengthPrefixedBytes(buf []byte, offset int, b []byte) int {
	//nolint:gosec // G115: byte-slice length is bounded by the surrounding encode size budget.
	coding.PutUint32(buf[offset:], uint32(len(b)))
	offset += 4
	copy(buf[offset:], b)
	return offset + len(b)
}

// decodeTag reads the payload for one tag and applies it to edit.
func decodeTag(r *coding.ByteReader, edit *VersionEdit, tag byte) error {
	switch tag {
	case tagNextFileID:
		v, err := r.ReadUint64()
		if err != nil {
			return ErrTruncatedEdit
		}
		edit.NextFileID = v
		edit.HasNextFileID = true
		return nil
	case tagLastSequence:
		v, err := r.ReadUint64()
		if err != nil {
			return ErrTruncatedEdit
		}
		edit.LastSequence = v
		edit.HasLastSequence = true
		return nil
	case tagLogNumber:
		v, err := r.ReadUint64()
		if err != nil {
			return ErrTruncatedEdit
		}
		edit.LogNumber = v
		edit.HasLogNumber = true
		return nil
	case tagDeleteFile:
		d, err := decodeDeletedFile(r)
		if err != nil {
			return err
		}
		edit.DeletedFiles = append(edit.DeletedFiles, d)
		return nil
	case tagAddFile:
		f, err := decodeAddedFile(r)
		if err != nil {
			return err
		}
		edit.AddedFiles = append(edit.AddedFiles, f)
		return nil
	default:
		return ErrUnknownTag
	}
}

// decodeDeletedFile reads [level u32][fileID u64].
func decodeDeletedFile(r *coding.ByteReader) (DeletedFile, error) {
	level, err := r.ReadUint32()
	if err != nil {
		return DeletedFile{}, ErrTruncatedEdit
	}
	fileID, err := r.ReadUint64()
	if err != nil {
		return DeletedFile{}, ErrTruncatedEdit
	}
	return DeletedFile{Level: level, FileID: fileID}, nil
}

// decodeAddedFile reads [level u32][fileID u64][size u64][sKey len+bytes][lKey len+bytes].
func decodeAddedFile(r *coding.ByteReader) (FileMetadata, error) {
	level, err := r.ReadUint32()
	if err != nil {
		return FileMetadata{}, ErrTruncatedEdit
	}
	fileID, err := r.ReadUint64()
	if err != nil {
		return FileMetadata{}, ErrTruncatedEdit
	}
	fileSize, err := r.ReadUint64()
	if err != nil {
		return FileMetadata{}, ErrTruncatedEdit
	}
	smallestBytes, err := readLengthPrefixedBytes(r)
	if err != nil {
		return FileMetadata{}, err
	}
	largestBytes, err := readLengthPrefixedBytes(r)
	if err != nil {
		return FileMetadata{}, err
	}
	smallestKey, err := keys.DecodeInternalKey(smallestBytes)
	if err != nil {
		return FileMetadata{}, fmt.Errorf("beachdb/manifest: decode AddFile smallest key: %w", err)
	}
	largestKey, err := keys.DecodeInternalKey(largestBytes)
	if err != nil {
		return FileMetadata{}, fmt.Errorf("beachdb/manifest: decode AddFile largest key: %w", err)
	}
	return FileMetadata{
		Level:       level,
		FileID:      fileID,
		Size:        fileSize,
		SmallestKey: smallestKey,
		LargestKey:  largestKey,
	}, nil
}

// readLengthPrefixedBytes reads a length-prefixed byte slice from r and returns
// an independent copy of the payload. Returns ErrTruncatedEdit on short read,
// ErrEditFieldTooLarge when the declared length exceeds maxLengthPrefixedField.
func readLengthPrefixedBytes(r *coding.ByteReader) ([]byte, error) {
	n, err := r.ReadUint32()
	if err != nil {
		return nil, ErrTruncatedEdit
	}
	if n > maxLengthPrefixedField {
		return nil, ErrEditFieldTooLarge
	}
	b, err := r.ReadBytes(int(n))
	if err != nil {
		return nil, ErrTruncatedEdit
	}
	out := make([]byte, n)
	copy(out, b)
	return out, nil
}
