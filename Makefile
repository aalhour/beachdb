.PHONY: all build test coverage lint fmt clean examples help

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

## fmt: Format code using golangci-lint formatters
fmt:
	golangci-lint fmt ./...

## check: Runs fmt, lint and test
check: fmt lint test

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
