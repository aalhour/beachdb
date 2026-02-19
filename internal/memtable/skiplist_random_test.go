package memtable

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/testutil"
)

func TestSkipList_RandomVsModel(t *testing.T) {
	// Deterministic seed for reproducibility
	//nolint:gosec // G404: Test code doesn't need crypto/rand
	rng := rand.New(rand.NewPCG(42, 1024))

	skipList := NewSkipList()
	referenceModel := testutil.NewModel()

	// Construct keys pool (small pool to get overwrites)
	const poolSize = 100
	keysPool := make([][]byte, 0, poolSize)
	for range poolSize {
		keysPool = append(keysPool, testutil.RandKey(rng, 100))
	}

	// Fill the skip list and reference model with random operations
	var seqno uint64
	const numOperations = 5000

	for i := range numOperations {
		seqno++

		// 70% Put, 30% Delete
		isPut := rng.Float64() < 0.70

		// Grab a random key from pool
		key := keysPool[rng.IntN(poolSize)]

		if isPut {
			// Random value (use testutil for variety)
			value := testutil.RandValue(rng, 128)

			skipList.Put(keys.InternalKey{
				UserKey: key,
				Seqno:   seqno,
				Kind:    keys.InternalKeyKindPut,
			}, value)
			referenceModel.PutAt(key, value, seqno)
		} else {
			// Delete (tombstone)
			skipList.Put(keys.InternalKey{
				UserKey: key,
				Seqno:   seqno,
				Kind:    keys.InternalKeyKindDelete,
			}, nil)
			referenceModel.DeleteAt(key, seqno)
		}

		// Step 3f: Pick 10 random keys, verify Get matches at current seqno
		for range 10 {
			checkKey := keysPool[rng.IntN(poolSize)]
			got, found := skipList.Get(checkKey, seqno)
			want, wantFound := referenceModel.GetAt(checkKey, seqno)

			if found != wantFound {
				t.Fatalf("iteration %d, key %q: found=%v, wantFound=%v",
					i, checkKey, found, wantFound)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("iteration %d, key %q: got=%x, want=%x",
					i, checkKey, got, want)
			}
		}
	}

	// Step 4: Final scan - verify ALL keys in pool match
	t.Logf("Final scan: verifying all %d keys at seqno %d", poolSize, seqno)
	for _, key := range keysPool {
		got, found := skipList.Get(key, seqno)
		want, wantFound := referenceModel.GetAt(key, seqno)

		if found != wantFound {
			t.Errorf("final scan, key %q: found=%v, wantFound=%v",
				key, found, wantFound)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("final scan, key %q: got=%x, want=%x",
				key, got, want)
		}
	}

	t.Logf("Completed %d operations with %d entries in skiplist",
		numOperations, skipList.Len())
}
