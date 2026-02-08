package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aalhour/beachdb/engine"
)

func main() {
	// Create a temporary directory for the database
	dir := "/tmp/beachdb-example-batch"
	defer os.RemoveAll(dir)

	// Open the database
	db, err := engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Create a batch of operations
	batch := engine.NewBatch()
	batch.Put([]byte("user:1:name"), []byte("alice"))
	batch.Put([]byte("user:1:email"), []byte("alice@example.com"))
	batch.Put([]byte("user:2:name"), []byte("bob"))
	batch.Put([]byte("user:2:email"), []byte("bob@example.com"))
	batch.Delete([]byte("user:3:name")) // Delete (tombstone)

	fmt.Printf("Batch contains %d operations\n", batch.Count())

	// Write the batch atomically
	if err := db.Write(ctx, batch); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Batch written atomically")

	// Verify the data
	keys := []string{"user:1:name", "user:1:email", "user:2:name", "user:2:email"}
	for _, key := range keys {
		value, err := db.Get(ctx, []byte(key))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %s = %s\n", key, value)
	}

	// Reuse the batch (Reset clears it)
	batch.Reset()
	fmt.Printf("\nAfter Reset: batch contains %d operations\n", batch.Count())

	batch.Put([]byte("new-key"), []byte("new-value"))
	fmt.Printf("After adding one: batch contains %d operations\n", batch.Count())

	fmt.Println("\n✓ Batch operations completed successfully")
}
