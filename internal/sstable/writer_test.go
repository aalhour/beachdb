package sstable

import (
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
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
	payload := finished[:len(finished)-checksumSize]
	storedCRC := coding.Uint32(finished[len(finished)-checksumSize:])
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
	payload := finished[:len(finished)-checksumSize]

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
	payload := finished[:len(finished)-checksumSize]
	storedCRC := coding.Uint32(finished[len(finished)-checksumSize:])
	if storedCRC != checksum.CRC32C(payload) {
		t.Fatal("checksum mismatch for binary key/value")
	}
}

func TestBlockBuilder_EmptyValue(t *testing.T) {
	b := newBlockBuilder()
	b.Add(putKey("key", 1), []byte{})

	finished := b.Finish()
	payload := finished[:len(finished)-checksumSize]

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
	footerData := data[len(data)-footerSize:]
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
	f, err := decodeFooter(data[len(data)-footerSize:])
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
	f, err := decodeFooter(data[len(data)-footerSize:])
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
	f, err := decodeFooter(data[len(data)-footerSize:])
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
	ft := data[len(data)-footerSize:]

	// Verify magic (bytes 0-7)
	magic := string(ft[0:8])
	if magic != sstMagic {
		t.Fatalf("magic: want %q, got %q", sstMagic, magic)
	}

	// Verify version (bytes 8-11)
	version := coding.Uint32(ft[8:])
	if version != sstVersion {
		t.Fatalf("version: want %d, got %d", sstVersion, version)
	}

	// Verify checksum (bytes 36-39 cover bytes 0-35)
	storedCRC := coding.Uint32(ft[36:])
	computedCRC := checksum.CRC32C(ft[:36])
	if storedCRC != computedCRC {
		t.Fatalf("footer checksum mismatch: stored=0x%08x computed=0x%08x", storedCRC, computedCRC)
	}

	// Verify index offset + size are within file bounds
	indexOffset := coding.Uint64(ft[12:])
	indexSize := coding.Uint32(ft[20:])
	if indexOffset+uint64(indexSize) > uint64(len(data))-footerSize {
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
	f, err := decodeFooter(data[len(data)-footerSize:])
	if err != nil {
		t.Fatal(err)
	}

	// Verify index block checksum
	indexData := data[f.indexOffset : f.indexOffset+uint64(f.indexSize)]
	idxPayload := indexData[:len(indexData)-checksumSize]
	idxStoredCRC := coding.Uint32(indexData[len(indexData)-checksumSize:])
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
		blockPayload := block[:len(block)-checksumSize]
		blockStoredCRC := coding.Uint32(block[len(block)-checksumSize:])
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

	if len(encoded) != footerSize {
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
	f, err := decodeFooter(data[len(data)-footerSize:])
	if err != nil {
		t.Fatal(err)
	}

	// Index block must come right before the footer
	indexEnd := f.indexOffset + uint64(f.indexSize)
	footerStart := uint64(len(data)) - footerSize
	if indexEnd != footerStart {
		t.Fatalf("index block (ends at %d) should be adjacent to footer (starts at %d)",
			indexEnd, footerStart)
	}

	// Parse index entries and verify data blocks are contiguous and in order
	indexPayload := data[f.indexOffset : f.indexOffset+uint64(f.indexSize)-checksumSize]
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
	f, err := decodeFooter(data[len(data)-footerSize:])
	if err != nil {
		t.Fatal(err)
	}
	if f.entryCount != uint64(len(entries)) {
		t.Fatalf("entry count: want %d, got %d", len(entries), f.entryCount)
	}

	// Verify all data block checksums via index
	indexData := data[f.indexOffset : f.indexOffset+uint64(f.indexSize)]
	idxPayload := indexData[:len(indexData)-checksumSize]
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
		payload := block[:len(block)-checksumSize]
		stored := coding.Uint32(block[len(block)-checksumSize:])
		if stored != checksum.CRC32C(payload) {
			t.Fatalf("block %d: checksum mismatch in randomized test", blockIdx)
		}
		blockIdx++
	}
}
