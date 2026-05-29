# Manifest Format (v1)

> The manifest is the official record of which SSTables exist. If a file isn't in the manifest, it doesn't count.

Introduced in BeachDB [v0.0.5](https://github.com/aalhour/beachdb/releases/tag/v0.0.5).

## Goals

- **Atomic state transitions**: each edit applies whole or not at all. No half-applied state on disk.
- **Crash-safe bootstrap**: I always know which manifest file is live, even if a crash happens during rotation.
- **Deterministic replay**: replaying N edits in order always rebuilds the same `Version`.
- **Forward-compatible**: room for compaction stats, snapshot metadata, and other future fields without breaking old readers.
- **Inspectable**: `manifest_dump` decodes the file and prints the reconstructed file set.

---

## File Layout

A BeachDB data directory contains two manifest-related files:

```
data/
├── CURRENT                  ← 1-line pointer to the active manifest
├── MANIFEST-000001          ← append-only log of VersionEdits
├── 000007.sst               ← SSTables (named by file ID)
├── 000008.sst
└── ...
```

- `CURRENT` always contains exactly one line: the filename of the live manifest, followed by `\n`.
- `MANIFEST-NNNNNN` is the active log. File IDs are 6-digit zero-padded so they sort lexicographically.
- v1 never rotates the manifest. One MANIFEST file per database lifetime.

The indirection through `CURRENT` exists because manifests can be rotated (rewritten compactly, then atomically swapped in). v1 doesn't rotate yet, but the indirection is there from day one. Adding it later would break recovery.

---

## Record Format

A MANIFEST file is a sequence of records. The framing is the same as the [WAL record format](wal.md#record-format), with one field changed:

| Field | WAL value | Manifest value |
|-------|-----------|----------------|
| `magic` | 8-byte ASCII `BEACHWAL` | 8-byte ASCII `BEACHMAN` |
| `version` | `0x01` | `0x01` |
| `type` | `0x01` (Full) | `0x01` (Full) |
| `length` | uint32 | uint32 |
| `checksum` | CRC32C of payload | CRC32C of payload |
| `payload` | encoded `Batch` | encoded `VersionEdit` |

Different magic bytes mean `wal_dump` cleanly rejects a manifest file and vice versa.

The header is 18 bytes: 8-byte magic, 1-byte version, 1-byte record type,
4-byte payload length, and 4-byte checksum. The payload is the encoded
`VersionEdit` described below.

---

## VersionEdit Encoding

A `VersionEdit` is one atomic change to the database's metadata: files added, files deleted, counter updates. Each edit is encoded as a sequence of tag-value pairs.

```
VersionEdit Encoding (v1)
=========================

Each field in a VersionEdit is optional. Encode only fields that are set.
For each set field, write:
  [tag: 1 byte][field payload]

Tag values:
  1 = AddFile:      [level: uint32][fileID: uint64][size: uint64]
                    [smallestKey: uint32 length + bytes]
                    [largestKey:  uint32 length + bytes]
  2 = DeleteFile:   [level: uint32][fileID: uint64]
  3 = NextFileID:   [value: uint64]
  4 = LastSequence: [value: uint64]
  5 = LogNumber:    [value: uint64]
```

All integers are big-endian. No padding between fields.

### Byte layout example

An edit with `NextFileID = 42`, `LastSequence = 100`, and one `AddFile{level=0, fileID=7, size=1024, smallestKey="apple", largestKey="zebra"}`:

```
Offset  Hex                          Meaning
------  ---                          -------
0       03                           tag = NextFileID
1..8    00 00 00 00 00 00 00 2A      uint64 BE = 42
9       04                           tag = LastSequence
10..17  00 00 00 00 00 00 00 64      uint64 BE = 100
18      01                           tag = AddFile
19..22  00 00 00 00                  level (uint32) = 0
23..30  00 00 00 00 00 00 00 07      fileID (uint64) = 7
31..38  00 00 00 00 00 00 04 00      size (uint64) = 1024
39..42  00 00 00 05                  smallestKey length (uint32) = 5
43..47  61 70 70 6C 65               smallestKey bytes = "apple"
48..51  00 00 00 05                  largestKey length (uint32) = 5
52..56  7A 65 62 72 61               largestKey bytes = "zebra"
```

Total body: 57 bytes. Wrapped by the 18-byte record header, the on-disk record is 75 bytes.

### Deterministic emit order

`Encode()` must produce the same bytes for the same input on every call. Tags are written in this order:

1. `NextFileID`   (if set)
2. `LastSequence` (if set)
3. `LogNumber`    (if set)
4. `DeleteFile`   entries, in input order (do not sort)
5. `AddFile`      entries, in input order (do not sort)

Counters first lets a reader see the new watermarks before processing file deltas. Same convention LevelDB uses.

### Decoder skeleton

```
i := 0
for i < len(data):
  tag := data[i]; i++
  switch tag:
    case 3: read 8 bytes → NextFileID;   HasNextFileID = true
    case 4: read 8 bytes → LastSequence; HasLastSequence = true
    case 5: read 8 bytes → LogNumber;    HasLogNumber = true
    case 2: read 4 bytes level, 8 bytes fileID → append DeletedFiles
    case 1: read level, fileID, size, then two length-prefixed key blobs → append AddedFiles
    default: return ErrUnknownTag
  bounds-check every read: short buffer → ErrTruncated
```

Unknown tags are a hard error in v1. See [Versioning Strategy](#versioning-strategy).

---

## CURRENT File

```
CURRENT Format
==============
"MANIFEST-NNNNNN\n"
```

One line. The filename of the live manifest, no directory prefix, terminated with a single `\n`. Nothing else.

### Atomic update protocol

To install a new CURRENT, used only during rotation in a future version, use this protocol:

```
1. Write "MANIFEST-NNNNNN\n" to "CURRENT.tmp" in the same directory
2. fsync("CURRENT.tmp")
3. rename("CURRENT.tmp", "CURRENT")        ← atomic on POSIX
4. fsync(parent directory)                  ← so the rename is durable
```

A crash at any step leaves either the old CURRENT intact or the new CURRENT fully written. Never a partial CURRENT.

---

## Recovery Semantics

### On Startup

1. Read `CURRENT`. If missing → fresh database, skip to step 5.
2. Read the manifest file named in `CURRENT`. If missing → corruption, fail loudly.
3. Replay records sequentially. For each `VersionEdit`:
   - Apply to in-memory `Version`.
   - Update counters (`NextFileID`, `LastSequence`, `LogNumber`) if present.
4. Open SSTable readers for every file in the final `Version`. If a referenced file doesn't exist on disk → corruption, fail loudly.
5. Replay the WAL on top of the recovered state.
6. If fresh database: create `MANIFEST-000001`, write one initial `VersionEdit` carrying zero counters, fsync, then write `CURRENT` atomically.

### Handling Truncation

A truncated last record means the process crashed mid-write to the manifest:

- **Truncated header** (< 18 bytes): ignore, treat as EOF, truncate the file back to the last valid offset.
- **Truncated payload** (header valid, payload incomplete): ignore, treat as EOF, truncate.

The incomplete edit was never `fsync`'d, so it never took effect from the database's perspective. Discarding it is correct.

### Handling Corruption

- **Bad magic on a record**: stop. The manifest is not what it claims to be.
- **Checksum mismatch on a complete record**: stop. Fail loudly. Do not continue replay.
- **Unknown tag in a payload**: stop. v1 readers do not skip unknown tags.
- **Missing SSTable referenced by manifest**: stop. This is different from WAL truncation. A truncated WAL tail means "crash after write, before sync" and is benign. A missing SSTable the manifest promises exists means data was deleted outside the manifest contract. That's a bug or corruption.

### Orphan SSTables

An orphan is an `.sst` file on disk that the manifest doesn't reference. Most common cause: a crash between writing the SSTable and appending the matching `AddFile` edit.

After manifest replay completes, before WAL replay:

1. Collect all `fileID`s in the recovered `Version`.
2. List all `.sst` files in the data directory.
3. For each `.sst` file not in the manifest: delete it. Log every deletion. If a deletion fails, log the failure and continue. Orphan cleanup is best-effort and must not block startup.

The strict invariant is that the database does not *use* an orphan: only files referenced by the manifest are opened as SSTables. Deletion is the secondary concern, and a failed deletion leaves the file on disk to be cleaned up next time.

---

## Durability Contract

The manifest and the SSTables it references must be durable in the right order. The rule:

```
1. Write the SSTable file
2. fsync the SSTable file
3. fsync the data directory (so the SSTable filename is durable)
4. Append the AddFile edit to the manifest
5. fsync the manifest file
```

Never reverse SSTable sync and manifest sync. If the manifest is synced first, it promises a file that may not be durable. If the order above is followed and a crash happens between step 3 and step 5, the SSTable exists on disk but the manifest doesn't know about it. It's an orphan, and orphan handling takes care of it.

Unlike the WAL (where `SyncOnWrite` is configurable), the manifest always syncs after each append. Metadata durability is not optional.

---

## Design Decisions

### Why a log of edits, not a snapshot?

I considered writing the full file set to disk on every change: `current state → JSON or TLV → atomic rename`. Simpler in some ways, but it has two problems:

1. **Every change rewrites everything.** A compaction that adds one file and removes one file shouldn't have to re-serialize the entire database state. With a log, the cost is proportional to the change.
2. **No audit trail.** A log lets `manifest_dump` show *how* the database arrived at its current state, not just where it is.

LevelDB, RocksDB, and Pebble all use a log of edits. Badger writes change sets to a manifest that compacts in place. TidesDB serializes the whole state. I went with the log model for the same reasons LevelDB did.

### Why a CURRENT file?

A future rotation step (write a fresh MANIFEST containing the compacted state, then atomically swap) needs a single atomic operation that flips from old to new. Renaming a small text file is that operation. Without CURRENT, the only way to swap manifests would be to rename the manifest file itself. But then readers that opened it just before the rename are stuck holding a stale handle, and recovery has to guess which manifest is current.

v1 doesn't rotate. But CURRENT is in the format from day one so I don't have to retrofit it later.

### Why reuse the WAL record framing?

The framing problem is identical for the WAL and the manifest: variable-length payloads with magic, length, checksum, and version. Solving it twice would mean two readers, two writers, two sets of truncation tests. The only thing that differs is the magic byte and the payload contents. Both are parameters.

LevelDB and RocksDB use the same `log::Writer` / `log::Reader` for both files. Pebble uses the same package, with two writer variants (one optimized for concurrent WAL appends). The on-disk format is identical across all three engines for both files. I followed the same path.

### Why TLV instead of a fixed struct?

The fields in a `VersionEdit` are sparse: most edits set only a few of them. A fixed struct would write zero bytes for every unused field. TLV writes only what's present, and old readers can detect unknown tags cleanly.

The trade-off is that the decoder is a `switch` on tag bytes instead of a straight read. That cost is paid once at recovery, not on every read.

### Why fsync on every append?

Unlike the WAL (where group commit is a future optimization), the manifest always syncs immediately. The reasoning: a manifest entry is metadata. If the SSTable it references exists on disk but the manifest entry hasn't been synced, a crash leaves the file invisible to the database. The next startup either ignores the orphan (safe) or wastes work recreating equivalent state (wasteful but correct).

Either way, the manifest is small and writes are infrequent. Sync latency is not a hot-path concern.

### Why hard-error on missing SSTables but tolerate WAL truncation?

WAL truncation is a benign crash signature: the write was in flight, never acknowledged, and discarding it is correct. The user never saw success.

A missing SSTable the manifest promises is a different signature entirely. The manifest entry was synced, which means the SSTable's write and sync completed before the manifest was synced (per the ordering rule above). For the file to be gone, something deleted it outside the database's control. That's either a bug in BeachDB or someone touching the data directory. Either way, silent recovery would hide the problem. Failing loudly is the right answer.

### Why no rotation in v1?

A single growing manifest is simpler. For a v1 database that hasn't even shipped a server yet, rotation is premature optimization. The infrastructure (CURRENT file, atomic install) is in place for when rotation matters. That likely starts when manifest replay time starts dominating startup.

---

## Versioning Strategy

v1 ships the minimum tags needed for one database without a table (just one global table): `AddFile`, `DeleteFile`, `NextFileID`, `LastSequence`, `LogNumber`. Unknown tags are a hard error, and there's no skip-unknown machinery. Forward compatibility lives in the format version byte. When new tags need to exist, I will bump the version byte and then introduce a newer format.

Tags I expect to need later include compaction pointers, Raft snapshot metadata, table catalogs, and per-table file tracking. They are deliberately not reserved. Each ships in v2, v3, v4 when the feature itself ships, and the version byte bumps with it.

The principle is the same one I follow in the WAL: reserved slots either don't fit the eventual feature or carry dead bytes forever. The version byte is the escape hatch. v1 stays small.

---

## Tooling

- **`cmd/manifest_dump`**: decode and print the manifest. Shows each `VersionEdit` in order, then the reconstructed `Version` at the end.

Example output:
```
$ manifest_dump data/
Manifest: MANIFEST-000001
Path:     data/MANIFEST-000001

Edit #0:
  next_file_id:  1
  last_sequence: 0
  log_number:    0
Edit #1:
  next_file_id:  2
  last_sequence: 42
  add_file:    level=0 id=1 size=1024 smallest="apple/40/Put" largest="zebra/42/Put"
Edit #2:
  next_file_id:  3
  last_sequence: 87
  add_file:    level=0 id=2 size=2048 smallest="alpha/80/Put" largest="yankee/87/Put"
Current Version:
  Level 0: 2 files (3072 bytes total)
    [1] apple..zebra (1024 bytes)
    [2] alpha..yankee (2048 bytes)
```

On corruption, the tool prints the `Version` reconstructed up to the failure,
reports the last valid edit, and exits non-zero:
```
$ manifest_dump data/
...
Current Version:
  Level 0: 1 files (1024 bytes total)
    [1] apple..zebra (1024 bytes)

Last valid edit: #1
Error: corrupt manifest at edit #2: beachdb/record: checksum mismatch
```

---

## References

- [LevelDB `VersionEdit` header](https://github.com/google/leveldb/blob/main/db/version_edit.h)
- [LevelDB `VersionEdit::EncodeTo` / `DecodeFrom`](https://github.com/google/leveldb/blob/main/db/version_edit.cc): TLV encoding, hard errors on unknown tags
- [LevelDB `VersionSet` (manifest read/write logic)](https://github.com/google/leveldb/blob/main/db/version_set.cc): reuses `log::Writer` / `log::Reader` from the WAL framing layer
- [RocksDB MANIFEST wiki](https://github.com/facebook/rocksdb/wiki/MANIFEST)
- [RocksDB `VersionEdit::DecodeFrom`](https://github.com/facebook/rocksdb/blob/main/db/version_edit.cc): same TLV approach, also hard errors on unknown tags
- [Pebble `internal/manifest/version_edit.go`](https://github.com/cockroachdb/pebble/blob/master/internal/manifest/version_edit.go): Go port of the same encoding
- [Pebble `record` package](https://github.com/cockroachdb/pebble/tree/master/record): shared framing used by both WAL and manifest
- [WAL Record Format](wal.md): manifest reuses the framing layer with a different magic byte
