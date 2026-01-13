package checksum

import "testing"

func TestCRC32C(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want uint32
	}{
		{
			name: "hello, world",
			data: []byte("hello, world"),
			// CRC32C of "hello world" using Castagnoli polynomial is 0x6999a41f
			want: uint32(0x6999a41f),
		},
		{
			name: "empty string",
			data: []byte(""),
			// CRC32C of empty input is always 0x00000000
			want: 0x00000000,
		},
		{
			name: "empty slice",
			data: make([]byte, 0),
			// CRC32C of empty slice is always 0x00000000
			want: 0x00000000,
		},
		{
			name: "binary data",
			data: []byte{0x00, 0x01, 0xfe, 0xff},
			// Precomputed value for Castagnoli
			want: 0xf01a361c,
		},
		{
			name: "numbers",
			data: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 101, 234},
			// Precomputed value for Castagnoli
			want: 0xe077ad1d,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := CRC32C(test.data)
			if test.want != got {
				t.Errorf(
					"CRC32C(%v) = 0x%x; want 0x%x",
					test.data, got, test.want,
				)
			}
		})
	}
}
