.PHONY: all build test coverage lint fmt-check fmt clean check examples crash-check help

CYCLES ?= 100

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
		echo "\n=== Running $$file ==="; \
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

## crash-check: Run the controller/worker crash harness ($(CYCLES) cycles) with a temporary workspace
crash-check:
	@set -eu; \
	tmpdir=$$(mktemp -d /tmp/beachdb-crash.XXXXXX); \
	dbdir="$$tmpdir/db"; \
	artdir="$$tmpdir/artifacts"; \
	echo "Running crash harness ($(CYCLES) cycles) in $$dbdir"; \
	echo ""; \
	go run ./cmd/crash run \
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
