package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aalhour/beachdb/engine"
)

func main() {
	ctx := context.Background()

	// === Example 1: Default options (sync on write) ===
	fmt.Println("=== Default: Sync on every write ===")
	dir1 := "/tmp/beachdb-example-sync"
	defer os.RemoveAll(dir1)

	db1, err := engine.Open(dir1) // Default: WithSync(true)
	if err != nil {
		log.Fatal(err)
	}

	if err := db1.Put(ctx, []byte("key1"), []byte("value1")); err != nil {
		log.Fatal(err)
	}
	fmt.Println("  ✓ Write completed with fsync (durable)")
	db1.Close()

	// === Example 2: Disable sync for performance ===
	fmt.Println("\n=== WithSync(false): No fsync on write ===")
	dir2 := "/tmp/beachdb-example-nosync"
	defer os.RemoveAll(dir2)

	db2, err := engine.Open(dir2, engine.WithSync(false))
	if err != nil {
		log.Fatal(err)
	}

	if err := db2.Put(ctx, []byte("key2"), []byte("value2")); err != nil {
		log.Fatal(err)
	}
	fmt.Println("  ✓ Write completed without fsync (faster, but less durable)")
	fmt.Println("  ⚠ Data not guaranteed to survive crash until Close()")
	db2.Close()

	// === Example 3: Multiple options (future-proof pattern) ===
	fmt.Println("\n=== Multiple options ===")
	dir3 := "/tmp/beachdb-example-multi"
	defer os.RemoveAll(dir3)

	db3, err := engine.Open(dir3,
		engine.WithSync(true),
		// Future options can be added here:
		// engine.WithCompression(true),
		// engine.WithCacheSize(64 * 1024 * 1024),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer db3.Close()

	if err := db3.Put(ctx, []byte("key3"), []byte("value3")); err != nil {
		log.Fatal(err)
	}
	fmt.Println("  ✓ Database configured with custom options")

	fmt.Println("\n✓ Options demonstration completed")
}
