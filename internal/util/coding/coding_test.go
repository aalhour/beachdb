package coding

import (
	"bytes"
	"slices"
	"testing"
)

func TestPutUint16AndUint16(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
	}{
		{"zero", 0},
		{"one", 1},
		{"max uint16", 0xFFFF},
		{"arbitrary", 0x1234},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, 2)
			PutUint16(buf, tt.val)
			got := Uint16(buf)
			if got != tt.val {
				t.Errorf("Uint16(PutUint16(%d)) = %d; want %d", tt.val, got, tt.val)
			}
		})
	}
}

func TestPutUint32AndUint32(t *testing.T) {
	tests := []struct {
		name string
		val  uint32
	}{
		{"zero", 0},
		{"one", 1},
		{"max uint32", 0xFFFFFFFF},
		{"arbitrary", 0x12345678},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, 4)
			PutUint32(buf, tt.val)
			got := Uint32(buf)
			if got != tt.val {
				t.Errorf("Uint32(PutUint32(%d)) = %d; want %d", tt.val, got, tt.val)
			}
		})
	}
}

func TestPutUint64AndUint64(t *testing.T) {
	tests := []struct {
		name string
		val  uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"max uint64", 0xFFFFFFFFFFFFFFFF},
		{"arbitrary", 0x123456789ABCDEF0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, 8)
			PutUint64(buf, tt.val)
			got := Uint64(buf)
			if got != tt.val {
				t.Errorf("Uint64(PutUint64(%d)) = %d; want %d", tt.val, got, tt.val)
			}
		})
	}
}

func TestUint16Endian(t *testing.T) {
	// Check that the encoding is big endian
	buf := []byte{0x01, 0x23}
	want := uint16(0x0123)
	got := Uint16(buf)
	if got != want {
		t.Errorf("Uint16(%x) = %x; want %x", buf, got, want)
	}
	// Round-trip test
	out := make([]byte, 2)
	PutUint16(out, want)
	if !bytes.Equal(out, buf) {
		t.Errorf("PutUint16(%x) = %x; want %x", want, out, buf)
	}
}

func TestUint32Endian(t *testing.T) {
	// Check that the encoding is big endian
	buf := []byte{0x01, 0x23, 0x45, 0x67}
	want := uint32(0x01234567)
	got := Uint32(buf)
	if got != want {
		t.Errorf("Uint32(%x) = %x; want %x", buf, got, want)
	}
	// Round-trip test
	out := make([]byte, 4)
	PutUint32(out, want)
	if !bytes.Equal(out, buf) {
		t.Errorf("PutUint32(%x) = %x; want %x", want, out, buf)
	}
}

func TestUint64Endian(t *testing.T) {
	// Check that the encoding is big endian
	buf := []byte{0xde, 0xad, 0xbe, 0xef, 0xfe, 0xed, 0xfa, 0xce}
	want := uint64(0xdeadbeeffeedface)
	got := Uint64(buf)
	if got != want {
		t.Errorf("Uint64(%x) = %x; want %x", buf, got, want)
	}
	// Round-trip test
	out := make([]byte, 8)
	PutUint64(out, want)
	if !bytes.Equal(out, buf) {
		t.Errorf("PutUint64(%x) = %x; want %x", want, out, buf)
	}
}

func Test_ByteReader_ReadByte(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect []byte // expected bytes to read, in order
		errAt  int    // index at which error is expected (-1: never)
	}{
		{
			name:   "single byte",
			input:  []byte{0xAB},
			expect: []byte{0xAB},
			errAt:  1,
		},
		{
			name:   "multiple bytes",
			input:  []byte{1, 2, 3, 4},
			expect: []byte{1, 2, 3, 4},
			errAt:  4,
		},
		{
			name:   "empty input",
			input:  []byte{},
			expect: []byte{},
			errAt:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewByteReader(tt.input)
			for i, want := range tt.expect {
				got, err := r.ReadByte()
				if err != nil {
					t.Fatalf("unexpected error at index %d: %v", i, err)
				}
				if got != want {
					t.Errorf("ReadByte #%d = 0x%02x, want 0x%02x", i, got, want)
				}
			}
			// Attempt to read past end, should error if errAt==len(expect) or on empty
			got, err := r.ReadByte()
			if tt.errAt == len(tt.expect) || (tt.errAt == 0 && len(tt.expect) == 0) {
				if err == nil {
					t.Errorf("expected error on excess ReadByte, got value 0x%02x", got)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func Test_ByteReader_ReadBytes(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		n       int
		expect  []byte
		wantErr bool
	}{
		{
			name:    "read all available bytes",
			input:   []byte{10, 20, 30, 40},
			n:       4,
			expect:  []byte{10, 20, 30, 40},
			wantErr: false,
		},
		{
			name:    "read partial bytes",
			input:   []byte{55, 66},
			n:       1,
			expect:  []byte{55},
			wantErr: false,
		},
		{
			name:    "read zero bytes",
			input:   []byte{0xAB, 0xCD},
			n:       0,
			expect:  []byte{},
			wantErr: false,
		},
		{
			name:    "read past end",
			input:   []byte{1, 2},
			n:       3,
			expect:  nil,
			wantErr: true,
		},
		{
			name:    "empty input, read zero",
			input:   []byte{},
			n:       0,
			expect:  []byte{},
			wantErr: false,
		},
		{
			name:    "empty input, read >0",
			input:   []byte{},
			n:       1,
			expect:  nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewByteReader(tt.input)
			got, err := r.ReadBytes(tt.n)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadBytes(%d): unexpected error state: got err=%v, wantErr=%v", tt.n, err, tt.wantErr)
			}
			if !tt.wantErr && !slices.Equal(got, tt.expect) {
				t.Errorf("ReadBytes(%d): got %v, want %v", tt.n, got, tt.expect)
			}
		})
	}
}

func Test_ByteReader_ReadUint32(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		expect  uint32
		wantErr bool
	}{
		{
			name:    "simple uint32",
			input:   []byte{0x12, 0x34, 0x56, 0x78},
			expect:  0x12345678,
			wantErr: false,
		},
		{
			name:    "all zero",
			input:   []byte{0, 0, 0, 0},
			expect:  0,
			wantErr: false,
		},
		{
			name:    "max uint32",
			input:   []byte{0xFF, 0xFF, 0xFF, 0xFF},
			expect:  0xFFFFFFFF,
			wantErr: false,
		},
		{
			name:    "less than 4 bytes",
			input:   []byte{1, 2, 3},
			expect:  0,
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   []byte{},
			expect:  0,
			wantErr: true,
		},
		{
			name:    "extra data after uint32",
			input:   []byte{0x01, 0x02, 0x03, 0x04, 0xFF},
			expect:  0x01020304,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewByteReader(tt.input)
			got, err := r.ReadUint32()
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadUint32: unexpected error state: got err=%v, wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.expect {
				t.Errorf("ReadUint32: got %08x, want %08x", got, tt.expect)
			}
		})
	}
}

func Test_ByteReader_Remaining(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		readN   int // number of bytes to read before calling Remaining
		want    int
		wantErr bool
	}{
		{
			name:  "empty input",
			input: []byte{},
			readN: 0,
			want:  0,
		},
		{
			name:  "no read, full length",
			input: []byte{1, 2, 3, 4, 5},
			readN: 0,
			want:  5,
		},
		{
			name:  "partial read",
			input: []byte{1, 2, 3, 4, 5},
			readN: 2,
			want:  3,
		},
		{
			name:  "full read",
			input: []byte{1, 2, 3, 4, 5},
			readN: 5,
			want:  0,
		},
		{
			name:  "read past end",
			input: []byte{1, 2},
			readN: 3, // beyond available bytes
			want:  0, // Remaining should never go negative, always min 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewByteReader(tt.input)
			for i := 0; i < tt.readN; i++ {
				_, _ = r.ReadByte() // ignore errors for this test
			}
			got := r.Remaining()
			if got != tt.want {
				t.Errorf("Remaining: got %d, want %d", got, tt.want)
			}
		})
	}
}
