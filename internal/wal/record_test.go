package wal

import (
	"bytes"
	"errors"
	"testing"

	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// validMagicBytes returns a copy of the WAL magic for use in header fixtures.
func validMagicBytes() []byte {
	out := make([]byte, walMagicSize)
	copy(out, walMagic)
	return out
}

// buildTestRecordHeader builds a header fixture from explicit field values,
// using the production layout offsets so the test never owns the layout.
func buildTestRecordHeader(magic []byte, version byte, recordType byte, length uint32, csum uint32) []byte {
	hdr := make([]byte, recordHeaderSize)
	copy(hdr[walMagicOffset:walMagicOffset+walMagicSize], magic)
	hdr[walVersionOffset] = version
	hdr[walTypeOffset] = recordType
	coding.PutUint32(hdr[walLengthOffset:walLengthOffset+walLengthSize], length)
	coding.PutUint32(hdr[walChecksumOffset:walChecksumOffset+walChecksumSize], csum)
	return hdr
}

// recordMagicSlice returns the magic bytes section of an encoded record.
func recordMagicSlice(rec []byte) []byte {
	return rec[walMagicOffset : walMagicOffset+walMagicSize]
}

// recordLengthSlice returns the length bytes section of an encoded record.
func recordLengthSlice(rec []byte) []byte {
	return rec[walLengthOffset : walLengthOffset+walLengthSize]
}

// recordChecksumSlice returns the checksum bytes section of an encoded record.
func recordChecksumSlice(rec []byte) []byte {
	return rec[walChecksumOffset : walChecksumOffset+walChecksumSize]
}

func TestEncodeRecord(t *testing.T) {
	tests := []struct {
		name       string
		payload    []byte
		wantLength int
	}{
		{
			name:       "nil payload produces header-only record",
			payload:    nil,
			wantLength: recordHeaderSize,
		},
		{
			name:       "empty payload produces header-only record",
			payload:    []byte{},
			wantLength: recordHeaderSize,
		},
		{
			name:       "single byte payload produces header + 1 byte",
			payload:    []byte{0x01},
			wantLength: recordHeaderSize + 1,
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
			wantLength: recordHeaderSize + 31,
		},
		{
			name:       "large payload",
			payload:    make([]byte, 10000),
			wantLength: recordHeaderSize + 10000,
		},
		{
			name:       "binary data with null bytes",
			payload:    []byte{0x00, 0xFF, 0x00, 0x42, 0x00},
			wantLength: recordHeaderSize + 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeRecord(tt.payload)

			if len(got) != tt.wantLength {
				t.Errorf("length = %d, want %d", len(got), tt.wantLength)
			}

			// Check magic
			if string(recordMagicSlice(got)) != walMagic {
				t.Errorf("magic = %q, want %q", recordMagicSlice(got), walMagic)
			}

			// Check version
			if got[walVersionOffset] != walVersion {
				t.Errorf("version = %d, want %d", got[walVersionOffset], walVersion)
			}

			// Check record type
			if got[walTypeOffset] != RecordTypeFull {
				t.Errorf("recordType = %d, want %d", got[walTypeOffset], RecordTypeFull)
			}

			// Check payload length field
			gotPayloadLen := coding.Uint32(recordLengthSlice(got))
			//nolint:gosec // G115: len(tt.payload) is bounded by test data, overflow not possible
			if gotPayloadLen != uint32(len(tt.payload)) {
				t.Errorf("payloadLen = %d, want %d", gotPayloadLen, len(tt.payload))
			}

			// Check checksum
			wantCRC := checksum.CRC32C(tt.payload)
			gotCRC := coding.Uint32(recordChecksumSlice(got))
			if gotCRC != wantCRC {
				t.Errorf("checksum = 0x%X, want 0x%X", gotCRC, wantCRC)
			}

			// Check payload is preserved correctly
			if !bytes.Equal(got[recordHeaderSize:], tt.payload) {
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
			name:           "valid header with zero-length payload",
			header:         buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, 0, 0),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        nil,
		},
		{
			name:           "valid header with non-zero payload length and checksum",
			header:         buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, 256, 0xDEADBEEF),
			wantPayloadLen: 256,
			wantChecksum:   0xDEADBEEF,
			wantErr:        nil,
		},
		{
			name: "valid header with max supported payload length",
			header: buildTestRecordHeader(
				validMagicBytes(), walVersion, RecordTypeFull, maxRecordPayloadSize, 0x12345678,
			),
			wantPayloadLen: maxRecordPayloadSize,
			wantChecksum:   0x12345678,
			wantErr:        nil,
		},
		{
			name: "oversized payload length is rejected",
			header: buildTestRecordHeader(
				validMagicBytes(), walVersion, RecordTypeFull, maxRecordPayloadSize+1, 0x12345678,
			),
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
			name:           "truncated header - one byte short",
			header:         make([]byte, recordHeaderSize-1),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrTruncated,
		},
		{
			name: "bad magic - first byte wrong",
			header: func() []byte {
				m := validMagicBytes()
				m[0] = 0x00
				return buildTestRecordHeader(m, walVersion, RecordTypeFull, 0, 0)
			}(),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrBadMagic,
		},
		{
			name: "bad magic - last byte wrong",
			header: func() []byte {
				m := validMagicBytes()
				m[walMagicSize-1] = 0x00
				return buildTestRecordHeader(m, walVersion, RecordTypeFull, 0, 0)
			}(),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrBadMagic,
		},
		{
			name:           "bad magic - all zeros",
			header:         buildTestRecordHeader(make([]byte, walMagicSize), walVersion, RecordTypeFull, 0, 0),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrBadMagic,
		},
		{
			name:           "unsupported version - zero",
			header:         buildTestRecordHeader(validMagicBytes(), 0x00, RecordTypeFull, 0, 0),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedVersion,
		},
		{
			name:           "unsupported version - future version",
			header:         buildTestRecordHeader(validMagicBytes(), 0x02, RecordTypeFull, 0, 0),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedVersion,
		},
		{
			name:           "unsupported record type - zero",
			header:         buildTestRecordHeader(validMagicBytes(), walVersion, 0x00, 0, 0),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedRecordType,
		},
		{
			name:           "unsupported record type - unknown type",
			header:         buildTestRecordHeader(validMagicBytes(), walVersion, 0xFF, 0, 0),
			wantPayloadLen: 0,
			wantChecksum:   0,
			wantErr:        ErrUnsupportedRecordType,
		},
		{
			name: "header with extra trailing bytes returns ErrBadHeader",
			header: append(
				buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, 10, 0xABCDEF12),
				0xFF, 0xFF, 0xFF,
			),
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
		record[recordHeaderSize] ^= 0xFF

		// Extract stored checksum and compute actual
		storedChecksum := coding.Uint32(recordChecksumSlice(record))
		actualChecksum := checksum.CRC32C(record[recordHeaderSize:])

		if storedChecksum == actualChecksum {
			t.Error("corruption should be detectable via checksum mismatch")
		}
	})

	t.Run("corrupted length field", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record := EncodeRecord(payload)

		// Corrupt the LSB of the length field so the new value still decodes
		// as a valid-sized length (otherwise the decoder rejects with ErrRecordTooLarge).
		record[walLengthOffset+walLengthSize-1] ^= 0xFF

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
		record[walMagicOffset] ^= 0xFF

		_, _, err := DecodeRecordHeader(record[:recordHeaderSize])
		if !errors.Is(err, ErrBadMagic) {
			t.Errorf("expected ErrBadMagic, got %v", err)
		}
	})

	t.Run("corrupted version detected", func(t *testing.T) {
		payload := []byte{0x01, 0x02, 0x03}
		record := EncodeRecord(payload)

		// Corrupt the version
		record[walVersionOffset] = 0x99

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
	f.Add(buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, 0, 0))

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
	header := buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, 5, 0x12345678)
	payloadLen, csum, err := DecodeRecordHeader(header)
	if err != nil {
		return
	}
	_, _ = payloadLen, csum
	// Output:
}
