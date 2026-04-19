package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/aalhour/beachdb/engine"
)

type oracleEntry struct {
	Key     []byte
	Value   []byte
	Deleted bool
}

// oracle models expected key state for acknowledged operations.
type oracle struct {
	state   map[string]oracleEntry
	touched map[string][]byte
}

// newOracle creates an empty oracle model.
func newOracle() *oracle {
	return &oracle{
		state:   make(map[string]oracleEntry),
		touched: make(map[string][]byte),
	}
}

// clone returns a deep copy of oracle state for alternative outcome checks.
func (o *oracle) clone() *oracle {
	cloned := &oracle{
		state:   make(map[string]oracleEntry, len(o.state)),
		touched: make(map[string][]byte, len(o.touched)),
	}

	for key, entry := range o.state {
		cloned.state[key] = oracleEntry{
			Key:     cloneBytes(entry.Key),
			Value:   cloneBytes(entry.Value),
			Deleted: entry.Deleted,
		}
	}
	for key, value := range o.touched {
		cloned.touched[key] = cloneBytes(value)
	}

	return cloned
}

// apply mutates oracle state according to one logical operation.
func (o *oracle) apply(op operation) {
	switch op.Kind {
	case opPut:
		o.applyPut(op.Key, op.Value)
	case opDelete:
		o.applyDelete(op.Key)
	case opBatch:
		for _, item := range op.Batch {
			if item.Kind == opPut {
				o.applyPut(item.Key, item.Value)
				continue
			}
			o.applyDelete(item.Key)
		}
	}
}

// applyPut records an upserted key/value in oracle state.
func (o *oracle) applyPut(key, value []byte) {
	keyCopy := cloneBytes(key)
	o.touched[string(keyCopy)] = keyCopy
	o.state[string(keyCopy)] = oracleEntry{
		Key:   keyCopy,
		Value: cloneBytes(value),
	}
}

// applyDelete records a tombstone in oracle state.
func (o *oracle) applyDelete(key []byte) {
	keyCopy := cloneBytes(key)
	o.touched[string(keyCopy)] = keyCopy
	o.state[string(keyCopy)] = oracleEntry{
		Key:     keyCopy,
		Deleted: true,
	}
}

// mergeTouchedFromOperation expands touched-key set with operation keys.
func (o *oracle) mergeTouchedFromOperation(op operation) {
	switch op.Kind {
	case opPut, opDelete:
		if op.Key != nil {
			o.touched[string(op.Key)] = cloneBytes(op.Key)
		}
	case opBatch:
		for _, item := range op.Batch {
			o.touched[string(item.Key)] = cloneBytes(item.Key)
		}
	}
}

// verificationResult summarizes successful verification checks.
type verificationResult struct {
	CheckedKeys int
	Allowed     []int
}

// verificationFailure describes the first mismatch found during verification.
type verificationFailure struct {
	Cycle       int    `json:"cycle"`
	OpID        int    `json:"op_id,omitempty"`
	KeyB64      string `json:"key_b64,omitempty"`
	ExpectedB64 string `json:"expected_b64,omitempty"`
	ActualB64   string `json:"actual_b64,omitempty"`
	Expected    string `json:"expected"`
	Actual      string `json:"actual"`
	Message     string `json:"message"`
}

// verifyDatabaseState reopens DB and validates it against allowed oracle states.
func verifyDatabaseState(
	ctx context.Context,
	dbDir string,
	base *oracle,
	pending *operation,
) (verificationResult, *verificationFailure, error) {
	db, err := engine.Open(dbDir)
	if err != nil {
		return verificationResult{}, nil, fmt.Errorf("opening db for verification: %w", err)
	}
	defer db.Close()

	allowed := []*oracle{base}
	allowedIDs := []int{}
	if pending != nil {
		alt := base.clone()
		alt.apply(*pending)
		allowed = append(allowed, alt)
		allowedIDs = append(allowedIDs, pending.ID)
	}

	keys := slices.Collect(maps.Values(base.touched))
	if pending != nil {
		shadow := base.clone()
		shadow.mergeTouchedFromOperation(*pending)
		keys = slices.Collect(maps.Values(shadow.touched))
	}

	for _, key := range keys {
		got, err := db.Get(ctx, key)
		if err != nil && !errors.Is(err, engine.ErrKeyNotFound) {
			return verificationResult{}, nil, fmt.Errorf("verifying key %x: %w", key, err)
		}

		if matchesAllowedState(key, got, err, allowed) {
			continue
		}

		failure := &verificationFailure{
			KeyB64: encodeB64(key),
		}
		if pending != nil {
			failure.OpID = pending.ID
		}
		if errors.Is(err, engine.ErrKeyNotFound) {
			failure.Actual = "missing"
		} else {
			failure.Actual = "present"
			failure.ActualB64 = encodeB64(got)
		}
		failure.Expected, failure.ExpectedB64 = expectedStateDescription(key, base)
		failure.Message = fmt.Sprintf("state for key %x did not match any allowed post-crash outcome", key)
		return verificationResult{}, failure, nil
	}

	return verificationResult{
		CheckedKeys: len(keys),
		Allowed:     allowedIDs,
	}, nil, nil
}

// matchesAllowedState returns true when actual key state matches any candidate.
func matchesAllowedState(key, got []byte, gotErr error, allowed []*oracle) bool {
	for _, candidate := range allowed {
		entry, ok := candidate.state[string(key)]
		if !ok || entry.Deleted {
			if errors.Is(gotErr, engine.ErrKeyNotFound) {
				return true
			}
			continue
		}
		if gotErr == nil && bytes.Equal(got, entry.Value) {
			return true
		}
	}

	return false
}

// expectedStateDescription converts oracle expectation to artifact-friendly text.
func expectedStateDescription(key []byte, model *oracle) (string, string) {
	entry, ok := model.state[string(key)]
	switch {
	case !ok, entry.Deleted:
		return "missing", ""
	default:
		return "present", encodeB64(entry.Value)
	}
}
