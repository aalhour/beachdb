package sstable

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/testutil"
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// --- helpers ---

func makeKey(userKey string, seqno uint64, kind byte) keys.InternalKey {
	return keys.InternalKey{UserKey: []byte(userKey), Seqno: seqno, Kind: kind}
}

func putKey(userKey string, seqno uint64) keys.InternalKey {
	return makeKey(userKey, seqno, keys.InternalKeyKindPut)
}

func deleteKey(userKey string, seqno uint64) keys.InternalKey {
	return makeKey(userKey, seqno, keys.InternalKeyKindDelete)
}

func createWriter(t *testing.T, opts ...WriterOption) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sst")
	f, err := os.Create(path) //nolint:gosec // test helper with temp dir
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(f, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return w, path
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test helper with temp dir
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// --- NewWriter tests ---

func TestNewWriter_NilFile(t *testing.T) {
	_, err := NewWriter(nil)
	if !errors.Is(err, ErrNilFile) {
		t.Fatalf("expected ErrNilFile, got %v", err)
	}
}

func TestNewWriter_InvalidBlockSize(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "test.sst")) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	tests := []struct {
		name string
		size int
	}{
		{"zero", 0},
		{"negative", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewWriter(f, WithBlockSize(tt.size))
			if !errors.Is(err, ErrInvalidBlockSize) {
				t.Fatalf("expected ErrInvalidBlockSize, got %v", err)
			}
		})
	}
}

func TestNewWriter_Defaults(t *testing.T) {
	w, _ := createWriter(t)
	defer w.Close()

	if w.targetBlockSize != 4096 {
		t.Fatalf("expected default block size 4096, got %d", w.targetBlockSize)
	}
	if !w.syncOnClose {
		t.Fatal("expected syncOnClose true by default")
	}
}

// --- blockBuilder tests ---

func TestBlockBuilder_Empty(t *testing.T) {
	b := newBlockBuilder()
	if !b.Empty() {
		t.Fatal("new block builder should be empty")
	}
	if b.Size() != 0 {
		t.Fatalf("expected size 0, got %d", b.Size())
	}
	if b.EntryCount() != 0 {
		t.Fatalf("expected entry count 0, got %d", b.EntryCount())
	}
}

func TestBlockBuilder_AddSingleEntry(t *testing.T) {
	b := newBlockBuilder()
	key := putKey("hello", 1)
	val := []byte("world")

	b.Add(key, val)

	if b.Empty() {
		t.Fatal("should not be empty after Add")
	}
	if b.EntryCount() != 1 {
		t.Fatalf("expected 1 entry, got %d", b.EntryCount())
	}

	// Expected size: 4 + len(encoded_key) + 4 + len(value)
	encoded := key.Encode()
	expectedSize := 4 + len(encoded) + 4 + len(val)
	if b.Size() != expectedSize {
		t.Fatalf("expected size %d, got %d", expectedSize, b.Size())
	}

	if b.FirstKey().Compare(key) != 0 {
		t.Fatal("FirstKey mismatch")
	}
	if b.LastKey().Compare(key) != 0 {
		t.Fatal("LastKey mismatch")
	}
}

func TestBlockBuilder_AddMultipleEntries(t *testing.T) {
	b := newBlockBuilder()
	k1 := putKey("aaa", 2)
	k2 := putKey("bbb", 1)

	b.Add(k1, []byte("v1"))
	b.Add(k2, []byte("v2"))

	if b.EntryCount() != 2 {
		t.Fatalf("expected 2 entries, got %d", b.EntryCount())
	}
	if b.FirstKey().Compare(k1) != 0 {
		t.Fatal("FirstKey should be the first added key")
	}
	if b.LastKey().Compare(k2) != 0 {
		t.Fatal("LastKey should be the last added key")
	}
}

func TestBlockBuilder_Finish_ChecksumTrailer(t *testing.T) {
	b := newBlockBuilder()
	b.Add(putKey("key", 1), []byte("val"))

	finished := b.Finish()

	// The last 4 bytes are the CRC32C of everything before them
	payload := finished[:len(finished)-int(checksumSize)]
	storedCRC := coding.Uint32(finished[len(finished)-int(checksumSize):])
	computedCRC := checksum.CRC32C(payload)

	if storedCRC != computedCRC {
		t.Fatalf("checksum mismatch: stored=0x%08x computed=0x%08x", storedCRC, computedCRC)
	}
}

func TestBlockBuilder_Finish_ByteLayout(t *testing.T) {
	b := newBlockBuilder()
	key := putKey("k", 1)
	val := []byte("v")

	b.Add(key, val)
	finished := b.Finish()
	payload := finished[:len(finished)-int(checksumSize)]

	// Parse the entry manually
	br := coding.NewByteReader(payload)

	keyLen, err := br.ReadUint32()
	if err != nil {
		t.Fatal(err)
	}
	encoded := key.Encode()
	if keyLen != uint32(len(encoded)) { //nolint:gosec // test assertion
		t.Fatalf("key length: want %d, got %d", len(encoded), keyLen)
	}

	keyBytes, err := br.ReadBytes(int(keyLen))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := keys.DecodeInternalKey(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Compare(key) != 0 {
		t.Fatal("decoded key mismatch")
	}

	valLen, err := br.ReadUint32()
	if err != nil {
		t.Fatal(err)
	}
	if valLen != uint32(len(val)) { //nolint:gosec // test assertion
		t.Fatalf("value length: want %d, got %d", len(val), valLen)
	}

	valBytes, err := br.ReadBytes(int(valLen))
	if err != nil {
		t.Fatal(err)
	}
	if string(valBytes) != "v" {
		t.Fatalf("value: want %q, got %q", "v", string(valBytes))
	}

	if br.Remaining() != 0 {
		t.Fatalf("expected 0 remaining bytes, got %d", br.Remaining())
	}
}

func TestBlockBuilder_Reset(t *testing.T) {
	b := newBlockBuilder()
	b.Add(putKey("key", 1), []byte("val"))
	b.Reset()

	if !b.Empty() {
		t.Fatal("should be empty after Reset")
	}
	if b.Size() != 0 {
		t.Fatalf("expected size 0 after Reset, got %d", b.Size())
	}
	if b.EntryCount() != 0 {
		t.Fatalf("expected entry count 0 after Reset, got %d", b.EntryCount())
	}
}

func TestBlockBuilder_ResetThenReuse(t *testing.T) {
	b := newBlockBuilder()
	b.Add(putKey("aaa", 1), []byte("v1"))
	b.Reset()

	// Reuse after reset
	k := putKey("bbb", 2)
	b.Add(k, []byte("v2"))

	if b.EntryCount() != 1 {
		t.Fatalf("expected 1 entry after reuse, got %d", b.EntryCount())
	}
	if b.FirstKey().Compare(k) != 0 {
		t.Fatal("FirstKey should reflect post-reset entry")
	}
}

func TestBlockBuilder_BinaryKeysAndValues(t *testing.T) {
	b := newBlockBuilder()

	// Key with null bytes and high-bit bytes
	key := keys.InternalKey{
		UserKey: []byte{0x00, 0xFF, 0x80, 0x01},
		Seqno:   42,
		Kind:    keys.InternalKeyKindPut,
	}
	// Value with null bytes
	val := []byte{0x00, 0x00, 0xFF}

	b.Add(key, val)
	finished := b.Finish()

	// Verify checksum
	payload := finished[:len(finished)-int(checksumSize)]
	storedCRC := coding.Uint32(finished[len(finished)-int(checksumSize):])
	if storedCRC != checksum.CRC32C(payload) {
		t.Fatal("checksum mismatch for binary key/value")
	}
}

func TestBlockBuilder_EmptyValue(t *testing.T) {
	b := newBlockBuilder()
	b.Add(putKey("key", 1), []byte{})

	finished := b.Finish()
	payload := finished[:len(finished)-int(checksumSize)]

	// Parse and verify value length is 0
	br := coding.NewByteReader(payload)
	keyLen, err := br.ReadUint32()
	if err != nil {
		t.Fatalf("reading key length: %v", err)
	}
	if _, err = br.ReadBytes(int(keyLen)); err != nil {
		t.Fatalf("reading key bytes: %v", err)
	}
	valLen, err := br.ReadUint32()
	if err != nil {
		t.Fatalf("reading value length: %v", err)
	}

	if valLen != 0 {
		t.Fatalf("expected value length 0, got %d", valLen)
	}
	if br.Remaining() != 0 {
		t.Fatalf("expected 0 remaining, got %d", br.Remaining())
	}
}

// --- Writer.Add tests ---

func TestWriter_AddAfterClose(t *testing.T) {
	w, _ := createWriter(t)
	w.Close()

	err := w.Add(putKey("key", 1), []byte("val"))
	if !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed, got %v", err)
	}
}

func TestWriter_RejectsOutOfOrderKeys(t *testing.T) {
	w, _ := createWriter(t)
	defer w.Close()

	if err := w.Add(putKey("bbb", 1), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	err := w.Add(putKey("aaa", 1), []byte("v2"))
	if !errors.Is(err, ErrOutOfOrderKey) {
		t.Fatalf("expected ErrOutOfOrderKey, got %v", err)
	}
}

func TestWriter_RejectsDuplicateKeys(t *testing.T) {
	w, _ := createWriter(t)
	defer w.Close()

	if err := w.Add(putKey("aaa", 5), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	// Same user key, same seqno — not strictly greater
	err := w.Add(putKey("aaa", 5), []byte("v2"))
	if !errors.Is(err, ErrOutOfOrderKey) {
		t.Fatalf("expected ErrOutOfOrderKey, got %v", err)
	}
}

func TestWriter_AllowsSameUserKeyDescendingSeqno(t *testing.T) {
	w, _ := createWriter(t)
	defer w.Close()

	// InternalKey.Compare sorts same user keys by descending seqno
	// so ("aaa", 10) < ("aaa", 5) in internal key order
	if err := w.Add(putKey("aaa", 10), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(putKey("aaa", 5), []byte("v2")); err != nil {
		t.Fatalf("should accept same user key with lower seqno: %v", err)
	}
}

func TestWriter_AllowsDeleteEntries(t *testing.T) {
	w, _ := createWriter(t)
	defer w.Close()

	if err := w.Add(putKey("aaa", 10), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(deleteKey("aaa", 5), []byte{}); err != nil {
		t.Fatalf("should accept delete entries: %v", err)
	}
}

func TestWriter_FirstAddAlwaysSucceeds(t *testing.T) {
	w, _ := createWriter(t)
	defer w.Close()

	if err := w.Add(putKey("zzz", 1), []byte("val")); err != nil {
		t.Fatalf("first Add should always succeed: %v", err)
	}
}

// --- Writer.Close tests ---

func TestWriter_EmptyTable(t *testing.T) {
	w, path := createWriter(t, WithSync(false))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := readFileBytes(t, path)

	// Empty SSTable = index block (just a 4-byte checksum) + 40-byte footer = 44 bytes
	if len(data) != 44 {
		t.Fatalf("expected 44 bytes for empty SSTable, got %d", len(data))
	}

	// Decode and verify footer
	footerData := data[len(data)-int(footerSize):]
	f, err := decodeFooter(footerData)
	if err != nil {
		t.Fatalf("decodeFooter: %v", err)
	}
	if f.entryCount != 0 {
		t.Fatalf("expected 0 entries, got %d", f.entryCount)
	}
	if f.dataBlockCount != 0 {
		t.Fatalf("expected 0 data blocks, got %d", f.dataBlockCount)
	}
	if f.indexSize != checksumSize {
		t.Fatalf("expected index size %d (checksum only), got %d", checksumSize, f.indexSize)
	}
}

func TestWriter_SingleEntry(t *testing.T) {
	w, path := createWriter(t, WithSync(false))

	if err := w.Add(putKey("hello", 1), []byte("world")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatalf("decodeFooter: %v", err)
	}

	if f.entryCount != 1 {
		t.Fatalf("expected 1 entry, got %d", f.entryCount)
	}
	if f.dataBlockCount != 1 {
		t.Fatalf("expected 1 data block, got %d", f.dataBlockCount)
	}
}

func TestWriter_MultipleBlocks(t *testing.T) {
	// Use a tiny block size to force multiple blocks
	w, path := createWriter(t, WithSync(false), WithBlockSize(64))

	// Each entry is ~30+ bytes encoded, so block size 64 forces rotation
	for i := range 10 {
		key := putKey("key"+string(rune('a'+i)), uint64(100-i))
		if err := w.Add(key, []byte("some-value-here")); err != nil {
			t.Fatalf("Add entry %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatalf("decodeFooter: %v", err)
	}

	if f.entryCount != 10 {
		t.Fatalf("expected 10 entries, got %d", f.entryCount)
	}
	if f.dataBlockCount <= 1 {
		t.Fatalf("expected multiple data blocks, got %d", f.dataBlockCount)
	}
}

func TestWriter_AllowsOversizedSingleEntry(t *testing.T) {
	w, path := createWriter(t, WithSync(false), WithBlockSize(16))

	// Value much larger than block size — should succeed in an empty block
	bigVal := make([]byte, 256)
	if err := w.Add(putKey("big", 1), bigVal); err != nil {
		t.Fatalf("oversized entry should be accepted: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatalf("decodeFooter: %v", err)
	}
	if f.entryCount != 1 {
		t.Fatalf("expected 1 entry, got %d", f.entryCount)
	}
	if f.dataBlockCount != 1 {
		t.Fatalf("expected 1 data block, got %d", f.dataBlockCount)
	}
}

func TestWriter_DoubleClose(t *testing.T) {
	w, _ := createWriter(t, WithSync(false))

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	err := w.Close()
	if !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("expected ErrWriterClosed on double close, got %v", err)
	}
}

func TestWriter_SyncOnClose(t *testing.T) {
	// Verify it doesn't error — we can't easily verify fsync was called
	w, _ := createWriter(t, WithSync(true))

	if err := w.Add(putKey("key", 1), []byte("val")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// --- Footer layout tests (raw byte inspection, no decodeFooter) ---

func TestWriter_FooterLayout(t *testing.T) {
	w, path := createWriter(t, WithSync(false))

	if err := w.Add(putKey("abc", 5), []byte("xyz")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := readFileBytes(t, path)
	ft := data[len(data)-int(footerSize):]

	// Verify magic
	magic := string(ft[sstMagicOffset : sstMagicOffset+sstMagicSize])
	if magic != sstMagic {
		t.Fatalf("magic: want %q, got %q", sstMagic, magic)
	}

	// Verify version
	version := coding.Uint32(ft[sstVersionOffset : sstVersionOffset+sstVersionSize])
	if version != sstVersion {
		t.Fatalf("version: want %d, got %d", sstVersion, version)
	}

	// Verify checksum covers everything before the checksum field
	storedCRC := coding.Uint32(ft[sstChecksumOffset : sstChecksumOffset+checksumSize])
	computedCRC := checksum.CRC32C(ft[:sstChecksumInputSize])
	if storedCRC != computedCRC {
		t.Fatalf("footer checksum mismatch: stored=0x%08x computed=0x%08x", storedCRC, computedCRC)
	}

	// Verify index offset + size are within file bounds
	indexOffset := coding.Uint64(ft[sstIndexOffsetOffset : sstIndexOffsetOffset+sstIndexOffsetSize])
	indexSize := coding.Uint32(ft[sstIndexSizeOffset : sstIndexSizeOffset+sstIndexSizeSize])
	if indexOffset+uint64(indexSize) > uint64(len(data))-uint64(footerSize) {
		t.Fatal("index block extends past the footer")
	}
}

func TestWriter_BlockChecksumsPresent(t *testing.T) {
	w, path := createWriter(t, WithSync(false), WithBlockSize(64))

	for i := range 5 {
		key := putKey("key"+string(rune('a'+i)), uint64(50-i))
		if err := w.Add(key, []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatal(err)
	}

	// Verify index block checksum
	indexData := data[f.indexOffset : f.indexOffset+uint64(f.indexSize)]
	idxPayload := indexData[:len(indexData)-int(checksumSize)]
	idxStoredCRC := coding.Uint32(indexData[len(indexData)-int(checksumSize):])
	if idxStoredCRC != checksum.CRC32C(idxPayload) {
		t.Fatal("index block checksum mismatch")
	}

	// Parse index entries and verify each data block's checksum
	br := coding.NewByteReader(idxPayload)
	blocksChecked := 0
	for br.Remaining() > 0 {
		keyLen, err := br.ReadUint32()
		if err != nil {
			t.Fatal(err)
		}
		_, err = br.ReadBytes(int(keyLen))
		if err != nil {
			t.Fatal(err)
		}
		blockOffset, err := br.ReadUint64()
		if err != nil {
			t.Fatal(err)
		}
		blockSize, err := br.ReadUint32()
		if err != nil {
			t.Fatal(err)
		}

		block := data[blockOffset : blockOffset+uint64(blockSize)]
		blockPayload := block[:len(block)-int(checksumSize)]
		blockStoredCRC := coding.Uint32(block[len(block)-int(checksumSize):])
		blockComputedCRC := checksum.CRC32C(blockPayload)
		if blockStoredCRC != blockComputedCRC {
			t.Fatalf("block %d checksum mismatch: stored=0x%08x computed=0x%08x",
				blocksChecked, blockStoredCRC, blockComputedCRC)
		}
		blocksChecked++
	}

	if uint32(blocksChecked) != f.dataBlockCount {
		t.Fatalf("checked %d blocks, footer says %d", blocksChecked, f.dataBlockCount)
	}
}

// --- Footer encode/decode round-trip tests ---

func TestFooter_EncodeDecodeRoundTrip(t *testing.T) {
	f := newFooter(12345, 678, 5, 100)
	encoded := f.encode()

	if len(encoded) != int(footerSize) {
		t.Fatalf("encoded footer size: want %d, got %d", footerSize, len(encoded))
	}

	decoded, err := decodeFooter(encoded)
	if err != nil {
		t.Fatalf("decodeFooter: %v", err)
	}

	if decoded.indexOffset != 12345 {
		t.Fatalf("indexOffset: want 12345, got %d", decoded.indexOffset)
	}
	if decoded.indexSize != 678 {
		t.Fatalf("indexSize: want 678, got %d", decoded.indexSize)
	}
	if decoded.dataBlockCount != 5 {
		t.Fatalf("dataBlockCount: want 5, got %d", decoded.dataBlockCount)
	}
	if decoded.entryCount != 100 {
		t.Fatalf("entryCount: want 100, got %d", decoded.entryCount)
	}
}

func TestFooter_DecodeRejectsBadMagic(t *testing.T) {
	f := newFooter(0, 4, 0, 0)
	encoded := f.encode()
	encoded[0] = 'X'

	_, err := decodeFooter(encoded)
	if !errors.Is(err, ErrBadMagic) {
		t.Fatalf("expected ErrBadMagic, got %v", err)
	}
}

func TestFooter_DecodeRejectsBadVersion(t *testing.T) {
	f := newFooter(0, 4, 0, 0)
	encoded := f.encode()
	// Set version to 99, recompute checksum so it passes
	coding.PutUint32(encoded[8:], 99)
	coding.PutUint32(encoded[36:], checksum.CRC32C(encoded[:36]))

	_, err := decodeFooter(encoded)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("expected ErrUnsupportedVersion, got %v", err)
	}
}

func TestFooter_DecodeRejectsBadChecksum(t *testing.T) {
	f := newFooter(0, 4, 0, 0)
	encoded := f.encode()
	// Corrupt a data byte without updating checksum
	encoded[20] ^= 0xFF

	_, err := decodeFooter(encoded)
	if !errors.Is(err, ErrCorruptFooter) {
		t.Fatalf("expected ErrCorruptFooter, got %v", err)
	}
}

func TestFooter_DecodeRejectsWrongSize(t *testing.T) {
	_, err := decodeFooter(make([]byte, 20))
	if !errors.Is(err, ErrCorruptFooter) {
		t.Fatalf("expected ErrCorruptFooter for short data, got %v", err)
	}

	_, err = decodeFooter(make([]byte, 50))
	if !errors.Is(err, ErrCorruptFooter) {
		t.Fatalf("expected ErrCorruptFooter for long data, got %v", err)
	}
}

// --- End-to-end file layout test ---

func TestWriter_FileLayoutOrder(t *testing.T) {
	w, path := createWriter(t, WithSync(false), WithBlockSize(64))

	for i := range 8 {
		key := putKey("key"+string(rune('a'+i)), uint64(80-i))
		if err := w.Add(key, []byte("value-data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatal(err)
	}

	// Index block must come right before the footer
	indexEnd := f.indexOffset + uint64(f.indexSize)
	footerStart := uint64(len(data)) - uint64(footerSize)
	if indexEnd != footerStart {
		t.Fatalf("index block (ends at %d) should be adjacent to footer (starts at %d)",
			indexEnd, footerStart)
	}

	// Parse index entries and verify data blocks are contiguous and in order
	indexPayload := data[f.indexOffset : f.indexOffset+uint64(f.indexSize)-uint64(checksumSize)]
	br := coding.NewByteReader(indexPayload)

	var prevEnd uint64
	blockIdx := 0
	for br.Remaining() > 0 {
		keyLen, err := br.ReadUint32()
		if err != nil {
			t.Fatalf("block %d: reading key length: %v", blockIdx, err)
		}
		if _, err = br.ReadBytes(int(keyLen)); err != nil {
			t.Fatalf("block %d: reading key bytes: %v", blockIdx, err)
		}
		offset, err := br.ReadUint64()
		if err != nil {
			t.Fatalf("block %d: reading offset: %v", blockIdx, err)
		}
		size, err := br.ReadUint32()
		if err != nil {
			t.Fatalf("block %d: reading size: %v", blockIdx, err)
		}

		if offset != prevEnd {
			t.Fatalf("block %d: expected offset %d, got %d (blocks not contiguous)",
				blockIdx, prevEnd, offset)
		}
		prevEnd = offset + uint64(size)
		blockIdx++
	}

	// Last data block should end where the index block starts
	if prevEnd != f.indexOffset {
		t.Fatalf("data blocks end at %d but index starts at %d", prevEnd, f.indexOffset)
	}
}

// --- Randomized round-trip test ---

func TestWriter_RandomizedEntries(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 0)) //nolint:gosec // deterministic seed for reproducible tests

	type entry struct {
		key keys.InternalKey
		val []byte
	}

	// Generate random entries with unique internal keys
	var entries []entry
	seen := make(map[string]bool)
	for len(entries) < 50 {
		userKey := testutil.RandKey(rng, 32)
		seqno := rng.Uint64()

		ik := keys.InternalKey{
			UserKey: userKey,
			Seqno:   seqno,
			Kind:    keys.InternalKeyKindPut,
		}

		sig := string(ik.Encode())
		if seen[sig] {
			continue
		}
		seen[sig] = true

		entries = append(entries, entry{
			key: ik,
			val: testutil.RandValue(rng, 64),
		})
	}

	// Sort by InternalKey order
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key.Compare(entries[j].key) < 0
	})

	// Write
	w, path := createWriter(t, WithSync(false), WithBlockSize(128))
	for i, e := range entries {
		if err := w.Add(e.key, e.val); err != nil {
			t.Fatalf("Add entry %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify footer
	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatal(err)
	}
	if f.entryCount != uint64(len(entries)) {
		t.Fatalf("entry count: want %d, got %d", len(entries), f.entryCount)
	}

	// Verify all data block checksums via index
	indexData := data[f.indexOffset : f.indexOffset+uint64(f.indexSize)]
	idxPayload := indexData[:len(indexData)-int(checksumSize)]
	br := coding.NewByteReader(idxPayload)

	blockIdx := 0
	for br.Remaining() > 0 {
		keyLen, err := br.ReadUint32()
		if err != nil {
			t.Fatalf("block %d: reading key length: %v", blockIdx, err)
		}
		if _, err = br.ReadBytes(int(keyLen)); err != nil {
			t.Fatalf("block %d: reading key bytes: %v", blockIdx, err)
		}
		blockOffset, err := br.ReadUint64()
		if err != nil {
			t.Fatalf("block %d: reading offset: %v", blockIdx, err)
		}
		blockSize, err := br.ReadUint32()
		if err != nil {
			t.Fatalf("block %d: reading size: %v", blockIdx, err)
		}

		block := data[blockOffset : blockOffset+uint64(blockSize)]
		payload := block[:len(block)-int(checksumSize)]
		stored := coding.Uint32(block[len(block)-int(checksumSize):])
		if stored != checksum.CRC32C(payload) {
			t.Fatalf("block %d: checksum mismatch in randomized test", blockIdx)
		}
		blockIdx++
	}
}

func TestWriter_BinaryKeysAndValues(t *testing.T) {
	w, path := createWriter(t, WithSync(false))

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{makeKey("\x00\x00", 3, keys.InternalKeyKindPut), []byte{0xFF, 0xFE, 0xFD}},
		{makeKey("\x00\x01", 2, keys.InternalKeyKindPut), []byte{}},            // empty value
		{makeKey("\x80\x81\x82", 1, keys.InternalKeyKindPut), []byte{0, 0, 0}}, // high-bit key
	}

	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			t.Fatalf("Add(%x): %v", e.key.UserKey, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify footer metadata
	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatal(err)
	}
	if f.entryCount != uint64(len(entries)) {
		t.Fatalf("entryCount = %d, want %d", f.entryCount, len(entries))
	}

	// Verify all data block checksums are valid
	indexData := data[f.indexOffset : f.indexOffset+uint64(f.indexSize)]
	idxPayload := indexData[:len(indexData)-int(checksumSize)]
	br := coding.NewByteReader(idxPayload)

	for br.Remaining() > 0 {
		keyLen, err := br.ReadUint32()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = br.ReadBytes(int(keyLen)); err != nil {
			t.Fatal(err)
		}
		blockOffset, err := br.ReadUint64()
		if err != nil {
			t.Fatal(err)
		}
		blockSize, err := br.ReadUint32()
		if err != nil {
			t.Fatal(err)
		}

		block := data[blockOffset : blockOffset+uint64(blockSize)]
		payload := block[:len(block)-int(checksumSize)]
		stored := coding.Uint32(block[len(block)-int(checksumSize):])
		if stored != checksum.CRC32C(payload) {
			t.Fatal("block checksum mismatch for binary key/value entry")
		}
	}
}

func TestWriter_ConcurrentAdd(t *testing.T) {
	t.Parallel()

	// Writer.Add holds a mutex. Concurrent Adds must not panic or
	// corrupt state. We can't guarantee ordering, so we pre-sort
	// keys and assign each goroutine a non-overlapping slice.
	const keysPerGoroutine = 50
	const numGoroutines = 4
	total := keysPerGoroutine * numGoroutines

	// Pre-generate all keys in sorted order
	allKeys := make([]keys.InternalKey, total)
	for i := range total {
		allKeys[i] = putKey(fmt.Sprintf("key-%06d", i), uint64(total-i)) //nolint:gosec // total-i is always non-negative
	}

	// Since concurrent goroutines can't guarantee Add order, we
	// serialize: each goroutine adds its slice sequentially, but
	// they race on the mutex. Only one goroutine can work at a time
	// because keys must be strictly ordered. Instead, test that
	// concurrent Adds to separate writers on separate files don't panic.
	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines)
	wg.Add(numGoroutines)

	for g := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			dir := t.TempDir()
			path := filepath.Join(dir, "concurrent.sst")
			f, err := os.Create(path) //nolint:gosec // test with temp dir
			if err != nil {
				errCh <- err
				return
			}
			w, err := NewWriter(f, WithSync(false))
			if err != nil {
				errCh <- err
				return
			}
			start := id * keysPerGoroutine
			for i := range keysPerGoroutine {
				if err := w.Add(allKeys[start+i], []byte("val")); err != nil {
					errCh <- fmt.Errorf("goroutine %d, key %d: %w", id, i, err)
					return
				}
			}
			if err := w.Close(); err != nil {
				errCh <- fmt.Errorf("goroutine %d close: %w", id, err)
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestWriter_LargeValue(t *testing.T) {
	w, path := createWriter(t, WithSync(false), WithBlockSize(64))

	// Value much larger than block size
	bigVal := make([]byte, 4096)
	for i := range bigVal {
		bigVal[i] = byte(i % 256) //nolint:gosec // intentional truncation
	}

	if err := w.Add(putKey("big", 1), bigVal); err != nil {
		t.Fatalf("Add with large value: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify footer and block checksum
	data := readFileBytes(t, path)
	f, err := decodeFooter(data[len(data)-int(footerSize):])
	if err != nil {
		t.Fatal(err)
	}
	if f.entryCount != 1 {
		t.Fatalf("entryCount = %d, want 1", f.entryCount)
	}
	if f.dataBlockCount != 1 {
		t.Fatalf("dataBlockCount = %d, want 1", f.dataBlockCount)
	}
}

// --- Benchmark helpers ---

// Value sizes used across benchmarks: small (64B) and large (4KB).
var (
	benchValSmall = make([]byte, 64)
	benchValLarge = make([]byte, 4096)
)

var benchValSizes = []struct {
	name string
	val  []byte
}{
	{"val-64B", benchValSmall},
	{"val-4KB", benchValLarge},
}

func createWriterB(b *testing.B, opts ...WriterOption) (*Writer, string) {
	b.Helper()
	dir := b.TempDir()
	path := filepath.Join(dir, "bench.sst")
	f, err := os.Create(path) //nolint:gosec // bench helper with temp dir
	if err != nil {
		b.Fatal(err)
	}
	w, err := NewWriter(f, opts...)
	if err != nil {
		b.Fatal(err)
	}
	return w, path
}

// writeBenchSSTable writes an SSTable with count sorted entries and returns
// the path. Keys are formatted as "key-XXXXXXXX" with descending seqnos.
func writeBenchSSTable(b *testing.B, count int, val []byte) string {
	b.Helper()
	w, path := createWriterB(b, WithSync(false))
	for i := range count {
		k := putKey(fmt.Sprintf("key-%08d", i), uint64(count-i)) //nolint:gosec // count-i is always non-negative
		if err := w.Add(k, val); err != nil {
			b.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	return path
}

func openReaderB(b *testing.B, path string) *Reader {
	b.Helper()
	f, err := os.Open(path) //nolint:gosec // bench helper with temp dir
	if err != nil {
		b.Fatal(err)
	}
	r, err := OpenReader(f)
	if err != nil {
		b.Fatal(err)
	}
	return r
}

// --- Writer benchmarks ---

func BenchmarkBlockBuilder_Add(b *testing.B) {
	key := putKey("benchmark-key", 1)
	for _, vs := range benchValSizes {
		b.Run(vs.name, func(b *testing.B) {
			bb := newBlockBuilder()
			bb.Add(key, vs.val)
			bb.Reset()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				bb.Add(key, vs.val)
			}
		})
	}
}

func BenchmarkBlockBuilder_Finish(b *testing.B) {
	for _, vs := range benchValSizes {
		b.Run(vs.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				bb := newBlockBuilder()
				for i := range 10 {
					bb.Add(putKey(fmt.Sprintf("k%02d", i), uint64(100-i)), vs.val)
				}
				_ = bb.Finish()
			}
		})
	}
}

func BenchmarkWriter_Add(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"10-entries", 10},
		{"100-entries", 100},
		{"1000-entries", 1000},
	}
	for _, vs := range benchValSizes {
		for _, s := range counts {
			ks := make([]keys.InternalKey, s.count)
			for i := range s.count {
				ks[i] = putKey(fmt.Sprintf("key-%08d", i), uint64(s.count-i)) //nolint:gosec // count-i is always non-negative
			}
			b.Run(vs.name+"/"+s.name, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					b.StopTimer()
					w, _ := createWriterB(b, WithSync(false))
					b.StartTimer()
					for _, k := range ks {
						_ = w.Add(k, vs.val)
					}
					b.StopTimer()
					_ = w.Close()
					b.StartTimer()
				}
			})
		}
	}
}

func BenchmarkWriter_Close(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"100-entries", 100},
		{"1000-entries", 1000},
	}
	for _, vs := range benchValSizes {
		for _, s := range counts {
			ks := make([]keys.InternalKey, s.count)
			for i := range s.count {
				ks[i] = putKey(fmt.Sprintf("key-%08d", i), uint64(s.count-i)) //nolint:gosec // count-i is always non-negative
			}
			b.Run(vs.name+"/"+s.name, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					b.StopTimer()
					w, _ := createWriterB(b, WithSync(false))
					for _, k := range ks {
						_ = w.Add(k, vs.val)
					}
					b.StartTimer()
					_ = w.Close()
				}
			})
		}
	}
}

func BenchmarkFooter_Encode(b *testing.B) {
	f := newFooter(12345, 678, 10, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = f.encode()
	}
}

func BenchmarkFooter_Decode(b *testing.B) {
	f := newFooter(12345, 678, 10, 1000)
	encoded := f.encode()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = decodeFooter(encoded)
	}
}

// --- Index codec benchmarks ---

func BenchmarkBuildIndexBlock(b *testing.B) {
	counts := []int{10, 100, 1000}
	for _, n := range counts {
		entries := make([]indexEntry, n)
		for i := range n {
			entries[i] = indexEntry{
				lastKey: putKey(fmt.Sprintf("key-%08d", i), 1),
				offset:  uint64(i) * 4096,
				size:    4096,
			}
		}
		b.Run(fmt.Sprintf("%d-entries", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = buildIndexBlock(entries)
			}
		})
	}
}

func BenchmarkDecodeIndexBlock(b *testing.B) {
	counts := []int{10, 100, 1000}
	for _, n := range counts {
		entries := make([]indexEntry, n)
		for i := range n {
			entries[i] = indexEntry{
				lastKey: putKey(fmt.Sprintf("key-%08d", i), 1),
				offset:  uint64(i) * 4096,
				size:    4096,
			}
		}
		encoded := buildIndexBlock(entries)
		b.Run(fmt.Sprintf("%d-entries", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, _ = decodeIndexBlock(encoded)
			}
		})
	}
}

// --- Block decode benchmark ---

func BenchmarkDecodeBlockEntries(b *testing.B) {
	for _, vs := range benchValSizes {
		bb := newBlockBuilder()
		for i := range 50 {
			bb.Add(putKey(fmt.Sprintf("key-%04d", i), uint64(50-i)), vs.val)
		}
		raw := bb.Finish()
		payload, err := verifyBlockChecksum(raw)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(vs.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, _ = decodeBlockEntries(payload)
			}
		})
	}
}

// --- Reader benchmarks ---

func BenchmarkReader_Open(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"100-entries", 100},
		{"1000-entries", 1000},
		{"10000-entries", 10000},
	}
	for _, s := range counts {
		path := writeBenchSSTable(b, s.count, benchValSmall)
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				f, err := os.Open(path) //nolint:gosec // bench helper
				if err != nil {
					b.Fatal(err)
				}
				r, err := OpenReader(f)
				if err != nil {
					b.Fatal(err)
				}
				r.Close()
			}
		})
	}
}

func BenchmarkReader_Get(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"100-entries", 100},
		{"1000-entries", 1000},
		{"10000-entries", 10000},
	}
	for _, vs := range benchValSizes {
		for _, s := range counts {
			path := writeBenchSSTable(b, s.count, vs.val)
			r := openReaderB(b, path)

			// Pre-compute a lookup key in the middle of the table
			hitKey := fmt.Appendf(nil, "key-%08d", s.count/2)
			missKey := []byte("key-zzzzzzzz")
			seqno := uint64(s.count) //nolint:gosec // bench count is small

			b.Run(vs.name+"/"+s.name+"/hit", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_, _ = r.Get(hitKey, seqno)
				}
			})
			b.Run(vs.name+"/"+s.name+"/miss", func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_, _ = r.Get(missKey, seqno)
				}
			})

			r.Close()
		}
	}
}

// --- Iterator benchmarks ---

func BenchmarkIterator_FullScan(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"100-entries", 100},
		{"1000-entries", 1000},
		{"10000-entries", 10000},
	}
	for _, vs := range benchValSizes {
		for _, s := range counts {
			path := writeBenchSSTable(b, s.count, vs.val)
			r := openReaderB(b, path)

			b.Run(vs.name+"/"+s.name, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					it := r.NewIterator()
					it.SeekToFirst()
					for it.Valid() {
						_ = it.Key()
						_ = it.Value()
						it.Next()
					}
				}
			})

			r.Close()
		}
	}
}

func BenchmarkIterator_SeekToFirst(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"100-entries", 100},
		{"1000-entries", 1000},
	}
	for _, s := range counts {
		path := writeBenchSSTable(b, s.count, benchValSmall)
		r := openReaderB(b, path)

		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				it := r.NewIterator()
				it.SeekToFirst()
			}
		})

		r.Close()
	}
}

func BenchmarkIterator_Seek(b *testing.B) {
	counts := []struct {
		name  string
		count int
	}{
		{"100-entries", 100},
		{"1000-entries", 1000},
		{"10000-entries", 10000},
	}
	for _, s := range counts {
		path := writeBenchSSTable(b, s.count, benchValSmall)
		r := openReaderB(b, path)

		// Pre-compute seek targets spread across the table
		targets := make([][]byte, 100)
		for i := range targets {
			idx := (i * s.count) / len(targets)
			targets[i] = fmt.Appendf(nil, "key-%08d", idx)
		}

		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				it := r.NewIterator()
				it.Seek(targets[i%len(targets)])
			}
		})

		r.Close()
	}
}

// --- AllocsPerRun tests ---

func TestBlockBuilder_Add_AllocsPerRun(t *testing.T) {
	bb := newBlockBuilder()
	key := putKey("bench-key", 1)
	val := make([]byte, 64)
	bb.Add(key, val)
	bb.Reset()

	allocs := testing.AllocsPerRun(100, func() {
		bb.Add(key, val)
	})
	// key.Encode() allocates exactly 1 []byte — unavoidable without a scratch buffer.
	if allocs > 1 {
		t.Errorf("BlockBuilder.Add: expected ≤1 alloc (key.Encode), got %v", allocs)
	}
}

func TestBlockBuilder_Reset_NoAllocs(t *testing.T) {
	bb := newBlockBuilder()
	bb.Add(putKey("key", 1), []byte("val"))

	allocs := testing.AllocsPerRun(100, func() {
		bb.Reset()
	})
	if allocs != 0 {
		t.Errorf("BlockBuilder.Reset: expected 0 allocs, got %v", allocs)
	}
}

func TestFooter_Encode_AllocsPerRun(t *testing.T) {
	f := newFooter(12345, 678, 10, 1000)
	allocs := testing.AllocsPerRun(100, func() {
		_ = f.encode()
	})
	// encode allocates one []byte for the buffer
	if allocs > 1 {
		t.Errorf("Footer.encode: expected ≤1 alloc, got %v", allocs)
	}
}

func TestFooter_Decode_AllocsPerRun(t *testing.T) {
	f := newFooter(12345, 678, 10, 1000)
	encoded := f.encode()
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = decodeFooter(encoded)
	})
	if allocs != 0 {
		t.Errorf("Footer.decode: expected 0 allocs, got %v", allocs)
	}
}

func TestDecodeBlockEntries_AllocsPerRun(t *testing.T) {
	bb := newBlockBuilder()
	for i := range 10 {
		bb.Add(putKey(fmt.Sprintf("key-%02d", i), uint64(10-i)), make([]byte, 32))
	}
	raw := bb.Finish()
	payload, err := verifyBlockChecksum(raw)
	if err != nil {
		t.Fatal(err)
	}

	allocs := testing.AllocsPerRun(100, func() {
		_, _ = decodeBlockEntries(payload)
	})
	// Each entry decodes a key (1 alloc for InternalKey.UserKey via DecodeInternalKey)
	// plus the entries slice itself. Exact count depends on implementation, but should
	// scale linearly with entry count, not quadratically.
	t.Logf("decodeBlockEntries (10 entries): %.1f allocs", allocs)
}

func TestReader_Get_AllocsPerRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alloc.sst")

	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, 100)
	for i := range 100 {
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{putKey(fmt.Sprintf("key-%04d", i), uint64(100-i)), make([]byte, 32)})
	}
	writeSSTable(t, path, entries)
	r := openReader(t, path)
	defer r.Close()

	lookupKey := []byte("key-0050")
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = r.Get(lookupKey, 100)
	})
	// Get allocates: block read buffer, decoded entries, value copy
	t.Logf("Reader.Get (100 entries, hit): %.1f allocs", allocs)
}

func TestIterator_Next_AllocsPerRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alloc.sst")

	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, 100)
	for i := range 100 {
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{putKey(fmt.Sprintf("key-%04d", i), uint64(100-i)), make([]byte, 32)})
	}
	writeSSTable(t, path, entries)
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	it.SeekToFirst()
	// Skip first entry so we're mid-block
	it.Next()

	allocs := testing.AllocsPerRun(100, func() {
		// Within-block Next should be zero-alloc
		if it.Valid() {
			it.Next()
		} else {
			// Reset if exhausted
			it.SeekToFirst()
			it.Next()
		}
	})
	// Within-block Next reads from the already-decoded entries slice
	t.Logf("Iterator.Next (within block): %.1f allocs", allocs)
}
