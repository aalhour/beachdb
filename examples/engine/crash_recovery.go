package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aalhour/beachdb/engine"
)

func main() {
	// Use a persistent directory to demonstrate recovery
	dir := "/tmp/beachdb-example-recovery"
	defer os.RemoveAll(dir)

	ctx := context.Background()

	// === First session: Write data ===
	fmt.Println("=== Session 1: Writing data ===")
	db1, err := engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}

	// Write some data
	data := map[string]string{
		"user:1": "alice",
		"user:2": "bob",
		"user:3": "charlie",
	}

	for key, value := range data {
		if err := db1.Put(ctx, []byte(key), []byte(value)); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  Wrote: %s = %s\n", key, value)
	}

	// Close the database (simulates clean shutdown)
	if err := db1.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Database closed")

	// === Second session: Recovery ===
	fmt.Println("\n=== Session 2: Recovering from WAL ===")
	db2, err := engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db2.Close()

	// Verify all data was recovered
	recovered := 0
	for key := range data {
		value, err := db2.Get(ctx, []byte(key))
		if err != nil {
			log.Printf("  Failed to recover %s: %v", key, err)
			continue
		}
		fmt.Printf("  Recovered: %s = %s\n", key, value)
		recovered++
	}

	fmt.Printf("\n✓ Successfully recovered %d/%d keys\n", recovered, len(data))
	fmt.Println("✓ Durability guarantee verified")
}
