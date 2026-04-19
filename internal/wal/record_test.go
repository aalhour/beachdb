package wal

import (
	"bytes"
	"errors"
	"testing"

	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

func TestEncodeRecord(t *testing.T) {
	tests := []struct {
		name       string
		payload    []byte
		wantLength int
	}{
		{
			name:       "nil payload produces 12 bytes",
			payload:    nil,
			wantLength: 12,
		},
		{
			name:       "empty payload produces 12 bytes",
			payload:    []byte{},
			wantLength: 12,
		},
		{
			name:       "single byte payload produces 13 bytes",
			payload:    []byte{0x01},
			wantLength: 13,
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
			wantLength: 43,
		},
		{
			name:       "large payload",
			payload:    make([]byte, 10000),
			wantLength: 10012,
		},
		{
			name:       "binary data with null bytes",
			payload:    []byte{0x00, 0xFF, 0x00, 0x42, 0x00},
			wantLength: 17,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeRecord(tt.payload)

			if len(got) != tt.wantLength {
				t.Errorf("length = %d, want %d", len(got), tt.wantLength)
			}

			// Check magic using constant
			gotMagic := coding.Uint16(got[0:2])
			if gotMagic != walMagic {
				t.Errorf("magic = 0x%X, want 0x%X", gotMagic, walMagic)
			}

			// Check version
			if got[2] != walVersion {
				t.Errorf("version = %d, want %d", got[2], walVersion)
			}

			// Check record type
			if got[3] != RecordTypeFull {
				t.Errorf("recordType = %d, want %d", got[3], RecordTypeFull)
			}

			// Check payload length field
			gotPayloadLen := coding.Uint32(got[4:8])
			//nolint:gosec // G115: len(tt.payload) is bounded by test data, overflow not possible
			if gotPayloadLen != uint32(len(tt.payload)) {
				t.Errorf("payloadLen = %d, want %d", gotPayloadLen, len(tt.payload))
			}

			// Check checksum
			wantCRC := checksum.CRC32C(tt.payload)
			gotCRC := coding.Uint32(got[8:12])
			if gotCRC != wantCRC {
				t.Errorf("checksum = 0x%X, want 0x%X", gotCRC, wantCRC)
			}

			// Check payload is preserved correctly
			if !bytes.Equal(got[12:], tt.payload) {
				t.Errorf("payload not preserved correctly")
			}
		})
	}
}

func TestEncodeRecordProperties(t *testing.T) {
	t.Run("checksum uniqueness", func(t *testing.T) {
		payload1 := []byte{0x01, 0x02, 0x03}
		payload2 := []byte{0x01, 0x02, 0x04}

		record1 := EncodeRecord(payload1)
		record2 := EncodeRecord(payload2)

		checksum1 := coding.Uint32(record1[8:12])
		checksum2 := coding.Uint32(record2[8:12])

		if checksum1 == checksum2 {
			t.Error("different payloads should produce different checksums")
		}
	})

	t.Run("payload immutability", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		payloadCopy := make([]byte, len(payload))
		copy(payloadCopy, payload)

		_ = EncodeRecord(payload)

		if !bytes.Equal(payload, payloadCopy) {
			t.Error("EncodeRecord should not modify the input payload")
		}
	})

	t.Run("deterministic encoding", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}

		record1 := EncodeRecord(payload)
		record2 := EncodeRecord(payload)

		if !bytes.Equal(record1, record2) {
			t.Error("encoding the same payload twice should produce identical results")
		}
	})
}

func TestDecodeRecordHeader(t *testing.T) {
	tests := []struct {
		name           string
		header         []byte
		wantPayloadLen uint32
		wantChecksum   uint32
		wantErr        error
	}{
		{
			name: "valid header with zero-length payload",
			header: []byte{
				0xBE, 0xAC, // magic
				0x01,       // version
				0x01,       // record type (Full)
				0, 0, 0, 0, // payload length = 0
				0, 0, 0, 0, // checksum = 0
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        nil,
		},
		{
			name: "valid header with non-zero payload length and checksum",
			header: []byte{
				0xBE, 0xAC, // magic
				0x01,             // version
				0x01,             // record type (Full)
				0, 0, 0x01, 0x00, // payload length = 256 (big endian)
				0xDE, 0xAD, 0xBE, 0xEF, // checksum
			},
			wantPayloadLen: 256,
			wantChecksum:   0xDEADBEEF,
			wantErr:        nil,
		},
		{
			name: "valid header with max supported payload length",
			header: []byte{
				0xBE, 0xAC, // magic
				0x01,                   // version
				0x01,                   // record type (Full)
				0x04, 0x00, 0x00, 0x00, // payload length = 64 MiB
				0x12, 0x34, 0x56, 0x78, // checksum
			},
			wantPayloadLen: maxRecordPayloadSize,
			wantChecksum:   0x12345678,
			wantErr:        nil,
		},
		{
			name: "oversized payload length is rejected",
			header: []byte{
				0xBE, 0xAC,
				0x01,
				0x01,
				0x04, 0x00, 0x00, 0x01,
				0x12, 0x34, 0x56, 0x78,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrRecordTooLarge,
		},
		{
			name:           "truncated header - empty",
			header:         []byte{},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrTruncated,
		},
		{
			name:           "truncated header - 11 bytes",
			header:         []byte{0xBE, 0xAC, 0x01, 0x01, 0, 0, 0, 5, 0, 0, 0},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrTruncated,
		},
		{
			name: "bad magic - first byte wrong",
			header: []byte{
				0x00, 0xAC, // bad magic
				0x01, 0x01,
				0, 0, 0, 0,
				0, 0, 0, 0,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrBadMagic,
		},
		{
			name: "bad magic - second byte wrong",
			header: []byte{
				0xBE, 0x00, // bad magic
				0x01, 0x01,
				0, 0, 0, 0,
				0, 0, 0, 0,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrBadMagic,
		},
		{
			name: "bad magic - both bytes wrong",
			header: []byte{
				0xCA, 0xFE, // bad magic
				0x01, 0x01,
				0, 0, 0, 0,
				0, 0, 0, 0,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrBadMagic,
		},
		{
			name: "unsupported version - zero",
			header: []byte{
				0xBE, 0xAC,
				0x00, // version 0
				0x01,
				0, 0, 0, 0,
				0, 0, 0, 0,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedVersion,
		},
		{
			name: "unsupported version - future version",
			header: []byte{
				0xBE, 0xAC,
				0x02, // version 2
				0x01,
				0, 0, 0, 0,
				0, 0, 0, 0,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedVersion,
		},
		{
			name: "unsupported record type - zero",
			header: []byte{
				0xBE, 0xAC,
				0x01,
				0x00, // record type 0
				0, 0, 0, 0,
				0, 0, 0, 0,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedRecordType,
		},
		{
			name: "unsupported record type - unknown type",
			header: []byte{
				0xBE, 0xAC,
				0x01,
				0xFF, // unknown record type
				0, 0, 0, 0,
				0, 0, 0, 0,
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedRecordType,
		},
		{
			name: "header with extra trailing bytes returns ErrBadHeader",
			header: []byte{
				0xBE, 0xAC,
				0x01, 0x01,
				0, 0, 0, 10,
				0xAB, 0xCD, 0xEF, 0x12,
				0xFF, 0xFF, 0xFF, // extra bytes (invalid - header must be exactly 12 bytes)
			},
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrBadHeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payloadLen, crc, err := DecodeRecordHeader(tt.header)

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			if payloadLen != tt.wantPayloadLen {
				t.Errorf("payloadLen = %d, want %d", payloadLen, tt.wantPayloadLen)
			}

			if crc != tt.wantChecksum {
				t.Errorf("checksum = 0x%X, want 0x%X", crc, tt.wantChecksum)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	payloads := [][]byte{
		nil,
		{},
		{0x00},
		{0x01, 0x02, 0x03},
		[]byte("hello world"),
		make([]byte, 1000),
	}

	for _, payload := range payloads {
		encoded := EncodeRecord(payload)
		payloadLen, crc, err := DecodeRecordHeader(encoded[:recordHeaderSize])
		if err != nil {
			t.Errorf("DecodeRecordHeader failed for payload len %d: %v", len(payload), err)
			continue
		}

		//nolint:gosec // G115: len(payload) is bounded by test data, overflow not possible
		if payloadLen != uint32(len(payload)) {
			t.Errorf("payload length mismatch: got %d, want %d", payloadLen, len(payload))
		}

		expectedCRC := checksum.CRC32C(payload)
		if crc != expectedCRC {
			t.Errorf("checksum mismatch: got 0x%X, want 0x%X", crc, expectedCRC)
		}

		// Verify payload bytes match
		if !bytes.Equal(encoded[recordHeaderSize:], payload) {
			t.Errorf("payload bytes mismatch for len %d", len(payload))
		}
	}
}

func TestCorruptionDetection(t *testing.T) {
	t.Run("corrupted payload detected by checksum", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		record := EncodeRecord(payload)

		// Corrupt a payload byte
		record[12] ^= 0xFF

		// Extract stored checksum and compute actual
		storedChecksum := coding.Uint32(record[8:12])
		actualChecksum := checksum.CRC32C(record[12:])

		if storedChecksum == actualChecksum {
			t.Error("corruption should be detectable via checksum mismatch")
		}
	})

	t.Run("corrupted length field", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record := EncodeRecord(payload)

		// Corrupt the length field
		record[7] ^= 0xFF

		payloadLen, _, err := DecodeRecordHeader(record[:recordHeaderSize])
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Length should no longer match actual payload
		//nolint:gosec // G115: len(payload) is bounded by test data, overflow not possible
		if payloadLen == uint32(len(payload)) {
			t.Error("corrupted length field should not match original")
		}
	})

	t.Run("corrupted magic detected", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record := EncodeRecord(payload)

		// Corrupt the magic
		record[0] ^= 0xFF

		_, _, err := DecodeRecordHeader(record[:recordHeaderSize])
		if !errors.Is(err, ErrBadMagic) {
			t.Errorf("expected ErrBadMagic, got %v", err)
		}
	})

	t.Run("corrupted version detected", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record := EncodeRecord(payload)

		// Corrupt the version
		record[2] = 0x99

		_, _, err := DecodeRecordHeader(record[:recordHeaderSize])
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Errorf("expected ErrUnsupportedVersion, got %v", err)
		}
	})
}

func TestValidateRecord(t *testing.T) {
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
				p[1] ^= 0x02 // Flip bit 1 of second byte (0x02 → 0x00)
				return p
			}(),
			expectedChecksum: checksum.CRC32C([]byte{0x01, 0x02, 0x03, 0x04, 0x05}),
			wantErr:          ErrChecksum,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotErr := ValidateRecord(test.payload, test.expectedChecksum)
			if !errors.Is(gotErr, test.wantErr) {
				t.Errorf("expected %v error but got %v instead", test.wantErr, gotErr)
			}
		})
	}
}

func TestValidateRecord_EmptyPayloadChecksum(t *testing.T) {
	emptyPayload := []byte{}
	csum := checksum.CRC32C(emptyPayload)

	if err := ValidateRecord(emptyPayload, csum); err != nil {
		t.Fatalf("expected empty payload checksum to validate: %v", err)
	}
	if err := ValidateRecord(emptyPayload, csum+1); !errors.Is(err, ErrChecksum) {
		t.Fatalf("expected checksum mismatch for empty payload, got %v", err)
	}
}

// Benchmarks

func BenchmarkEncodeRecord(b *testing.B) {
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
				EncodeRecord(payload)
			}
		})
	}
}

func BenchmarkDecodeRecordHeader(b *testing.B) {
	payload := make([]byte, 1024)
	record := EncodeRecord(payload)
	header := record[:recordHeaderSize]

	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = DecodeRecordHeader(header)
	}
}

// Fuzz tests

func FuzzEncodeRecord(f *testing.F) {
	// Seed corpus
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x01, 0x02, 0x03})
	f.Add([]byte("hello world"))

	f.Fuzz(func(t *testing.T, payload []byte) {
		record := EncodeRecord(payload)

		// Verify length
		if len(record) != recordHeaderSize+len(payload) {
			t.Errorf("unexpected record length: got %d, want %d", len(record), recordHeaderSize+len(payload))
		}

		// Verify round-trip via header decode
		payloadLen, crc, err := DecodeRecordHeader(record[:recordHeaderSize])
		if err != nil {
			t.Errorf("DecodeRecordHeader failed: %v", err)
		}

		//nolint:gosec // G115: len(payload) is bounded by fuzz input, overflow not possible
		if payloadLen != uint32(len(payload)) {
			t.Errorf("payload length mismatch: got %d, want %d", payloadLen, len(payload))
		}

		expectedCRC := checksum.CRC32C(payload)
		if crc != expectedCRC {
			t.Errorf("checksum mismatch: got 0x%X, want 0x%X", crc, expectedCRC)
		}

		// Verify payload preserved
		if !bytes.Equal(record[recordHeaderSize:], payload) {
			t.Error("payload not preserved")
		}
	})
}

func FuzzDecodeRecordHeader(f *testing.F) {
	// Seed with valid headers
	validHeader := EncodeRecord([]byte{0x01, 0x02, 0x03})[:recordHeaderSize]
	f.Add(validHeader)
	f.Add([]byte{})
	f.Add([]byte{0xBE, 0xAC, 0x01, 0x01, 0, 0, 0, 0, 0, 0, 0, 0})

	f.Fuzz(func(_ *testing.T, header []byte) {
		// Just ensure it doesn't panic
		_, _, _ = DecodeRecordHeader(header)
	})
}

// ExampleEncodeRecord demonstrates encoding a WAL record.
func ExampleEncodeRecord() {
	payload := []byte("hello, world")
	record := EncodeRecord(payload)
	_ = record
	// Output:
}

// ExampleDecodeRecordHeader demonstrates decoding a WAL record header.
func ExampleDecodeRecordHeader() {
	// Create a valid header
	header := make([]byte, recordHeaderSize)
	coding.PutUint16(header[0:], walMagic)
	header[2] = walVersion
	header[3] = RecordTypeFull
	coding.PutUint32(header[4:], 5)          // payload length = 5
	coding.PutUint32(header[8:], 0x12345678) // checksum

	payloadLen, checksum, err := DecodeRecordHeader(header)
	if err != nil {
		return
	}
	_, _ = payloadLen, checksum
	// Output:
}
