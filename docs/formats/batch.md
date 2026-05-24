# Batch Encoding Format (v1)

> The Batch is BeachDB's unit of atomicity. It's the payload that goes into the WAL, and later becomes a Raft log entry.

Introduced in BeachDB [v0.0.1](https://github.com/aalhour/beachdb/releases/tag/v0.0.1).

## Purpose

A `Batch` is a sequence of Put and Delete operations that are applied atomically. The encoding serializes this in-memory structure into a flat byte array that can be:

- Written to the WAL for durability
- Sent over the network to replicas (future)
- Used as a Raft log entry payload (future)

The format is designed to be **deterministic** (same batch → same bytes) and **self-describing** (you can decode without external schema).

---

## Binary Format

```
Batch Encoding Layout (v1)
==========================

┌─────────────────────────────────────────────────────────────┐
│                      BATCH HEADER (8 bytes)                 │
├─────────┬─────────────────────┬─────────────────────────────┤
│ version │ reserved            │ op_count                    │
│ 1 byte  │ 3 bytes (0x000000)  │ 4 bytes (big-endian uint32) │
├─────────┴─────────────────────┴─────────────────────────────┤
│                      OPERATIONS (variable)                  │
├─────────────────────────────────────────────────────────────┤
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Op (Put)                                                │ │
│ │ ┌─────────┬─────────┬───────┬───────────┬─────────────┐ │ │
│ │ │ op_type │ key_len │ key   │ value_len │ value       │ │ │
│ │ │ 1 byte  │ 4 bytes │ K     │ 4 bytes   │ V           │ │ │
│ │ └─────────┴─────────┴───────┴───────────┴─────────────┘ │ │
│ └─────────────────────────────────────────────────────────┘ │
│ ┌─────────────────────────────────────────────────────────┐ │
│ │ Op (Delete)                                             │ │
│ │ ┌─────────┬─────────┬───────┐                           │ │
│ │ │ op_type │ key_len │ key   │  (no value for Delete)    │ │
│ │ │ 1 byte  │ 4 bytes │ K     │                           │ │
│ │ └─────────┴─────────┴───────┘                           │ │
│ └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

### Header Fields

| Offset | Size | Field | Description |
|--------|------|-------|-------------|
| 0 | 1 | `version` | Format version (`0x01` for v1) |
| 1 | 3 | `reserved` | Reserved bytes (`0x00 0x00 0x00`), aligns op_count to 4-byte boundary |
| 4 | 4 | `op_count` | Number of operations (big-endian uint32) |

### Operation Fields

Each operation is encoded sequentially after the header:

**Put Operation:**

| Offset | Size | Field | Description |
|--------|------|-------|-------------|
| 0 | 1 | `op_type` | `0x01` (Put) |
| 1 | 4 | `key_len` | Key length in bytes (big-endian uint32) |
| 5 | K | `key` | Key bytes |
| 5+K | 4 | `value_len` | Value length in bytes (big-endian uint32) |
| 9+K | V | `value` | Value bytes |

**Delete Operation:**

| Offset | Size | Field | Description |
|--------|------|-------|-------------|
| 0 | 1 | `op_type` | `0x02` (Delete) |
| 1 | 4 | `key_len` | Key length in bytes (big-endian uint32) |
| 5 | K | `key` | Key bytes |

Delete operations have no value fields — they only need to identify the key being deleted.

### Operation Types

| Type | Value | Description |
|------|-------|-------------|
| Put | `0x01` | Store a key-value pair |
| Delete | `0x02` | Remove a key (tombstone) |

The `op_type` byte has 256 possible values. v1 uses two. The other 254 are unallocated — v1 readers reject any unknown value as a hard error, so silent misinterpretation isn't possible.

New op variants: table-aware writes, merge operators, or range deletes will belong in a v2 batch format, signaled by the version byte in the header. v1 won't be retrofitted with new op types.

---

## Example

A batch with two operations:
```go
batch.Put([]byte("name"), []byte("ahmad"))
batch.Delete([]byte("age"))
```

Encodes to 34 bytes:

```
Offset   Hex                        Meaning
------   ---                        -------
0        01                         version = 1
1        00 00 00                   reserved
4        00 00 00 02                op_count = 2

--- Op #1: Put("name", "ahmad") ---
8        01                         op_type = Put
9        00 00 00 04                key_len = 4
13       6E 61 6D 65                key = "name"
17       00 00 00 05                value_len = 5
21       61 68 6D 61 64             value = "ahmad"

--- Op #2: Delete("age") ---
26       02                         op_type = Delete
27       00 00 00 03                key_len = 3
31       61 67 65                   key = "age"
```

---

## Design Decisions

### Why length-prefixed strings?

I use 4-byte length prefixes instead of null terminators or delimiters because:

1. **Binary-safe**: Keys and values can contain any bytes, including `0x00`.
2. **Predictable parsing**: The decoder knows exactly how many bytes to read.
3. **No escaping**: No need to escape special characters.

### Why no value for Delete?

A Delete operation only needs to record *which key* is being deleted. The tombstone semantics are handled by the memtable and compaction layers — the batch just records the intent.

This saves space: a Delete is 5 bytes smaller than a Put with an empty value would be.

### Why big-endian?

Consistency with the WAL record format, and readability in hex dumps. See [wal.md](wal.md#why-big-endian-byte-order) for the full rationale.

### Why reserved bytes in the header?

The 3 reserved bytes after the version serve two purposes:

1. **Alignment**: The `op_count` field starts at offset 4, which is a 4-byte aligned boundary. This is slightly friendlier for memory-mapped access (not used in v1, but doesn't hurt).

2. **Future expansion**: If I need to add header flags or fields later, I have 3 bytes to use before bumping the format version.

### Why is version a single byte?

One byte gives me 255 future versions. If I ever need more, I can use the reserved bytes or define a version 255 that indicates "look elsewhere for the real version."

---

## Determinism Guarantee

The same batch always encodes to the same bytes:

- Operations are encoded in insertion order
- No padding between operations (beyond the header's reserved bytes)
- No optional fields or variable encoding

This is critical for:
- **WAL checksums**: The checksum must be reproducible
- **Raft**: Replicas must compute the same state from the same log entry

---

## Size Calculation

To calculate the encoded size of a batch:

```
size = 8  (header)

for each op:
    size += 1              (op_type)
    size += 4              (key_len)
    size += len(key)       (key bytes)
    
    if op is Put:
        size += 4          (value_len)
        size += len(value) (value bytes)
```

---

## Related Formats

- [WAL Record Format](wal.md) — wraps the encoded batch with framing and checksum
