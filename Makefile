.PHONY: all build test clean help

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

## clean: Remove build artifacts
clean:
	rm -rf bin/

## help: Show this help message
help:
	@echo "Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /' | column -t -s ':'
