# Memtable Semantics

The memtable is where writes land before they hit disk. It's a skip list — a
probabilistic sorted structure that gives us O(log n) inserts and lookups
without the rebalancing drama of a tree. Here's what you need to know about
how it behaves.

---

## Key ordering: the one invariant that matters

Keys are sorted by `(user_key ASC, seqno DESC)`.

That "seqno descending" bit is load-bearing. For the same user key, the entry
with the highest sequence number appears first. This means when you search or
iterate, you hit the newest version before any older ones. "Latest version wins"
isn't a policy we enforce — it falls out of the ordering.

Example:

```
Put("foo", "v1") at seqno 5
Put("foo", "v2") at seqno 10
Delete("foo") at seqno 15
```

The skip list stores these as:

```
("foo", 15, Delete) -> nil
("foo", 10, Put)    -> "v2"
("foo", 5,  Put)    -> "v1"
```

A `Get("foo")` at seqno 20 finds the tombstone first → "not found."
A `Get("foo")` at seqno 12 skips the tombstone (15 > 12) → returns "v2."

---

## Writes: always insert, never update

There is no "update" operation. A `Put` with the same user key creates a **new
entry** with a higher sequence number. The old entry still exists in the skip
list. We don't overwrite in place because MVCC needs those old versions for
snapshot reads.

This is different from a hash map mental model. The memtable is an append-only
log in sorted form. Old versions stick around until compaction evicts them.

---

## Deletes: tombstones are real

`Delete(key)` doesn't remove anything. It writes a **tombstone**: an entry with
`Kind=Delete` instead of `Kind=Put`. Tombstones have sequence numbers. They
participate in ordering. They're first-class entries.

When `Get` finds a tombstone as the newest version, it returns "not found."
The key is logically deleted, but the tombstone physically exists until
compaction merges it away.

Why not just remove the entry? Because:

1. **Snapshot reads**: A read at seqno 12 shouldn't see a delete that happened at seqno 15. The tombstone's seqno lets us make that decision.
2. **Flush correctness**: When we flush to an SSTable, we need to write the tombstone so older SSTables know the key is dead.
3. **Compaction**: The tombstone propagates down levels until it's safe to drop.

Tombstones are the price of supporting snapshots and crash recovery. You don't
get to pretend deletes are free.

---

## Snapshot reads

A snapshot is just a sequence number captured at a point in time.

"Read at snapshot S" means: ignore any entry with `seqno > S`. This is how
concurrent reads stay stable even as new writes land. The memtable doesn't
enforce snapshots — it stores everything — but `Get` filters by seqno, and
the merge iterator (higher layer) does the same.

Snapshot reads are stable:

- They don't see writes that happen after the snapshot was taken.
- They don't "time travel" forward during iteration.
- They're not affected by flush or compaction (those operations preserve seqno ordering).

---

## Iterator visibility: everything, unfiltered

The memtable iterator sees **all entries**: puts, tombstones, every version of
every key. It does not filter, deduplicate, or hide anything.

This is intentional. The iterator's job is to produce a sorted stream of
internal keys. Filtering (skip old versions, hide tombstones, respect snapshot
boundaries) happens at the merge iterator layer during reads.

If you iterate a memtable directly, you'll see the raw internals:

```
("foo", 15, Delete) -> nil
("foo", 10, Put)    -> "v2"
("foo", 5,  Put)    -> "v1"
("bar", 8,  Put)    -> "baz"
```

This is the truth. Higher layers decide what to expose to the user.

---

## Concurrency: one lock to rule them all (for now)

The skip list uses a single `sync.RWMutex`:

- `Put` takes an exclusive lock
- `Get` takes a shared lock
- Iterators hold a shared lock from `SeekToFirst`/`Seek` until `Close`

This means **iterators block writers**. It's a deliberate v1 simplicity choice.
The frozen-memtable pattern routes new writes to a fresh memtable while the old
one drains to an SSTable, so iterator lock contention becomes a non-issue in
practice.

**Callers MUST call `Close()` on iterators.** Forgetting this deadlocks writers.
The compiler won't save you.
