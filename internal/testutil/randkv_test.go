package testutil

import (
	"math/rand/v2"
	"testing"
)

func TestRandKey(t *testing.T) {
	//nolint:gosec // G404: Test code doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(42, 1024))

	t.Run("length is in valid range", func(t *testing.T) {
		const maxLen = 32
		for range 100 {
			key := RandKey(rng, maxLen)
			if len(key) < 1 || len(key) > maxLen {
				t.Errorf("key length %d out of range [1, %d]", len(key), maxLen)
			}
		}
	})

	t.Run("keys are never empty", func(t *testing.T) {
		for range 100 {
			key := RandKey(rng, 10)
			if len(key) == 0 {
				t.Error("RandKey produced empty key")
			}
		}
	})

	t.Run("reproducible with same seed", func(t *testing.T) {
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng1 := rand.New(rand.NewPCG(123, 456))
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng2 := rand.New(rand.NewPCG(123, 456))

		keys1 := make([][]byte, 10)
		keys2 := make([][]byte, 10)

		for i := range 10 {
			keys1[i] = RandKey(rng1, 32)
			keys2[i] = RandKey(rng2, 32)
		}

		for i := range 10 {
			if len(keys1[i]) != len(keys2[i]) {
				t.Errorf("key %d: length mismatch: %d != %d", i, len(keys1[i]), len(keys2[i]))
			}
			for j := range keys1[i] {
				if keys1[i][j] != keys2[i][j] {
					t.Errorf("key %d: byte mismatch at offset %d", i, j)
					break
				}
			}
		}
	})

	t.Run("different seeds produce different keys", func(t *testing.T) {
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng1 := rand.New(rand.NewPCG(1, 2))
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng2 := rand.New(rand.NewPCG(3, 4))

		key1 := RandKey(rng1, 32)
		key2 := RandKey(rng2, 32)

		// Very unlikely to be identical with different seeds
		identical := len(key1) == len(key2)
		if identical {
			for i := range key1 {
				if key1[i] != key2[i] {
					identical = false
					break
				}
			}
		}

		if identical {
			t.Error("different seeds produced identical keys (extremely unlikely)")
		}
	})

	t.Run("maxLen of 1 produces single byte", func(t *testing.T) {
		for range 10 {
			key := RandKey(rng, 1)
			if len(key) != 1 {
				t.Errorf("maxLen=1 produced key of length %d", len(key))
			}
		}
	})

	t.Run("large maxLen works", func(t *testing.T) {
		key := RandKey(rng, 1024)
		if len(key) < 1 || len(key) > 1024 {
			t.Errorf("large maxLen produced invalid length: %d", len(key))
		}
	})
}

func TestRandValue(t *testing.T) {
	//nolint:gosec // G404: Test code doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(42, 1024))

	t.Run("length is in valid range", func(t *testing.T) {
		const maxLen = 128
		for range 100 {
			value := RandValue(rng, maxLen)
			if len(value) > maxLen {
				t.Errorf("value length %d out of range [0, %d]", len(value), maxLen)
			}
		}
	})

	t.Run("values can be empty", func(t *testing.T) {
		// Run many times to ensure we hit empty case
		foundEmpty := false
		for range 1000 {
			value := RandValue(rng, 10)
			if len(value) == 0 {
				foundEmpty = true
				break
			}
		}
		if !foundEmpty {
			t.Error("RandValue never produced empty value after 1000 attempts")
		}
	})

	t.Run("reproducible with same seed", func(t *testing.T) {
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng1 := rand.New(rand.NewPCG(789, 101112))
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng2 := rand.New(rand.NewPCG(789, 101112))

		values1 := make([][]byte, 10)
		values2 := make([][]byte, 10)

		for i := range 10 {
			values1[i] = RandValue(rng1, 128)
			values2[i] = RandValue(rng2, 128)
		}

		for i := range 10 {
			if len(values1[i]) != len(values2[i]) {
				t.Errorf("value %d: length mismatch: %d != %d", i, len(values1[i]), len(values2[i]))
			}
			for j := range values1[i] {
				if values1[i][j] != values2[i][j] {
					t.Errorf("value %d: byte mismatch at offset %d", i, j)
					break
				}
			}
		}
	})

	t.Run("maxLen of 0 produces empty value", func(t *testing.T) {
		for range 10 {
			value := RandValue(rng, 0)
			if len(value) != 0 {
				t.Errorf("maxLen=0 produced value of length %d", len(value))
			}
		}
	})

	t.Run("large maxLen works", func(t *testing.T) {
		value := RandValue(rng, 2048)
		if len(value) > 2048 {
			t.Errorf("large maxLen produced invalid length: %d", len(value))
		}
	})
}

func TestFillRandomBytes(t *testing.T) {
	//nolint:gosec // G404: Test code doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(999, 888))

	t.Run("fills all bytes", func(t *testing.T) {
		// Test various lengths including edge cases around 8 bytes
		lengths := []int{1, 2, 3, 7, 8, 9, 15, 16, 17, 32, 100}

		for _, n := range lengths {
			buf := make([]byte, n)
			fillRandomBytes(rng, buf)

			// Check that we got n bytes
			if len(buf) != n {
				t.Errorf("length %d: buffer size changed", n)
			}

			// Check that not all bytes are zero (statistically improbable)
			allZero := true
			for _, b := range buf {
				if b != 0 {
					allZero = false
					break
				}
			}
			if allZero && n > 0 {
				t.Errorf("length %d: all bytes are zero (very unlikely)", n)
			}
		}
	})

	t.Run("handles empty slice", func(_ *testing.T) {
		buf := make([]byte, 0)
		fillRandomBytes(rng, buf) // Should not panic
	})

	t.Run("reproducible", func(t *testing.T) {
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng1 := rand.New(rand.NewPCG(111, 222))
		//nolint:gosec // G404: Test code doesn't need crypto/rand
		rng2 := rand.New(rand.NewPCG(111, 222))

		buf1 := make([]byte, 50)
		buf2 := make([]byte, 50)

		fillRandomBytes(rng1, buf1)
		fillRandomBytes(rng2, buf2)

		for i := range buf1 {
			if buf1[i] != buf2[i] {
				t.Errorf("byte %d mismatch: %d != %d", i, buf1[i], buf2[i])
			}
		}
	})
}
