package sstable

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/testutil"
	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// --- helpers ---

// writeSSTable creates an SSTable at path with the given key-value pairs.
// Keys must be pre-sorted in internal key order.
func writeSSTable(t *testing.T, path string, entries []struct {
	key   keys.InternalKey
	value []byte
}, opts ...WriterOption) {
	t.Helper()
	f, err := os.Create(path) //nolint:gosec // test helper with temp dir
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(f, opts...)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// openReader is a test helper that opens an SSTable reader at path.
func openReader(t *testing.T, path string) *Reader {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test helper with temp dir
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// --- OpenReader tests ---

func TestReader_OpenEmptyTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.sst")

	// Write an empty SSTable (no entries, just footer)
	writeSSTable(t, path, nil)

	r := openReader(t, path)
	defer r.Close()

	if len(r.index) != 0 {
		t.Fatalf("expected 0 index entries, got %d", len(r.index))
	}
}

func TestReader_OpenNilFile(t *testing.T) {
	_, err := OpenReader(nil)
	if !errors.Is(err, ErrNilFile) {
		t.Fatalf("expected ErrNilFile, got %v", err)
	}
}

func TestReader_FileTooSmall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.sst")

	// Write fewer bytes than footerSize
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, err = OpenReader(f)
	if !errors.Is(err, ErrFileTooSmall) {
		t.Fatalf("expected ErrFileTooSmall, got %v", err)
	}
}

func TestReader_BadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_magic.sst")

	// Write a valid SSTable
	writeSSTable(t, path, nil)

	// Corrupt the magic bytes (first 8 bytes of footer, which is last 40 bytes of file)
	data, err := os.ReadFile(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}

	footerStart := len(data) - int(footerSize)
	data[footerStart] = 0xFF // corrupt magic

	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test with temp dir
		t.Fatal(err)
	}

	f, err := os.Open(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, err = OpenReader(f)
	if !errors.Is(err, ErrBadMagic) {
		t.Fatalf("expected ErrBadMagic, got %v", err)
	}
}

func TestReader_BadVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_version.sst")

	writeSSTable(t, path, nil)

	data, err := os.ReadFile(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}

	// Version is at offset 8 within the footer (4 bytes, after 8-byte magic)
	footerStart := len(data) - int(footerSize)
	versionOffset := footerStart + 8

	// Write an unsupported version and recompute the footer checksum
	coding.PutUint32(data[versionOffset:], 99)
	crc := checksum.CRC32C(data[footerStart : footerStart+36])
	coding.PutUint32(data[footerStart+36:], crc)

	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test with temp dir
		t.Fatal(err)
	}

	f, err := os.Open(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, err = OpenReader(f)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("expected ErrUnsupportedVersion, got %v", err)
	}
}

func TestReader_BadFooterChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_footer_crc.sst")

	writeSSTable(t, path, nil)

	data, err := os.ReadFile(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}

	// Flip a byte in the footer body (not magic or checksum) to trigger checksum mismatch
	footerStart := len(data) - int(footerSize)
	data[footerStart+20] ^= 0xFF

	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test with temp dir
		t.Fatal(err)
	}

	f, err := os.Open(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, err = OpenReader(f)
	if !errors.Is(err, ErrCorruptFooter) {
		t.Fatalf("expected ErrCorruptFooter, got %v", err)
	}
}

func TestReader_CorruptIndexBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_index.sst")

	// Write an SSTable with at least one entry so it has a non-empty index
	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("val-a")},
	}
	writeSSTable(t, path, entries)

	data, err := os.ReadFile(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}

	// Decode footer to find where the index block is
	footerBytes := data[len(data)-int(footerSize):]
	ft, err := decodeFooter(footerBytes)
	if err != nil {
		t.Fatal(err)
	}

	// Flip a byte in the index block body (before its checksum)
	indexStart := int(ft.indexOffset) //nolint:gosec // G115: test file, offset is small
	data[indexStart] ^= 0xFF

	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test with temp dir
		t.Fatal(err)
	}

	f, err := os.Open(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, err = OpenReader(f)
	if !errors.Is(err, ErrIndexChecksumMismatch) {
		t.Fatalf("expected ErrIndexChecksumMismatch, got %v", err)
	}
}

func TestReader_Close(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "close.sst")

	writeSSTable(t, path, nil)

	r := openReader(t, path)

	if err := r.Close(); err != nil {
		t.Fatalf("first Close should succeed: %v", err)
	}

	// Double close should return ErrReaderClosed
	err := r.Close()
	if !errors.Is(err, ErrReaderClosed) {
		t.Fatalf("expected ErrReaderClosed on double close, got %v", err)
	}
}

func TestReader_CloseNilReader(t *testing.T) {
	var r *Reader
	err := r.Close()
	if !errors.Is(err, ErrReaderClosed) {
		t.Fatalf("expected ErrReaderClosed for nil reader, got %v", err)
	}
}

// --- Block reading and entry decoding tests ---

func TestReader_BlockChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad_block.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("key1", 1), []byte("val1")},
	}
	writeSSTable(t, path, entries)

	data, err := os.ReadFile(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}

	// Flip a byte in the first data block (at offset 0)
	data[0] ^= 0xFF

	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test with temp dir
		t.Fatal(err)
	}

	// Open should succeed (only footer + index are read on open)
	r := openReader(t, path)
	defer r.Close()

	// Get should fail when it tries to read the corrupt block
	_, err = r.Get([]byte("key1"), 1)
	if !errors.Is(err, ErrBlockChecksumMismatch) {
		t.Fatalf("expected ErrBlockChecksumMismatch, got %v", err)
	}
}

// --- Point lookup tests ---

// Spec: "TestReader_GetSingleVersion"
func TestReader_GetSingleVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "single.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("apple", 3), []byte("red")},
		{putKey("banana", 2), []byte("yellow")},
		{putKey("cherry", 1), []byte("dark-red")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	for _, e := range entries {
		val, err := r.Get(e.key.UserKey, e.key.Seqno)
		if err != nil {
			t.Fatalf("Get(%s): %v", e.key.UserKey, err)
		}
		if string(val) != string(e.value) {
			t.Fatalf("Get(%s) = %q, want %q", e.key.UserKey, val, e.value)
		}
	}
}

func TestReader_GetTombstone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tombstone.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{deleteKey("gone", 1), nil},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	_, err := r.Get([]byte("gone"), 1)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("expected ErrKeyDeleted for tombstone, got %v", err)
	}
}

func TestReader_GetMultipleVersions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "versions.sst")

	// Internal keys sort by (userKey ASC, seqno DESC)
	// So seqno=10 comes before seqno=5 for the same user key
	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("foo", 10), []byte("new-value")},
		{putKey("foo", 5), []byte("old-value")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	// Get at seqno=10 should see the newest version
	val, err := r.Get([]byte("foo"), 10)
	if err != nil {
		t.Fatalf("Get(foo, 10): %v", err)
	}
	if string(val) != "new-value" {
		t.Fatalf("Get(foo, 10) = %q, want %q", val, "new-value")
	}

	// Get at seqno=7 should see seqno=5's value (newest visible)
	val, err = r.Get([]byte("foo"), 7)
	if err != nil {
		t.Fatalf("Get(foo, 7): %v", err)
	}
	if string(val) != "old-value" {
		t.Fatalf("Get(foo, 7) = %q, want %q", val, "old-value")
	}

	// Get at seqno=3 should see nothing (both versions are newer)
	_, err = r.Get([]byte("foo"), 3)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Get(foo, 3): expected ErrKeyNotFound, got %v", err)
	}
}

func TestReader_GetAcrossBlockBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cross_block.sst")

	// Use a tiny block size to force versions of the same key across blocks.
	// Each entry is ~30+ bytes, so a 64-byte block size means roughly
	// 1-2 entries per block.
	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("key", 10), []byte("v10")},
		{putKey("key", 8), []byte("v8")},
		{putKey("key", 5), []byte("v5")},
		{putKey("key", 2), []byte("v2")},
	}
	writeSSTable(t, path, entries, WithBlockSize(64))

	r := openReader(t, path)
	defer r.Close()

	// Verify we have multiple blocks
	if len(r.index) < 2 {
		t.Fatalf("expected multiple blocks with small block size, got %d", len(r.index))
	}

	// Get at seqno=10 should find v10 (in the first block)
	val, err := r.Get([]byte("key"), 10)
	if err != nil {
		t.Fatalf("Get(key, 10): %v", err)
	}
	if string(val) != "v10" {
		t.Fatalf("Get(key, 10) = %q, want %q", val, "v10")
	}

	// Get at seqno=6 should find v5 (may be in a later block)
	val, err = r.Get([]byte("key"), 6)
	if err != nil {
		t.Fatalf("Get(key, 6): %v", err)
	}
	if string(val) != "v5" {
		t.Fatalf("Get(key, 6) = %q, want %q", val, "v5")
	}

	// Get at seqno=2 should find v2 (last version, possibly last block)
	val, err = r.Get([]byte("key"), 2)
	if err != nil {
		t.Fatalf("Get(key, 2): %v", err)
	}
	if string(val) != "v2" {
		t.Fatalf("Get(key, 2) = %q, want %q", val, "v2")
	}

	// Get at seqno=1 should miss (all versions are newer)
	_, err = r.Get([]byte("key"), 1)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Get(key, 1): expected ErrKeyNotFound, got %v", err)
	}
}

func TestReader_GetMiss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "miss.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("aaa", 1), []byte("val-a")},
		{putKey("ccc", 1), []byte("val-c")},
		{putKey("eee", 1), []byte("val-e")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	// Key that doesn't exist (between existing keys)
	_, err := r.Get([]byte("bbb"), 1)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for missing key, got %v", err)
	}

	// Key smaller than all entries
	_, err = r.Get([]byte("000"), 1)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for key before all entries, got %v", err)
	}

	// Key larger than all entries
	_, err = r.Get([]byte("zzz"), 1)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for key after all entries, got %v", err)
	}
}

func TestReader_GetEmptyTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.sst")

	writeSSTable(t, path, nil)

	r := openReader(t, path)
	defer r.Close()

	_, err := r.Get([]byte("anything"), 1)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for empty table, got %v", err)
	}
}

func TestReader_GetValueIsCopied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "copy.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("key", 1), []byte("original")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	val1, err := r.Get([]byte("key"), 1)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate the returned value
	val1[0] = 'X'

	// Read again — should still return the original
	val2, err := r.Get([]byte("key"), 1)
	if err != nil {
		t.Fatal(err)
	}

	if string(val2) != "original" {
		t.Fatalf("value was aliased, got %q after mutation", val2)
	}
}

func TestReader_GetTombstoneHidesOlderPut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tombstone_hides.sst")

	// Delete at seqno=5 should hide the put at seqno=3
	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{deleteKey("key", 5), nil},
		{putKey("key", 3), []byte("old-value")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	// At seqno=5, the delete is visible
	_, err := r.Get([]byte("key"), 5)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("expected ErrKeyDeleted (tombstone), got %v", err)
	}

	// At seqno=10, the delete is still the newest visible version
	_, err = r.Get([]byte("key"), 10)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("expected ErrKeyDeleted (tombstone at higher seqno), got %v", err)
	}
}

func TestReader_GetManyKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "many.sst")

	const numKeys = 200
	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, numKeys)
	for i := range numKeys {
		k := putKey(string(keyForIndex(i)), uint64(i+1))
		v := []byte(string(valForIndex(i)))
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{k, v})
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	// Verify every key
	for i := range numKeys {
		ukey := keyForIndex(i)
		val, err := r.Get(ukey, uint64(i+1))
		if err != nil {
			t.Fatalf("Get(key_%04d): %v", i, err)
		}
		expected := valForIndex(i)
		if string(val) != string(expected) {
			t.Fatalf("Get(key_%04d) = %q, want %q", i, val, expected)
		}
	}
}

func TestReader_GetBinaryKeysAndValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("\x00\x00", 1), []byte{0xFF, 0xFE, 0xFD}},
		{putKey("\x00\x01", 1), []byte{}},            // empty value
		{putKey("\x80\x81\x82", 1), []byte{0, 0, 0}}, // high-bit key
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	for _, e := range entries {
		val, err := r.Get(e.key.UserKey, 1)
		if err != nil {
			t.Fatalf("Get(%x): %v", e.key.UserKey, err)
		}
		if string(val) != string(e.value) {
			t.Fatalf("Get(%x) = %x, want %x", e.key.UserKey, val, e.value)
		}
	}
}

func TestReader_GetAfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "closed_get.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("key", 1), []byte("val")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// Get on a closed reader must not panic
	_, err := r.Get([]byte("key"), 1)
	if err == nil {
		t.Fatal("expected error from Get after Close")
	}
}

func TestReader_MultiBlockDistinctKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multiblock.sst")

	// Small block size forces each key into its own block
	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("alpha", 1), []byte("v-alpha")},
		{putKey("bravo", 1), []byte("v-bravo")},
		{putKey("charlie", 1), []byte("v-charlie")},
		{putKey("delta", 1), []byte("v-delta")},
		{putKey("echo", 1), []byte("v-echo")},
	}
	writeSSTable(t, path, entries, WithBlockSize(64))

	r := openReader(t, path)
	defer r.Close()

	if len(r.index) < 2 {
		t.Fatalf("expected multiple blocks, got %d", len(r.index))
	}

	// Every key should be reachable via binary search
	for _, e := range entries {
		val, err := r.Get(e.key.UserKey, 1)
		if err != nil {
			t.Fatalf("Get(%s): %v", e.key.UserKey, err)
		}
		if string(val) != string(e.value) {
			t.Fatalf("Get(%s) = %q, want %q", e.key.UserKey, val, e.value)
		}
	}

	// Keys between existing keys should miss
	_, err := r.Get([]byte("azure"), 1)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound for missing key between blocks, got %v", err)
	}
}

func TestReader_FooterMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("1")},
		{putKey("b", 1), []byte("2")},
		{putKey("c", 1), []byte("3")},
	}
	writeSSTable(t, path, entries, WithBlockSize(64))

	r := openReader(t, path)
	defer r.Close()

	if r.footer.entryCount != 3 {
		t.Fatalf("footer.entryCount = %d, want 3", r.footer.entryCount)
	}
	if r.footer.dataBlockCount != uint32(len(r.index)) { //nolint:gosec // G115: test, index is small
		t.Fatalf("footer.dataBlockCount = %d, but len(index) = %d",
			r.footer.dataBlockCount, len(r.index))
	}
	if r.footer.dataBlockCount < 1 {
		t.Fatal("expected at least 1 data block")
	}
}

// ---------------------------------------------------------------------------
// Metadata accessor tests
// ---------------------------------------------------------------------------

func TestReader_EntryCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entrycount.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 3), []byte("v1")},
		{putKey("b", 2), []byte("v2")},
		{putKey("c", 1), []byte("v3")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	if got := r.EntryCount(); got != 3 {
		t.Fatalf("EntryCount() = %d, want 3", got)
	}
}

func TestReader_EntryCount_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.sst")

	writeSSTable(t, path, nil)

	r := openReader(t, path)
	defer r.Close()

	if got := r.EntryCount(); got != 0 {
		t.Fatalf("EntryCount() = %d, want 0", got)
	}
}

func TestReader_DataBlockCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocks.sst")

	// Use a tiny block size so 3 entries span multiple blocks
	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("aaa", 3), []byte("value-1")},
		{putKey("bbb", 2), []byte("value-2")},
		{putKey("ccc", 1), []byte("value-3")},
	}
	writeSSTable(t, path, entries, WithBlockSize(32))

	r := openReader(t, path)
	defer r.Close()

	if got := r.DataBlockCount(); got < 2 {
		t.Fatalf("DataBlockCount() = %d, want at least 2 with block size 32", got)
	}
}

func TestReader_IndexOffsetAndSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("x", 2), []byte("val-x")},
		{putKey("y", 1), []byte("val-y")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	// The index block must sit after all data blocks and before the footer
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	indexEnd := r.IndexOffset() + uint64(r.IndexSize())
	footerStart := uint64(fi.Size()) - uint64(footerSize) //nolint:gosec // test file, size always > footer
	if indexEnd != footerStart {
		t.Fatalf("index block ends at %d but footer starts at %d — gap or overlap", indexEnd, footerStart)
	}

	if r.IndexSize() == 0 {
		t.Fatal("IndexSize() = 0, want > 0")
	}
}

func TestReader_BlockInfo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blockinfo.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("aaa", 3), []byte("v1")},
		{putKey("bbb", 2), []byte("v2")},
		{putKey("ccc", 1), []byte("v3")},
	}
	writeSSTable(t, path, entries, WithBlockSize(32))

	r := openReader(t, path)
	defer r.Close()

	n := r.DataBlockCount()
	if n < 2 {
		t.Fatalf("need at least 2 blocks for this test, got %d", n)
	}

	// Block offsets must be strictly increasing
	var prevEnd uint64
	for i := range n {
		lastKey, offset, size := r.BlockInfo(int(i))
		if size == 0 {
			t.Fatalf("Block %d: size = 0", i)
		}
		if offset < prevEnd {
			t.Fatalf("Block %d: offset %d overlaps previous block ending at %d", i, offset, prevEnd)
		}
		if len(lastKey.UserKey) == 0 {
			t.Fatalf("Block %d: lastKey has empty UserKey", i)
		}
		prevEnd = offset + uint64(size)
	}

	// Last block's last key should be the last entry we wrote
	lastKey, _, _ := r.BlockInfo(int(n - 1))
	if string(lastKey.UserKey) != "ccc" {
		t.Fatalf("last block lastKey = %q, want %q", lastKey.UserKey, "ccc")
	}
}

func TestReader_BlockInfo_PanicOutOfRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panic.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("a", 1), []byte("v")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic for out-of-range BlockInfo, got none")
		}
	}()

	// This should panic — index only has DataBlockCount() entries
	r.BlockInfo(int(r.DataBlockCount()))
}

func TestReader_TruncatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("key", 1), []byte("value")},
	}
	writeSSTable(t, path, entries)

	// Read the file, then truncate it so the data block is cut short
	// but the footer is still present (rewrite with truncated data + original footer)
	data, err := os.ReadFile(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}

	ft := data[len(data)-int(footerSize):]
	// Keep only half the data block bytes + the full footer
	halfData := len(data)/2 - int(footerSize)
	halfData = max(halfData, 1)
	truncated := append(data[:halfData], ft...)
	if err := os.WriteFile(path, truncated, 0o600); err != nil { //nolint:gosec // test with temp dir
		t.Fatal(err)
	}

	f, err := os.Open(path) //nolint:gosec // test with temp dir
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// OpenReader may fail (index is now at wrong offset) or Get may fail
	// Either way, we must not panic
	r, openErr := OpenReader(f)
	if openErr != nil {
		return // acceptable — corrupt file rejected at open
	}
	defer r.Close()

	_, getErr := r.Get([]byte("key"), 1)
	if getErr == nil {
		t.Fatal("expected error reading from truncated file")
	}
}

// --- helpers for generating sorted keys ---

func keyForIndex(i int) []byte {
	return fmt.Appendf(nil, "key_%04d", i)
}

func valForIndex(i int) []byte {
	return fmt.Appendf(nil, "val_%04d", i)
}

// --- Concurrent access tests ---

func TestReader_ConcurrentGetsSameReader(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.sst")

	const numKeys = 100
	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, numKeys)
	for i := range numKeys {
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{putKey(string(keyForIndex(i)), uint64(i+1)), valForIndex(i)})
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)
	defer r.Close()

	// Launch many goroutines all reading from the same reader
	const numGoroutines = 20
	errCh := make(chan error, numGoroutines*numKeys)

	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for g := range numGoroutines {
		go func(id int) {
			defer wg.Done()
			// Each goroutine reads all keys
			for i := range numKeys {
				val, err := r.Get(keyForIndex(i), uint64(i+1))
				if err != nil {
					errCh <- fmt.Errorf(
						"goroutine %d: Get(key_%04d): %w", id, i, err,
					)
					return
				}
				expected := valForIndex(i)
				if string(val) != string(expected) {
					errCh <- fmt.Errorf(
						"goroutine %d: Get(key_%04d) = %q, want %q",
						id, i, val, expected,
					)
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

func TestReader_ConcurrentReadersOnSameFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "shared.sst")

	const numKeys = 50
	entries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, 0, numKeys)
	for i := range numKeys {
		entries = append(entries, struct {
			key   keys.InternalKey
			value []byte
		}{putKey(string(keyForIndex(i)), uint64(i+1)), valForIndex(i)})
	}
	writeSSTable(t, path, entries)

	// Open multiple independent readers on the same file
	const numReaders = 10
	readers := make([]*Reader, numReaders)
	for i := range numReaders {
		readers[i] = openReader(t, path)
	}
	defer func() {
		for _, rd := range readers {
			rd.Close()
		}
	}()

	errCh := make(chan error, numReaders*numKeys)

	var wg sync.WaitGroup
	wg.Add(numReaders)
	for ri := range numReaders {
		go func(readerIdx int) {
			defer wg.Done()
			rd := readers[readerIdx]
			for i := range numKeys {
				val, err := rd.Get(keyForIndex(i), uint64(i+1))
				if err != nil {
					errCh <- fmt.Errorf(
						"reader %d: Get(key_%04d): %w",
						readerIdx, i, err,
					)
					return
				}
				expected := valForIndex(i)
				if string(val) != string(expected) {
					errCh <- fmt.Errorf(
						"reader %d: Get(key_%04d) = %q, want %q",
						readerIdx, i, val, expected,
					)
					return
				}
			}
		}(ri)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

func TestReader_ConcurrentGetAndClose(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "close_race.sst")

	entries := []struct {
		key   keys.InternalKey
		value []byte
	}{
		{putKey("key", 1), []byte("val")},
	}
	writeSSTable(t, path, entries)

	r := openReader(t, path)

	// Launch goroutines that read concurrently while we close.
	// The test passes if nothing panics. Errors are expected
	// after close — we just must not crash.
	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for range numGoroutines {
		go func() {
			defer wg.Done()
			for range 100 {
				_, _ = r.Get([]byte("key"), 1)
			}
		}()
	}

	// Close while readers are running
	_ = r.Close()

	wg.Wait()
}

// --- Randomized round-trip test ---

// Spec: "TestRoundTrip_Randomized — write N random entries, sort them by
// internal key, write to an SSTable, read back via iterator, verify all
// entries match."
func TestRoundTrip_Randomized(t *testing.T) {
	t.Parallel()

	const numEntries = 500
	rng := rand.New(rand.NewPCG(42, 99)) //nolint:gosec // deterministic seed for reproducibility

	// Generate random entries
	type kv struct {
		key   keys.InternalKey
		value []byte
	}
	entries := make([]kv, 0, numEntries)
	seen := make(map[string]bool)

	for len(entries) < numEntries {
		userKey := testutil.RandKey(rng, 32)
		seqno := rng.Uint64N(10000) + 1

		// Deduplicate (userKey, seqno) pairs to avoid writer rejection
		dedup := string(userKey) + "/" + strconv.FormatUint(seqno, 10)
		if seen[dedup] {
			continue
		}
		seen[dedup] = true

		kind := keys.InternalKeyKindPut
		if rng.IntN(10) == 0 {
			kind = keys.InternalKeyKindDelete
		}

		var val []byte
		if kind == keys.InternalKeyKindPut {
			val = testutil.RandValue(rng, 128)
		}

		entries = append(entries, kv{
			key:   keys.InternalKey{UserKey: userKey, Seqno: seqno, Kind: kind},
			value: val,
		})
	}

	// Sort by internal key order (the writer requires sorted input)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key.Compare(entries[j].key) < 0
	})

	// Write
	dir := t.TempDir()
	path := filepath.Join(dir, "random.sst")

	writerEntries := make([]struct {
		key   keys.InternalKey
		value []byte
	}, len(entries))
	for i, e := range entries {
		writerEntries[i] = struct {
			key   keys.InternalKey
			value []byte
		}{e.key, e.value}
	}
	writeSSTable(t, path, writerEntries)

	// Read back via iterator and verify
	r := openReader(t, path)
	defer r.Close()

	it := r.NewIterator()
	idx := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		if idx >= len(entries) {
			t.Fatalf("iterator returned more entries than written (%d)", len(entries))
		}

		gotKey := it.Key()
		wantKey := entries[idx].key
		if gotKey.Compare(wantKey) != 0 {
			t.Fatalf("entry %d: key mismatch: got {%q, %d, %d}, want {%q, %d, %d}",
				idx, gotKey.UserKey, gotKey.Seqno, gotKey.Kind,
				wantKey.UserKey, wantKey.Seqno, wantKey.Kind)
		}

		gotVal := it.Value()
		wantVal := entries[idx].value
		if !bytes.Equal(gotVal, wantVal) {
			t.Fatalf("entry %d: value mismatch: got %x, want %x", idx, gotVal, wantVal)
		}

		idx++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if idx != len(entries) {
		t.Fatalf("iterator returned %d entries, want %d", idx, len(entries))
	}
}
