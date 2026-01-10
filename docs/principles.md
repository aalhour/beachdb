# BeachDB — Guiding Principles + API Style

## Guiding principles (with rationale)

### 1) Learning-first, truth-first
- **Goal:** capture near-production truths (correctness, durability, observability) without building a forever-project.
- **Rationale:** the project is a teaching instrument; we optimize for clarity, inspectability, and falsifiable experiments.

### 2) Embedded engine first, layers later
- **We build in arcs:**
  1. **Engine (LSM KV)** → 2. **Server mode** → 3. **Raft replication** → 4. **Table layer** (bigger-than-KV)
- **Rationale:** distributed bugs are storage bugs with extra noise. We earn distribution by first making storage boring and reliable.

### 3) Idiomatic Go public API (not RocksDB-compatible)
- **Decision:** BeachDB is **100% idiomatic Go** (contexts, errors, functional options), not a drop-in RocksDB API.
- **Rationale:** BeachDB is a learning/reference implementation. A RocksDB drop-in replacement can be a follow-up project once the core truths are internalized.

### 4) Durability default is strict
- **Decision (v0.1):** `fsync` **on every committed batch** (safe by default).
- **Rationale:** durability must be explicit and measurable. We will document the cost and trade-offs, and later add group-commit as a dedicated milestone.

### 5) Explicit semantics: snapshots + iterators
- **Decision:** reads are **snapshot-based** via monotonic `seqno`.
- **Decision:** iterators are **forward-only** with `Seek/Next` and **fixed snapshot** at creation.
- **Rationale:** LSMs are fundamentally ordered structures. If iteration isn’t correct and stable, nothing above it (server, replication, table scans) will be trustworthy.

### 6) Concurrency is intentionally limited early
- **Decision:** single-writer in early versions.
- **Decision:** no concurrent/background compaction early (may start with explicit/manual compaction, then controlled background compaction later).
- **Rationale:** early concurrency creates heisenbugs that dilute learning. We first lock down invariants, formats, and recovery; then we add concurrency as a focused chapter.

### 7) One story per subsystem (minimal knobs)
- **Decision:** one compaction strategy (knob-free) when compaction arrives.
- **Rationale:** BeachDB should teach the core economics (read/write amplification) without becoming a tuning simulator.

### 8) Inspectability is a feature
- **Decision:** every on-disk format ships with a dump tool:
  - `wal_dump`, `sst_dump`, `manifest_dump`
- **Rationale:** “I don’t trust it until I can dump it.” Debug tooling turns invisible state into evidence.

### 9) Testing is part of the product
- **Decision:**
  - reference-model randomized tests (vs a simple model)
  - crash-loop tests (kill mid-write, reopen, verify invariants)
  - fuzzing for parsers later (WAL/SST/RPC)
- **Rationale:** databases fail in the gaps between the happy path and the real world.

### 10) Table layer comes later (by design)
- **Decision:** v0.1 is an opaque `[]byte`→`[]byte` KV engine.
- **Rationale:** table semantics (cells, versions, deletes, scans) are a layer on top of a correct engine. Adding it later keeps the engine clean and makes the evolution itself educational.

---

## API design: RocksDB vs idiomatic Go

| API | RocksDB | 100% idiomatic Go |
|---|---|---|
| Open | `rocksdb::DB::Open(options, name, &db)` | `BeachDB.Open(path string, opts ...Option) (*DB, error)` |
| Close | `delete db;` / RAII wrappers | `db.Close() error` |
| Put | `db->Put(WriteOptions, key, value)` | `db.Put(ctx context.Context, key, value []byte) error` |
| Get | `db->Get(ReadOptions, key, &value)` | `db.Get(ctx context.Context, key []byte) ([]byte, error)` |
| Delete | `db->Delete(WriteOptions, key)` | `db.Delete(ctx context.Context, key []byte) error` |
| WriteBatch apply | `db->Write(WriteOptions, &batch)` | `db.Write(ctx context.Context, b *Batch) error` |
| Batch builder | `rocksdb::WriteBatch` with `Put/Delete` | `type Batch struct { ... }` with `b.Put/b.Delete`, `db.Write(ctx, b)` |
| Read options | `ReadOptions{snapshot, verify_checksums, fill_cache, ...}` | `db.Get(ctx, key, opts ...ReadOption)` / `db.NewIter(opts ...IterOption)` |
| Write options | `WriteOptions{sync, disableWAL, ...}` | `db.Put(ctx, k, v, opts ...WriteOption)` / default safe; override via options |
| Sync per write (fsync) | `WriteOptions.sync=true` | default: `fsync each batch` (override with `WithSync(false)` later) |
| Snapshots | `db->GetSnapshot()/ReleaseSnapshot()` | `snap := db.Snapshot(); defer snap.Close()` or `snap := db.Snapshot(); db.Get(ctx, key, WithSnapshot(snap))` |
| Iterator create | `db->NewIterator(ReadOptions)` | `it := db.NewIterator(opts ...IterOption)` |
| Iterator seek | `it->Seek(key)` | `it.Seek(key []byte) bool` |
| Iterator advance | `it->Next()` | `it.Next() bool` |
| Iterator validity | `it->Valid()` | `it.Valid() bool` or `bool` returned by `Seek/Next` |
| Iterator key/value access | `it->key()/it->value()` | `it.Key() []byte`, `it.Value() []byte` |
| Iterator lifecycle | `delete it;` | `it.Close() error` |
| Range scan helper | user loops iterator | `db.Scan(ctx, start, end []byte, fn func(k,v []byte) bool, opts ...ScanOption) error` (optional ergonomic layer) |
| Errors | `Status` return values | `error` (typed/sentinel errors e.g. `ErrNotFound`, `ErrCorrupt`) |
| Thread safety contract | documented; internal mutexes | explicit in GoDoc (e.g. `DB` safe for concurrent reads; writes serialized) |
| Context / cancellation | not built-in | `context.Context` on public APIs that may block (I/O, scans, compaction triggers) |
| Metrics | `rocksdb::Statistics` / properties | `db.Stats() Stats` + `expvar`/Prometheus hooks |
| File inspection tools | `sst_dump`, `ldb` | `cmd/BeachDB` + `tools/*_dump` (Go binaries) |
| Internal memtable | skiplist/arena allocators | `internal/memtable` (skiplist or `btree`), simpler alloc story |
| Internal WAL | log writer w/ options | `internal/wal` with explicit `Append(batch)` + `Sync()` and syscall notes |
| Internal versioning | VersionSet/ColumnFamily | `internal/version` (one CF only v0.1), manifest as append-only edits |
| Internal comparator | pluggable comparator | fixed lexicographic `[]byte` comparator (v0.1) |
| Public config style | giant `Options` struct | functional options: `Option`, `ReadOption`, `WriteOption`, `IterOption` |
| Private invariants | implicit, spread | explicit `internal/invariants` checks + test helpers |
