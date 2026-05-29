# SSTable Format (v1)

> The SSTable is BeachDB's immutable on-disk sorted file. It is the bridge between the in-memory memtable and the rest of the LSM tree.

BeachDB does **not** have user-facing "tables" yet. In this document, "SSTable" means **Sorted String Table** in the LevelDB/RocksDB sense: a sorted key-value file for the storage engine itself, not an HBase-style table abstraction.

Introduced in BeachDB [v0.0.3](https://github.com/aalhour/beachdb/releases/tag/v0.0.3).

## Goals

- **Immutable sorted file**: Written once, read many times.
- **Fast point reads**: Find candidate blocks without scanning the entire file.
- **Forward scans**: Iterate entries in internal-key order.
- **Corruption detection**: Validate blocks before trusting them.
- **Inspectability**: Easy to dump with `sst_dump` and reason about byte-by-byte.

---

## Scope (v1)

This format supports:

- opaque byte-slice user keys and values
- full internal keys (`user_key + seqno + kind`)
- point lookup within one SSTable
- forward iteration
- per-block CRC32C checksums

This format does **not** include:

- compression
- bloom filters
- prefix compression / restart arrays
- block cache metadata
- manifest/version metadata
- user-facing table schemas or column-family semantics

Those come later, if at all. v1 is intentionally small and inspectable.

---

## File Layout

An SSTable file is laid out like this:

```text
[data block 0][data block 1]...[data block N][index block][footer]
```

- **Data blocks** store sorted key-value entries.
- **Index block** stores one entry per data block, mapping a block boundary key to the data block's offset and size.
- **Footer** is a fixed-size record at EOF that tells the reader where the index block lives.

The reader bootstraps from the footer:

1. seek to EOF minus footer size
2. read and validate footer
3. read and validate index block
4. lazily read data blocks as needed

---

## Byte Order and Core Rules

- All multi-byte integers are encoded in **big-endian** order.
- All variable-sized byte fields are length-prefixed.
- Internal keys are encoded using `keys.InternalKey.Encode()`, which yields:
  `[user_key][seqno:8][kind:1]`
- Data and index blocks are checksummed with **CRC32C**.
- The footer has its own checksum.

This format uses one comparator only:

- **user key**: ascending lexicographic byte order
- **seqno**: descending within the same user key

That is the same ordering the memtable already uses.

---

## Entry Encoding

Each SSTable data entry is encoded as:

```text
[internal_key_len:4][internal_key_bytes][value_len:4][value_bytes]
```

Where:

- `internal_key_len` is the length in bytes of `internal_key_bytes`
- `internal_key_bytes` is the encoded `InternalKey`
- `value_len` is the length in bytes of the value
- `value_bytes` is the raw value

Notes:

- Values are opaque bytes. The SSTable layer does not interpret them.
- Deletes are represented by internal keys whose `Kind` is `Delete`; the value may be empty.
- v1 stores **full internal keys**, not compressed prefixes.

---

## Data Block Format

A data block contains a sequence of encoded entries, followed by a checksum trailer.

```text
Data Block Layout (v1)
======================

[entry 0][entry 1]...[entry N][checksum:4]
```

### Trailer

| Field | Size | Purpose |
|-------|------|---------|
| `checksum` | 4 bytes | CRC32C of the block payload bytes only (all entry bytes before the trailer). |

### Invariants

- Entries in a data block are sorted by internal key.
- An entry is never split across blocks.
- A block may contain one oversized entry if that entry alone exceeds the configured target block size.
- A user key's versions may span adjacent blocks. The writer does **not** keep all versions of one user key in the same block.

### Empty Blocks

- Data blocks are never emitted empty.
- An SSTable with zero entries therefore has **zero data blocks**.

---

## Index Block Format

The index block contains one entry per data block.

Each index entry is encoded as:

```text
[last_internal_key_len:4][last_internal_key_bytes][block_offset:8][block_size:4]
```

Where:

- `last_internal_key_bytes` is the last internal key stored in that data block
- `block_offset` is the absolute byte offset of the data block in the file
- `block_size` is the full on-disk size of that block, including its checksum trailer

The index block itself is block-encoded like a data block:

```text
[index entry 0][index entry 1]...[index entry N][checksum:4]
```

### Why the Last Key?

The index stores the **last internal key** of each data block. This means:

- index entries are sorted in the same order as data blocks
- the reader can binary-search the index to find the first block whose `last_key` is large enough to contain the target

For point lookup by user key `k`, the reader constructs a synthetic maximum internal key:

```text
(user_key = k, seqno = MaxUint64, kind = 0xFF)
```

and binary-searches for the first index entry whose `last_key >= synthetic_key`.

This finds the earliest plausible block for that user key.

### What the Index Is For

The SSTable index is for:

- point lookup inside one SSTable
- `Seek()` inside one SSTable
- avoiding whole-file scans for ordinary reads

It is **not** for:

- choosing which SSTable files to read across the engine
- cross-file merge ordering
- manifest/version state

Those concerns live above the SSTable format.

---

## Footer Format

The footer is a fixed-size record at EOF.

```text
Footer Layout (v1)
==================

Offset  Size  Field            Description
------  ----  -----            -----------
0       8     magic            File magic: ASCII "BEACHSST"
8       4     version          SSTable format version (uint32, v1 = 1)
12      8     index_offset     Absolute file offset of the index block
20      4     index_size       Full on-disk size of the index block, including checksum trailer
24      4     data_block_count Number of data blocks in the file
28      8     entry_count      Total number of entries across all data blocks
36      4     checksum         CRC32C of footer bytes [0:36]

Footer size: 40 bytes
```

### Field Details

| Field | Value | Purpose |
|-------|-------|---------|
| `magic` | `BEACHSST` | Identifies the file as a BeachDB SSTable before any deeper parsing happens. |
| `version` | `0x00000001` | Reject unsupported future layouts. |
| `index_offset` | uint64 | Lets the reader find the index block without scanning the file. |
| `index_size` | uint32 | Tells the reader exactly how many bytes to read for the index block. |
| `data_block_count` | uint32 | Allows inspection and sanity-checking. |
| `entry_count` | uint64 | Total entry count for tooling and validation. |
| `checksum` | uint32 | CRC32C of the first 36 footer bytes. |

### Footer Magic

The 8-byte footer magic is the ASCII string:

```text
B E A C H S S T
```

This is encoded literally in the file as 8 bytes, and interpreted as a big-endian `uint64` in code if desired.

---

## Empty SSTable Semantics

An SSTable with zero entries is valid.

A valid empty SSTable contains:

- zero data blocks
- an index block with zero index entries and a checksum trailer
- a valid footer

A **zero-byte file is not a valid SSTable**.

This matters because "empty table" and "corrupt or missing file" are different conditions and must not be conflated.

---

## Read Semantics

### Opening a Table

To open an SSTable:

1. Stat the file and ensure it is at least 40 bytes long.
2. Seek to `file_size - footer_size`.
3. Read the 40-byte footer.
4. Validate:
   - footer checksum
   - magic
   - supported version
   - `index_offset` and `index_size` are in bounds
5. Read the index block.
6. Validate the index block checksum.
7. Parse all index entries into memory.

The reader does **not** load data blocks eagerly.

### Point Lookup

To look up a user key at sequence number `S`:

1. Build a synthetic maximum internal key for the target user key.
2. Binary-search the index for the earliest candidate block.
3. Read and validate that block.
4. Scan entries in order.
5. For matching user keys, return the first version whose `seqno <= S`.
6. If that version is a tombstone, return not found.

Because one user key may span adjacent blocks, lookup may continue into the next block while that user key may still be present.

This is a deliberate v1 tradeoff:

- the writer stays simple
- the reader handles the rare cross-block continuation case

### Iteration

Iteration walks the SSTable in full internal-key order:

- block 0, then block 1, and so on
- within each block, entry by entry

The iterator:

- loads the index eagerly
- loads data blocks lazily
- does **not** collapse versions of the same user key
- does **not** filter tombstones

Higher layers handle snapshot filtering and merge semantics later.

---

## Validation and Corruption Handling

The reader must reject the following conditions:

- file smaller than footer size
- bad footer checksum
- bad footer magic
- unsupported version
- index block offset/size outside file bounds
- index block checksum mismatch
- data block checksum mismatch
- malformed length prefixes
- truncated entry payloads
- internal keys that fail to decode

v1 does not attempt repair.

Corruption is **detected and surfaced**, not silently skipped.

### Truncation Policy

Unlike the WAL, SSTables are immutable completed files. A truncated SSTable is invalid and must be rejected. There is no "ignore trailing partial record" behavior here.

---

## Design Decisions

### Why blocks instead of one giant sorted array?

Because a blocked layout gives you:

1. **Faster seeks**: binary-search the index, then scan one block.
2. **Checksums at useful granularity**: detect corruption per block.
3. **Future flexibility**: compression and filters can be added per block later.

### Why store full internal keys?

Because this is a learning-first v1 format.

Full keys are:

- easier to hex-dump
- easier to debug
- easier to specify correctly

Prefix compression can come later if benchmarks justify it.

### Why keep the index inside the SSTable?

Because each file should be self-describing.

If `sst_dump` opens a single `.sst` file, it should be able to explain that file completely without consulting external metadata.

### Why a footer at EOF?

Because it gives the reader a fixed bootstrap point:

1. seek to the end
2. read one fixed-size structure
3. find the index from there

No scanning, no guesswork.

### Why CRC32C?

CRC32C is:

- fast
- hardware-accelerated on modern CPUs
- widely used in storage systems

It is a good fit for v1 corruption detection.

### Why big-endian integers?

Because they are easier to read in hex dumps and keep BeachDB's binary formats consistent.

### Why allow a user key to span adjacent blocks?

Because the alternative is a more complicated writer that tries to preserve user-key grouping at block boundaries.

That adds complexity in the wrong place for v1.

BeachDB v1 keeps block writing simple and accepts a little reader-side continuation logic instead.

### Why doesn't this doc define WAL retirement?

Because WAL lifecycle is not part of the SSTable binary format.

The SSTable format describes the immutable file itself. WAL rotation, manifest state, and obsolete-log tracking belong to higher-level engine metadata and later milestones.

---

## Tooling

- **`cmd/sst_dump`**: Opens an SSTable, prints footer metadata, index entries, and optionally all entries.

Example output:

```text
$ sst_dump /tmp/mydb/000001.sst
SSTable: /tmp/mydb/000001.sst
  Version: 1
  Entries: 1523
  Data blocks: 12
  Index block: offset=98304 size=812

Blocks:
  Block 0: offset=0 size=8192 last_key="user:00123#17"
  Block 1: offset=8192 size=8192 last_key="user:00256#11"
```

With entries:

```text
$ sst_dump -entries /tmp/mydb/000001.sst
  [0] Put    key="user:00001" seqno=9 value=128 bytes
  [1] Delete key="user:00001" seqno=7 value=0 bytes
  [2] Put    key="user:00002" seqno=6 value=64 bytes
```

---

## Worked Example

Suppose an SSTable contains three entries:

```text
("apple",  9, Put)    -> "red"
("banana", 7, Put)    -> "yellow"
("banana", 5, Delete) -> ""
```

With a small block size, the file might look like:

```text
[data block 0: apple, banana@7]
[data block 1: banana@5]
[index block: last_key(block0), last_key(block1)]
[footer]
```

Point lookup for `"banana"` at seqno 6:

1. search index using synthetic key `("banana", MaxUint64, 0xFF)`
2. land on the first block whose last key can contain `"banana"`
3. scan entries
4. continue into the next block if `"banana"` is still present
5. return the visible version at or below seqno 6, which is the tombstone at seqno 5

This example is exactly why the reader cannot assume one user key fits in one block.

---

## References

- **RocksDB**
  - [A Tutorial of RocksDB SST formats](https://github.com/facebook/rocksdb/wiki/A-Tutorial-of-RocksDB-SST-formats)
  - [RocksDB BlockBasedTable Format](https://github.com/facebook/rocksdb/wiki/rocksdb-blockbasedtable-format)
  - [RocksDB Bloom Filter](https://github.com/facebook/rocksdb/wiki/RocksDB-Bloom-Filter)

- **LevelDB**
  - [LevelDB table file format](https://github.com/google/leveldb/blob/main/doc/table_format.md)
  - [LevelDB implementation overview](https://github.com/google/leveldb/blob/main/doc/impl.md)
  - [LevelDB table format code (`table/format.h`)](https://github.com/google/leveldb/blob/main/table/format.h)

- **HBase / HFile**
  - [HFile API docs](https://hbase.apache.org/devapidocs/org/apache/hadoop/hbase/io/hfile/HFile.html)
  - [HFileScanner API docs](https://hbase.apache.org/2.4/devapidocs/org/apache/hadoop/hbase/io/hfile/HFileScanner.html)
  - [StoreFile, HBase Reference Guide](https://hbase.apache.org/book.html#hfile)

- **TidesDB**
  - [How does TidesDB work?](https://tidesdb.com/getting-started/how-does-tidesdb-work/)

These were useful as **directional references**, not as templates to copy mechanically. BeachDB v1 intentionally keeps a much smaller format: blocked layout, internal per-file index, fixed footer bootstrap, and no bloom/filter block until the later read-acceleration milestone.
