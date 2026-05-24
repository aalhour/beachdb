package manifest

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
)

// putKey returns an InternalKey of kind Put with the given user key and seqno.
func putKey(userKey string, seqno uint64) keys.InternalKey {
	return keys.InternalKey{
		UserKey: []byte(userKey),
		Seqno:   seqno,
		Kind:    keys.InternalKeyKindPut,
	}
}

// fileMeta is a convenience constructor for FileMetadata fixtures.
func fileMeta(level uint32, fileID uint64, size uint64, smallest, largest keys.InternalKey) FileMetadata {
	return FileMetadata{
		Level:       level,
		FileID:      fileID,
		Size:        size,
		SmallestKey: smallest,
		LargestKey:  largest,
	}
}

// assertEditsEqual compares two VersionEdits field-by-field and fails the test
// on any mismatch. Used because reflect.DeepEqual fails on InternalKey when
// UserKey is nil vs an empty slice.
func assertEditsEqual(t *testing.T, got, want *VersionEdit) {
	t.Helper()
	if got.HasNextFileID != want.HasNextFileID || got.NextFileID != want.NextFileID {
		t.Errorf("NextFileID: got (has=%v, val=%d), want (has=%v, val=%d)",
			got.HasNextFileID, got.NextFileID, want.HasNextFileID, want.NextFileID)
	}
	if got.HasLastSequence != want.HasLastSequence || got.LastSequence != want.LastSequence {
		t.Errorf("LastSequence: got (has=%v, val=%d), want (has=%v, val=%d)",
			got.HasLastSequence, got.LastSequence, want.HasLastSequence, want.LastSequence)
	}
	if got.HasLogNumber != want.HasLogNumber || got.LogNumber != want.LogNumber {
		t.Errorf("LogNumber: got (has=%v, val=%d), want (has=%v, val=%d)",
			got.HasLogNumber, got.LogNumber, want.HasLogNumber, want.LogNumber)
	}
	if len(got.DeletedFiles) != len(want.DeletedFiles) {
		t.Fatalf("DeletedFiles length: got %d, want %d", len(got.DeletedFiles), len(want.DeletedFiles))
	}
	for i := range want.DeletedFiles {
		if got.DeletedFiles[i] != want.DeletedFiles[i] {
			t.Errorf("DeletedFiles[%d]: got %+v, want %+v", i, got.DeletedFiles[i], want.DeletedFiles[i])
		}
	}
	if len(got.AddedFiles) != len(want.AddedFiles) {
		t.Fatalf("AddedFiles length: got %d, want %d", len(got.AddedFiles), len(want.AddedFiles))
	}
	for i := range want.AddedFiles {
		g, w := got.AddedFiles[i], want.AddedFiles[i]
		if g.Level != w.Level || g.FileID != w.FileID || g.Size != w.Size {
			t.Errorf("AddedFiles[%d] scalars: got {L=%d, ID=%d, Sz=%d}, want {L=%d, ID=%d, Sz=%d}",
				i, g.Level, g.FileID, g.Size, w.Level, w.FileID, w.Size)
		}
		if !bytes.Equal(g.SmallestKey.UserKey, w.SmallestKey.UserKey) ||
			g.SmallestKey.Seqno != w.SmallestKey.Seqno ||
			g.SmallestKey.Kind != w.SmallestKey.Kind {
			t.Errorf("AddedFiles[%d].SmallestKey: got %+v, want %+v", i, g.SmallestKey, w.SmallestKey)
		}
		if !bytes.Equal(g.LargestKey.UserKey, w.LargestKey.UserKey) ||
			g.LargestKey.Seqno != w.LargestKey.Seqno ||
			g.LargestKey.Kind != w.LargestKey.Kind {
			t.Errorf("AddedFiles[%d].LargestKey: got %+v, want %+v", i, g.LargestKey, w.LargestKey)
		}
	}
}

func TestVersionEdit_RoundTrip(t *testing.T) {
	edit := &VersionEdit{
		HasNextFileID:   true,
		NextFileID:      42,
		HasLastSequence: true,
		LastSequence:    100,
		HasLogNumber:    true,
		LogNumber:       7,
		DeletedFiles: []DeletedFile{
			{Level: 1, FileID: 11},
		},
		AddedFiles: []FileMetadata{
			fileMeta(0, 7, 1024, putKey("apple", 1), putKey("zebra", 2)),
		},
	}

	encoded := edit.Encode()
	if len(encoded) == 0 {
		t.Fatal("expected non-empty encoding for non-empty edit")
	}

	decoded, err := DecodeVersionEdit(encoded)
	if err != nil {
		t.Fatalf("DecodeVersionEdit failed: %v", err)
	}

	assertEditsEqual(t, decoded, edit)
}

func TestVersionEdit_RoundTrip_Partial(t *testing.T) {
	// Only AddedFiles set; all counters and DeletedFiles must decode as zero values.
	edit := &VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(2, 99, 2048, putKey("aaa", 5), putKey("zzz", 6)),
		},
	}

	encoded := edit.Encode()
	decoded, err := DecodeVersionEdit(encoded)
	if err != nil {
		t.Fatalf("DecodeVersionEdit failed: %v", err)
	}

	if decoded.HasNextFileID || decoded.HasLastSequence || decoded.HasLogNumber {
		t.Errorf("unset counters should decode with Has* == false; got %+v", decoded)
	}
	if decoded.NextFileID != 0 || decoded.LastSequence != 0 || decoded.LogNumber != 0 {
		t.Errorf("unset counter values should be zero; got %+v", decoded)
	}
	if len(decoded.DeletedFiles) != 0 {
		t.Errorf("expected zero DeletedFiles, got %d", len(decoded.DeletedFiles))
	}

	assertEditsEqual(t, decoded, edit)
}

func TestVersionEdit_RoundTrip_Empty(t *testing.T) {
	edit := &VersionEdit{}

	encoded := edit.Encode()
	if len(encoded) != 0 {
		t.Errorf("empty edit should encode to zero-byte body, got %d bytes", len(encoded))
	}

	decoded, err := DecodeVersionEdit(encoded)
	if err != nil {
		t.Fatalf("DecodeVersionEdit on empty body failed: %v", err)
	}

	if !reflect.DeepEqual(decoded, &VersionEdit{}) {
		t.Errorf("decoded empty edit should equal zero VersionEdit, got %+v", decoded)
	}
}

func TestVersionEdit_MultipleFiles(t *testing.T) {
	edit := &VersionEdit{
		HasNextFileID: true,
		NextFileID:    100,
		DeletedFiles: []DeletedFile{
			{Level: 0, FileID: 1},
			{Level: 1, FileID: 5},
		},
		AddedFiles: []FileMetadata{
			fileMeta(0, 10, 512, putKey("a", 1), putKey("c", 2)),
			fileMeta(0, 11, 768, putKey("d", 3), putKey("f", 4)),
			fileMeta(1, 12, 4096, putKey("g", 5), putKey("z", 6)),
		},
	}

	encoded := edit.Encode()
	decoded, err := DecodeVersionEdit(encoded)
	if err != nil {
		t.Fatalf("DecodeVersionEdit failed: %v", err)
	}

	assertEditsEqual(t, decoded, edit)
}

func TestVersionEdit_Encode_Deterministic(t *testing.T) {
	edit := &VersionEdit{
		HasNextFileID:   true,
		NextFileID:      42,
		HasLastSequence: true,
		LastSequence:    100,
		AddedFiles: []FileMetadata{
			fileMeta(0, 7, 1024, putKey("apple", 1), putKey("zebra", 2)),
			fileMeta(0, 8, 2048, putKey("alpha", 3), putKey("yankee", 4)),
		},
	}

	first := edit.Encode()
	second := edit.Encode()
	if !bytes.Equal(first, second) {
		t.Errorf("Encode should be deterministic; first=%x second=%x", first, second)
	}
}

func TestVersionEdit_Encode_EmitOrder(t *testing.T) {
	// Counter tags must precede file deltas. Same convention as LevelDB.
	edit := &VersionEdit{
		HasNextFileID:   true,
		NextFileID:      1,
		HasLastSequence: true,
		LastSequence:    2,
		HasLogNumber:    true,
		LogNumber:       3,
		DeletedFiles:    []DeletedFile{{Level: 0, FileID: 99}},
		AddedFiles: []FileMetadata{
			fileMeta(0, 100, 1, putKey("a", 1), putKey("b", 2)),
		},
	}

	encoded := edit.Encode()
	if len(encoded) < 28 {
		t.Fatalf("encoded too short: %d bytes", len(encoded))
	}

	// First three tags should be the counter tags in fixed order.
	if encoded[0] != tagNextFileID {
		t.Errorf("byte[0] = 0x%X, want tagNextFileID (0x%X)", encoded[0], tagNextFileID)
	}
	if encoded[9] != tagLastSequence {
		t.Errorf("byte[9] = 0x%X, want tagLastSequence (0x%X)", encoded[9], tagLastSequence)
	}
	if encoded[18] != tagLogNumber {
		t.Errorf("byte[18] = 0x%X, want tagLogNumber (0x%X)", encoded[18], tagLogNumber)
	}
	// Then DeleteFile before AddFile.
	if encoded[27] != tagDeleteFile {
		t.Errorf("byte[27] = 0x%X, want tagDeleteFile (0x%X)", encoded[27], tagDeleteFile)
	}
}

func TestVersionEdit_Decode_UnknownTag(t *testing.T) {
	// Inject an unknown tag byte (0x63 = 99).
	bad := []byte{0x63}
	_, err := DecodeVersionEdit(bad)
	if !errors.Is(err, ErrUnknownTag) {
		t.Errorf("expected ErrUnknownTag for unknown tag, got %v", err)
	}
}

func TestVersionEdit_Decode_UnknownTag_AfterValidTags(t *testing.T) {
	// Valid prefix (NextFileID=42) followed by unknown tag must still fail.
	good := (&VersionEdit{HasNextFileID: true, NextFileID: 42}).Encode()
	bad := append(append([]byte(nil), good...), 0xFF)

	_, err := DecodeVersionEdit(bad)
	if !errors.Is(err, ErrUnknownTag) {
		t.Errorf("expected ErrUnknownTag, got %v", err)
	}
}

func TestVersionEdit_Decode_Truncated_CounterTag(t *testing.T) {
	// Tag byte present but uint64 payload missing.
	bad := []byte{tagNextFileID, 0x00, 0x00, 0x00}
	_, err := DecodeVersionEdit(bad)
	if !errors.Is(err, ErrTruncatedEdit) {
		t.Errorf("expected ErrTruncatedEdit, got %v", err)
	}
}

func TestVersionEdit_Decode_Truncated_DeleteFile(t *testing.T) {
	// Tag + level u32 only; fileID missing.
	bad := []byte{tagDeleteFile, 0x00, 0x00, 0x00, 0x01}
	_, err := DecodeVersionEdit(bad)
	if !errors.Is(err, ErrTruncatedEdit) {
		t.Errorf("expected ErrTruncatedEdit, got %v", err)
	}
}

func TestVersionEdit_Decode_Truncated_AddFile_Header(t *testing.T) {
	// Tag + partial level only.
	bad := []byte{tagAddFile, 0x00, 0x00}
	_, err := DecodeVersionEdit(bad)
	if !errors.Is(err, ErrTruncatedEdit) {
		t.Errorf("expected ErrTruncatedEdit, got %v", err)
	}
}

func TestVersionEdit_Decode_Truncated_AddFile_Keys(t *testing.T) {
	// Build a valid AddFile then chop the body inside the smallestKey payload.
	full := (&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 100, putKey("hello", 1), putKey("world", 2)),
		},
	}).Encode()

	// Truncate to a point inside the smallestKey bytes (last few bytes lopped off).
	truncated := full[:len(full)-3]

	_, err := DecodeVersionEdit(truncated)
	if !errors.Is(err, ErrTruncatedEdit) {
		t.Errorf("expected ErrTruncatedEdit, got %v", err)
	}
}

func TestVersionEdit_Decode_OversizedLengthPrefix(t *testing.T) {
	// AddFile with a smallestKey length prefix beyond the v1 cap. Without
	// the cap, this would allocate 4 GiB. With the cap, decode rejects fast.
	bad := []byte{
		tagAddFile,
		0x00, 0x00, 0x00, 0x00, // level = 0
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, // fileID = 1
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, // size = 16
		0xFF, 0xFF, 0xFF, 0xFF, // smallestKey length = 4 GiB
	}

	_, err := DecodeVersionEdit(bad)
	if !errors.Is(err, ErrEditFieldTooLarge) {
		t.Errorf("expected ErrEditFieldTooLarge, got %v", err)
	}
}

func TestVersionEdit_Decode_InvalidInternalKey(t *testing.T) {
	// AddFile with a smallestKey payload that fails keys.DecodeInternalKey.
	// The key bytes are too short (< 9 bytes), so keys returns ErrCorruptInternalKey.
	// DecodeVersionEdit must wrap that error with manifest context.
	bad := []byte{
		tagAddFile,
		0x00, 0x00, 0x00, 0x00, // level = 0
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, // fileID = 1
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, // size = 16
		0x00, 0x00, 0x00, 0x02, // smallestKey length = 2 (too short for InternalKey)
		0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, // largestKey length = 0
	}

	_, err := DecodeVersionEdit(bad)
	if err == nil {
		t.Fatal("expected error for invalid InternalKey")
	}
	// Should propagate keys.ErrCorruptInternalKey under the wrap.
	if !errors.Is(err, keys.ErrCorruptInternalKey) {
		t.Errorf("expected wrapped keys.ErrCorruptInternalKey, got %v", err)
	}
}

func FuzzVersionEditDecode(f *testing.F) {
	// Seed with a few valid encodings and some malformed inputs.
	f.Add([]byte{})
	f.Add((&VersionEdit{HasNextFileID: true, NextFileID: 1}).Encode())
	f.Add((&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 10, putKey("a", 1), putKey("b", 2)),
		},
	}).Encode())
	f.Add([]byte{0xFF})          // unknown tag
	f.Add([]byte{tagAddFile})    // truncated AddFile
	f.Add([]byte{tagNextFileID}) // truncated counter

	f.Fuzz(func(_ *testing.T, data []byte) {
		// Only requirement: never panic, always return error or success.
		_, _ = DecodeVersionEdit(data)
	})
}
