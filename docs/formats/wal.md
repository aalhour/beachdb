# WAL format + durability notes (v0.1)

> This doc is a living spec

## Goals

- Deterministic recovery
- Corruption detection (checksums)
- Clear commit boundaries for `Write(batch)`
- Evidence-friendly: easy to dump and reason about

## Record model (draft)

- Header: magic, version, record type, length
- Payload: encoded batch (ops)
- Trailer: checksum (CRC32C or similar)

## Commit boundary

- A `Write(batch)` is one committed record (or a small sequence with an explicit commit marker).
- v0.1 default durability: `fsync` on each committed batch.

## Syscalls to cover (tracked)

- `fsync(2)`
- `fdatasync(2)`
- `open(2)` flags and `O_DSYNC` / `O_SYNC` (if used)
- page cache implications (buffered I/O)
- what “durable” actually means on modern SSDs (at a practical level)

## Tooling

- `tools/wal_dump`: decode and print records; detect checksum failures.
