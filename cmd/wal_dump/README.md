# wal_dump

Inspects BeachDB WAL files and prints record information, detecting truncation and corruption.

## Usage

```bash
# Basic usage
./bin/wal_dump /path/to/beachdb.wal

# Show batch operation counts
./bin/wal_dump -decode /path/to/beachdb.wal
```

Example output:
```
Reading WAL: /tmp/mydb/beachdb.wal

Record 0: 26 bytes
Record 1: 27 bytes
Record 2: truncated (incomplete write)

End of WAL (2 complete records, 1 incomplete)
```
