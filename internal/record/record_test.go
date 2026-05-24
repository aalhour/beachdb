package record

import (
	"bytes"
	"errors"
	"testing"

	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// testMagic is a stable 8-byte magic used to exercise the Format type.
const testMagic string = "BEACHTST"

// newTestFormat returns a Format with testMagic and DefaultMaxPayloadSize.
func newTestFormat(t *testing.T) *Format {
	t.Helper()
	f, err := NewFormat(testMagic, DefaultMaxPayloadSize)
	if err != nil {
		t.Fatalf("NewFormat failed: %v", err)
	}
	return f
}

// validMagicBytes returns a copy of testMagic for header fixtures.
func validMagicBytes() []byte {
	out := make([]byte, magicSize)
	copy(out, testMagic)
	return out
}

// buildTestRecordHeader builds a header fixture from explicit field values,
// using the production layout offsets so tests never own the layout.
func buildTestRecordHeader(magic []byte, version byte, recordType byte, length uint32, csum uint32) []byte {
	hdr := make([]byte, HeaderSize)
	copy(hdr[magicOffset:magicOffset+magicSize], magic)
	hdr[versionOffset] = version
	hdr[typeOffset] = recordType
	coding.PutUint32(hdr[lengthOffset:lengthOffset+lengthSize], length)
	coding.PutUint32(hdr[checksumOffset:checksumOffset+checksumSize], csum)
	return hdr
}

// recordMagicSlice returns the magic bytes section of an encoded record.
func recordMagicSlice(rec []byte) []byte {
	return rec[magicOffset : magicOffset+magicSize]
}

// recordLengthSlice returns the length bytes section of an encoded record.
func recordLengthSlice(rec []byte) []byte {
	return rec[lengthOffset : lengthOffset+lengthSize]
}

// recordChecksumSlice returns the checksum bytes section of an encoded record.
func recordChecksumSlice(rec []byte) []byte {
	return rec[checksumOffset : checksumOffset+checksumSize]
}

func TestNewFormat(t *testing.T) {
	t.Run("valid magic returns format", func(t *testing.T) {
		f, err := NewFormat("BEACHWAL", DefaultMaxPayloadSize)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(f.Magic[:]) != "BEACHWAL" {
			t.Errorf("magic = %q, want %q", f.Magic[:], "BEACHWAL")
		}
		if f.MaxPayloadSize != DefaultMaxPayloadSize {
			t.Errorf("MaxPayloadSize = %d, want %d", f.MaxPayloadSize, DefaultMaxPayloadSize)
		}
	})

	t.Run("zero max payload size falls back to default", func(t *testing.T) {
		f, err := NewFormat("BEACHWAL", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.MaxPayloadSize != DefaultMaxPayloadSize {
			t.Errorf("MaxPayloadSize = %d, want %d", f.MaxPayloadSize, DefaultMaxPayloadSize)
		}
	})

	t.Run("short magic returns error", func(t *testing.T) {
		_, err := NewFormat("SHORT", DefaultMaxPayloadSize)
		if err == nil {
			t.Error("expected error for short magic")
		}
	})

	t.Run("long magic returns error", func(t *testing.T) {
		_, err := NewFormat("TOO_LONG_MAGIC", DefaultMaxPayloadSize)
		if err == nil {
			t.Error("expected error for long magic")
		}
	})
}

func TestFormat_Encode(t *testing.T) {
	f := newTestFormat(t)

	tests := []struct {
		name       string
		payload    []byte
		wantLength int
	}{
		{
			name:       "nil payload produces header-only record",
			payload:    nil,
			wantLength: HeaderSize,
		},
		{
			name:       "empty payload produces header-only record",
			payload:    []byte{},
			wantLength: HeaderSize,
		},
		{
			name:       "single byte payload produces header + 1 byte",
			payload:    []byte{0x01},
			wantLength: HeaderSize + 1,
		},
		{
			name: "real payload with operations produce correct checksum and length",
			payload: []byte{
				1,       // version
				0, 0, 0, // reserved (3 bytes)
				0, 0, 0, 1, // op_count = 1
				// ---
				1,          // op_type = Put
				0, 0, 0, 4, // key_len = 4
				'n', 'a', 'm', 'e', // key = "name"
				0, 0, 0, 10, // value_len = 10
				'j', 'o', 'h', 'n', ' ', 's', 'm', 'i', 't', 'h', // value = "john smith",
			},
			wantLength: HeaderSize + 31,
		},
		{
			name:       "large payload",
			payload:    make([]byte, 10000),
			wantLength: HeaderSize + 10000,
		},
		{
			name:       "binary data with null bytes",
			payload:    []byte{0x00, 0xFF, 0x00, 0x42, 0x00},
			wantLength: HeaderSize + 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := f.Encode(tt.payload)
			if err != nil {
				t.Fatalf("Encode failed: %v", err)
			}

			if len(got) != tt.wantLength {
				t.Errorf("length = %d, want %d", len(got), tt.wantLength)
			}

			if string(recordMagicSlice(got)) != testMagic {
				t.Errorf("magic = %q, want %q", recordMagicSlice(got), testMagic)
			}

			if got[versionOffset] != Version {
				t.Errorf("version = %d, want %d", got[versionOffset], Version)
			}

			if Type(got[typeOffset]) != TypeFull {
				t.Errorf("recordType = %d, want %d", got[typeOffset], TypeFull)
			}

			gotPayloadLen := coding.Uint32(recordLengthSlice(got))
			//nolint:gosec // G115: len(tt.payload) is bounded by test data, overflow not possible
			if gotPayloadLen != uint32(len(tt.payload)) {
				t.Errorf("payloadLen = %d, want %d", gotPayloadLen, len(tt.payload))
			}

			wantCRC := checksum.CRC32C(tt.payload)
			gotCRC := coding.Uint32(recordChecksumSlice(got))
			if gotCRC != wantCRC {
				t.Errorf("checksum = 0x%X, want 0x%X", gotCRC, wantCRC)
			}

			if !bytes.Equal(got[HeaderSize:], tt.payload) {
				t.Errorf("payload not preserved correctly")
			}
		})
	}
}

func TestFormat_Encode_RejectsOversizedPayload(t *testing.T) {
	f, err := NewFormat(testMagic, 8) // tiny cap for the test
	if err != nil {
		t.Fatalf("NewFormat failed: %v", err)
	}

	_, err = f.Encode(make([]byte, 9))
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Errorf("expected ErrRecordTooLarge, got %v", err)
	}
}

func TestFormat_Encode_Properties(t *testing.T) {
	f := newTestFormat(t)

	t.Run("checksum uniqueness", func(t *testing.T) {
		payload1 := []byte{0x01, 0x02, 0x03}
		payload2 := []byte{0x01, 0x02, 0x04}

		record1, _ := f.Encode(payload1)
		record2, _ := f.Encode(payload2)

		checksum1 := coding.Uint32(recordChecksumSlice(record1))
		checksum2 := coding.Uint32(recordChecksumSlice(record2))

		if checksum1 == checksum2 {
			t.Error("different payloads should produce different checksums")
		}
	})

	t.Run("payload immutability", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		payloadCopy := make([]byte, len(payload))
		copy(payloadCopy, payload)

		_, _ = f.Encode(payload)

		if !bytes.Equal(payload, payloadCopy) {
			t.Error("Encode should not modify the input payload")
		}
	})

	t.Run("deterministic encoding", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}

		record1, _ := f.Encode(payload)
		record2, _ := f.Encode(payload)

		if !bytes.Equal(record1, record2) {
			t.Error("encoding the same payload twice should produce identical results")
		}
	})
}

func TestFormat_DecodeHeader(t *testing.T) {
	f := newTestFormat(t)

	tests := []struct {
		name        string
		header      []byte
		wantHeader  Header
		wantErr     error
		skipPayload bool
	}{
		{
			name:       "valid header with zero-length payload",
			header:     buildTestRecordHeader(validMagicBytes(), Version, byte(TypeFull), 0, 0),
			wantHeader: Header{Type: TypeFull, Length: 0, Checksum: 0},
		},
		{
			name:       "valid header with non-zero payload length and checksum",
			header:     buildTestRecordHeader(validMagicBytes(), Version, byte(TypeFull), 256, 0xDEADBEEF),
			wantHeader: Header{Type: TypeFull, Length: 256, Checksum: 0xDEADBEEF},
		},
		{
			name:       "valid header with max supported payload length",
			header:     buildTestRecordHeader(validMagicBytes(), Version, byte(TypeFull), DefaultMaxPayloadSize, 0x12345678),
			wantHeader: Header{Type: TypeFull, Length: DefaultMaxPayloadSize, Checksum: 0x12345678},
		},
		{
			name:    "oversized payload length is rejected",
			header:  buildTestRecordHeader(validMagicBytes(), Version, byte(TypeFull), DefaultMaxPayloadSize+1, 0x12345678),
			wantErr: ErrRecordTooLarge,
		},
		{
			name:    "truncated header - empty",
			header:  []byte{},
			wantErr: ErrTruncated,
		},
		{
			name:    "truncated header - one byte short",
			header:  make([]byte, HeaderSize-1),
			wantErr: ErrTruncated,
		},
		{
			name: "bad magic - first byte wrong",
			header: func() []byte {
				m := validMagicBytes()
				m[0] = 0x00
				return buildTestRecordHeader(m, Version, byte(TypeFull), 0, 0)
			}(),
			wantErr: ErrBadMagic,
		},
		{
			name: "bad magic - last byte wrong",
			header: func() []byte {
				m := validMagicBytes()
				m[magicSize-1] = 0x00
				return buildTestRecordHeader(m, Version, byte(TypeFull), 0, 0)
			}(),
			wantErr: ErrBadMagic,
		},
		{
			name:    "bad magic - all zeros",
			header:  buildTestRecordHeader(make([]byte, magicSize), Version, byte(TypeFull), 0, 0),
			wantErr: ErrBadMagic,
		},
		{
			name:    "unsupported version - zero",
			header:  buildTestRecordHeader(validMagicBytes(), 0x00, byte(TypeFull), 0, 0),
			wantErr: ErrUnsupportedVersion,
		},
		{
			name:    "unsupported version - future version",
			header:  buildTestRecordHeader(validMagicBytes(), 0x02, byte(TypeFull), 0, 0),
			wantErr: ErrUnsupportedVersion,
		},
		{
			name:    "unsupported record type - zero",
			header:  buildTestRecordHeader(validMagicBytes(), Version, 0x00, 0, 0),
			wantErr: ErrUnsupportedRecordType,
		},
		{
			name:    "unsupported record type - unknown type",
			header:  buildTestRecordHeader(validMagicBytes(), Version, 0xFF, 0, 0),
			wantErr: ErrUnsupportedRecordType,
		},
		{
			name: "header with extra trailing bytes returns ErrBadHeader",
			header: append(
				buildTestRecordHeader(validMagicBytes(), Version, byte(TypeFull), 10, 0xABCDEF12),
				0xFF, 0xFF, 0xFF,
			),
			wantErr: ErrBadHeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := f.DecodeHeader(tt.header)

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if got != tt.wantHeader {
				t.Errorf("header = %+v, want %+v", got, tt.wantHeader)
			}
		})
	}
}

func TestFormat_EncodeDecode_RoundTrip(t *testing.T) {
	f := newTestFormat(t)

	payloads := [][]byte{
		nil,
		{},
		{0x00},
		{0x01, 0x02, 0x03},
		[]byte("hello world"),
		make([]byte, 1000),
	}

	for _, payload := range payloads {
		encoded, err := f.Encode(payload)
		if err != nil {
			t.Errorf("Encode failed for payload len %d: %v", len(payload), err)
			continue
		}

		hdr, err := f.DecodeHeader(encoded[:HeaderSize])
		if err != nil {
			t.Errorf("DecodeHeader failed for payload len %d: %v", len(payload), err)
			continue
		}

		//nolint:gosec // G115: len(payload) is bounded by test data, overflow not possible
		if hdr.Length != uint32(len(payload)) {
			t.Errorf("payload length mismatch: got %d, want %d", hdr.Length, len(payload))
		}

		expectedCRC := checksum.CRC32C(payload)
		if hdr.Checksum != expectedCRC {
			t.Errorf("checksum mismatch: got 0x%X, want 0x%X", hdr.Checksum, expectedCRC)
		}

		if !bytes.Equal(encoded[HeaderSize:], payload) {
			t.Errorf("payload bytes mismatch for len %d", len(payload))
		}
	}
}

func TestFormat_CorruptionDetection(t *testing.T) {
	f := newTestFormat(t)

	t.Run("corrupted payload detected by checksum", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		record, _ := f.Encode(payload)

		record[HeaderSize] ^= 0xFF

		storedChecksum := coding.Uint32(recordChecksumSlice(record))
		actualChecksum := checksum.CRC32C(record[HeaderSize:])

		if storedChecksum == actualChecksum {
			t.Error("corruption should be detectable via checksum mismatch")
		}
	})

	t.Run("corrupted length field", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record, _ := f.Encode(payload)

		// Corrupt the LSB of the length field so the new value still decodes
		// as a valid-sized length (otherwise the decoder rejects with ErrRecordTooLarge).
		record[lengthOffset+lengthSize-1] ^= 0xFF

		hdr, err := f.DecodeHeader(record[:HeaderSize])
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		//nolint:gosec // G115: len(payload) is bounded by test data, overflow not possible
		if hdr.Length == uint32(len(payload)) {
			t.Error("corrupted length field should not match original")
		}
	})

	t.Run("corrupted magic detected", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record, _ := f.Encode(payload)

		record[magicOffset] ^= 0xFF

		_, err := f.DecodeHeader(record[:HeaderSize])
		if !errors.Is(err, ErrBadMagic) {
			t.Errorf("expected ErrBadMagic, got %v", err)
		}
	})

	t.Run("corrupted version detected", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record, _ := f.Encode(payload)

		record[versionOffset] = 0x99

		_, err := f.DecodeHeader(record[:HeaderSize])
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("expected ErrUnsupportedVersion, got %v", err)
		}
	})
}

func TestValidatePayload(t *testing.T) {
	tests := []struct {
		name             string
		payload          []byte
		expectedChecksum uint32
		wantErr          error
	}{
		{
			name:             "correct checksum returns no error",
			payload:          []byte{0x01, 0x02, 0x03, 0x04, 0x05},
			expectedChecksum: checksum.CRC32C([]byte{0x01, 0x02, 0x03, 0x04, 0x05}),
			wantErr:          nil,
		},
		{
			name: "corrupted payload returns checksum error",
			payload: func() []byte {
				p := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
				p[1] ^= 0x02
				return p
			}(),
			expectedChecksum: checksum.CRC32C([]byte{0x01, 0x02, 0x03, 0x04, 0x05}),
			wantErr:          ErrChecksum,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotErr := ValidatePayload(test.payload, test.expectedChecksum)
			if !errors.Is(gotErr, test.wantErr) {
				t.Errorf("expected %v error but got %v instead", test.wantErr, gotErr)
			}
		})
	}
}

func TestValidatePayload_EmptyPayloadChecksum(t *testing.T) {
	emptyPayload := []byte{}
	csum := checksum.CRC32C(emptyPayload)

	if err := ValidatePayload(emptyPayload, csum); err != nil {
		t.Fatalf("expected empty payload checksum to validate: %v", err)
	}
	if err := ValidatePayload(emptyPayload, csum+1); !errors.Is(err, ErrChecksum) {
		t.Fatalf("expected checksum mismatch for empty payload, got %v", err)
	}
}

// Benchmarks

func BenchmarkFormat_Encode(b *testing.B) {
	f, _ := NewFormat(testMagic, DefaultMaxPayloadSize)
	sizes := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"small", 64},
		{"medium", 1024},
		{"large", 64 * 1024},
	}

	for _, s := range sizes {
		payload := make([]byte, s.size)
		b.Run(s.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = f.Encode(payload)
			}
		})
	}
}

func BenchmarkFormat_DecodeHeader(b *testing.B) {
	f, _ := NewFormat(testMagic, DefaultMaxPayloadSize)
	payload := make([]byte, 1024)
	rec, _ := f.Encode(payload)
	header := rec[:HeaderSize]

	b.ReportAllocs()
	for b.Loop() {
		_, _ = f.DecodeHeader(header)
	}
}

// Fuzz tests

func FuzzFormat_Encode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x01, 0x02, 0x03})
	f.Add([]byte("hello world"))

	format, _ := NewFormat(testMagic, DefaultMaxPayloadSize)

	f.Fuzz(func(t *testing.T, payload []byte) {
		rec, err := format.Encode(payload)
		if err != nil {
			return
		}

		if len(rec) != HeaderSize+len(payload) {
			t.Errorf("unexpected record length: got %d, want %d", len(rec), HeaderSize+len(payload))
		}

		hdr, err := format.DecodeHeader(rec[:HeaderSize])
		if err != nil {
			t.Errorf("DecodeHeader failed: %v", err)
		}

		//nolint:gosec // G115: len(payload) is bounded by fuzz input, overflow not possible
		if hdr.Length != uint32(len(payload)) {
			t.Errorf("payload length mismatch: got %d, want %d", hdr.Length, len(payload))
		}

		expectedCRC := checksum.CRC32C(payload)
		if hdr.Checksum != expectedCRC {
			t.Errorf("checksum mismatch: got 0x%X, want 0x%X", hdr.Checksum, expectedCRC)
		}

		if !bytes.Equal(rec[HeaderSize:], payload) {
			t.Error("payload not preserved")
		}
	})
}

func FuzzFormat_DecodeHeader(f *testing.F) {
	format, _ := NewFormat(testMagic, DefaultMaxPayloadSize)
	validRec, _ := format.Encode([]byte{0x01, 0x02, 0x03})
	f.Add(validRec[:HeaderSize])
	f.Add([]byte{})
	f.Add(buildTestRecordHeader(validMagicBytes(), Version, byte(TypeFull), 0, 0))

	f.Fuzz(func(_ *testing.T, header []byte) {
		_, _ = format.DecodeHeader(header)
	})
}
