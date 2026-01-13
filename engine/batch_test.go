package engine

import (
	"slices"
	"testing"
)

func TestBatchPutCount(t *testing.T) {
	tests := []struct {
		name      string
		pairs     []map[string]string
		wantCount int
	}{
		{
			name:      "empty",
			pairs:     []map[string]string{},
			wantCount: 0,
		},
		{
			name: "a few",
			pairs: []map[string]string{
				{"key": "name", "value": "highlander"},
				{"key": "name", "value": "midlander"},
				{"key": "name", "value": "lowlander"},
			},
			wantCount: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := NewBatch()
			for _, pair := range test.pairs {
				batch.Put([]byte(pair["key"]), []byte(pair["value"]))
			}

			if batch.Count() != test.wantCount {
				t.Errorf(
					"Expected count to be %v, but got %v instead",
					test.wantCount, batch.Count(),
				)
			}
		})
	}
}

func TestBatchDeleteCount(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		wantCount int
	}{
		{
			name:      "empty",
			keys:      []string{},
			wantCount: 0,
		},
		{
			name:      "A few",
			keys:      []string{"name", "age", "city"},
			wantCount: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := NewBatch()
			for _, key := range test.keys {
				batch.Delete([]byte(key))
			}

			if batch.Count() != test.wantCount {
				t.Errorf(
					"Expected count to be %v, but got %v instead",
					test.wantCount, batch.Count(),
				)
			}
		})
	}
}

func TestBatchReset(t *testing.T) {
	tests := []struct {
		name       string
		putData    []map[string]string
		deleteData []string
		wantCount  int
	}{
		{
			name:       "empty",
			putData:    []map[string]string{},
			deleteData: []string{},
		},
		{
			name: "a few",
			putData: []map[string]string{
				{"key": "name", "value": "highlander"},
				{"key": "name", "value": "midlander"},
				{"key": "name", "value": "lowlander"},
			},
			deleteData: []string{"name", "age", "city"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := NewBatch()
			for _, pair := range test.putData {
				batch.Put([]byte(pair["key"]), []byte(pair["value"]))
			}

			for _, key := range test.deleteData {
				batch.Delete([]byte(key))
			}

			batch.Reset()

			if batch.Count() != 0 {
				t.Errorf(
					"Expected count to be %v, but got %v instead",
					test.wantCount, batch.Count(),
				)
			}
		})
	}
}

func TestBatchEncode(t *testing.T) {
	tests := []struct {
		name  string
		batch *Batch
		want  []byte
	}{
		{
			name:  "empty batch",
			batch: &Batch{ops: make([]operation, 0)},
			want: []byte{
				1,       // version
				0, 0, 0, // reserved (3 bytes)
				0, 0, 0, 0, // op_count = 0
			},
		},
		{
			name: "batch with 1 put op",
			batch: &Batch{ops: []operation{{
				opType: OpTypePut,
				key:    []byte("name"),
				value:  []byte("john smith"),
			}}},
			want: []byte{
				1,       // version
				0, 0, 0, // reserved (3 bytes)
				0, 0, 0, 1, // op_count = 1
				// ---
				1,          // op_type = Put
				0, 0, 0, 4, // key_len = 4
				'n', 'a', 'm', 'e', // key = "name"
				0, 0, 0, 10, // value_len = 10
				'j', 'o', 'h', 'n', ' ', 's', 'm', 'i', 't', 'h', // value = "john smith"
			},
		},
		{
			name: "batch with 1 delete op",
			batch: &Batch{ops: []operation{{
				opType: OpTypeDelete,
				key:    []byte("age"),
			}}},
			want: []byte{
				1,       // version
				0, 0, 0, // reserved (3 bytes)
				0, 0, 0, 1, // op_count = 1
				// ---
				2,          // op_type = Delete
				0, 0, 0, 3, // key_len = 3
				'a', 'g', 'e', // key = "age"
			},
		},
		{
			name: "batch with multiple ops",
			batch: &Batch{ops: []operation{
				{
					opType: OpTypePut,
					key:    []byte("name"),
					value:  []byte("john smith"),
				},
				{
					opType: OpTypeDelete,
					key:    []byte("name"),
				},
				{
					opType: OpTypePut,
					key:    []byte("age"),
					value:  []byte("21"),
				},
				{
					opType: OpTypeDelete,
					key:    []byte("age"),
				},
			}},
			want: []byte{
				1,       // version
				0, 0, 0, // reserved (3 bytes)
				0, 0, 0, 4, // op_count = 4
				// ---
				1,          // op_type = Put
				0, 0, 0, 4, // key_len = 4
				'n', 'a', 'm', 'e', // key = "name"
				0, 0, 0, 10, // value_len = 10
				'j', 'o', 'h', 'n', ' ', 's', 'm', 'i', 't', 'h', // value = "john smith"
				// ---
				2,          // op_type = Delete
				0, 0, 0, 4, // key_len = 4
				'n', 'a', 'm', 'e', // key = "name"
				// ---
				1,          // op_type = Put
				0, 0, 0, 3, // key_len = 3
				'a', 'g', 'e', // key = "age"
				0, 0, 0, 2, // value_len = 2
				'2', '1', // value = "21"
				// ---
				2,          // op_type = Delete
				0, 0, 0, 3, // key_len = 3
				'a', 'g', 'e', // key = "age"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// First encoding
			got1 := test.batch.Encode()

			if !slices.Equal(got1, test.want) {
				t.Errorf(
					"Expected {%v} but got {%v} instead",
					test.want, got1,
				)
			}

			// Second encoding to test basic deterministic behavior
			got2 := test.batch.Encode()

			if !slices.Equal(got2, test.want) {
				t.Errorf(
					"Expected {%v} but got {%v} instead",
					test.want, got2,
				)
			}
		})
	}
}

func TestBatchDecode(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantErr bool
		wantOps []operation // only for success cases, optional
	}{
		{
			name: "happy path: one put",
			input: []byte{
				1, 0, 0, 0, // version=1, 3 reserved
				0, 0, 0, 1, // op_count = 1
				1,          // op_type = Put
				0, 0, 0, 3, // key_len = 3
				'f', 'o', 'o',
				0, 0, 0, 3, // value_len = 3
				'b', 'a', 'r',
			},
			wantErr: false,
			wantOps: []operation{
				{opType: OpTypePut, key: []byte("foo"), value: []byte("bar")},
			},
		},
		{
			name: "happy path: one delete",
			input: []byte{
				1, 0, 0, 0, // version=1
				0, 0, 0, 1, // op_count=1
				2,          // op_type = Delete
				0, 0, 0, 3, // key_len=3
				'b', 'a', 'z',
			},
			wantErr: false,
			wantOps: []operation{
				{opType: OpTypeDelete, key: []byte("baz"), value: nil},
			},
		},
		{
			name: "truncated header: < 8 bytes total",
			input: []byte{
				1, 0, 0, 0, 0, 0, 0, // only 7 bytes (should be at least 8),
			},
			wantErr: true,
		},
		{
			name: "wrong version byte",
			input: []byte{
				9, 0, 0, 0, // version=9 (should be 1)
				0, 0, 0, 0, // op_count = 0
			},
			wantErr: true,
		},
		{
			name: "op_count=1 but truncated after header",
			input: []byte{
				1, 0, 0, 0,
				0, 0, 0, 1, // op_count 1
				// 0 ops, so truncated
			},
			wantErr: true,
		},
		{
			name: "op_type unknown, triggers error",
			input: []byte{
				1, 0, 0, 0,
				0, 0, 0, 1,
				99, // unknown op_type
				0, 0, 0, 1,
				'x',
			},
			wantErr: true,
		},
		{
			name: "key_len present but truncated before key",
			input: []byte{
				1, 0, 0, 0,
				0, 0, 0, 1,
				1,          // Put
				0, 0, 0, 4, // key_len=4
				'e', 'f', // only 2 key bytes (needs 4)
			},
			wantErr: true,
		},
		{
			name: "Put op: value_len present, truncated before value bytes",
			input: []byte{
				1, 0, 0, 0,
				0, 0, 0, 1,
				1,          // Put
				0, 0, 0, 2, // key_len=2
				'a', 'b',
				0, 0, 0, 4, // value_len=4
				1, 2, // only 2 bytes delivered
			},
			wantErr: true,
		},
		{
			name: "Delete op, key_len = 1, but empty key",
			input: []byte{
				1, 0, 0, 0,
				0, 0, 0, 1,
				2,          // Delete
				0, 0, 0, 1, // key_len = 1
				// No key byte!
			},
			wantErr: true,
		},
		{
			name: "Put op, missing value_len (truncated)",
			input: []byte{
				1, 0, 0, 0,
				0, 0, 0, 1,
				1,          // Put
				0, 0, 0, 1, // key_len = 1
				'a', // key = 'a'
				// value_len missing
			},
			wantErr: true,
		},
		{
			name: "two ops, second incomplete",
			input: []byte{
				1, 0, 0, 0, // version
				0, 0, 0, 2, // op_count = 2
				1, 0, 0, 0, 1, 'x', 0, 0, 0, 1, 'A', // full op1: Put "x":"A"
				2, 0, 0, 0, 1, 'y', // op2 starts (Del "y"), but missing rest (but this is enough for key, so ok)
			},
			wantErr: false,
			wantOps: []operation{
				{opType: OpTypePut, key: []byte("x"), value: []byte("A")},
				{opType: OpTypeDelete, key: []byte("y"), value: nil},
			},
		},
		{
			name: "op_count larger than actual ops provided (early EOF)",
			input: []byte{
				1, 0, 0, 0, // version
				0, 0, 0, 2, // op_count = 2
				1, 0, 0, 0, 1, 'a', 0, 0, 0, 2, 'b', 'c',
				// Missing second op
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			batch, err := DecodeBatch(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error but got none (batch = %+v)", batch)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				} else if tc.wantOps != nil {
					// Compare batch.ops and tc.wantOps
					if len(batch.ops) != len(tc.wantOps) {
						t.Fatalf("expected %d ops, got %d", len(tc.wantOps), len(batch.ops))
					}
					for i := range tc.wantOps {
						got, want := batch.ops[i], tc.wantOps[i]
						if got.opType != want.opType {
							t.Errorf("op %d: expected opType %v, got %v", i, want.opType, got.opType)
						}
						if string(got.key) != string(want.key) {
							t.Errorf("op %d: expected key %q, got %q", i, want.key, got.key)
						}
						if want.value == nil && got.value != nil {
							t.Errorf("op %d: expected nil value, got %v", i, got.value)
						}
						if want.value != nil && string(got.value) != string(want.value) {
							t.Errorf("op %d: expected value %q, got %q", i, want.value, got.value)
						}
					}
				}
			}
		})
	}
}

func TestBatchEncodeDecodeRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		ops  []struct {
			op    string
			key   []byte
			value []byte
		}
	}{
		{
			name: "Empty batch",
			ops: []struct {
				op         string
				key, value []byte
			}{},
		},
		{
			name: "Put and Delete, two ops",
			ops: []struct {
				op    string
				key   []byte
				value []byte
			}{
				{"put", []byte("foo"), []byte("bar")},
				{"del", []byte("foo"), nil},
			},
		},
		{
			name: "Multiple Puts and Deletes",
			ops: []struct {
				op    string
				key   []byte
				value []byte
			}{
				{"put", []byte("name"), []byte("john smith")},
				{"del", []byte("name"), nil},
				{"put", []byte("age"), []byte("21")},
				{"del", []byte("age"), nil},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := NewBatch()
			for _, op := range test.ops {
				switch op.op {
				case "put":
					batch.Put(op.key, op.value)
				case "del":
					batch.Delete(op.key)
				}
			}

			encoded := batch.Encode()
			decoded, err := DecodeBatch(encoded)
			if err != nil {
				t.Fatalf("Decode error: %v", err)
			}

			// Compare ops length
			if len(decoded.ops) != len(batch.ops) {
				t.Errorf("decoded ops length %d, want %d", len(decoded.ops), len(batch.ops))
			}

			// Compare content for each op
			for i, op := range batch.ops {
				dop := decoded.ops[i]
				if op.opType != dop.opType {
					t.Errorf("at op #%d: got opType=%v, want=%v", i, dop.opType, op.opType)
				}
				if !slices.Equal(op.key, dop.key) {
					t.Errorf("at op #%d: got key=%v, want=%v", i, dop.key, op.key)
				}
				if !slices.Equal(op.value, dop.value) {
					t.Errorf("at op #%d: got value=%v, want=%v", i, dop.value, op.value)
				}
			}
		})
	}
}
