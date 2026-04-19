package main

import (
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/aalhour/beachdb/internal/testutil"
)

// generateWorkload builds a deterministic mixed-operation workload from config.
func generateWorkload(cfg runConfig) []operation {
	//nolint:gosec // test harness uses deterministic pseudorandom generation
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x5eed))
	ops := make([]operation, 0, cfg.Ops)
	knownKeys := make([][]byte, 0, cfg.Ops)

	for opID := range cfg.Ops {
		kind := chooseKind(rng, cfg.PutRatio, cfg.DeleteRatio, cfg.BatchRatio, len(knownKeys) > 0)
		switch kind {
		case opPut:
			key := chooseKey(rng, cfg, knownKeys)
			value := testutil.RandValue(rng, cfg.MaxValueLen)
			ops = append(ops, operation{
				ID:    opID,
				Kind:  opPut,
				Key:   key,
				Value: value,
			})
			knownKeys = appendIfNewKey(knownKeys, key)
		case opDelete:
			key := cloneBytes(knownKeys[rng.IntN(len(knownKeys))])
			ops = append(ops, operation{
				ID:   opID,
				Kind: opDelete,
				Key:  key,
			})
		case opBatch:
			batchLen := 2 + rng.IntN(3)
			items := make([]batchItem, 0, batchLen)
			for range batchLen {
				itemKind := chooseKind(rng, cfg.PutRatio, cfg.DeleteRatio, 0, len(knownKeys) > 0)
				switch itemKind {
				case opPut:
					key := chooseKey(rng, cfg, knownKeys)
					value := testutil.RandValue(rng, cfg.MaxValueLen)
					items = append(items, batchItem{
						Kind:  opPut,
						Key:   key,
						Value: value,
					})
					knownKeys = appendIfNewKey(knownKeys, key)
				case opDelete:
					key := cloneBytes(knownKeys[rng.IntN(len(knownKeys))])
					items = append(items, batchItem{
						Kind: opDelete,
						Key:  key,
					})
				default:
					panic(fmt.Sprintf("unsupported batch item kind %q", itemKind))
				}
			}
			ops = append(ops, operation{
				ID:    opID,
				Kind:  opBatch,
				Batch: items,
			})
		default:
			panic(fmt.Sprintf("unsupported operation kind %q", kind))
		}
	}

	return ops
}

// chooseKind samples an operation kind based on configured ratios.
func chooseKind(rng *rand.Rand, putRatio, deleteRatio, batchRatio int, allowDelete bool) opKind {
	type weightedKind struct {
		kind   opKind
		weight int
	}

	choices := []weightedKind{
		{kind: opPut, weight: putRatio},
		{kind: opBatch, weight: batchRatio},
	}
	if allowDelete {
		choices = append(choices, weightedKind{kind: opDelete, weight: deleteRatio})
	}

	total := 0
	for _, choice := range choices {
		total += choice.weight
	}
	pick := rng.IntN(total)
	for _, choice := range choices {
		if pick < choice.weight {
			return choice.kind
		}
		pick -= choice.weight
	}

	return opPut
}

// chooseKey samples either a hot key or a new random key.
func chooseKey(rng *rand.Rand, cfg runConfig, knownKeys [][]byte) []byte {
	if len(knownKeys) > 0 && rng.IntN(100) < cfg.HotKeyRatio {
		return cloneBytes(knownKeys[rng.IntN(len(knownKeys))])
	}
	return testutil.RandKey(rng, cfg.MaxKeyLen)
}

// appendIfNewKey adds key to known set if it does not already exist.
func appendIfNewKey(knownKeys [][]byte, key []byte) [][]byte {
	if slices.ContainsFunc(knownKeys, func(candidate []byte) bool {
		return string(candidate) == string(key)
	}) {
		return knownKeys
	}

	return append(knownKeys, cloneBytes(key))
}
