.PHONY: all build test coverage lint fmt-check fmt clean check examples crash-check bench fuzz help

CYCLES ?= 100

# Crash harness profile: `full` (uses CYCLES) or `ci` (fast deterministic preset).
PROFILE ?= full

# Bench knobs. Override on the CLI: `make bench PKG=./internal/wal BENCH=BenchmarkX BENCHTIME=3s`
PKG ?= ./...
BENCH ?= .
BENCHTIME ?= 1s

# Fuzz knobs. By default `make fuzz` discovers every Fuzz* function across the
# tree and runs each for FUZZTIME. -fuzz only accepts one target at a time
# (and one package), so the target loops rather than passes `./...`.
FUZZPKG ?= ./...
FUZZ ?= ^Fuzz
FUZZTIME ?= 30s

# Default target
all: build

## build: Build all commands from cmd/ into bin/
build:
	@mkdir -p bin
	@for dir in cmd/*/; do \
		name=$$(basename $$dir); \
		echo "Building $$name..."; \
		go build -o bin/$$name ./$$dir; \
	done

## test: Run all tests
test:
	go test ./...

## examples: Run all example programs
examples:
	@echo "Running examples..."
	@for file in $$(find examples -name "*.go" -type f | sort); do \
		header="=== Running $$file ==="; \
		sep=$$(printf '%*s' $${#header} '' | tr ' ' '='); \
		echo "\n$$sep"; \
		echo "$$header"; \
		echo "$$sep"; \
		go run $$file || exit 1; \
	done
	@echo "\n✓ All examples completed successfully"

## coverage: Run tests with coverage report
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## fmt-check: Checks code formatting using golangci-lint
fmt-check:
	golangci-lint fmt --diff

## fmt: Format code using golangci-lint formatters
fmt:
	golangci-lint fmt ./...

## check: Runs fmt-check, lint and test
check: fmt-check lint test

## bench: Run benchmarks project-wide. Override with `make bench PKG=./internal/wal BENCH=BenchmarkX BENCHTIME=3s`
bench:
	go test -run=^$$ -bench=$(BENCH) -benchmem -benchtime=$(BENCHTIME) $(PKG)

## fuzz: Run every fuzz target FUZZTIME each across FUZZPKG (default ./..., 30s each). `make fuzz FUZZTIME=1m`
fuzz:
	@set -eu; \
	for pkg in $$(go list $(FUZZPKG)); do \
		targets=$$(go test -list '$(FUZZ)' $$pkg 2>/dev/null | grep -E '^Fuzz' || true); \
		for t in $$targets; do \
			echo "==> $$pkg :: $$t ($(FUZZTIME))"; \
			go test -run=^$$ -fuzz="^$$t$$" -fuzztime=$(FUZZTIME) $$pkg; \
		done; \
	done

## crash-check: Run the controller/worker crash harness with a temporary workspace. `make crash-check PROFILE=ci`
crash-check:
	@set -eu; \
	tmpdir=$$(mktemp -d /tmp/beachdb-crash.XXXXXX); \
	dbdir="$$tmpdir/db"; \
	artdir="$$tmpdir/artifacts"; \
	echo "Running crash harness (profile=$(PROFILE), cycles=$(CYCLES)) in $$dbdir"; \
	echo ""; \
	go run ./cmd/crash run \
		--profile=$(PROFILE) \
		--dbdir="$$dbdir" \
		--artifact-dir="$$artdir" \
		--cycles=$(CYCLES) \
		--seed=777 \
		--ops=64 \
		--min-delay-ms=10 \
		--max-delay-ms=30 \
		--verify-every-cycle=true; \
	echo ""; \
	echo "Crash run data kept in "$$tmpdir". Delete it when done: 'rm -rf $$tmpdir'";

## clean: Remove build artifacts and test output
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html
	rm -f *.test *.out *.prof
	rm -rf cpu.prof mem.prof

## help: Show this help message
help:
	@echo "Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /' | column -t -s ':'
