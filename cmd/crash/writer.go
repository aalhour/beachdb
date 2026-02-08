package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/aalhour/beachdb/engine"
	"github.com/aalhour/beachdb/internal/testutil"
)

// runWriter writes random data to the DB until killed.
// It tracks committed keys in a state file for the orchestrator to verify.
func runWriter(dbDir string, stateFile string) error {
	// Open the database
	db, err := engine.Open(dbDir)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	defer db.Close()

	// Initialize RNG with process-specific seed
	//nolint:gosec // G404: Test utility doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(os.Getpid())))
	ctx := context.Background()

	// Track what we've successfully written
	var committed []string

	// Write forever (until killed)
	for {
		key := testutil.RandKey(rng, 32)
		value := testutil.RandValue(rng, 128)

		if err := db.Put(ctx, key, value); err != nil {
			log.Printf("Write failed: %v", err)
			continue
		}

		// Record successful write
		committed = append(committed, string(key))

		// Update state file atomically (what we've committed)
		if err := writeState(stateFile, committed); err != nil {
			log.Printf("Failed to update state: %v", err)
		}

		// Small delay to increase likelihood of being killed mid-operation
		time.Sleep(time.Millisecond)
	}
}

func writeState(path string, keys []string) error {
	// Atomic write: tmp file + rename
	tmpPath := path + ".tmp"
	data := []byte(strings.Join(keys, "\n"))
	//nolint:gosec // G306: state file permissions are acceptable
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
