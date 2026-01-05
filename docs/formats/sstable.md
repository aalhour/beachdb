# SSTable format (v0.1)

> Minimal, inspectable, and good enough to support point reads + forward scans.

## Goals

- Immutable sorted file
- Blocked layout to avoid full-file scans
- Dumpable/inspectable format (`sst_dump`)

## High-level layout (draft)

- Data blocks: sorted key/value entries
- Index block: maps key ranges → data block offsets
- Footer: offsets + format version + checksum

## Options (v0.1)

- Lexicographic byte comparator only
- No compression (v0.1)
- Checksums per block (recommended)

## Tooling

- `tools/sst_dump`: print footer, index, and a sample of entries.
