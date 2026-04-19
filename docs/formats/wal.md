# WAL Format (v1)

> The Write-Ahead Log is BeachDB's durability spine. Every committed batch lands here before it's acknowledged.

## Goals

- **Deterministic recovery**: Replay the same WAL twice, get the same state.
- **Corruption detection**: Checksums catch silent bit flips and torn writes.
- **Clear commit boundaries**: One record = one atomic batch.
- **Inspectability**: Easy to dump, easy to reason about.

---

## Record Format

A WAL file is a sequence of records. Each record wraps one encoded `Batch`.

```
WAL Record Layout (v1)
======================

Offset  Size  Field       Description
------  ----  -----       -----------
0       2     magic       0xBE 0xAC ("BEach")
2       1     version     0x01 for v1
3       1     type        Record type (0x01 = Full)
4       4     length      Payload length in bytes (big-endian uint32)
8       4     checksum    CRC32C of payload (big-endian uint32)
12      N     payload     Encoded batch (N = length bytes)

Header size: 12 bytes
Total record size: 12 + length bytes
```

### Field Details

| Field | Value | Purpose |
|-------|-------|---------|
| `magic` | `0xBEAC` | Identifies this as a BeachDB WAL record. Helps `wal_dump` reject garbage files and aids recovery scanning. |
| `version` | `0x01` | Format version. Allows future changes without silent misinterpretation. |
| `type` | `0x01` | Record type. v1 only uses `Full` (complete record). Reserved for future fragmentation support. |
| `length` | uint32 | Payload size. Tells the reader exactly how many bytes to read after the header. |
| `checksum` | uint32 | CRC32C of the payload bytes. Detects corruption before we trust the data. |
| `payload` | bytes | The encoded `Batch` (see [batch.md](batch.md)). |

### Record Types

| Type | Value | Description |
|------|-------|-------------|
| `Full` | `0x01` | Complete record in a single piece. |
| `First` | `0x02` | (Future) First fragment of a large record. |
| `Middle` | `0x03` | (Future) Middle fragment. |
| `Last` | `0x04` | (Future) Final fragment. |

v1 only uses `Full`. Fragmentation is reserved for when batches exceed a block boundary (not implemented).

---

## Batch Encoding

The payload of a WAL record is an encoded `Batch`. See [batch.md](batch.md) for the full format specification.

---

## Recovery Semantics

### On Startup

1. Open the WAL file for reading.
2. Read records sequentially until EOF or error.
3. For each valid record: decode the batch, apply to in-memory state.
4. Open the WAL file for append (new writes go at the end).

### Handling Truncation

A truncated record at the end of the WAL means the process crashed mid-write:

- **Truncated header** (< 12 bytes): Ignore, treat as EOF.
- **Truncated payload** (header valid, payload incomplete): Ignore, treat as EOF.
- After recovery, truncate the WAL back to the last fully validated record
  before reopening it for append.

The incomplete record was never `fsync`'d, so the batch was never acknowledged to the caller. Discarding it is correct.

### Handling Corruption

- **Bad magic**: Stop. Either file isn't a WAL, or we've hit garbage.
- **Checksum mismatch**: Stop. Data is corrupted. Fail loudly.
- **Unsupported version**: Stop. We don't understand this format.

v1 does not attempt repair. Corruption is surfaced, not hidden.

---

## Durability Contract

- **Commit = fsync**: A batch is committed only after `fsync` returns.
- **Acknowledged = durable**: If `Write(batch)` returns success, the batch survives restart.
- **Crash before fsync**: The batch is lost, but the caller never saw success.

---

## Design Decisions

### Why checksum in the header, not a trailer?

I decided to put the checksum in the header (before the payload) rather than as a trailing field. This means the reader can:

1. Read 12 bytes (fixed header size).
2. Know the payload length and expected checksum before reading anything else.
3. Reject obviously broken headers (bad magic, absurd length) without reading the payload at all.

To be clear: we still have to read the entire payload to compute the checksum and compare it. There's no magic here — CRC32 needs all the bytes. But with everything in the header, we read once (header), know exactly how much to read next (payload), and have the expected checksum ready for comparison. No backtracking, no extra read for a trailer.

The tradeoff is that the writer must compute the checksum before writing the header, which means buffering the payload in memory first. For BeachDB's workload (small batches, fsync per batch), this is fine.

### Why checksum only the payload, not the header?

I checksum the payload only, not the header fields. The reasoning:

1. **Header corruption is self-evident.** If magic is wrong, I reject immediately. If length is corrupted, I'll either read too few bytes (truncation) or too many (likely hitting the next record's magic or EOF).

2. **Payload is the valuable data.** The batch contents are what I'm protecting. If the payload checksum passes, I trust the data.

3. **Simplicity.** One checksum, one thing being checksummed. RocksDB and LevelDB do the same.

If I were paranoid, I could checksum header + payload together. But that complicates the write path (you can't write the header until you've checksummed everything including the header... which includes the checksum field itself). Not worth it for v1.

### Why big-endian byte order?

I chose big-endian (network byte order) for all multi-byte integers. Reasons:

1. **Hex dumps are readable.** `0x00 0x00 0x00 0x2A` clearly reads as 42. Little-endian would be `0x2A 0x00 0x00 0x00`, which is harder to scan visually.

2. **Convention for file formats.** Most binary file formats and network protocols use big-endian. It's what people expect when inspecting with `hexdump`.

3. **Consistency.** One byte order everywhere. No mental context-switching.

Performance difference is negligible on modern CPUs.

### Why CRC32C specifically?

CRC32C (Castagnoli) over plain CRC32 because:

1. **Hardware acceleration.** Modern CPUs (Intel SSE 4.2, ARM) have native CRC32C instructions. Go's `hash/crc32` uses them automatically.

2. **Better error detection.** CRC32C has better hamming distance properties for certain error patterns.

3. **Industry standard.** Used by RocksDB, LevelDB, Spanner, and others.

### Why a magic number?

The 2-byte magic (`0xBEAC`) serves two purposes:

1. **File identification.** If `wal_dump` opens a JPEG by accident, it fails immediately with "bad magic" instead of interpreting pixel data as batches.

2. **Recovery scanning.** If I ever need to scan a partially corrupted WAL looking for valid records, the magic gives me a sync point. (Not implemented in v1, but the format supports it.)

Two bytes is enough for these purposes without wasting space.

### Why a version byte?

The version byte (`0x01`) is insurance against my future self. If I change the header layout, add fields, or switch checksum algorithms, old readers can say "I don't understand version 2" instead of silently misinterpreting bytes.

One byte gives me 255 future versions. That's plenty.

### Why record types if v1 only uses Full?

I'm reserving the record type field for future fragmentation support. If a batch ever exceeds a convenient size (say, 32KB block boundary), I'd split it into First/Middle/Last fragments.

For v1, every record is `Full`. But having the field costs 1 byte and saves a format version bump later.

---

## Tooling

- **`cmd/wal_dump`**: Decode and print records. Shows record count, lengths, checksum status. Detects truncation and corruption.

Example output:
```
$ wal_dump /tmp/beachdb/wal
Record 0: 47 bytes, checksum OK
Record 1: 23 bytes, checksum OK
Record 2: 31 bytes, checksum OK
End of WAL (3 records)
```

On corruption:
```
$ wal_dump /tmp/beachdb/wal
Record 0: 47 bytes, checksum OK
Record 1: checksum mismatch (expected 0xABCD1234, got 0xDEADBEEF)
Stopped at record 1
```

---

## Syscalls (Durability Mechanics)

These are the syscalls that make durability real:

| Syscall | Purpose |
|---------|---------|
| `write(2)` | Append bytes to the file. Data lands in kernel page cache. |
| `fsync(2)` | Flush file data AND metadata to stable storage. |
| `fdatasync(2)` | Flush file data only (not metadata). Faster, but doesn't update mtime/size. |

BeachDB v1 uses `fsync` after each committed batch. This is the safest default:

- Data is on disk (not just page cache).
- Metadata is updated (file size reflects the write).
- Survives power loss (assuming honest hardware).

Future optimization: group commit (batch multiple `Write` calls into one `fsync`). Not in v1.

---

## References

- [LevelDB Log Format](https://github.com/google/leveldb/blob/v1.20/doc/log_format.md)
- [RocksDB WAL](https://github.com/facebook/rocksdb/wiki/Write-Ahead-Log-File-Format)
- [CRC32C in Go](https://pkg.go.dev/hash/crc32)
