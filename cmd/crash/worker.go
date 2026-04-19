package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/aalhour/beachdb/engine"
)

// workerCommand parses worker flags and runs the worker subprocess loop.
func workerCommand(args []string) error {
	var cfg workerConfig
	fs := newFlagSet("worker")
	fs.StringVar(&cfg.DBDir, "dbdir", "", "Database directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	return runWorker(cfg)
}

// runWorker consumes operation messages, executes them, and emits events.
func runWorker(cfg workerConfig) error {
	db, err := engine.Open(cfg.DBDir)
	if err != nil {
		return fmt.Errorf("opening db: %w", err)
	}
	defer db.Close()

	if _, err := os.Stdout.Write(eventBytes(eventMessage{Kind: eventReady})); err != nil {
		return fmt.Errorf("writing ready event: %w", err)
	}

	dec := json.NewDecoder(os.Stdin)
	ctx := context.Background()

	for {
		var msg operationMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decoding operation: %w", err)
		}

		op, err := msg.toOperation()
		if err != nil {
			return err
		}
		if _, err := os.Stdout.Write(eventBytes(eventMessage{Kind: eventStart, OpID: op.ID})); err != nil {
			return fmt.Errorf("writing start event: %w", err)
		}

		if err := executeOperation(ctx, db, op); err != nil {
			if _, writeErr := os.Stdout.Write(eventBytes(eventMessage{
				Kind:  eventFail,
				OpID:  op.ID,
				Error: err.Error(),
			})); writeErr != nil {
				return fmt.Errorf("writing fail event: %w", writeErr)
			}
			continue
		}

		if _, err := os.Stdout.Write(eventBytes(eventMessage{Kind: eventAck, OpID: op.ID})); err != nil {
			return fmt.Errorf("writing ack event: %w", err)
		}
	}
}

// executeOperation maps one workload operation to engine API calls.
func executeOperation(ctx context.Context, db *engine.DB, op operation) error {
	switch op.Kind {
	case opPut:
		return db.Put(ctx, op.Key, op.Value)
	case opDelete:
		return db.Delete(ctx, op.Key)
	case opBatch:
		batch := engine.NewBatch()
		for _, item := range op.Batch {
			if item.Kind == opPut {
				batch.Put(item.Key, item.Value)
				continue
			}
			batch.Delete(item.Key)
		}
		return db.Write(ctx, batch)
	default:
		return fmt.Errorf("unsupported operation kind %q", op.Kind)
	}
}
