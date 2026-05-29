# manifest_dump

CLI tool for inspecting BeachDB's MANIFEST files without running the database.
It reads `CURRENT`, replays the manifest it points at, prints every
`VersionEdit` in order, and then prints the reconstructed `Version`.

## Build

```sh
make build
# or
go build -o bin/manifest_dump ./cmd/manifest_dump
```

## Usage

```
manifest_dump <db-dir>
```

The argument is the database directory (the one containing `CURRENT` and the
`MANIFEST-NNNNNN` files), not a manifest file path.

## Examples

### Normal database

```sh
$ manifest_dump /tmp/mydb
```

```
Manifest: MANIFEST-000001
Path:     /tmp/mydb/MANIFEST-000001

Edit #0:
  next_file_id:  1
  last_sequence: 0
  log_number:    0
Edit #1:
  next_file_id:  2
  last_sequence: 3
  add_file:    level=0 id=1 size=170 smallest="apple/2/Put" largest="cherry/3/Put"
Edit #2:
  next_file_id:  3
  last_sequence: 5
  add_file:    level=0 id=2 size=126 smallest="apple/5/Delete" largest="kiwi/4/Put"
Current Version:
  Level 0: 2 files (296 bytes total)
    [1] apple..cherry (170 bytes)
    [2] apple..kiwi (126 bytes)
```

Each edit prints only the fields it sets. `add_file` keys are shown as
`<userkey>/<seqno>/<kind>` (the same convention as `sst_dump`), where kind is
`Put` or `Delete`.

### Fresh database

A directory with no `CURRENT` file is a fresh database — there is nothing to
dump, and the tool exits 0:

```sh
$ manifest_dump /tmp/empty-dir
No CURRENT file in /tmp/empty-dir — fresh database, nothing to dump.
```

### Truncated manifest

A partial trailing record (a crash mid-append) is recoverable. The tool notes
it, prints the `Version` reconstructed from the complete edits, and exits 0:

```
Edit #2: incomplete trailing record (crash mid-append, recoverable)

Current Version:
  Level 0: 1 files (170 bytes total)
    [1] apple..cherry (170 bytes)
```

### Corrupt manifest

A checksum mismatch or undecodable edit is fatal. The tool prints the `Version`
reconstructed up to the failure, reports the last valid edit, and exits
non-zero:

```sh
$ manifest_dump /tmp/mydb
...
Current Version:
  Level 0: 1 files (170 bytes total)
    [1] apple..cherry (170 bytes)

Last valid edit: #1
Error: corrupt manifest at edit #2: beachdb/record: checksum mismatch
```

## Exit codes

| Code | Meaning                                                        |
|------|----------------------------------------------------------------|
| `0`  | Manifest dumped (or fresh database, or recoverable truncation) |
| `1`  | Bad arguments, unreadable directory, or a corrupt manifest     |
