package wal

import (
	"bytes"
	"testing"

	"github.com/aalhour/beachdb/internal/util/checksum"
	"github.com/aalhour/beachdb/internal/util/coding"
)

func TestChecksumUniqueness(t *testing.T) {
	payload1 := []byte{0x01, 0x02, 0x03}
	payload2 := []byte{0x01, 0x02, 0x04}

	record1 := EncodeRecord(payload1)
	record2 := EncodeRecord(payload2)

	checksum1 := coding.Uint32(record1[8:12])
	checksum2 := coding.Uint32(record2[8:12])

	if checksum1 == checksum2 {
		t.Errorf("Different payloads should produce different checksums")
	}
}

func TestPayloadImmutability(t *testing.T) {
	payload := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	payloadCopy := make([]byte, len(payload))
	copy(payloadCopy, payload)

	_ = EncodeRecord(payload)

	if !bytes.Equal(payload, payloadCopy) {
		t.Errorf("EncodeRecord should not modify the input payload")
	}
}

func TestHeaderConsistency(t *testing.T) {
	// Ensure that records with the same payload always produce the same encoding
	payload := []byte{0x01, 0x02, 0x03}

	record1 := EncodeRecord(payload)
	record2 := EncodeRecord(payload)

	if !bytes.Equal(record1, record2) {
		t.Errorf("Encoding the same payload twice should produce identical results")
	}
}

func TestEncodeRecord(t *testing.T) {
	tests := []struct {
		name       string
		payload    []byte
		wantLength int
	}{
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

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EncodeRecord(test.payload)

			if len(got) != test.wantLength {
				t.Errorf(
					"Wanted encoded length of %v but got %v instead",
					test.wantLength, len(got),
				)
			}

			// Check the magic
			if got[0] != 0xBE || got[1] != 0xAC {
				t.Errorf(
					"Expected magic at [0:2] but got %v instead",
					got[0:2],
				)
			}

			// Check the version
			if got[2] != walVersion {
				t.Errorf(
					"Expected version at [2] but got %v instead",
					got[2],
				)
			}

			// Check the record type
			if got[3] != RecordTypeFull {
				t.Errorf(
					"Expected RecordTypeFull at [3] but got %v instead",
					got[3],
				)
			}

			// Check the payload length field
			if coding.Uint32(got[4:8]) != uint32(len(test.payload)) {
				t.Errorf(
					"Expected payload length of %v at [4:8] but got %v instead",
					len(test.payload), got[4:8],
				)
			}

			// Check the checksum
			wantCRC32C := checksum.CRC32C(test.payload)
			if coding.Uint32(got[8:12]) != wantCRC32C {
				t.Errorf(
					"Expected checksum '%v' at [8:12] but got %v instead",
					wantCRC32C, got[8:12],
				)
			}

			// Check payload is preserved correctly
			if !bytes.Equal(got[12:], test.payload) {
				t.Errorf(
					"Expected payload to be preserved, but got %v instead",
					got[12:],
				)
			}
		})
	}
}
