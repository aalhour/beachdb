# Glossary

## Engine terms

- **WAL**: write-ahead log; the durability spine.
- **Memtable**: sorted in-memory structure that holds recent writes.
- **Immutable memtable**: sealed memtable waiting to be flushed.
- **SSTable**: immutable sorted table file on disk.
- **Manifest**: append-only metadata log that records which SSTables exist and where.
- **Snapshot**: a read view at a particular sequence number (`seqno`).
- **Seqno**: monotonic sequence number assigned to writes; used for snapshot reads.

## Project arcs (Season 1)

- **Engine**: durability + storage formats + compaction.
- **Server**: protocol framing + backpressure + observability.
- **Raft**: replicated state machine where a log entry is a serialized WriteBatch.

## Not in Season 1 (by design)

- Regions / splits / rebalancing
- SQL
- full transactions
