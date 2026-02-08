# BeachDB Examples

Runnable examples demonstrating BeachDB usage patterns.

## Running Examples

```bash
# Run an example
go run examples/engine/basic_usage.go

# Or build and run
go build -o bin/example examples/engine/basic_usage.go
./bin/example
```

## Examples

### Engine

- **basic_usage.go** — Simple Put/Get/Delete operations
- **batch_operations.go** — Atomic batch writes
- **crash_recovery.go** — Durability and WAL recovery
- **options.go** — Configuration with functional options

Each example is self-contained and includes cleanup code.
