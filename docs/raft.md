# Raft replication (TODO)

Season 1 milestone: a single Raft group where log entries are serialized `WriteBatch`.

This doc will define:

- state machine boundary (apply)
- determinism rules
- snapshotting rules
- leader-only write path
