package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aalhour/beachdb/engine"
)

func main() {
	dir := "/tmp/beachdb-example-manifest"
	defer os.RemoveAll(dir)

	ctx := context.Background()

	// === Session 1: write data and flush it into SSTables ===
	//
	// Auto-flush is left disabled (the default), so each explicit Flush turns
	// the active memtable into exactly one SSTable. Behind the scenes the
	// engine records every new SSTable in the MANIFEST — an append-only log of
	// "which files exist, at which level, covering which key range". Three
	// flushes produce three SSTables and three manifest entries.
	fmt.Println("=== Session 1: writing and flushing three SSTables ===")
	db, err := engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}

	batches := []map[string]string{
		{"apple": "red", "apricot": "orange"},
		{"banana": "yellow", "blueberry": "blue"},
		{"cherry": "red", "cranberry": "crimson"},
	}

	for i, batch := range batches {
		for _, k := range sortedKeys(batch) {
			if err := db.Put(ctx, []byte(k), []byte(batch[k])); err != nil {
				log.Fatal(err)
			}
		}
		if err := db.Flush(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  Flushed batch %d (%d keys) -> new SSTable, tracked by the manifest\n", i+1, len(batch))
	}

	if err := db.Close(); err != nil {
		log.Fatal(err)
	}

	// === On-disk layout ===
	//
	// CURRENT is a one-line pointer to the live MANIFEST file. The MANIFEST
	// records the SSTable inventory; the .sst files hold the data. Inspect the
	// manifest's individual edits with the manifest_dump tool:
	//
	//	go run ./cmd/manifest_dump /tmp/beachdb-example-manifest
	fmt.Println("\n=== On-disk layout ===")
	printDirLayout(dir)

	// === Session 2: reopen — state is rebuilt from the manifest ===
	//
	// All data was flushed before Close, so the WAL has nothing left to replay.
	// On Open the engine reads CURRENT, replays the manifest to learn which
	// SSTables exist, and opens exactly those files. Every key below is served
	// from an SSTable the manifest pointed at — no directory scan, no WAL.
	fmt.Println("\n=== Session 2: reopening (state rebuilt from the manifest) ===")
	db, err = engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	for _, batch := range batches {
		for _, k := range sortedKeys(batch) {
			val, err := db.Get(ctx, []byte(k))
			if err != nil {
				log.Fatalf("Get(%s): %v", k, err)
			}
			fmt.Printf("  %s = %s\n", k, val)
		}
	}

	fmt.Println("\n✓ SSTable inventory survived the restart via the manifest")
}

// printDirLayout lists the database directory, grouping the manifest machinery
// (CURRENT, MANIFEST-*) apart from the SSTables it tracks and the WAL.
func printDirLayout(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Fatal(err)
	}

	var manifestFiles, sstables, other []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == "CURRENT" || strings.HasPrefix(name, "MANIFEST-"):
			manifestFiles = append(manifestFiles, name)
		case filepath.Ext(name) == ".sst":
			sstables = append(sstables, name)
		default:
			other = append(other, name)
		}
	}

	fmt.Println("  Manifest:")
	for _, n := range manifestFiles {
		fmt.Printf("    %s\n", n)
	}
	fmt.Println("  SSTables (tracked by the manifest):")
	for _, n := range sstables {
		fmt.Printf("    %s\n", n)
	}
	fmt.Println("  Other:")
	for _, n := range other {
		fmt.Printf("    %s\n", n)
	}
}

// sortedKeys returns the keys of m in ascending order so the example writes
// and reads them deterministically.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
