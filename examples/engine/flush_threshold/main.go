package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/aalhour/beachdb/engine"
)

func main() {
	dir := "/tmp/beachdb-example-flush"
	defer os.RemoveAll(dir)

	// Open with a small flush threshold so auto-flush triggers during this example.
	db, err := engine.Open(dir, engine.WithMemtableFlushSize(1024))
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	// Write enough data to exceed the 1024-byte threshold and trigger auto-flush.
	const numKeys = 50
	for i := range numKeys {
		key := fmt.Appendf(nil, "user:%05d", i)
		val := fmt.Appendf(nil, "data-for-user-%05d", i)
		if err := db.Put(ctx, key, val); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Printf("Wrote %d keys\n", numKeys)

	// Close waits for any in-progress flush to complete.
	if err := db.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Closed database (flush drained)")

	// Count SSTable files on disk to confirm data was flushed.
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Fatal(err)
	}
	sstCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".sst" {
			sstCount++
		}
	}
	fmt.Printf("SSTable files on disk: %d\n", sstCount)

	// Reopen — data is recovered from SSTables, not just WAL replay.
	db, err = engine.Open(dir, engine.WithMemtableFlushSize(1024))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	fmt.Println("Reopened database")

	// Read back every key to prove data survived the restart.
	for i := range numKeys {
		key := fmt.Appendf(nil, "user:%05d", i)
		val, err := db.Get(ctx, key)
		if err != nil {
			log.Fatalf("Get(%s): %v", key, err)
		}
		if i == 0 || i == numKeys-1 {
			fmt.Printf("  %s = %s\n", key, val)
		}
	}
	fmt.Printf("All %d keys recovered successfully\n", numKeys)
}
