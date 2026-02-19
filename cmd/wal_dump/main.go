// Package main provides the wal_dump CLI tool for inspecting WAL files.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aalhour/beachdb/engine"
	"github.com/aalhour/beachdb/internal/wal"
)

var decodeBatches = flag.Bool("decode", false, "decode and print batch contents")

func main() {
	flag.Parse()

	if flag.NArg() < 1 {
		//nolint:gosec // G705: os.Args[0] is the program name, not user input
		fmt.Fprintf(os.Stderr, "Usage: %s [-decode] <wal-file-path>\n", os.Args[0])
		os.Exit(1)
	}

	walPath := flag.Arg(0)

	if err := dumpWAL(walPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func dumpWAL(path string) error {
	fmt.Printf("Reading WAL: %s\n\n", path)

	// Open the WAL for reading
	reader, err := wal.NewReader(path)
	if err != nil {
		return fmt.Errorf("failed to open WAL: %w", err)
	}
	defer reader.Close()

	var (
		recordCount int
		totalBytes  int64
		incomplete  int
	)

	// Read all records
	for {
		payload, err := reader.Next()

		if errors.Is(err, io.EOF) {
			// Clean end of WAL
			break
		}

		if errors.Is(err, wal.ErrTruncated) {
			// Incomplete record (crash mid-write)
			fmt.Printf("Record %d: truncated (incomplete write)\n", recordCount)
			incomplete++
			break
		}

		if errors.Is(err, wal.ErrChecksum) {
			// Corrupted record
			fmt.Printf("Record %d: CORRUPTED (checksum mismatch)\n", recordCount)
			incomplete++
			break
		}

		if err != nil {
			// Other I/O error
			return fmt.Errorf("failed to read record %d: %w", recordCount, err)
		}

		// Valid record
		fmt.Printf("Record %d: %d bytes", recordCount, len(payload))

		// Optionally decode and print batch contents
		if *decodeBatches {
			batch, err := engine.DecodeBatch(payload)
			if err != nil {
				fmt.Printf(" [failed to decode: %v]", err)
			} else {
				fmt.Printf(" (batch: %d operations)", batch.Count())
			}
		}

		fmt.Println()
		recordCount++
		totalBytes += int64(len(payload))
	}

	// Summary
	fmt.Println()
	if incomplete > 0 {
		fmt.Printf("End of WAL (%d complete records, %d incomplete)\n",
			recordCount, incomplete)
	} else {
		fmt.Printf("End of WAL (%d records, %d bytes total)\n",
			recordCount, totalBytes)
	}

	return nil
}
