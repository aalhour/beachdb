# crash

`cmd/crash` is BeachDB's controller/worker crash harness.

It generates a deterministic workload, feeds one operation at a time to a
worker subprocess, kills that subprocess at controlled points, reopens the
database after every cycle, and verifies that recovered state is consistent
with the set of acknowledged operations.

## Subcommands

### `crash run`

Runs a new crash harness session.

```bash
./bin/crash run \
  --dbdir=/tmp/beachdb-crash-db \
  --artifact-dir=/tmp/beachdb-crash-artifacts \
  --cycles=100 \
  --seed=777 \
  --ops=500
```

Important flags:

- `--dbdir`: required, must be missing or empty
- `--artifact-dir`: required, stores the run artifact JSON
- `--cycles`: number of worker crash/reopen cycles
- `--seed`: deterministic workload and kill-schedule seed
- `--ops`: number of logical operations in the generated workload
- `--profile=ci|full`: deterministic preset profiles
- `--crash-point`: internal crash point to arm for phase-2 testing
- `--fault-point`: internal fault point to arm for phase-2 testing

### `crash replay`

Replays a recorded artifact with the same workload and deterministic schedule.

```bash
./bin/crash replay \
  --artifact=/tmp/beachdb-crash-artifacts/crash-20260419T213015.123Z.json \
  --dbdir=/tmp/beachdb-crash-replay-db
```

### `crash worker`

Internal subprocess used by `run` and `replay`. It is not intended to be used
manually.

## Artifact model

Each run writes a JSON artifact containing:

- the run configuration
- the deterministic seed
- the full generated workload
- the ordered worker event stream
- per-cycle worker metadata and verification results
- the last acknowledged op ID
- the first verification failure, if any

Keys and values are base64-encoded so binary payloads survive round-trip
without newline or text parsing issues.

## Durability contract

The worker emits:

- `ready` after opening the DB
- `start` immediately before executing an operation
- `ack` only after the DB call succeeds
- `fail` if the DB call returns an error

The controller treats:

- `ack`ed operations as required after recovery
- the single `start`ed-but-not-`ack`ed operation as indeterminate
- never-started operations as irrelevant to that cycle

Per-cycle verification therefore checks that recovered state matches either:

1. all acknowledged operations only, or
2. all acknowledged operations plus the one in-flight idempotent operation

## Internal crash and fault points

Phase-2 hook points are internal-only and activated by environment variables
that the controller passes to the worker:

- crash points:
  - `wal_after_append`
  - `wal_after_sync`
  - `flush_after_file_sync`
  - `flush_after_publish`
- fault points:
  - `wal_sync_error`
  - `sst_write_error`
  - `sst_publish_error`
