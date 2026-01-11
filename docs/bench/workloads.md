# Bench workloads (v0.1)

The goal is *repeatable* experiments that teach real costs.

## How to run

- Keep workloads stable and versioned.
- Always report:
  - machine info (CPU, RAM, disk type)
  - filesystem + mount options
  - dataset size / key size / value size
  - run duration and warmup rules

> Tip: store raw results under `bench/results/YYYY-MM-DD/`.

## Workload A — Write-heavy (append + overwrite)

Purpose: observe WAL cost, memtable pressure, flush behavior.

- Key distribution: sequential keys + occasional overwrites
- Mix: 95% Put, 5% Get
- Value size: fixed (e.g., 256B) for baseline
- Metrics:
  - ops/sec
  - p50/p99 latency
  - bytes written to WAL
  - fsync time distribution

## Workload B — Read-heavy (cache-friendly)

Purpose: measure point-lookup path and caching behavior.

- Preload dataset
- Mix: 95% Get, 5% Put
- Key distribution: uniform random over existing keys
- Metrics:
  - ops/sec
  - blocks read per Get (where available)
  - cache hit rate (if implemented)

## Workload C — Miss-heavy (the bloom/index story)

Purpose: highlight why avoiding I/O matters.

- Preload dataset of N keys
- Query keys from a larger space so most reads miss
- Mix: 99% Get (miss), 1% Put
- Metrics:
  - blocks touched per miss
  - ops/sec
  - before/after of block index + bloom filter

## Rules

- No “benchmark spam”: prefer a small number of workloads with clear lessons.
- Any optimization must include:
  1) hypothesis
  2) measurement
  3) explanation (what changed in the I/O path?)
