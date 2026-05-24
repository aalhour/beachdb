package wal

import (
	"bytes"
	"errors"
	"testing"

	"github.com/aalhour/beachdb/internal/record"
	"github.com/aalhour/beachdb/internal/util/coding"
)

// Test-only mirrors of the layout constants owned by the record package.
// Tests need these to construct deliberately malformed headers; production
// code uses record.HeaderSize and the offsets internal to internal/record.
const (
	walMagicSize     = 8
	walVersion       = record.Version
	RecordTypeFull   = byte(record.TypeFull) //nolint:revive // test-only mirror name
	recordHeaderSize = record.HeaderSize
	//nolint:gosec // G115: DefaultMaxPayloadSize fits in uint32 by definition.
	maxRecordPayloadSize = record.DefaultMaxPayloadSize

	walMagicOffset    = 0
	walVersionOffset  = walMagicOffset + walMagicSize
	walTypeOffset     = walVersionOffset + 1
	walLengthOffset   = walTypeOffset + 1
	walLengthSize     = 4
	walChecksumOffset = walLengthOffset + walLengthSize
	walChecksumSize   = 4
)

// validMagicBytes returns a copy of the WAL magic for use in header fixtures.
func validMagicBytes() []byte {
	out := make([]byte, walMagicSize)
	copy(out, walMagic)
	return out
}

// buildTestRecordHeader builds an 18-byte WAL header from explicit field values.
// Used to construct malformed headers that exercise reader error paths.
func buildTestRecordHeader(magic []byte, version byte, recordType byte, length uint32, csum uint32) []byte {
	hdr := make([]byte, recordHeaderSize)
	copy(hdr[walMagicOffset:walMagicOffset+walMagicSize], magic)
	hdr[walVersionOffset] = version
	hdr[walTypeOffset] = recordType
	coding.PutUint32(hdr[walLengthOffset:walLengthOffset+walLengthSize], length)
	coding.PutUint32(hdr[walChecksumOffset:walChecksumOffset+walChecksumSize], csum)
	return hdr
}

func TestEncodeRecord_UsesWALMagic(t *testing.T) {
	rec, err := EncodeRecord([]byte("hello"))
	if err != nil {
		t.Fatalf("EncodeRecord failed: %v", err)
	}
	if string(rec[:walMagicSize]) != walMagic {
		t.Errorf("magic = %q, want %q", rec[:walMagicSize], walMagic)
	}
}

func TestEncodeRecord_TooLarge(t *testing.T) {
	// Construct a payload one byte larger than the WAL format's cap.
	payload := make([]byte, int(maxRecordPayloadSize)+1)
	_, err := EncodeRecord(payload)
	if !errors.Is(err, record.ErrRecordTooLarge) {
		t.Errorf("expected ErrRecordTooLarge, got %v", err)
	}
}

func TestDecodeRecordHeader_RoundTrip(t *testing.T) {
	payload := []byte("round-trip")
	rec, err := EncodeRecord(payload)
	if err != nil {
		t.Fatalf("EncodeRecord failed: %v", err)
	}

	payloadLen, _, err := DecodeRecordHeader(rec[:recordHeaderSize])
	if err != nil {
		t.Fatalf("DecodeRecordHeader failed: %v", err)
	}
	//nolint:gosec // G115: len(payload) is bounded by test data, overflow not possible
	if payloadLen != uint32(len(payload)) {
		t.Errorf("payloadLen = %d, want %d", payloadLen, len(payload))
	}

	if !bytes.Equal(rec[recordHeaderSize:], payload) {
		t.Error("payload bytes not preserved")
	}
}

func TestValidateRecord(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03}
	rec, err := EncodeRecord(payload)
	if err != nil {
		t.Fatalf("EncodeRecord failed: %v", err)
	}

	_, crc, err := DecodeRecordHeader(rec[:recordHeaderSize])
	if err != nil {
		t.Fatalf("DecodeRecordHeader failed: %v", err)
	}

	if err := ValidateRecord(payload, crc); err != nil {
		t.Errorf("ValidateRecord on intact payload failed: %v", err)
	}

	corrupted := append([]byte(nil), payload...)
	corrupted[0] ^= 0xFF
	if err := ValidateRecord(corrupted, crc); !errors.Is(err, record.ErrChecksum) {
		t.Errorf("expected ErrChecksum on corrupted payload, got %v", err)
	}
}
