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

// TestVersionEdit_Encode_EmitOrder_Canary is intentionally white-box: it
// inspects byte offsets to lock in the wire-format emit order documented in
// docs/formats/manifest.md (counters first, then deletes, then adds). If a
// refactor changes the emit order, update the format spec deliberately, then
// update this canary — do not just adjust the offsets.
func TestVersionEdit_Encode_EmitOrder_Canary(t *testing.T) {
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

func TestVersionEdit_Decode_Truncated(t *testing.T) {
	// Build a valid AddFile body once, for the keys-truncation case.
	validAddFile := (&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 100, putKey("hello", 1), putKey("world", 2)),
		},
	}).Encode()

	cases := []struct {
		name    string
		body    []byte
		wantErr error
	}{
		{
			name:    "counter tag missing uint64 payload",
			body:    []byte{tagNextFileID, 0x00, 0x00, 0x00},
			wantErr: ErrTruncatedEdit,
		},
		{
			name:    "delete file missing fileID after level",
			body:    []byte{tagDeleteFile, 0x00, 0x00, 0x00, 0x01},
			wantErr: ErrTruncatedEdit,
		},
		{
			name:    "add file partial level u32",
			body:    []byte{tagAddFile, 0x00, 0x00},
			wantErr: ErrTruncatedEdit,
		},
		{
			name:    "add file mid smallest key bytes",
			body:    validAddFile[:len(validAddFile)-3],
			wantErr: ErrTruncatedEdit,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeVersionEdit(tc.body)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("got %v, want %v", err, tc.wantErr)
			}
		})
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

// addFileHeader builds the constant tag + level + fileID + size prefix of an
// AddFile TLV record (everything before the smallestKey length prefix).
func addFileHeader() []byte {
	return []byte{
		tagAddFile,
		0x00, 0x00, 0x00, 0x00, // level = 0
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, // fileID = 1
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, // size = 16
	}
}

// appendLengthPrefixed appends a length-prefixed byte slice to buf using the
// production encoder helper, so test fixtures stay aligned with the wire format.
func appendLengthPrefixed(buf, b []byte) []byte {
	lp := make([]byte, 4+len(b))
	writeLengthPrefixedBytes(lp, 0, b)
	return append(buf, lp...)
}

func TestVersionEdit_Decode_OversizedLengthPrefix_LargestKey(t *testing.T) {
	// AddFile where smallestKey is valid but largestKey length prefix is oversized.
	// Defends against an asymmetric bug in the second readLengthPrefixedBytes call.
	smallest := putKey("a", 1).Encode()
	bad := make([]byte, 0, len(addFileHeader())+4+len(smallest)+4)
	bad = append(bad, addFileHeader()...)
	bad = appendLengthPrefixed(bad, smallest)
	bad = append(bad, 0xFF, 0xFF, 0xFF, 0xFF) // largestKey length = 4 GiB

	_, err := DecodeVersionEdit(bad)
	if !errors.Is(err, ErrEditFieldTooLarge) {
		t.Errorf("expected ErrEditFieldTooLarge, got %v", err)
	}
}

func TestVersionEdit_Decode_InvalidInternalKey_LargestKey(t *testing.T) {
	// AddFile where smallestKey is valid but largestKey payload is too short
	// to be a valid InternalKey. Confirms the wrap site for largest key at
	// decodeAddedFile returns errors.Is(_, keys.ErrCorruptInternalKey).
	smallest := putKey("a", 1).Encode()
	bad := make([]byte, 0, len(addFileHeader())+4+len(smallest)+6)
	bad = append(bad, addFileHeader()...)
	bad = appendLengthPrefixed(bad, smallest)
	bad = append(bad,
		0x00, 0x00, 0x00, 0x02, // largestKey length = 2 (too short)
		0x00, 0x00,
	)

	_, err := DecodeVersionEdit(bad)
	if err == nil {
		t.Fatal("expected error for invalid largest InternalKey")
	}
	if !errors.Is(err, keys.ErrCorruptInternalKey) {
		t.Errorf("expected wrapped keys.ErrCorruptInternalKey, got %v", err)
	}
}

func TestVersionEdit_RoundTrip_ZeroLengthUserKey(t *testing.T) {
	// Empty user key is valid: encoded InternalKey is just the 9-byte trailer.
	// Exercises the len=0 length-prefix path on both encode and decode.
	edit := &VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 64,
				keys.InternalKey{UserKey: nil, Seqno: 1, Kind: keys.InternalKeyKindPut},
				keys.InternalKey{UserKey: []byte{}, Seqno: 2, Kind: keys.InternalKeyKindPut},
			),
		},
	}

	encoded := edit.Encode()
	decoded, err := DecodeVersionEdit(encoded)
	if err != nil {
		t.Fatalf("DecodeVersionEdit failed: %v", err)
	}
	assertEditsEqual(t, decoded, edit)
}

func TestVersionEdit_Encode_Length_Matches_EncodedSize(t *testing.T) {
	// Silent-divergence canary: if encodedSize and Encode disagree on byte count,
	// Encode would either panic on out-of-bounds write or leave trailing zeros.
	cases := []struct {
		name string
		edit *VersionEdit
	}{
		{"empty", &VersionEdit{}},
		{"counters only", &VersionEdit{
			HasNextFileID: true, NextFileID: 1,
			HasLastSequence: true, LastSequence: 2,
			HasLogNumber: true, LogNumber: 3,
		}},
		{"deletes only", &VersionEdit{
			DeletedFiles: []DeletedFile{{Level: 0, FileID: 1}, {Level: 1, FileID: 2}},
		}},
		{"adds only", &VersionEdit{
			AddedFiles: []FileMetadata{
				fileMeta(0, 1, 100, putKey("a", 1), putKey("b", 2)),
				fileMeta(2, 3, 200, putKey("c", 3), putKey("d", 4)),
			},
		}},
		{"mixed", &VersionEdit{
			HasNextFileID: true, NextFileID: 7,
			DeletedFiles: []DeletedFile{{Level: 0, FileID: 1}},
			AddedFiles: []FileMetadata{
				fileMeta(1, 2, 50, putKey("x", 1), putKey("y", 2)),
			},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addedBytes := make([]addedFileBytes, len(tc.edit.AddedFiles))
			for i := range tc.edit.AddedFiles {
				addedBytes[i].smallest = tc.edit.AddedFiles[i].SmallestKey.Encode()
				addedBytes[i].largest = tc.edit.AddedFiles[i].LargestKey.Encode()
			}
			want := tc.edit.encodedSize(addedBytes)
			got := len(tc.edit.Encode())
			if got != want {
				t.Errorf("len(Encode()) = %d, encodedSize() = %d", got, want)
			}
		})
	}
}

func TestVersionEdit_Decode_TagOrderIndependent(t *testing.T) {
	// The decoder loops over whatever tag appears next, with no order
	// assumption. Hand-craft a stream where adds precede counters and deletes,
	// and verify decode produces the same VersionEdit as the canonical order.
	canonical := &VersionEdit{
		HasNextFileID: true, NextFileID: 42,
		DeletedFiles: []DeletedFile{{Level: 1, FileID: 99}},
		AddedFiles: []FileMetadata{
			fileMeta(0, 7, 1024, putKey("a", 1), putKey("z", 2)),
		},
	}

	// Build a non-canonical stream manually: AddFile first, then DeleteFile, then NextFileID.
	addedBytes := addedFileBytes{
		smallest: canonical.AddedFiles[0].SmallestKey.Encode(),
		largest:  canonical.AddedFiles[0].LargestKey.Encode(),
	}
	bufSize := 1 + 4 + 8 + 8 + 4 + len(addedBytes.smallest) + 4 + len(addedBytes.largest) +
		1 + 4 + 8 +
		1 + 8
	buf := make([]byte, bufSize)
	off := 0
	off = writeAddedFile(buf, off, canonical.AddedFiles[0], addedBytes)
	off = writeDeletedFile(buf, off, canonical.DeletedFiles[0])
	_ = writeCounterTag(buf, off, tagNextFileID, true, canonical.NextFileID)

	decoded, err := DecodeVersionEdit(buf)
	if err != nil {
		t.Fatalf("DecodeVersionEdit failed: %v", err)
	}
	assertEditsEqual(t, decoded, canonical)
}

func FuzzVersionEditDecode(f *testing.F) {
	// Minimal seeds: happy path + error paths.
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

	// Wider seeds: bootstrap corpus for multi-file, large-key, and counter variants.
	manyFiles := make([]FileMetadata, 12)
	for i := range manyFiles {
		manyFiles[i] = fileMeta(uint32(i%3), uint64(i+100), uint64(1024*(i+1)),
			putKey(string([]byte{'a' + byte(i)}), uint64(i+1)),
			putKey(string([]byte{'b' + byte(i)}), uint64(i+2)),
		)
	}
	f.Add((&VersionEdit{AddedFiles: manyFiles}).Encode())

	bigUserKey := make([]byte, 4*1024)
	for i := range bigUserKey {
		bigUserKey[i] = byte(i % 251)
	}
	f.Add((&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 1,
				keys.InternalKey{UserKey: bigUserKey, Seqno: 1, Kind: keys.InternalKeyKindPut},
				keys.InternalKey{UserKey: bigUserKey, Seqno: 2, Kind: keys.InternalKeyKindPut},
			),
		},
	}).Encode())

	f.Add((&VersionEdit{HasLastSequence: true, LastSequence: 1234567}).Encode())

	f.Add((&VersionEdit{
		HasNextFileID: true, NextFileID: 10,
		HasLastSequence: true, LastSequence: 20,
		HasLogNumber: true, LogNumber: 30,
		DeletedFiles: []DeletedFile{{Level: 0, FileID: 1}},
		AddedFiles: []FileMetadata{
			fileMeta(0, 2, 100, putKey("a", 1), putKey("b", 2)),
			fileMeta(1, 3, 200, putKey("c", 3), putKey("d", 4)),
		},
	}).Encode())

	f.Fuzz(func(_ *testing.T, data []byte) {
		// Only requirement: never panic, always return error or success.
		_, _ = DecodeVersionEdit(data)
	})
}
