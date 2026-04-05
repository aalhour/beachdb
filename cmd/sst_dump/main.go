// Package main provides the sst_dump CLI tool for inspecting SST files.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/sstable"
)

func main() {
	entries := flag.Bool("entries", false, "print all entries")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: sst_dump [-entries] <path>\n")
		os.Exit(1)
	}

	if err := run(flag.Arg(0), *entries); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(path string, showEntries bool) error {
	file, err := os.Open(path) //nolint:gosec // CLI tool — path comes from user argument
	if err != nil {
		return fmt.Errorf("couldn't open sstable at %q: %w", path, err)
	}

	reader, err := sstable.OpenReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()

	fmt.Printf("SSTable: %s\n", path)
	fmt.Printf("  Version: 1\n")
	fmt.Printf("  Entries: %d\n", reader.EntryCount())
	fmt.Printf("  Data blocks: %d\n", reader.DataBlockCount())
	fmt.Printf("  Index block: offset=%d size=%d\n", reader.IndexOffset(), reader.IndexSize())

	fmt.Printf("\nBlocks:\n")
	for i := range reader.DataBlockCount() {
		lastKey, offset, size := reader.BlockInfo(int(i))
		fmt.Printf("  Block %d: offset=%d size=%d last_key=%q seqno=%d\n",
			i, offset, size, string(lastKey.UserKey), lastKey.Seqno)
	}

	if showEntries {
		fmt.Printf("\nEntries:\n")
		iter := reader.NewIterator()
		defer iter.Close()
		iter.SeekToFirst()
		for idx := 0; iter.Valid(); idx++ {
			key := iter.Key()
			val := iter.Value()
			kind := "Put   "
			if key.Kind == keys.InternalKeyKindDelete {
				kind = "Delete"
			}
			fmt.Printf("  [%d] %s key=%q seqno=%d value=%d bytes\n",
				idx, kind, string(key.UserKey), key.Seqno, len(val))
			iter.Next()
		}
		if err := iter.Err(); err != nil {
			return fmt.Errorf("iterator error: %w", err)
		}
	}

	return nil
}
