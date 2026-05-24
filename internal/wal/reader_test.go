package wal

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aalhour/beachdb/internal/record"
	"github.com/aalhour/beachdb/internal/util/checksum"
)

func TestNewReader(t *testing.T) {
	t.Run("opens existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing.wal")

		// Create the file first
		//nolint:gosec // G306: 0644 is acceptable for test files
		if err := os.WriteFile(path, []byte("test data"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader failed: %v", err)
		}
		defer r.Close()

		if r.file == nil {
			t.Error("file should not be nil")
		}
		if r.buf == nil {
			t.Error("buf should not be nil")
		}
	})

	t.Run("returns error for non-existent file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nonexistent.wal")

		_, err := NewReader(path)
		if err == nil {
			t.Error("expected error for non-existent file")
		}
	})

	t.Run("returns error for invalid path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nonexistent", "subdir", "file.wal")

		_, err := NewReader(path)
		if err == nil {
			t.Error("expected error for invalid path")
		}
	})
}

func TestReader_Close(t *testing.T) {
	t.Run("closes successfully", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "close.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)
		err := r.Close()
		if err != nil {
			t.Errorf("Close failed: %v", err)
		}
	})

	t.Run("double close returns ErrReaderClosed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "double_close.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)

		err1 := r.Close()
		if err1 != nil {
			t.Fatalf("first Close failed: %v", err1)
		}

		err2 := r.Close()
		if !errors.Is(err2, ErrReaderClosed) {
			t.Errorf("second Close should return ErrReaderClosed, got: %v", err2)
		}
	})

	t.Run("triple close all return ErrReaderClosed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "triple_close.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)
		r.Close()

		for i := 2; i <= 3; i++ {
			err := r.Close()
			if !errors.Is(err, ErrReaderClosed) {
				t.Errorf("Close #%d should return ErrReaderClosed, got: %v", i, err)
			}
		}
	})

	t.Run("marks reader as closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mark_closed.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)
		r.Close()

		if r.file != nil {
			t.Error("file should be nil after close")
		}
		if r.buf != nil {
			t.Error("buf should be nil after close")
		}
	})

	t.Run("concurrent close attempts", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "concurrent_close.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)

		done := make(chan error, 10)
		for range 10 {
			go func() {
				done <- r.Close()
			}()
		}

		var successCount int
		var closedCount int
		for range 10 {
			err := <-done
			switch {
			case err == nil:
				successCount++
			case errors.Is(err, ErrReaderClosed):
				closedCount++
			}
		}

		if successCount != 1 {
			t.Errorf("expected exactly 1 successful close, got %d", successCount)
		}
		if closedCount != 9 {
			t.Errorf("expected 9 ErrReaderClosed, got %d", closedCount)
		}
	})
}

func TestReader_Close_EdgeCases(t *testing.T) {
	t.Run("closed reader returns ErrReaderClosed", func(t *testing.T) {
		r := &Reader{file: nil, buf: nil}

		err := r.Close()
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("Close on closed reader should return ErrReaderClosed, got: %v", err)
		}
	})

	t.Run("close after file externally closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "external_close.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)

		// Externally close the file
		r.file.Close()

		// Close should still work (file.Close returns error but reader marks as closed)
		err := r.Close()
		// May or may not error depending on OS, but should not panic
		_ = err

		// Subsequent close should return ErrReaderClosed
		err2 := r.Close()
		if !errors.Is(err2, ErrReaderClosed) {
			t.Errorf("subsequent Close should return ErrReaderClosed, got: %v", err2)
		}
	})

	t.Run("handles nil buf with valid file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nil_buf.wal")
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		file, _ := os.Create(path)

		r := &Reader{file: file, buf: nil}

		err := r.Close()
		if err != nil {
			t.Errorf("Close with nil buf should succeed, got: %v", err)
		}
	})
}

// TestReader_Close_NoPanic verifies Close never panics under various edge cases
func TestReader_Close_NoPanic(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on nil receiver: %v", r)
			}
		}()

		var r *Reader
		_ = r.Close() // Will panic on nil receiver - this tests if we handle it
	})

	t.Run("zero value struct", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on zero value: %v", r)
			}
		}()

		r := Reader{}
		_ = r.Close()
	})

	t.Run("zero value pointer", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on zero value pointer: %v", r)
			}
		}()

		r := &Reader{}
		_ = r.Close()
	})

	t.Run("only file set", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked with only file set: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "only_file.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		file, _ := os.Open(path)

		r := &Reader{file: file, buf: nil}
		_ = r.Close()
	})

	t.Run("100 rapid closes", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked during rapid closes: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "rapid.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)

		for range 100 {
			_ = r.Close()
		}
	})

	t.Run("close after file externally closed", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked after external file close: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "external_close.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)
		r.file.Close()

		_ = r.Close()
	})

	t.Run("close with corrupted buf state", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked with corrupted buf: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "corrupted_buf.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)
		r.buf = nil

		_ = r.Close()
	})

	t.Run("concurrent close and next", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked during concurrent operations: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "concurrent_ops.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)

		done := make(chan struct{})

		// Goroutine doing Next calls
		go func() {
			defer close(done)
			for range 100 {
				_, _ = r.Next()
			}
		}()

		// Main goroutine doing closes
		for range 10 {
			_ = r.Close()
		}

		<-done
	})

	t.Run("close on freshly created reader", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on fresh reader: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "fresh.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)
		_ = r.Close()
	})

	t.Run("stress test concurrent closes", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked during stress test: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "stress.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte("data"), 0644)

		r, _ := NewReader(path)

		done := make(chan struct{}, 100)
		for range 100 {
			go func() {
				_ = r.Close()
				done <- struct{}{}
			}()
		}

		for range 100 {
			<-done
		}
	})
}

func TestReader_Next(t *testing.T) {
	t.Run("reads single record successfully", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "single.wal")
		payload := []byte("hello, world")

		// Write a valid record using Writer
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}
		if err := w.Append(payload); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Writer.Close failed: %v", err)
		}

		// Read it back
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader failed: %v", err)
		}
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}

		if !slices.Equal(got, payload) {
			t.Errorf("got %q, want %q", got, payload)
		}
	})

	t.Run("reads multiple records", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "multiple.wal")
		payloads := [][]byte{
			[]byte("first"),
			[]byte("second"),
			[]byte("third"),
		}

		// Write records
		w, _ := NewWriter(path)
		for _, p := range payloads {
			w.Append(p)
		}
		w.Close()

		// Read them back
		r, _ := NewReader(path)
		defer r.Close()

		for i, want := range payloads {
			got, err := r.Next()
			if err != nil {
				t.Fatalf("Next #%d failed: %v", i, err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("record #%d: got %q, want %q", i, got, want)
			}
		}
	})

	t.Run("returns EOF on empty file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.wal")
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, []byte{}, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, io.EOF) {
			t.Errorf("expected io.EOF, got %v", err)
		}
	})

	t.Run("returns EOF after last record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "eof.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("only record"))
		w.Close()

		r, _ := NewReader(path)
		defer r.Close()

		// First read succeeds
		_, err := r.Next()
		if err != nil {
			t.Fatalf("first Next failed: %v", err)
		}

		// Second read returns EOF
		_, err = r.Next()
		if !errors.Is(err, io.EOF) {
			t.Errorf("expected io.EOF, got %v", err)
		}
	})

	t.Run("returns error on truncated header", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncated_header.wal")

		// Write partial header (less than recordHeaderSize bytes)
		partial := make([]byte, recordHeaderSize-1)
		copy(partial, walMagic)
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, partial, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if err == nil {
			t.Error("expected error for truncated header")
		}
	})

	t.Run("returns error on truncated payload", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncated_payload.wal")

		// Create a valid header claiming 100 bytes payload, but provide fewer
		header := buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, 100, 0)

		// Only provide 10 bytes of payload
		data := append(append([]byte(nil), header...), make([]byte, 10)...)
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, data, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, record.ErrTruncated) {
			t.Errorf("expected ErrTruncated, got %v", err)
		}
	})

	t.Run("returns error on bad magic", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad_magic.wal")

		// Create header with wrong magic
		bad := make([]byte, walMagicSize)
		copy(bad, []byte("DEADBEEF"))
		header := buildTestRecordHeader(bad, walVersion, RecordTypeFull, 0, 0)
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, header, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, record.ErrBadMagic) {
			t.Errorf("expected ErrBadMagic, got %v", err)
		}
	})

	t.Run("returns error on unsupported version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad_version.wal")

		header := buildTestRecordHeader(validMagicBytes(), 0x99, RecordTypeFull, 0, 0)
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, header, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, record.ErrUnsupportedVersion) {
			t.Errorf("expected ErrUnsupportedVersion, got %v", err)
		}
	})

	t.Run("returns error on oversized payload length", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized_payload.wal")

		header := buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, maxRecordPayloadSize+1, 0)
		//nolint:gosec // G306: 0644 is acceptable for test files
		if err := os.WriteFile(path, header, 0644); err != nil {
			t.Fatalf("failed to write oversized header: %v", err)
		}

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, record.ErrRecordTooLarge) {
			t.Errorf("expected ErrRecordTooLarge, got %v", err)
		}
	})

	t.Run("returns error on checksum mismatch", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad_checksum.wal")

		payload := []byte("test payload")

		// Write a valid record first
		w, _ := NewWriter(path)
		w.Append(payload)
		w.Close()

		// Corrupt the payload (flip a bit)
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		data, _ := os.ReadFile(path)
		data[recordHeaderSize] ^= 0x01 // flip bit in first byte of payload
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, data, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, record.ErrChecksum) {
			t.Errorf("expected ErrChecksum, got %v", err)
		}
	})

	t.Run("reads empty payload", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty_payload.wal")

		w, _ := NewWriter(path)
		w.Append([]byte{}) // empty payload
		w.Close()

		r, _ := NewReader(path)
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty payload, got %q", got)
		}
	})

	t.Run("reads binary payload with null bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "binary.wal")
		payload := []byte{0x00, 0x01, 0x02, 0xFF, 0x00, 0xFE}

		w, _ := NewWriter(path)
		w.Append(payload)
		w.Close()

		r, _ := NewReader(path)
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		if !slices.Equal(got, payload) {
			t.Errorf("got %v, want %v", got, payload)
		}
	})

	t.Run("reads large payload", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "large.wal")

		// 1MB payload
		payload := make([]byte, 1024*1024)
		for i := range payload {
			payload[i] = byte(i % 256)
		}

		w, _ := NewWriter(path)
		w.Append(payload)
		w.Close()

		r, _ := NewReader(path)
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		if !slices.Equal(got, payload) {
			t.Errorf("payload mismatch (len got=%d, want=%d)", len(got), len(payload))
		}
	})
}

func TestReader_Next_Roundtrip(t *testing.T) {
	t.Run("write then read many records", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "roundtrip.wal")

		payloads := make([][]byte, 100)
		for i := range payloads {
			payloads[i] = []byte("record-" + string(rune('0'+i%10)) + string(rune('0'+i/10)))
		}

		// Write all
		w, _ := NewWriter(path)
		for _, p := range payloads {
			w.Append(p)
		}
		w.Close()

		// Read all
		r, _ := NewReader(path)
		defer r.Close()

		for i, want := range payloads {
			got, err := r.Next()
			if err != nil {
				t.Fatalf("Next #%d failed: %v", i, err)
			}
			if !slices.Equal(got, want) {
				t.Errorf("record #%d: got %q, want %q", i, got, want)
			}
		}

		// Verify EOF
		_, err := r.Next()
		if !errors.Is(err, io.EOF) {
			t.Errorf("expected EOF after all records, got %v", err)
		}
	})
}

func TestReader_Next_EdgeCases(t *testing.T) {
	t.Run("returns ErrReaderClosed on closed reader", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "closed.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("test"))
		w.Close()

		r, _ := NewReader(path)
		r.Close()

		_, err := r.Next()
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("expected ErrReaderClosed, got %v", err)
		}
	})

	t.Run("returns error on unsupported record type", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad_type.wal")

		header := buildTestRecordHeader(validMagicBytes(), walVersion, 0x99, 0, 0)
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, header, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, record.ErrUnsupportedRecordType) {
			t.Errorf("expected ErrUnsupportedRecordType, got %v", err)
		}
	})

	t.Run("returns EOF after partial read leaves file exhausted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "partial.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("only one"))
		w.Close()

		r, _ := NewReader(path)
		defer r.Close()

		// First call succeeds
		_, err := r.Next()
		if err != nil {
			t.Fatalf("first Next failed: %v", err)
		}

		// Second, third, fourth all return EOF
		for i := range 3 {
			_, err = r.Next()
			if !errors.Is(err, io.EOF) {
				t.Errorf("call #%d: expected io.EOF, got %v", i+2, err)
			}
		}
	})

	t.Run("concurrent Next calls are safe", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "concurrent_next.wal")

		// Write multiple records
		w, _ := NewWriter(path)
		for range 10 {
			w.Append([]byte("record"))
		}
		w.Close()

		r, _ := NewReader(path)
		defer r.Close()

		// Read concurrently - mutex should protect
		done := make(chan []byte, 10)
		for range 10 {
			go func() {
				payload, err := r.Next()
				if err == nil {
					done <- payload
				} else {
					done <- nil
				}
			}()
		}

		// Collect all results
		var successCount int
		for range 10 {
			if <-done != nil {
				successCount++
			}
		}

		// All 10 reads should succeed (mutex serializes them)
		if successCount != 10 {
			t.Errorf("expected 10 successful reads, got %d", successCount)
		}
	})
}

func TestReader_Next_ManualRecordConstruction(t *testing.T) {
	// These tests manually construct records to test edge cases

	t.Run("valid manually constructed record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manual.wal")
		payload := []byte("test")
		record, err := EncodeRecord(payload)
		if err != nil {
			t.Fatalf("EncodeRecord failed: %v", err)
		}
		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, record, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		if !slices.Equal(got, payload) {
			t.Errorf("got %q, want %q", got, payload)
		}
	})

	t.Run("corrupted checksum in manually constructed record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt.wal")
		payload := []byte("test")

		// Manually construct with wrong checksum
		//nolint:gosec // G115: test data, length is small
		header := buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, uint32(len(payload)), 0xDEADBEEF)
		rec := append(append([]byte(nil), header...), payload...)

		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, rec, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		_, err := r.Next()
		if !errors.Is(err, record.ErrChecksum) {
			t.Errorf("expected ErrChecksum, got %v", err)
		}
	})

	t.Run("correct checksum validates", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "correct_checksum.wal")
		payload := []byte("test payload")
		csum := checksum.CRC32C(payload)

		//nolint:gosec // G115: test data, length is small
		header := buildTestRecordHeader(validMagicBytes(), walVersion, RecordTypeFull, uint32(len(payload)), csum)
		rec := append(append([]byte(nil), header...), payload...)

		//nolint:gosec // G306: 0644 is acceptable for test files
		os.WriteFile(path, rec, 0644)

		r, _ := NewReader(path)
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next failed: %v", err)
		}
		if !slices.Equal(got, payload) {
			t.Errorf("got %q, want %q", got, payload)
		}
	})
}

func TestReader_ValidOffsetProgression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offset_progress.wal")
	payloads := [][]byte{
		[]byte("first"),
		[]byte("second"),
	}

	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter failed: %v", err)
	}
	for _, payload := range payloads {
		if err := w.Append(payload); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Writer.Close failed: %v", err)
	}

	r, err := NewReader(path)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer r.Close()

	var expectedOffset int64
	for i, payload := range payloads {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next #%d failed: %v", i, err)
		}
		if !slices.Equal(got, payload) {
			t.Fatalf("record #%d mismatch: got %q want %q", i, got, payload)
		}

		expectedOffset += int64(recordHeaderSize + len(payload))
		if gotOffset := r.ValidOffset(); gotOffset != expectedOffset {
			t.Fatalf("offset after record #%d = %d, want %d", i, gotOffset, expectedOffset)
		}
	}

	_, err = r.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after all records, got %v", err)
	}
	if gotOffset := r.ValidOffset(); gotOffset != expectedOffset {
		t.Fatalf("offset changed after EOF: got %d, want %d", gotOffset, expectedOffset)
	}
}

func TestReader_ValidOffsetDoesNotAdvanceOnCorruptionOrTruncation(t *testing.T) {
	t.Run("checksum mismatch keeps last valid offset", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "checksum_offset.wal")
		first := []byte("first")
		second := []byte("second")

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}
		if err := w.Append(first); err != nil {
			t.Fatalf("Append first failed: %v", err)
		}
		if err := w.Append(second); err != nil {
			t.Fatalf("Append second failed: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Writer.Close failed: %v", err)
		}

		// Corrupt a byte in the second payload.
		//nolint:gosec // G304: path is controlled by test
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile failed: %v", err)
		}
		firstRecordBytes := recordHeaderSize + len(first)
		data[firstRecordBytes+recordHeaderSize] ^= 0x01
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // test file
			t.Fatalf("WriteFile failed: %v", err)
		}

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader failed: %v", err)
		}
		defer r.Close()

		if _, err := r.Next(); err != nil {
			t.Fatalf("first Next failed: %v", err)
		}
		validAfterFirst := r.ValidOffset()

		_, err = r.Next()
		if !errors.Is(err, record.ErrChecksum) {
			t.Fatalf("expected ErrChecksum, got %v", err)
		}
		if got := r.ValidOffset(); got != validAfterFirst {
			t.Fatalf("offset advanced after checksum error: got %d, want %d", got, validAfterFirst)
		}
	})

	t.Run("truncated payload keeps last valid offset", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncated_offset.wal")
		first := []byte("first")
		second := []byte("second")

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}
		if err := w.Append(first); err != nil {
			t.Fatalf("Append first failed: %v", err)
		}
		if err := w.Append(second); err != nil {
			t.Fatalf("Append second failed: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Writer.Close failed: %v", err)
		}

		// Remove one byte from the final payload.
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat failed: %v", err)
		}
		if err := os.Truncate(path, info.Size()-1); err != nil {
			t.Fatalf("Truncate failed: %v", err)
		}

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader failed: %v", err)
		}
		defer r.Close()

		if _, err := r.Next(); err != nil {
			t.Fatalf("first Next failed: %v", err)
		}
		validAfterFirst := r.ValidOffset()

		_, err = r.Next()
		if !errors.Is(err, record.ErrTruncated) {
			t.Fatalf("expected ErrTruncated, got %v", err)
		}
		if got := r.ValidOffset(); got != validAfterFirst {
			t.Fatalf("offset advanced after truncated record: got %d, want %d", got, validAfterFirst)
		}
	})
}

// ExampleReader demonstrates basic WAL reader usage.
func ExampleReader() {
	path := "/tmp/example.wal"
	r, err := NewReader(path)
	if err != nil {
		return
	}
	defer r.Close()

	for {
		payload, err := r.Next()
		if err != nil {
			break
		}
		_ = payload
	}
	// Output:
}

// --- Benchmarks ---

func BenchmarkReader_Next(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"small-64B", 64},
		{"medium-1KB", 1024},
		{"large-64KB", 64 * 1024},
	}
	const numRecords = 100
	for _, s := range sizes {
		payload := make([]byte, s.size)
		b.Run(s.name, func(b *testing.B) {
			// Pre-write a fixed number of records
			path := filepath.Join(b.TempDir(), "bench.wal")
			w, err := NewWriter(path)
			if err != nil {
				b.Fatal(err)
			}
			for i := range numRecords {
				if err := w.Append(payload); err != nil {
					b.Fatalf("Append record %d: %v", i, err)
				}
			}
			if err := w.Close(); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := NewReader(path)
				if err != nil {
					b.Fatal(err)
				}
				for {
					_, err := r.Next()
					if err != nil {
						break
					}
				}
				r.Close()
			}
		})
	}
}
