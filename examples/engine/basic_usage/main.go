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
	dir := "/tmp/beachdb-example-basic"
	defer os.RemoveAll(dir)

	// Open the database
	db, err := engine.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Put a key-value pair
	key := []byte("name")
	value := []byte("alice")
	if err := db.Put(ctx, key, value); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Stored: %s = %s\n", key, value)

	// Get the value back
	retrieved, err := db.Get(ctx, key)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Retrieved: %s = %s\n", key, retrieved)

	// Delete the key
	if err := db.Delete(ctx, key); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Deleted: %s\n", key)

	// Try to get deleted key
	_, err = db.Get(ctx, key)
	if err == engine.ErrKeyNotFound {
		fmt.Printf("Key not found (expected after delete)\n")
	}

	fmt.Println("\n✓ Basic operations completed successfully")
}
