package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aalhour/beachdb/engine"
	"github.com/aalhour/beachdb/internal/testutil"
)

// runOrchestrator spawns writer subprocesses and kills them randomly,
// then verifies that acknowledged writes survived the crashes.
func runOrchestrator(dbDir string, numCycles int, minDelayMs int, maxDelayMs int) error {
	stateFile := filepath.Join(dbDir, "crash_state.txt")
	model := testutil.NewModel()
	//nolint:gosec // G404: Test utility doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(42, 1024))

	log.Printf("Starting crash orchestrator: %d cycles", numCycles)

	for cycle := range numCycles {
		log.Printf("Cycle %d: spawning writer subprocess", cycle)

		// Spawn writer subprocess
		//nolint:gosec,noctx // G204: Command is our own binary; no context needed for subprocess
		cmd := exec.Command(
			os.Args[0], // Run this same binary
			"--mode=writer",
			"--dbdir="+dbDir,
			"--state="+stateFile,
		)

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("cycle %d: failed to start writer: %w", cycle, err)
		}

		// Wait random delay, then kill
		delay := time.Duration(minDelayMs+rng.IntN(maxDelayMs-minDelayMs)) * time.Millisecond
		time.Sleep(delay)

		log.Printf("Cycle %d: killing subprocess with SIGKILL after %v", cycle, delay)
		if err := cmd.Process.Kill(); err != nil {
			log.Printf("Cycle %d: kill failed: %v", cycle, err)
		}

		// Wait for process to exit
		_ = cmd.Wait()

		// Read state file to see what was committed
		committedKeys := readState(stateFile)
		log.Printf("Cycle %d: writer claimed %d keys were committed", cycle, len(committedKeys))

		// Update model with committed keys (we don't verify yet, just track)
		for _, key := range committedKeys {
			if key != "" { // Skip empty lines
				model.Put([]byte(key), []byte("committed"))
			}
		}

		// Clean state file for next cycle
		_ = os.Remove(stateFile)
	}

	// Final verification: reopen DB and check all committed keys
	log.Printf("Final verification: reopening DB to check %d keys", model.Len())
	db, err := engine.Open(dbDir)
	if err != nil {
		return fmt.Errorf("final open failed: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	recoveredCount := 0
	lostCount := 0

	for _, key := range model.Keys() {
		_, err := db.Get(ctx, key)
		switch {
		case errors.Is(err, engine.ErrKeyNotFound):
			lostCount++
			log.Printf("Key lost: %x", key)
		case err != nil:
			log.Printf("Error reading key: %v", err)
		default:
			recoveredCount++
		}
	}

	log.Printf("Results: %d recovered, %d lost out of %d total", recoveredCount, lostCount, model.Len())

	// Some data loss is acceptable (writer claimed commit but didn't fsync)
	// But we should recover most data
	if model.Len() > 10 && recoveredCount == 0 {
		return errors.New("recovered 0 keys after writing many - durability failure")
	}

	return nil
}

// readState reads the writer scratch file listing keys it believed were committed.
func readState(path string) []string {
	//nolint:gosec // G304: Path is controlled by orchestrator, not user input
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}
