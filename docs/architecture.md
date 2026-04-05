# BeachDB: Architecture (v0.1)

This doc is intentionally short. It's meant to answer: *what are the parts, and what must always be true?*

## Components

- **WAL**: append-only log of committed batches; source of truth for recovery.
- **Memtable**: sorted in-memory view of recent writes (including tombstones), keyed by user-key + internal seqno.
- **Immutable memtable**: a sealed memtable being flushed to an SSTable by the background flush goroutine. At most one exists at a time. Reads check it between the active memtable and SSTables.
- **SSTables**: immutable sorted files on disk, produced by memtable flushes. Each is self-describing (data blocks, index block, footer with checksums).
- **Manifest**: *(not yet implemented)* append-only record of which SSTables exist and how they're organized. Currently the engine discovers SSTables by scanning the database directory for `*.sst` files at startup.

## Data flow

### Write path
1. Assign seqno(s) for the batch
2. Append batch record to WAL + checksum
3. `fsync` WAL (default)
4. Apply batch to memtable
5. If auto-flush is enabled and the memtable exceeds the configured size threshold:
   - Stall the writer if an immutable memtable is already being flushed (`sync.Cond` wait)
   - Swap the active memtable into the immutable slot; replace it with a fresh memtable
   - Signal the background flush goroutine

### Flush path
1. Background goroutine wakes on signal, grabs the immutable memtable
2. Releases the lock, writes a new SSTable to disk (`fsync` file + `fsync` directory)
3. Re-acquires the lock, publishes the new SSTable reader, clears the immutable slot
4. Broadcasts to wake any stalled writers

### Read path (`Get`)
1. Check the active memtable
2. Check the immutable memtable (non-nil during flush)
3. Scan SSTables newest-first (reverse filename order)
4. First match wins; tombstones stop the search

### Read path (iteration)
- *(not yet implemented)* Merge iterator across memtable(s) and SSTables.

## Invariants (must always be true)

- **Atomic batch visibility**: a batch is either fully visible or not visible at all at a given snapshot.
- **Snapshot stability**: an iterator must not observe future writes after creation.
- **Deterministic recovery**: WAL replay yields the same state every time.
- **No silent corruption**: corrupted WAL/SST records are detected and surfaced.

## Not yet implemented

- Merge iterator (cross-source iteration)
- Manifest/versioning (durable SSTable metadata, replacing directory scanning)
- Bloom filters (skip SSTables that cannot contain a key)
- Compaction (merge old SSTables to reclaim space)
- Block cache (cache hot data blocks in memory)
- Transactions
- Replication
