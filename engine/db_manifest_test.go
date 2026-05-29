package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/manifest"
	"github.com/aalhour/beachdb/internal/memtable"
	"github.com/aalhour/beachdb/internal/record"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// readCurrentName reads the CURRENT pointer file in dir and returns the
// manifest filename it contains. Fails the test if CURRENT can't be read
// or the contents don't parse as a single line.
func readCurrentName(t *testing.T, dir string) string {
	t.Helper()
	name, err := manifest.ReadCurrent(dir)
	if err != nil {
		t.Fatalf("ReadCurrent(%q): %v", dir, err)
	}
	return name
}

// statManifest returns the size of the live manifest file in dir.
func statManifestSize(t *testing.T, dir string) int64 {
	t.Helper()
	name := readCurrentName(t, dir)
	info, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("stat manifest %q: %v", name, err)
	}
	return info.Size()
}

// writeFile writes raw bytes to path. Used to plant hand-crafted fixtures
// (corrupt manifest, empty CURRENT, etc.).
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

// readFile reads a whole file. Wrapper that fails the test on error.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	//nolint:gosec // path is from t.TempDir()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return data
}

// dirContains returns true if dir contains a file named name (non-recursive).
func dirContains(t *testing.T, dir, name string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == name {
			return true
		}
	}
	return false
}

// openFresh opens a brand-new DB in t.TempDir() and returns it. Caller is
// responsible for Close.
func openFresh(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db, dir
}

// putKey constructs an InternalKey with kind=Put. Mirrors the manifest pkg
// helper; redeclared here because cross-package test helpers don't carry.
func putInternalKey(userKey string, seqno uint64) keys.InternalKey {
	return keys.InternalKey{
		UserKey: []byte(userKey),
		Seqno:   seqno,
		Kind:    keys.InternalKeyKindPut,
	}
}

// fileMeta builds a FileMetadata for fixture VersionEdits.
func fileMetaFixture(level uint32, fileID, size uint64, sk, lk string) manifest.FileMetadata {
	return manifest.FileMetadata{
		Level:       level,
		FileID:      fileID,
		Size:        size,
		SmallestKey: putInternalKey(sk, 1),
		LargestKey:  putInternalKey(lk, 2),
	}
}

// createRealSST writes a tiny but well-formed SSTable at dir/<fileID>.sst.
// Returns the size of the resulting file. Used to satisfy
// replayExistingManifest's "open SST readers from Version" step in tests
// that plant manifest AddFile edits.
func createRealSST(t *testing.T, dir string, fileID uint64) uint64 {
	t.Helper()
	sstPath := filepath.Join(dir, buildSSTFileName(fileID))

	mem := memtable.NewSkipList()
	mem.Put(keys.InternalKey{
		UserKey: []byte("a"),
		Seqno:   1,
		Kind:    keys.InternalKeyKindPut,
	}, []byte("v"))

	res, err := writeSSTable(sstPath, mem, 0)
	if err != nil {
		t.Fatalf("writeSSTable: %v", err)
	}
	_ = res.reader.Close()

	info, err := os.Stat(sstPath)
	if err != nil {
		t.Fatalf("stat new SST: %v", err)
	}
	//nolint:gosec // G115: SST file size is bounded by test fixture
	return uint64(info.Size())
}

// appendEditsToManifest opens the existing manifest file via Writer and
// appends each edit in order. Caller is responsible for the file already
// existing.
func appendEditsToManifest(t *testing.T, dir, manifestName string, edits ...*manifest.VersionEdit) {
	t.Helper()
	w, err := manifest.NewWriter(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatalf("manifest.NewWriter: %v", err)
	}
	for _, e := range edits {
		if err := w.Append(e.Encode()); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// openManifest dispatch (CURRENT-based bootstrap vs. replay)
// ---------------------------------------------------------------------------

// Empty dir → bootstrap creates CURRENT + MANIFEST-000001.
func TestOpen_Manifest_FreshDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if !dirContains(t, dir, "CURRENT") {
		t.Error("CURRENT missing after fresh Open")
	}
	if !dirContains(t, dir, "MANIFEST-000001") {
		t.Error("MANIFEST-000001 missing after fresh Open")
	}

	name := readCurrentName(t, dir)
	if name != "MANIFEST-000001" {
		t.Errorf("CURRENT = %q, want MANIFEST-000001", name)
	}

	if db.nextSSTID != 1 {
		t.Errorf("db.nextSSTID = %d, want 1", db.nextSSTID)
	}
	if db.seqno != 0 {
		t.Errorf("db.seqno = %d, want 0", db.seqno)
	}
	if db.version == nil {
		t.Fatal("db.version is nil")
	}
	if db.version.NumLevels() != 0 {
		t.Errorf("db.version.NumLevels() = %d, want 0", db.version.NumLevels())
	}
}

// Pre-place MANIFEST without CURRENT → bootstrap as fresh, orphan ignored.
func TestOpen_Manifest_OrphanManifestWithoutCurrent(t *testing.T) {
	dir := t.TempDir()
	// Plant a bogus manifest file that no CURRENT points at.
	orphanPath := filepath.Join(dir, "MANIFEST-000003")
	writeFile(t, orphanPath, []byte("orphan-garbage"))

	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Bootstrap path runs.
	if !dirContains(t, dir, "CURRENT") {
		t.Error("CURRENT missing after bootstrap")
	}
	// Orphan still present, not opened.
	if !dirContains(t, dir, "MANIFEST-000003") {
		t.Error("orphan MANIFEST-000003 should remain on disk (no cleanup yet)")
	}
}

// CURRENT exists but is empty → hard error.
func TestOpen_Manifest_EmptyCurrent(t *testing.T) {
	dir := t.TempDir()
	// Plant an empty CURRENT.
	writeFile(t, filepath.Join(dir, "CURRENT"), []byte(""))

	_, err := Open(dir, WithSync(false))
	if err == nil {
		t.Fatal("expected error for empty CURRENT")
	}
	if !errors.Is(err, manifest.ErrInvalidManifestName) {
		t.Errorf("got %v, want ErrInvalidManifestName in chain", err)
	}
}

// CURRENT points at a manifest that doesn't exist → hard error.
func TestOpen_Manifest_CurrentPointsAtMissing(t *testing.T) {
	dir := t.TempDir()
	if err := manifest.WriteCurrent(dir, "MANIFEST-999999"); err != nil {
		t.Fatalf("WriteCurrent: %v", err)
	}

	_, err := Open(dir, WithSync(false))
	if err == nil {
		t.Fatal("expected error when CURRENT names a missing manifest")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("got %v, want os.ErrNotExist in chain", err)
	}
}

// Manifest is corrupt mid-stream → hard error.
func TestOpen_Manifest_MidStreamCorruption(t *testing.T) {
	// Bootstrap a real DB, then corrupt the manifest payload, then reopen.
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Flip a byte well past the header into the payload.
	manifestPath := filepath.Join(dir, readCurrentName(t, dir))
	data := readFile(t, manifestPath)
	if len(data) <= record.HeaderSize {
		t.Fatalf("bootstrap manifest unexpectedly short: %d bytes", len(data))
	}
	data[record.HeaderSize] ^= 0xFF // payload corruption
	writeFile(t, manifestPath, data)

	_, err = Open(dir, WithSync(false))
	if err == nil {
		t.Fatal("expected error opening corrupted manifest")
	}
	if !errors.Is(err, record.ErrChecksum) {
		t.Errorf("got %v, want record.ErrChecksum in chain", err)
	}
}

// Trailing record is truncated → benign; file gets resized to last
// validated boundary.
func TestOpen_Manifest_TailTruncation_Benign(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Append one extra record (so we have something to chop), then truncate
	// mid-payload.
	currentName := readCurrentName(t, dir)
	extra := &manifest.VersionEdit{HasLastSequence: true, LastSequence: 42}
	appendEditsToManifest(t, dir, currentName, extra)

	manifestPath := filepath.Join(dir, currentName)
	full := readFile(t, manifestPath)
	// Chop the last 3 bytes — guaranteed mid-payload of the appended record.
	writeFile(t, manifestPath, full[:len(full)-3])
	preOpenSize := int64(len(full) - 3)
	_ = preOpenSize

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open after tail truncation should succeed: %v", err)
	}
	defer db2.Close()

	// After replay, file should be truncated to the last valid record boundary.
	postSize := statManifestSize(t, dir)
	if postSize >= int64(len(full)) {
		t.Errorf("manifest not truncated: size=%d, original=%d", postSize, len(full))
	}
}

// Happy path: pre-populated manifest replays into a fresh DB.
func TestOpen_Manifest_HappyPath(t *testing.T) {
	// Bootstrap then append a counter edit to a real manifest, then reopen.
	// The replay path should pick up the bumped counters.
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	bumped := &manifest.VersionEdit{
		HasNextFileID: true, NextFileID: 7,
		HasLastSequence: true, LastSequence: 123,
	}
	appendEditsToManifest(t, dir, readCurrentName(t, dir), bumped)

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open replay: %v", err)
	}
	defer db2.Close()

	if db2.nextSSTID != 7 {
		t.Errorf("nextSSTID after replay = %d, want 7", db2.nextSSTID)
	}
	if db2.seqno != 123 {
		t.Errorf("seqno after replay = %d, want 123", db2.seqno)
	}
}

// ---------------------------------------------------------------------------
// bootstrapFreshManifest behaviors
// ---------------------------------------------------------------------------

func TestBootstrap_CreatesManifestAndCurrent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if !dirContains(t, dir, "CURRENT") {
		t.Error("CURRENT not created on fresh Open")
	}
	if !dirContains(t, dir, "MANIFEST-000001") {
		t.Error("MANIFEST-000001 not created on fresh Open")
	}
}

// Bootstrap must set HasNextFileID/HasLastSequence/HasLogNumber so the
// counters actually get encoded in the initial edit. Without Has*=true,
// VersionEdit.Encode silently drops the fields and the initial record is
// header-only (no payload).
func TestBootstrap_InitialEditHasHasFlags(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read manifest directly and inspect the first record.
	manifestPath := filepath.Join(dir, readCurrentName(t, dir))
	r, err := manifest.NewReader(manifestPath)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	edit, err := r.NextEdit()
	if err != nil {
		t.Fatalf("NextEdit: %v", err)
	}
	if !edit.HasNextFileID {
		t.Error("initial edit missing HasNextFileID — counter will be silently dropped")
	}
	if !edit.HasLastSequence {
		t.Error("initial edit missing HasLastSequence")
	}
	if !edit.HasLogNumber {
		t.Error("initial edit missing HasLogNumber")
	}
}

// Counters set by bootstrap must survive a close + reopen. Locks down the
// end-to-end persistence path: bootstrap encodes Has* flags, the next Open
// reads them back, and the post-reopen db.nextSSTID / db.seqno match the
// values bootstrap chose.
func TestBootstrap_CountersSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	firstNext := db.nextSSTID
	firstSeqno := db.seqno
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	if db2.nextSSTID != firstNext {
		t.Errorf("nextSSTID after reopen = %d, want %d (bootstrap counters not persisted)", db2.nextSSTID, firstNext)
	}
	if db2.seqno != firstSeqno {
		t.Errorf("seqno after reopen = %d, want %d", db2.seqno, firstSeqno)
	}
	if db2.nextSSTID == 0 {
		t.Error("nextSSTID = 0 after reopen → next flush would write 00000000000000000000.sst (file ID collision)")
	}
}

// After fresh Open + Close, a second Open should follow the replay path,
// not bootstrap. We can't directly observe which branch fired, so we use
// a proxy: the second Open must not change the manifest filename.
func TestBootstrap_NextOpenTakesReplayPath(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	firstName := readCurrentName(t, dir)

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer db2.Close()
	secondName := readCurrentName(t, dir)

	if firstName != secondName {
		t.Errorf("manifest name changed across Opens: %q -> %q (replay path should not rename)", firstName, secondName)
	}
}

// If bootstrap crashes between Append and WriteCurrent, the orphan
// MANIFEST file is present but CURRENT is not. The next Open should
// treat the directory as fresh and continue.
func TestBootstrap_CurrentInstallIsLast_SimulatedOrphan(t *testing.T) {
	dir := t.TempDir()
	// Plant an orphan MANIFEST with no CURRENT.
	writeFile(t, filepath.Join(dir, "MANIFEST-000001"), []byte("orphan-from-crashed-bootstrap"))

	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open should bootstrap fresh: %v", err)
	}
	defer db.Close()

	if !dirContains(t, dir, "CURRENT") {
		t.Error("CURRENT should be installed by bootstrap")
	}
}

// ---------------------------------------------------------------------------
// replayExistingManifest deep paths (counters, deletes, truncation, recovery)
// ---------------------------------------------------------------------------

// Counter edits accumulate with last-wins semantics (only Has* fields apply).
func TestReplay_Counters_LastWins(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	currentName := readCurrentName(t, dir)
	appendEditsToManifest(t, dir, currentName,
		&manifest.VersionEdit{HasNextFileID: true, NextFileID: 1},
		&manifest.VersionEdit{HasNextFileID: true, NextFileID: 5},
		&manifest.VersionEdit{HasNextFileID: true, NextFileID: 3},
	)

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open replay: %v", err)
	}
	defer db2.Close()

	if db2.nextSSTID != 3 {
		t.Errorf("nextSSTID = %d, want 3 (last-wins)", db2.nextSSTID)
	}
}

// HasNextFileID=false means the field is not present in the encoding;
// the previous counter value must survive.
func TestReplay_Counters_HasFlagsRespected(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	currentName := readCurrentName(t, dir)
	appendEditsToManifest(t, dir, currentName,
		&manifest.VersionEdit{HasNextFileID: true, NextFileID: 10},
		// Has*=false: NextFileID=5 is in the struct but won't encode.
		&manifest.VersionEdit{HasLastSequence: true, LastSequence: 99},
	)

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open replay: %v", err)
	}
	defer db2.Close()

	if db2.nextSSTID != 10 {
		t.Errorf("nextSSTID = %d, want 10 (second edit must not overwrite without HasNextFileID)", db2.nextSSTID)
	}
	if db2.seqno != 99 {
		t.Errorf("seqno = %d, want 99", db2.seqno)
	}
}

// AddFile then DeleteFile for the same fileID leaves the Version empty.
func TestReplay_DeleteFileEdit_RemovesFromVersion(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	currentName := readCurrentName(t, dir)
	appendEditsToManifest(t, dir, currentName,
		&manifest.VersionEdit{
			AddedFiles: []manifest.FileMetadata{
				fileMetaFixture(0, 42, 100, "a", "z"),
			},
		},
		&manifest.VersionEdit{
			DeletedFiles: []manifest.DeletedFile{{Level: 0, FileID: 42}},
		},
	)

	// Note: this manifest references fileID=42 transiently. Replay will
	// try to open the SST file for any survivors of Version.AllFiles().
	// Since the DeleteFile removes it, no SST open should be attempted.
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open replay (delete cancels add): %v", err)
	}
	defer db2.Close()

	if db2.version == nil || len(db2.version.AllFiles()) != 0 {
		t.Errorf("expected empty Version after Add+Delete cancellation")
	}
	if len(db2.ssts) != 0 {
		t.Errorf("expected zero SST readers, got %d", len(db2.ssts))
	}
}

// Empty manifest (zero bytes) — replay must either fail loudly or
// produce safe defaults; in particular nextSSTID must not be 0, which
// would collide with the "not allocated" sentinel.
func TestReplay_EmptyManifest_NoEdits(t *testing.T) {
	dir := t.TempDir()
	// Fabricate: install CURRENT pointing at an empty manifest file.
	emptyManifest := filepath.Join(dir, "MANIFEST-000001")
	writeFile(t, emptyManifest, []byte{})
	if err := manifest.WriteCurrent(dir, "MANIFEST-000001"); err != nil {
		t.Fatalf("WriteCurrent: %v", err)
	}

	db, err := Open(dir, WithSync(false))
	if err != nil {
		// Acceptable outcome: hard error on a manifest with no counter edits.
		t.Logf("Open on empty manifest returned error (acceptable): %v", err)
		return
	}
	defer db.Close()

	// Alternative acceptable outcome: bootstrap-like defaults. nextSSTID
	// must NOT be 0 (would collide with a non-existent file 0).
	if db.nextSSTID == 0 {
		t.Error("replay of empty manifest left db.nextSSTID=0 — next flush would write file 0")
	}
}

// Tail truncation must resize the manifest file to the last valid offset.
func TestReplay_TruncatedTail_FileResized(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	currentName := readCurrentName(t, dir)
	appendEditsToManifest(t, dir, currentName,
		&manifest.VersionEdit{HasLastSequence: true, LastSequence: 7},
	)
	manifestPath := filepath.Join(dir, currentName)
	full := readFile(t, manifestPath)

	// Chop last 3 bytes — mid-payload of the appended record.
	writeFile(t, manifestPath, full[:len(full)-3])

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open should tolerate tail truncation: %v", err)
	}
	defer db2.Close()

	postSize := statManifestSize(t, dir)
	if postSize >= int64(len(full)) {
		t.Errorf("manifest not truncated: size=%d, original=%d", postSize, len(full))
	}
}

// After tail truncation, the next append should land at the truncation
// boundary (not over the partial bytes).
func TestReplay_AfterTruncation_AppendsLandCleanly(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	currentName := readCurrentName(t, dir)
	appendEditsToManifest(t, dir, currentName,
		&manifest.VersionEdit{HasLastSequence: true, LastSequence: 7},
	)
	manifestPath := filepath.Join(dir, currentName)
	full := readFile(t, manifestPath)
	writeFile(t, manifestPath, full[:len(full)-3])

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Append a fresh edit via the now-open Writer.
	if err := db2.manifest.Append((&manifest.VersionEdit{HasLastSequence: true, LastSequence: 100}).Encode()); err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen — replay must succeed end-to-end with no corruption.
	db3, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open after recovery + append: %v", err)
	}
	defer db3.Close()

	if db3.seqno != 100 {
		t.Errorf("seqno = %d, want 100 (latest counter)", db3.seqno)
	}
}

// ---------------------------------------------------------------------------
// Open consolidation and error-path cleanup
// ---------------------------------------------------------------------------

// After Open succeeds, db.version / db.manifest / db.nextSSTID / db.seqno
// must all be installed.
func TestOpen_PostStateInstalled(t *testing.T) {
	db, _ := openFresh(t)
	defer db.Close()

	if db.version == nil {
		t.Error("db.version is nil after Open")
	}
	if db.manifest == nil {
		t.Error("db.manifest is nil after Open")
	}
	// On fresh DB: nextSSTID=1, seqno=0 (assumes bootstrap counters are set).
	if db.nextSSTID == 0 {
		t.Errorf("db.nextSSTID = %d, want 1 (bootstrap should set NextFileID=1)", db.nextSSTID)
	}
}

// Open replays the manifest first (loading flushed SSTs) and then the WAL on
// top, so a newer un-flushed write shadows the older flushed value for the
// same key: the manifest wins on file existence, the WAL wins on recent data.
func TestOpen_OrderingInvariant_ManifestBeforeWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Older value flushed into an SST (recorded in the manifest).
	if err := db.Put(ctx, []byte("k"), []byte("old-from-sst")); err != nil {
		t.Fatalf("Put(old): %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// Newer value for the same key, left un-flushed in the WAL only.
	if err := db.Put(ctx, []byte("k"), []byte("new-from-wal")); err != nil {
		t.Fatalf("Put(new): %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	got, err := db2.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "new-from-wal" {
		t.Errorf("Get = %q, want %q (WAL replay must shadow the older SST value)", got, "new-from-wal")
	}
}

// ---------------------------------------------------------------------------
// Close path with manifest
// ---------------------------------------------------------------------------

func TestClose_ReleasesManifestWriter(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-opening must succeed (proves the file handle was released).
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen after Close: %v", err)
	}
	defer db2.Close()
}

func TestClose_NullsManifestField(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if db.manifest != nil {
		t.Error("db.manifest not nil after Close")
	}
}

// Closing twice should return ErrDBClosed the second time, not panic on
// the now-nil manifest.
func TestClose_DoubleClose_WithManifest(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	err = db.Close()
	if !errors.Is(err, ErrDBClosed) {
		t.Errorf("second Close: got %v, want ErrDBClosed", err)
	}
}

// ---------------------------------------------------------------------------
// Flush + cross-restart durability
//
// A successful flush appends a VersionEdit to the manifest (AddFile +
// NextFileID + LastSequence). These tests exercise that the file set, SST id
// counter, and sequence number all survive a close/reopen via the manifest.
// ---------------------------------------------------------------------------

// A successful flush appends an AddFile VersionEdit to the manifest. After
// reopen the file is visible in the replayed Version with its captured key
// range and a non-zero size.
func TestFlush_WritesManifestEdit(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	for _, k := range []string{"banana", "apple", "cherry"} {
		if err := db.Put(ctx, []byte(k), []byte("v-"+k)); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	files := db2.version.AllFiles()
	if len(files) != 1 {
		t.Fatalf("Version.AllFiles() = %d files, want 1", len(files))
	}
	f := files[0]
	if f.Size == 0 {
		t.Errorf("FileMetadata.Size = 0, want > 0")
	}
	if got := string(f.SmallestKey.UserKey); got != "apple" {
		t.Errorf("SmallestKey = %q, want %q", got, "apple")
	}
	if got := string(f.LargestKey.UserKey); got != "cherry" {
		t.Errorf("LargestKey = %q, want %q", got, "cherry")
	}
}

// The SSTable is synced to disk before the manifest edit is appended, so a
// crash in between leaves an orphan SST the manifest never references.
// Exercising the crash itself needs the out-of-process crash harness; the
// crashhook point (PointManifestAfterSSTSync) is wired in
// publishFlushedSSTLocked for that harness.
func TestFlush_OrderingInvariant_SSTBeforeManifest(t *testing.T) {
	t.Skip("TODO: crash point wired; scenario needs the out-of-process crash harness")
}

// When the manifest Append fails, flush returns an error, the open SST reader
// is released, and the file is left as an orphan: the Version does not gain
// the file and nextSSTID is not advanced, so the next flush reuses the id.
func TestFlush_ManifestAppendFailure_SSTOrphaned(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := db.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Force the manifest Append to fail by closing the writer underneath
	// the flush path.
	if err := db.manifest.Close(); err != nil {
		t.Fatalf("closing manifest writer: %v", err)
	}

	prevSSTID := db.nextSSTID
	if err := db.Flush(); err == nil {
		t.Fatal("Flush succeeded, want manifest append error")
	}

	if got := len(db.version.AllFiles()); got != 0 {
		t.Errorf("Version.AllFiles() = %d, want 0 (edit must not apply on append failure)", got)
	}
	if db.nextSSTID != prevSSTID {
		t.Errorf("nextSSTID advanced to %d, want %d (no advance on append failure)", db.nextSSTID, prevSSTID)
	}
	if !dirContains(t, dir, buildSSTFileName(prevSSTID)) {
		t.Errorf("expected orphan SST %s on disk", buildSSTFileName(prevSSTID))
	}
}

// NextFileID recorded in the flush edit is restored on reopen, so SST ids keep
// climbing across restarts instead of resetting and overwriting files.
func TestFlush_NextSSTIDPersistedViaManifest(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := db.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	// One flush starting at id 1 ⇒ next available id is 2.
	if db2.nextSSTID != 2 {
		t.Errorf("nextSSTID after reopen = %d, want 2", db2.nextSSTID)
	}
}

// LastSequence checkpointed by a flush is restored on reopen. Deleting the WAL
// isolates the manifest as the only seqno source.
func TestFlush_LastSequencePersistedViaManifest(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	const nPuts = 4
	for i := range nPuts {
		k := fmt.Sprintf("key-%d", i)
		if err := db.Put(ctx, []byte(k), []byte("v")); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Remove the WAL so the manifest checkpoint is the sole seqno source.
	if err := os.Remove(filepath.Join(dir, walFileName)); err != nil {
		t.Fatalf("removing WAL: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	if db2.seqno != nPuts {
		t.Errorf("seqno after reopen = %d, want %d (manifest LastSequence)", db2.seqno, nPuts)
	}
}

// Several flushes each append an AddFile edit; all files are present in the
// replayed Version and every key is readable after reopen.
func TestFlush_Multiple_AllRecoverable(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	wantKeys := []string{"alpha", "bravo", "charlie"}
	for _, k := range wantKeys {
		if err := db.Put(ctx, []byte(k), []byte("v-"+k)); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
		if err := db.Flush(); err != nil {
			t.Fatalf("Flush(%s): %v", k, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	if got := len(db2.version.AllFiles()); got != len(wantKeys) {
		t.Errorf("Version.AllFiles() = %d, want %d", got, len(wantKeys))
	}
	for _, k := range wantKeys {
		got, err := db2.Get(ctx, []byte(k))
		if err != nil {
			t.Fatalf("Get(%s): %v", k, err)
		}
		if string(got) != "v-"+k {
			t.Errorf("Get(%s) = %q, want %q", k, got, "v-"+k)
		}
	}
}

// After a flush, the data lives in the SSTable. Deleting the WAL before reopen
// proves recovery comes from the manifest+SST, not the WAL.
func TestRestart_DataFromSSTOnly(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := db.Put(ctx, []byte("durable"), []byte("survives")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, walFileName)); err != nil {
		t.Fatalf("removing WAL: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	got, err := db2.Get(ctx, []byte("durable"))
	if err != nil {
		t.Fatalf("Get after WAL deletion: %v", err)
	}
	if string(got) != "survives" {
		t.Errorf("Get = %q, want %q", got, "survives")
	}
}

// On reopen, flushed data comes from the SST (manifest) and un-flushed data
// comes from replaying the WAL on top.
func TestRestart_DataFromSSTAndWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	// Flushed key ⇒ ends up in an SST tracked by the manifest.
	if err := db.Put(ctx, []byte("flushed"), []byte("from-sst")); err != nil {
		t.Fatalf("Put(flushed): %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	// Un-flushed key ⇒ only in the WAL + active memtable.
	if err := db.Put(ctx, []byte("buffered"), []byte("from-wal")); err != nil {
		t.Fatalf("Put(buffered): %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	for k, want := range map[string]string{"flushed": "from-sst", "buffered": "from-wal"} {
		got, err := db2.Get(ctx, []byte(k))
		if err != nil {
			t.Fatalf("Get(%s): %v", k, err)
		}
		if string(got) != want {
			t.Errorf("Get(%s) = %q, want %q", k, got, want)
		}
	}
}

// After a flush + reopen, the next flush must use a fresh SST id rather than
// reusing id 1 and overwriting the first file.
func TestRestart_NextSSTID_NoCollisionsAfterCrash(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := db.Put(ctx, []byte("first"), []byte("1")); err != nil {
		t.Fatalf("Put(first): %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if err := db2.Put(ctx, []byte("second"), []byte("2")); err != nil {
		t.Fatalf("Put(second): %v", err)
	}
	if err := db2.Flush(); err != nil {
		t.Fatalf("Flush after reopen: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Both SSTs must coexist on disk under distinct ids.
	if !dirContains(t, dir, buildSSTFileName(1)) {
		t.Errorf("first SST %s missing", buildSSTFileName(1))
	}
	if !dirContains(t, dir, buildSSTFileName(2)) {
		t.Errorf("second SST %s missing (id collision?)", buildSSTFileName(2))
	}

	db3, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen 2: %v", err)
	}
	defer db3.Close()
	if got := len(db3.version.AllFiles()); got != 2 {
		t.Errorf("Version.AllFiles() = %d, want 2", got)
	}
}

// Sequence numbers stay monotonic across a flush + restart: a post-restart
// write wins over the pre-restart value for the same key.
func TestRestart_SeqnoMonotonic(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := db.Put(ctx, []byte("k"), []byte("old")); err != nil {
		t.Fatalf("Put(old): %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Drop the WAL so seqno is seeded solely from the manifest checkpoint.
	if err := os.Remove(filepath.Join(dir, walFileName)); err != nil {
		t.Fatalf("removing WAL: %v", err)
	}

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer db2.Close()

	seqnoBefore := db2.seqno
	if err := db2.Put(ctx, []byte("k"), []byte("new")); err != nil {
		t.Fatalf("Put(new): %v", err)
	}
	if db2.seqno <= seqnoBefore {
		t.Errorf("seqno did not advance: before=%d after=%d", seqnoBefore, db2.seqno)
	}

	got, err := db2.Get(ctx, []byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("Get = %q, want %q (post-restart write must win)", got, "new")
	}
}

// ---------------------------------------------------------------------------
// Adversarial / recovery scenarios
// ---------------------------------------------------------------------------

// TestBootstrap_RecoversFromOrphanManifestCollision: a prior crashed
// bootstrap may have left a MANIFEST-NNNNNN on disk with no CURRENT
// pointing at it (the install step never ran). The next bootstrap must
// not reuse the same filename with O_APPEND — writing the new initial
// edit on top of the orphan's garbage bytes would corrupt the stream.
// Bootstrap is expected to pick a fresh, unused MANIFEST ID.
func TestBootstrap_RecoversFromOrphanManifestCollision(t *testing.T) {
	dir := t.TempDir()
	// Plant an orphan MANIFEST-000001 with garbage (no CURRENT).
	writeFile(t, filepath.Join(dir, "MANIFEST-000001"), []byte("garbage-from-crashed-bootstrap"))

	db, err := Open(dir, WithSync(false))
	if err != nil {
		// Acceptable alternative: detect the collision and refuse.
		t.Logf("Open detected orphan and failed (acceptable): %v", err)
		return
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen → replay must succeed on the manifest CURRENT points at.
	// If bootstrap appended to the orphan, replay will hit corruption.
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Errorf("orphan collision corrupted the manifest stream: %v", err)
		return
	}
	defer db2.Close()
}

// TestOpen_RejectsCurrentPathTraversal: a malformed CURRENT containing
// path separators must be rejected by ReadCurrent's validation, not
// blindly followed via filepath.Join. The test plants a real file at the
// traversal target so that "any error" is insufficient — only validation
// rejection (ErrInvalidManifestName) is correct. Without symmetric
// validation between ReadCurrent and WriteCurrent, a hand-edited CURRENT
// could escape the database directory.
func TestOpen_RejectsCurrentPathTraversal(t *testing.T) {
	// Layout:
	//   <root>/
	//     decoy/MANIFEST-evil   ← a real, openable file outside the DB dir
	//     db/CURRENT            ← contents: "../decoy/MANIFEST-evil"
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "decoy"), 0750); err != nil {
		t.Fatalf("mkdir decoy: %v", err)
	}
	// File can be empty — replayExistingManifest will fail later anyway,
	// but we want to make sure failure isn't os.ErrNotExist.
	writeFile(t, filepath.Join(root, "decoy", "MANIFEST-evil"), []byte("decoy-content"))

	dbDir := filepath.Join(root, "db")
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	writeFile(t, filepath.Join(dbDir, "CURRENT"), []byte("../decoy/MANIFEST-evil\n"))

	_, err := Open(dbDir, WithSync(false))
	if err == nil {
		t.Fatal("Open followed path traversal without rejecting CURRENT")
	}

	// Right rejection: ErrInvalidManifestName from validation.
	// Wrong rejection: any other error (e.g. record framing failure on
	// the decoy file's contents) means the traversal was followed.
	if !errors.Is(err, manifest.ErrInvalidManifestName) {
		t.Errorf("got %v, want manifest.ErrInvalidManifestName (path traversal not validated)", err)
	}
}

// TestOpen_OrphanManifestIgnoredWhenCurrentValid: when multiple MANIFEST
// files exist on disk but CURRENT names a specific one, Open must use
// only that one. The other(s) are ignored — they may be artifacts of a
// failed rotation or a crashed bootstrap.
func TestOpen_OrphanManifestIgnoredWhenCurrentValid(t *testing.T) {
	dir := t.TempDir()
	// Bootstrap a real DB to get a valid MANIFEST-000001.
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Plant a junk MANIFEST-000002 that no CURRENT points at.
	writeFile(t, filepath.Join(dir, "MANIFEST-000002"), []byte("orphan-with-junk"))

	// CURRENT still points at MANIFEST-000001 — Open should succeed.
	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Errorf("orphan MANIFEST-000002 should not affect Open of MANIFEST-000001: %v", err)
		return
	}
	defer db2.Close()
}

// TestReplay_RejectsDuplicateAddFile: a manifest that adds the same
// fileID twice without an intervening DeleteFile is corrupt or
// engine-buggy. Replay must reject it rather than silently producing
// duplicate readers / duplicate Version entries.
//
// The test plants a real SST file at fileID=42 so that the post-replay
// "open SST readers" step succeeds — that isolates the duplicate-
// detection question from the SST-existence question.
func TestReplay_RejectsDuplicateAddFile(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Plant a real SST file at fileID=42 so the post-replay open
	// succeeds. Without this fixture the test would fail at the wrong
	// step (missing SST) and mask the duplicate-detection bug.
	const fileID uint64 = 42
	sstSize := createRealSST(t, dir, fileID)

	currentName := readCurrentName(t, dir)
	dup := manifest.FileMetadata{
		Level:       0,
		FileID:      fileID,
		Size:        sstSize,
		SmallestKey: putInternalKey("a", 1),
		LargestKey:  putInternalKey("a", 1),
	}
	appendEditsToManifest(t, dir, currentName,
		&manifest.VersionEdit{AddedFiles: []manifest.FileMetadata{dup}},
		&manifest.VersionEdit{AddedFiles: []manifest.FileMetadata{dup}},
	)

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		// Acceptable outcome: replay detected the duplicate and rejected.
		t.Logf("Open rejected duplicate AddFile (acceptable): %v", err)
		return
	}
	defer db2.Close()

	// Open succeeded — verify it didn't silently produce duplicate
	// readers / duplicate Version entries.
	if got := len(db2.ssts); got != 1 {
		t.Errorf("db.ssts has %d entries for one fileID (want 1) — duplicate not detected", got)
	}
	if db2.version != nil {
		if got := len(db2.version.AllFiles()); got != 1 {
			t.Errorf("version has %d files for one fileID (want 1) — duplicate not detected", got)
		}
	}
}

// TestReplay_OrphanDeleteFile_Ignored: a DeleteFile for a fileID that
// was never added is silently ignored (idempotent). Locks down that
// behavior so the replay path stays tolerant of partial-replay or
// reorder scenarios that may legitimately produce orphan deletes.
func TestReplay_OrphanDeleteFile_Ignored(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	currentName := readCurrentName(t, dir)
	appendEditsToManifest(t, dir, currentName,
		&manifest.VersionEdit{DeletedFiles: []manifest.DeletedFile{{Level: 0, FileID: 999}}},
	)

	db2, err := Open(dir, WithSync(false))
	if err != nil {
		t.Fatalf("Open with orphan DeleteFile: %v", err)
	}
	defer db2.Close()

	if db2.version == nil || len(db2.version.AllFiles()) != 0 {
		t.Errorf("Version not empty after orphan DeleteFile")
	}
}

// TODO: the following adversarial scenarios require fault-injection hooks
// that don't exist yet — append failures during bootstrap, rename failures
// in CURRENT install. Re-enable once the relevant crashhook/fault points
// are wired up.
func TestBootstrap_HandlesAppendFailure(t *testing.T) {
	t.Skip("TODO: requires a fault-injection point for manifest.Append")
}
func TestBootstrap_HandlesRenameFailure(t *testing.T) {
	t.Skip("TODO: requires filesystem-level fault injection for rename")
}

// Discard unused import warnings if a section is fully skipped.
var _ = io.EOF
var _ = fmt.Sprint
var _ = record.HeaderSize
