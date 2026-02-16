package engine

import (
	"slices"
	"testing"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		name   string
		first  InternalKey
		second InternalKey
		want   int
	}{
		{
			name:   "empty structs are equal",
			first:  InternalKey{},
			second: InternalKey{},
			want:   0,
		},
		{
			name:  "empty first struct is less than non-empty struct",
			first: InternalKey{},
			second: InternalKey{
				UserKey: []byte("second"),
				Seqno:   1,
				Kind:    InternalKeyKindPut,
			},
			want: -1,
		},
		{
			name: "non-empty struct is greater than empty struct",
			first: InternalKey{
				UserKey: []byte("first"),
				Seqno:   1,
				Kind:    InternalKeyKindPut,
			},
			second: InternalKey{},
			want:   1,
		},
		{
			name: "shorter keys return -1 when other fields are equal",
			first: InternalKey{
				UserKey: []byte("key1"),
				Seqno:   1,
				Kind:    InternalKeyKindPut,
			},
			second: InternalKey{
				UserKey: []byte("key2"),
				Seqno:   1,
				Kind:    InternalKeyKindPut,
			},
			want: -1,
		},
		{
			name: "higher seqno means newer (comes first) when key and kind are equal",
			first: InternalKey{
				UserKey: []byte("key"),
				Seqno:   1,
				Kind:    InternalKeyKindPut,
			},
			second: InternalKey{
				UserKey: []byte("key"),
				Seqno:   2,
				Kind:    InternalKeyKindPut,
			},
			want: 1,
		},
		{
			name: "equality means key and seqno are equal - kind is ignored",
			first: InternalKey{
				UserKey: []byte("key"),
				Seqno:   1,
				Kind:    InternalKeyKindPut,
			},
			second: InternalKey{
				UserKey: []byte("key"),
				Seqno:   1,
				Kind:    InternalKeyKindDelete,
			},
			want: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.first.Compare(test.second)
			if test.want != got {
				t.Fatalf("expected %v but got %v instead", test.want, got)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	tests := []struct {
		name string
		key  InternalKey
		want []byte
	}{
		{
			name: "Encode an empty user key",
			key: InternalKey{
				UserKey: []byte{},
				Seqno:   123456789,
				Kind:    InternalKeyKindPut,
			},
			want: []byte{
				0, 0, 0, 0, 7, 91, 205, 21, // 123456789 in 8 bytes
				InternalKeyKindPut,
			},
		},
		{
			name: "Encode a basic put key",
			key: InternalKey{
				UserKey: []byte("key"),
				Seqno:   42,
				Kind:    InternalKeyKindPut,
			},
			want: append(
				append(
					[]byte("key"),
					0, 0, 0, 0, 0, 0, 0, 42, // Seqno: 42 in 8 bytes (big endian)
				),
				InternalKeyKindPut,
			),
		},
		{
			name: "Encode a basic delete key",
			key: InternalKey{
				UserKey: []byte("foo"),
				Seqno:   0,
				Kind:    InternalKeyKindDelete,
			},
			want: append(
				append(
					[]byte("foo"),
					0, 0, 0, 0, 0, 0, 0, 0, // Seqno: 0
				),
				InternalKeyKindDelete,
			),
		},
		{
			name: "Encode a longer user key and a higher seqno",
			key: InternalKey{
				UserKey: []byte("averylongeruserkey"),
				Seqno:   0x0102030405060708,
				Kind:    InternalKeyKindPut,
			},
			want: append(
				append(
					[]byte("averylongeruserkey"),
					1, 2, 3, 4, 5, 6, 7, 8,
				),
				InternalKeyKindPut,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.key.Encode()
			if !slices.Equal(test.want, got) {
				t.Fatalf("expected '%v' but got '%v' instead", test.want, got)
			}
		})
	}
}

func TestDecodeInternalKey(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want InternalKey
	}{
		{
			name: "Decode a basic put key",
			data: append(
				append(
					[]byte("key"),
					0, 0, 0, 0, 0, 0, 0, 42, // Seqno: 42 in 8 bytes (big endian)
				),
				InternalKeyKindPut,
			),
			want: InternalKey{
				UserKey: []byte("key"),
				Seqno:   42,
				Kind:    InternalKeyKindPut,
			},
		},
		{
			name: "Decode a basic delete key",
			data: append(
				append(
					[]byte("foo"),
					0, 0, 0, 0, 0, 0, 0, 0, // Seqno: 0
				),
				InternalKeyKindDelete,
			),
			want: InternalKey{
				UserKey: []byte("foo"),
				Seqno:   0,
				Kind:    InternalKeyKindDelete,
			},
		},
		{
			name: "Decode a longer user key and a higher seqno",
			data: append(
				append(
					[]byte("averylongeruserkey"),
					1, 2, 3, 4, 5, 6, 7, 8,
				),
				InternalKeyKindPut,
			),
			want: InternalKey{
				UserKey: []byte("averylongeruserkey"),
				Seqno:   0x0102030405060708,
				Kind:    InternalKeyKindPut,
			},
		},
		{
			name: "Decode an empty user key",
			data: []byte{
				0, 0, 0, 0, 7, 91, 205, 21, // 123456789 in 8 bytes
				InternalKeyKindPut,
			},
			want: InternalKey{
				UserKey: []byte{},
				Seqno:   123456789,
				Kind:    InternalKeyKindPut,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := DecodeInternalKey(test.data)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.want.Compare(got) != 0 {
				t.Fatalf("expected '%v' but got '%v' instead", test.want, got)
			}
		})
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	tests := []struct {
		name string
		key  InternalKey
	}{
		{
			name: "Encode an empty user key",
			key: InternalKey{
				UserKey: []byte{},
				Seqno:   123456789,
				Kind:    InternalKeyKindPut,
			},
		},
		{
			name: "Encode a basic put key",
			key: InternalKey{
				UserKey: []byte("key"),
				Seqno:   42,
				Kind:    InternalKeyKindPut,
			},
		},
		{
			name: "Encode a basic delete key",
			key: InternalKey{
				UserKey: []byte("foo"),
				Seqno:   0,
				Kind:    InternalKeyKindDelete,
			},
		},
		{
			name: "Encode a longer user key and a higher seqno",
			key: InternalKey{
				UserKey: []byte("averylongeruserkey"),
				Seqno:   0x0102030405060708,
				Kind:    InternalKeyKindPut,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.key.Encode()
			decodedKey, err := DecodeInternalKey(data)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.key.Compare(decodedKey) != 0 {
				t.Fatalf("expected '%v' but got '%v' instead", test.key, decodedKey)
			}
		})
	}
}
