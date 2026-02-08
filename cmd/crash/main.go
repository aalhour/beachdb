// Package main provides the crash utility for testing database durability
// under SIGKILL conditions. It spawns writer subprocesses and kills them
// randomly to verify that acknowledged writes survive crashes.
package main

import (
	"flag"
	"fmt"
	"os"
)

var (
	mode       = flag.String("mode", "orchestrator", "Mode: orchestrator or writer")
	dbDir      = flag.String("dbdir", "", "Database directory")
	stateFile  = flag.String("state", "", "State file path (writer mode)")
	numCycles  = flag.Int("cycles", 50, "Number of crash cycles (orchestrator mode)")
	minDelayMs = flag.Int("min-delay", 10, "Minimum delay before kill (ms)")
	maxDelayMs = flag.Int("max-delay", 100, "Maximum delay before kill (ms)")
)

func main() {
	flag.Parse()

	if *dbDir == "" {
		fmt.Fprintf(os.Stderr, "Error: --dbdir required\n")
		flag.Usage()
		os.Exit(1)
	}

	var err error
	switch *mode {
	case "writer":
		if *stateFile == "" {
			fmt.Fprintf(os.Stderr, "Error: --state required in writer mode\n")
			os.Exit(1)
		}
		err = runWriter(*dbDir, *stateFile)
	case "orchestrator":
		err = runOrchestrator(*dbDir, *numCycles, *minDelayMs, *maxDelayMs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mode %q\n", *mode)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
