# Manifest / versioning (v0.1)

> The manifest is metadata durability.

## Goals

- Crash-safe tracking of live SSTables
- Replayable edits (append-only)
- Deterministic startup reconstruction

## Model (draft)

- Manifest is a log of version edits:
  - AddFile(level, file_id, smallest_key, largest_key, size_bytes, ...)
  - DeleteFile(level, file_id)

## Startup

- Load the latest manifest state
- Open current file set
- Replay WAL to reach latest state (per scope)

## Tooling

- `tools/manifest_dump`: print edits and reconstructed file set.
