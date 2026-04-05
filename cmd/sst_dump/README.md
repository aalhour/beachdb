# sst_dump

CLI tool for inspecting BeachDB SSTable files without running the database.

## Build

```sh
make build
# or
go build -o bin/sst_dump ./cmd/sst_dump
```

## Usage

```
sst_dump [-entries] <path>
```

### Flags

| Flag       | Description                     |
|------------|---------------------------------|
| `-entries` | Print all key-value entries     |

## Examples

### Summary (default)

```sh
$ sst_dump /tmp/mydb/000001.sst
```

```
SSTable: /tmp/mydb/000001.sst
  Version: 1
  Entries: 1523
  Data blocks: 12
  Index block: offset=98304 size=812

Blocks:
  Block 0: offset=0 size=8192 last_key="user:00123" seqno=17
  Block 1: offset=8192 size=8192 last_key="user:00256" seqno=11
  ...
```

### Entry listing

```sh
$ sst_dump -entries /tmp/mydb/000001.sst
```

Prints the summary above, followed by:

```
Entries:
  [0] Put    key="user:00001" seqno=9 value=128 bytes
  [1] Delete key="user:00001" seqno=7 value=0 bytes
  [2] Put    key="user:00002" seqno=6 value=64 bytes
  ...
```

### Error cases

Missing file:

```sh
$ sst_dump /tmp/nonexistent.sst
error: couldn't open sstable at "/tmp/nonexistent.sst": open /tmp/nonexistent.sst: no such file or directory
```

Corrupt file:

```sh
$ sst_dump /tmp/corrupt.sst
error: beachdb/sstable: corrupt footer checksum
```
