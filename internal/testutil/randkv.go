package testutil

import (
	"encoding/binary"
	"math/rand/v2"
)

// RandKey generates a random key of length 1 to maxLen bytes.
// Uses the provided RNG for reproducibility in tests.
func RandKey(rng *rand.Rand, maxLen int) []byte {
	// Key length: 1 to maxLen (inclusive)
	n := 1 + rng.IntN(maxLen)
	key := make([]byte, n)
	fillRandomBytes(rng, key)
	return key
}

// RandValue generates a random value of length 0 to maxLen bytes.
// Uses the provided RNG for reproducibility in tests.
func RandValue(rng *rand.Rand, maxLen int) []byte {
	// Value length: 0 to maxLen (inclusive) — values can be empty
	n := rng.IntN(maxLen + 1)
	value := make([]byte, n)
	fillRandomBytes(rng, value)
	return value
}

// fillRandomBytes fills the byte slice with random data from the RNG.
// Uses Uint64() which is the most efficient method.
func fillRandomBytes(rng *rand.Rand, p []byte) {
	// Fill 8 bytes at a time using Uint64
	for i := 0; i+8 <= len(p); i += 8 {
		val := rng.Uint64()
		binary.LittleEndian.PutUint64(p[i:], val)
	}

	// Fill remaining bytes (if length not multiple of 8)
	if remaining := len(p) % 8; remaining > 0 {
		val := rng.Uint64()
		for i := range remaining {
			//nolint:gosec // G602: Index is mathematically safe: remaining = len(p) % 8, i < remaining
			p[len(p)-remaining+i] = byte(val >> (8 * i))
		}
	}
}
