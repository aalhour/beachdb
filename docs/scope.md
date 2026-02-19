# BeachDB v0.1 — Scope Contract

## What BeachDB is

A learning-first, RocksDB-inspired **LSM key-value store** in Go.

Core goals:

- **Small + inspectable** (you can dump on-disk state and understand it)
- **Correct + crash-safe** (WAL + recovery are first-class)
- **Documented semantics** (snapshots + iterators have explicit guarantees)

This is a database built to **teach**—to me, and to anyone who wants to tinker under the hood.

---

## What BeachDB is not (yet)

Intentionally out of scope for v0.1:

- SQL / query planner
- Transactions / MVCC beyond snapshot reads (no serializable isolation)
- Secondary indexes
- Compression buffet (none for v0.1)
- Column families / multi-tenancy
- TTL, encryption, backups, online migration
- Replication / consensus (Raft comes later)
- Sharding / regions (mini-HBase layer comes later)

---

## Public API surface (v0.1)

### KV operations

- `Put(key, value)`
- `Get(key) -> value | not_found`
- `Delete(key)`
- `Write(batch)` — atomic batch apply

### Iteration

- `NewIterator(snapshot)` produces a **forward-only** iterator with:
  - `Seek(key)`
  - `Next()`
  - `Key()/Value()`

Notes:

- Iteration + ordering are **first-class** goals in v0.1.
- Reverse iterators, prefix iterators, and fancy scan options are out of scope.

---

## Keys and values

- Keys and values are **opaque byte slices** (`[]byte`).
- Key ordering is **lexicographic byte order**.
- Any "HBase-like composite key encoding" (row|family|qualifier|ts) belongs in a higher layer later, without changing the engine contract.

---

## Read semantics (snapshot-based)

- All reads happen at a **snapshot**.
- A snapshot is represented by a **monotonic sequence number** (`seqno`).
- Default read behavior:
  - `Get()` reads at the **latest committed** snapshot.
  - `Iterator` reads at the snapshot captured at iterator creation time.
- A snapshot read is **stable**:
  - It must not observe partial state during flush/compaction.
  - It must not "time travel" forward while iterating.

---

## Write semantics

- v0.1 assumes a **single writer** (simple and deterministic).
- Every write is assigned a **seqno**.
- `Write(batch)` is the unit of:
  - atomicity
  - durability
  - (future) replication via Raft log entries

---

## Durability contract (WAL + recovery)

- WAL is append-only and includes:
  - checksums per record (detect corruption)
  - clear record boundaries
  - explicit commit boundaries for `Write(batch)`
- On startup:
  - WAL recovery is **deterministic**
  - Replaying the same WAL twice yields the same final state (**idempotent apply**)
- Corruption handling:
  - v0.1 prefers **detect + fail fast** over repair
  - no silent success on corrupted records

### Durability syscalls (tracked topics)

We will explicitly cover these in the WAL/durability work and its corresponding article(s):

- `fsync(2)` vs `fdatasync(2)` — what they promise, and what they cost
- buffered I/O vs direct I/O implications (when relevant)
- barriers / drive caches / “did it *actually* hit stable storage?”
- batching/group-commit as a later milestone

See [`docs/formats/wal.md`](formats/wal.md) and [`docs/formats/batch.md`](formats/batch.md) for wire formats.

---

## Storage model (LSM)

- **Memtable** holds recent writes in a sorted in-memory structure.
- **Flush** turns a memtable into an immutable **SSTable**.
- **Read path** merges:
  - memtable
  - immutable SSTables
  using merge iterators, respecting tombstones and snapshot seqno.

Compaction:

- v0.1 may ship without compaction initially, but the design must allow adding:
  - exactly **one** compaction strategy (knob-free story) in v0.2+

---

## Memtable semantics

- Keys sorted by `(user_key ASC, seqno DESC)` — newest version first
- Writes always insert; never update in place (MVCC)
- Deletes write tombstones (entries with `Kind=Delete`)
- Iterator sees all entries unfiltered; higher layers filter by snapshot

See [`docs/memtable.md`](memtable.md) for detailed semantics.

---

## Metadata (manifest / versioning)

- The set of SSTables and their levels is tracked in a **manifest/version log**.
- Startup reconstruction uses:
  - manifest (file set + ordering)
  - WAL (replay newest state)
- The manifest is treated as a durability-critical artifact and must be replayable.

---

## Testing philosophy (v0.1)

- **Reference-model tests**:
  - randomized Put/Get/Delete sequences compared against a simple in-memory reference model
  - iterator ordering correctness against the same model at a snapshot
- **Crash loop tests**:
  - run a workload, crash/kill mid-write, reopen, verify invariants
- **Inspection tools are first-class**:
  - `wal_dump`, `sst_dump`, `manifest_dump`
  - "I don't trust it until I can dump it."

---

## Performance philosophy (v0.1)

- Correctness > speed.
- Benchmarks exist to reveal:
  - read amplification
  - write amplification
  - the cost of durability (`fsync`)
- Use a small set of fixed workloads documented in `bench/workloads.md`.
- No micro-optimizations without a clear measured story.

---

## Future direction (mini-HBase + Raft)

This contract is designed to make distributed work boring later:

- `Write(batch)` becomes a Raft log entry (replicated command).
- Snapshot reads make consistent scans possible.
- SST metadata (min/max key, sizes) supports later region splitting/routing.

Principle:

> **Build an engine that can act as a replicated state machine without changing its soul.**
