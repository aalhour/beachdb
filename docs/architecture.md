# BeachDB: Architecture (v0.1)

This doc is intentionally short. It’s meant to answer: *what are the parts, and what must always be true?*

## Components

- **WAL**: append-only log of committed batches; source of truth for recovery.
- **Memtable**: sorted in-memory view of recent writes (including tombstones), keyed by user-key + internal seqno.
- **Immutable memtables**: sealed memtables waiting to be flushed into SSTables.
- **SSTables**: immutable sorted files on disk.
- **Manifest**: append-only record of which SSTables exist and how they’re organized.

## Data flow

### Write path
1. Assign seqno(s) for the batch
2. Append batch record to WAL + checksum
3. `fsync` WAL (v0.1 default)
4. Apply batch to memtable
5. (Later) rotate memtable → immutable → flush to SST

### Read path
- At a snapshot seqno:
  - point lookup: consult memtable(s), then SSTables
  - iteration: merge iterator across memtable(s) and SSTables

## Invariants (must always be true)

- **Atomic batch visibility**: a batch is either fully visible or not visible at all at a given snapshot.
- **Snapshot stability**: an iterator must not observe future writes after creation.
- **Deterministic recovery**: WAL replay yields the same state every time.
- **No silent corruption**: corrupted WAL/SST records are detected and surfaced.

## Non-goals (v0.1)

- Concurrent writers
- Background compaction
- Transactions
- Replication
