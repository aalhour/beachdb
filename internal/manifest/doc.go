// Package manifest implements the MANIFEST log and CURRENT pointer that
// track BeachDB's on-disk state across restarts.
//
// # What is a manifest?
//
// An LSM-tree database is a soup of files: a WAL, a memtable, and a growing
// pile of SSTables that flushes and compactions keep producing. Someone has
// to remember which files exist, which level each SSTable belongs to, what
// key range it covers, and where the next file ID starts. That memory is the
// manifest.
//
// The manifest is an append-only log of [VersionEdit] records. Each edit
// describes one atomic change to the file set: "add this SSTable at level 0",
// "delete that SSTable at level 1", "next file ID is N", "last sequence
// number written was S". On startup, the database replays every edit in
// order to reconstruct the current [Version] — the immutable snapshot of
// "which SSTables exist right now."
//
// LevelDB and Pebble use the same idea. The append-only design means
// recording a state change is one synchronous record append plus an fsync,
// not a rewrite of the whole inventory.
//
// # File layout
//
// On disk, a BeachDB directory contains:
//
//	beachdb.wal             ← the write-ahead log
//	CURRENT                 ← one-line pointer to the live MANIFEST
//	MANIFEST-000001         ← the live append-only log of version edits
//	000001.sst, 000002.sst  ← the SSTables referenced by the manifest
//
// CURRENT exists because manifests can be rotated (rewritten compactly,
// then atomically swapped in) in a future version. v1 does not rotate, but
// the indirection ships from day one so recovery code never depends on a
// fixed manifest filename.
//
// See docs/formats/manifest.md for the byte-level wire format.
//
// # VersionEdit
//
// [VersionEdit] is the unit of change. Each edit is a set of optional fields:
//
//   - AddedFiles, DeletedFiles  — SSTables entering or leaving the file set
//   - NextFileID                 — bump the file-number allocator
//   - LastSequence               — advance the global sequence counter
//   - LogNumber                  — the WAL number this edit is associated with
//
// Fields are encoded with a TLV (tag-length-value) scheme. Unknown tags are
// a hard error in v1 — there is no skip-unknown machinery. Future format
// changes ship via a manifest format-version bump rather than tag insertion.
//
// # Version
//
// [Version] is the in-memory snapshot the database operates on. Applying a
// VersionEdit to a Version produces a new Version (immutable, copy-on-write).
// Callers atomically swap the active pointer once an edit is durable on
// disk. v1 sorts L1+ by SmallestKey for efficient lookups; L0 is left in
// insertion order because L0 files overlap and need a merging iterator.
//
// # Writer and Reader
//
// [Writer] appends records to the manifest. Each Append encodes a record
// (magic + length + checksum + payload), writes it to the file, and fsyncs.
// The fsync happens inside Append, not as a separate call — manifest writes
// are infrequent (per flush, per compaction) and each one gates other engine
// actions, so there is no batching benefit to a separate Sync call. This is
// the deliberate asymmetry with the WAL writer, where Append and Sync are
// split so the WAL can group-commit many writes per fsync.
//
// [Reader] reads records sequentially from a manifest file. [Reader.Next]
// returns the raw record payload; [Reader.NextEdit] is the convenience
// wrapper that decodes it as a VersionEdit. Returns io.EOF cleanly at end
// of stream. Short reads surface as record.ErrTruncated; callers (replay
// code) can treat a trailing truncation as expected (crash during write)
// or as corruption depending on policy. [Reader.ValidOffset] reports the
// byte boundary after the last validated record so recovery can truncate
// a partial tail.
//
// # CURRENT file
//
// [ReadCurrent] reads the single-line CURRENT file and returns the live
// manifest's filename. Missing CURRENT returns [ErrNoCurrentFile] so the
// caller can treat a missing pointer as "fresh database, bootstrap from
// scratch" rather than a hard error.
//
// [WriteCurrent] installs a new pointer atomically:
//
//  1. Write "MANIFEST-NNNNNN\n" to CURRENT.tmp
//  2. fsync(CURRENT.tmp)
//  3. rename(CURRENT.tmp, CURRENT)   — atomic on POSIX
//  4. fsync(parent directory)         — so the rename is durable
//
// A crash at any step leaves either the old CURRENT intact or the new
// CURRENT fully written. Never a partial CURRENT. Same rename-into-place
// pattern as SSTable installation.
//
// # Recovery semantics
//
// On startup, the engine:
//
//  1. Reads CURRENT. If missing → fresh database.
//  2. Opens the manifest named in CURRENT.
//  3. Replays every VersionEdit in order, applying each to an initially
//     empty Version. Counter tags advance the file-number / sequence /
//     log-number watermarks.
//  4. Opens SSTable readers for every file in the final Version. A
//     referenced file missing on disk is corruption — fail loudly.
//
// A truncated trailing record (crash during write) is recoverable: replay
// stops at the last valid record, and the engine can truncate the manifest
// file to [Reader.ValidOffset] before continuing.
//
// # Concurrency model
//
// Both [Writer] and [Reader] are single-instance per database. Their
// mutexes defend against accidental concurrent use, not designed
// concurrency. The engine wires the writer behind its own serialization
// (flush / compaction completion).
//
// # Current simplifications
//
// The following features are intentionally omitted from v1 and will land
// in later milestones:
//
//   - No manifest rotation (the file grows unbounded for now)
//   - No skip-unknown tag support (unknown tags hard-error)
//   - No compaction pointers, snapshots, or per-table catalogs
//   - No checksum on the manifest file as a whole (per-record checksum only)
//
// See docs/formats/manifest.md for the full format specification and
// versioning strategy.
package manifest
