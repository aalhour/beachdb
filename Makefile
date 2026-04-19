.PHONY: all build test coverage lint fmt-check fmt clean check examples crash-test help

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

## fmt: Checks code formatting using golangci-lint
fmt-check:
	golangci-lint fmt --diff

## fmt: Format code using golangci-lint formatters
fmt:
	golangci-lint fmt ./...

## check: Runs fmt-check, lint and test
check: fmt-check lint test

## crash-test: Run crash harness in writer+orchestrator modes ($(CYCLES) cycles) using /tmp workspace
crash-test:
	@set -eu; \
	tmpdir=$$(mktemp -d /tmp/beachdb-crash.XXXXXX); \
	trap 'rm -rf "$$tmpdir"' EXIT INT TERM; \
	writer_db="$$tmpdir/writer-db"; \
	orchestrator_db="$$tmpdir/orchestrator-db"; \
	mkdir -p "$$writer_db" "$$orchestrator_db"; \
	echo "Running writer mode crash loop ($(CYCLES) cycles) in $$writer_db"; \
	for cycle in $$(seq 1 $(CYCLES)); do \
		state_file="$$tmpdir/writer-state-$$cycle.txt"; \
		go run ./cmd/crash --mode=writer --dbdir="$$writer_db" --state="$$state_file" >/dev/null 2>&1 & \
		pid=$$!; \
		sleep 0.05; \
		kill -9 $$pid >/dev/null 2>&1 || true; \
		wait $$pid >/dev/null 2>&1 || true; \
	done; \
	echo "Running orchestrator mode crash loop ($(CYCLES) cycles) in $$orchestrator_db"; \
	go run ./cmd/crash --mode=orchestrator --dbdir="$$orchestrator_db" --cycles=$(CYCLES)

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
