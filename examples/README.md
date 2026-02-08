# BeachDB Examples

Runnable examples demonstrating BeachDB usage patterns.

## Running Examples

```bash
# Run an example
go run examples/engine/basic_usage/main.go

# Or build and run
go build -o bin/example examples/engine/basic_usage
./bin/example
```

## Examples

### Engine

- **basic_usage/** — Simple Put/Get/Delete operations
- **batch_operations/** — Atomic batch writes
- **crash_recovery/** — Durability and WAL recovery
- **options/** — Configuration with functional options

Each example is self-contained and includes cleanup code.
