// Package main provides a controller/worker crash harness for exercising
// BeachDB durability under process crashes and injected engine fault points.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
)

// main dispatches crash harness subcommands and reports user-facing errors.
func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCommand(os.Args[2:])
	case "replay":
		err = replayCommand(os.Args[2:])
	case "worker":
		err = workerCommand(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown subcommand %q\n\n", os.Args[1])
		printUsage(os.Stderr)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// printUsage renders the crash harness subcommand help.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  crash run [flags]")
	fmt.Fprintln(w, "  crash replay --artifact=FILE [--dbdir=DIR]")
	fmt.Fprintln(w, "  crash worker --dbdir=DIR")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  run     Execute a controller-driven crash harness run")
	fmt.Fprintln(w, "  replay  Replay a recorded artifact deterministically")
	fmt.Fprintln(w, "  worker  Internal worker subprocess used by run/replay")
}

// newFlagSet constructs a crash subcommand flag set with consistent behavior.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
