# Testing Strategy (in progress)

This document describes how BeachDB is tested, what tools we use, and where
we intend to go. It is a living document — sections marked **(TODO)** are
planned but not yet implemented.

---

## Goals

1. Every public behaviour has a test that breaks if the behaviour changes.
2. Every on-disk format has a round-trip test that catches encoding regressions.
3. Hot-path allocations are guarded so regressions fail the build.
4. Performance is measurable and comparable across commits.

---

## Unit tests

Standard `go test` with table-driven cases. Every package has a `*_test.go`
file in the same package so it can exercise unexported helpers.

Naming convention: `TestType_Behaviour_Scenario`.

```
TestDB_PutGet
TestDB_ContextCancellation
TestWriter_RejectsOutOfOrderKeys
TestFooter_DecodeRejectsBadMagic
TestSkipList_Get_Hit
```

Edge-case tests verify error paths, nil inputs, double-close, and
concurrent access. See `engine/db_test.go` and `internal/wal/writer_test.go`
for representative examples.

---

## Crash and recovery tests

`engine/crash_test.go` simulates mid-write crashes by:

1. Writing data, closing the DB normally, then truncating or corrupting the
   WAL file before reopening.
2. Verifying that committed data survives, incomplete writes are skipped, and
   corrupted WALs fail fast.
3. Running randomised write-crash-reopen cycles to exercise recovery under
   varied conditions.

These tests rely on `t.TempDir()` for isolation — each test gets its own
database directory.

---

## Reference-model testing

`internal/testutil` provides two helpers used across packages:

- **`Model`** — an in-memory map that mirrors the database's read semantics
  (seqno-aware gets, tombstone handling). Tests perform the same operations
  on the real implementation and the model, then compare results.
  See `internal/memtable/skiplist_random_test.go` for the randomised variant.

- **`RandKey` / `RandValue`** — seeded random generators that produce
  reproducible test data. All callers use `rand.NewPCG(seed, stream)` so
  failures are deterministic.

---

## Fuzz tests

Go-native fuzz targets (`func Fuzz*(f *testing.F)`) exercise decoders with
arbitrary input to catch panics and logic errors in parsing paths:

```
FuzzDecodeBatch       — engine/batch_test.go
FuzzEncodeRecord      — internal/wal/record_test.go
FuzzDecodeRecordHeader — internal/wal/record_test.go
```

Run with `go test -fuzz=FuzzDecodeBatch ./engine/...`.

---

## Benchmark tests

Benchmarks measure throughput (ns/op), allocations (B/op, allocs/op), and —
where relevant — data throughput (MB/s).

### Techniques in use

| Technique | What it measures | Example |
|---|---|---|
| `b.ReportAllocs()` | Heap allocs per op | Every benchmark |
| `b.SetBytes(n)` | Throughput in MB/s | `BenchmarkCRC32C` |
| `b.Run(name, fn)` | Same op at different sizes | `BenchmarkWriter_Add/100-entries` |
| `b.StopTimer/StartTimer` | Excludes setup from measurement | `BenchmarkWriter_Add` |
| `testing.AllocsPerRun` | Allocation regression guard (fails the build) | `TestBlockBuilder_Add_AllocsPerRun` |

### Benchmark coverage

| Package | Benchmarks |
|---|---|
| `engine` | `DB.Put`, `DB.Get`, `DB.Write` (batch), `Batch.Encode/Decode`, `syncDir`, `mkdirAllAndSync` |
| `internal/sstable` | `blockBuilder.Add/Finish`, `Writer.Add`, `footer.encode/decodeFooter` |
| `internal/memtable` | `SkipList.Put/Get`, `Iterator.FullScan/Seek/Next` |
| `internal/wal` | `Writer.Append`, `Reader.Next`, `EncodeRecord`, `DecodeRecordHeader` |
| `internal/keys` | `Encode`, `Decode`, `Compare` |
| `internal/util/checksum` | `CRC32C` (5 payload sizes, reports MB/s) |
| `internal/util/coding` | `PutUint32/64`, `Uint32/64`, `ByteReader.ReadUint32/64` |
| `internal/testutil` | `RandKey`, `RandValue`, `fillRandomBytes` |

### Running benchmarks

```bash
# All benchmarks
go test ./... -bench=. -benchmem

# Single package
go test ./internal/sstable/... -bench=. -benchmem

# Filter by name
go test ./internal/memtable/... -bench=BenchmarkSkipList -benchmem

# Compare across commits (requires benchstat)
go test ./... -bench=. -benchmem -count=10 > old.txt
# make changes
go test ./... -bench=. -benchmem -count=10 > new.txt
benchstat old.txt new.txt
```

### Allocation regression guards

Two `testing.AllocsPerRun` tests run as unit tests (not benchmarks) and
**fail the build** if allocations regress:

```go
// blockBuilder.Add must not allocate beyond key.Encode()
func TestBlockBuilder_Add_AllocsPerRun(t *testing.T) {
    allocs := testing.AllocsPerRun(100, func() {
        bb.Add(key, val)
    })
    if allocs > 1 {
        t.Errorf("expected ≤1 alloc, got %v", allocs)
    }
}

// blockBuilder.Reset reuses the backing array — zero allocs
func TestBlockBuilder_Reset_NoAllocs(t *testing.T) {
    allocs := testing.AllocsPerRun(100, func() {
        bb.Reset()
    })
    if allocs != 0 {
        t.Errorf("expected 0 allocs, got %v", allocs)
    }
}
```

### Techniques not yet used

| Technique | What it measures | Status |
|---|---|---|
| `b.Loop()` | Benchmark loop (Go 1.24+) — prevents compiler from hoisting invariants | Available, not adopted yet |
| `b.ReportMetric()` | Custom per-op metrics (e.g. entries/s) | Planned |
| `b.SetBytes()` on I/O benchmarks | MB/s for WAL and SSTable writes/reads | Planned |
| `-gcflags='-m=2'` | Escape analysis — confirms stack vs heap placement | On-demand |
| `-cpuprofile` / `-memprofile` | Line-level profiling via `go tool pprof` | On-demand |

---

## Fault injection **(TODO)**

Planned approach: wrap `os.File` with an interface that can inject errors at
controlled points — `EIO` on write, short reads, `ENOSPC` on sync.

Targets:

- WAL writer: error mid-record, error on sync, error on close.
- SSTable writer: error during block flush, error writing footer.
- SSTable reader: corrupt block checksum, truncated index, missing footer.
- DB open/recovery: WAL present but unreadable, partial WAL replay.

Implementation will live in `internal/testutil/faultfs.go` or similar.

---

## Stress testing **(TODO)**

Planned workloads:

- **Write flood** — sustained random writes at max throughput, verify no
  data loss after clean shutdown.
- **Concurrent read/write** — multiple goroutines writing while readers
  iterate, verify snapshot consistency.
- **Large dataset** — millions of keys to exercise block splitting, multi-level
  index, and memory pressure.
- **Crash loop** — repeated open/write/kill/recover cycles (extension of
  existing crash tests to longer durations and randomised timing).

These will use the reference model (`testutil.Model`) to verify correctness
under load.

---

## Jepsen-style correctness testing **(TODO)**

Planned scope: verify linearisability of read/write operations under
concurrent access and crash recovery.

Approach:

1. Record a history of operations (put, get, delete) with wall-clock
   timestamps.
2. Crash the database at random points during the history.
3. Recover and verify that the observable state is consistent with some
   linearisation of the recorded history.
4. Check that acknowledged writes are durable and that no phantom reads
   occur.

This is a Season 2+ goal and depends on the server protocol being in place.

---

## Test infrastructure

| Tool | Purpose |
|---|---|
| `go test` | Unit tests, benchmarks, fuzz |
| `golangci-lint` | Static analysis (40+ linters) |
| `make check` | Lint + test in one command |
| `t.TempDir()` | Per-test directory isolation |
| `testutil.Model` | Reference-model verification |
| `testutil.RandKey/RandValue` | Deterministic random test data |
| `benchstat` | Statistical benchmark comparison |

---

## Related docs

- [Architecture](architecture.md) — component overview
- [Scope contract](scope.md) — what is and isn't tested
- [Bench workloads](bench/workloads.md) — benchmark scenarios
