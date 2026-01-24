package wal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewWriter(t *testing.T) {
	t.Run("creates new file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.wal")

		// Verify file doesn't exist
		_, err := os.Stat(path)
		if !os.IsNotExist(err) {
			t.Fatal("file should not exist before test")
		}

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}
		defer w.Close()

		// Verify file now exists
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("file should exist after NewWriter: %v", err)
		}
		if info.Size() != 0 {
			t.Errorf("new file should be empty, got size %d", info.Size())
		}
	})

	t.Run("opens existing file and preserves content", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing.wal")

		// Create file with existing content
		existingData := []byte("existing WAL data")
		//nolint:gosec // G306: 0644 is acceptable for test files
		if err := os.WriteFile(path, existingData, 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}

		// Verify existing content is preserved (append mode)
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}
		if !bytes.HasPrefix(content, existingData) {
			t.Error("existing content should be preserved")
		}
	})

	t.Run("returns error for invalid path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nonexistent", "subdir", "file.wal")

		_, err := NewWriter(path)
		if err == nil {
			t.Error("expected error for invalid path")
		}
	})
}

func TestWriter_Append(t *testing.T) {
	t.Run("writes record to file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "append.wal")

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}

		payload := []byte("test payload")
		if err := w.Append(payload); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}

		// Verify file has content
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("failed to stat file: %v", err)
		}

		expectedSize := int64(recordHeaderSize + len(payload))
		if info.Size() != expectedSize {
			t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
		}
	})

	t.Run("handles nil payload", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "append.wal")

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}

		err = w.Append(nil)
		if err != nil {
			t.Fatalf("Append failed: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}

		// Verify file has content
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("failed to stat file: %v", err)
		}

		// expectedSize is the same as the record header size because nil = nothing!
		expectedSize := int64(recordHeaderSize)
		if info.Size() != expectedSize {
			t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
		}
	})

	t.Run("appends multiple records", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "multi.wal")

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}

		payloads := [][]byte{
			[]byte("first"),
			[]byte("second"),
			[]byte("third"),
		}

		for _, p := range payloads {
			if err := w.Append(p); err != nil {
				t.Fatalf("Append failed: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}

		// Verify total file size
		info, _ := os.Stat(path)
		expectedSize := int64(0)
		for _, p := range payloads {
			expectedSize += int64(recordHeaderSize + len(p))
		}
		if info.Size() != expectedSize {
			t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
		}
	})

	t.Run("appends to existing file with new writer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "reopen.wal")

		// Write first record
		w1, _ := NewWriter(path)
		w1.Append([]byte("first"))
		w1.Close()

		info1, _ := os.Stat(path)
		sizeAfterFirst := info1.Size()

		// Write second record with new writer
		w2, _ := NewWriter(path)
		w2.Append([]byte("second"))
		w2.Close()

		info2, _ := os.Stat(path)
		sizeAfterSecond := info2.Size()

		// File should have grown
		if sizeAfterSecond <= sizeAfterFirst {
			t.Errorf("file should grow: first=%d, second=%d", sizeAfterFirst, sizeAfterSecond)
		}

		// Verify exact size
		expectedSize := int64(recordHeaderSize+len("first")) + int64(recordHeaderSize+len("second"))
		if sizeAfterSecond != expectedSize {
			t.Errorf("file size = %d, want %d", sizeAfterSecond, expectedSize)
		}
	})

	t.Run("writes valid record format", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "format.wal")

		w, _ := NewWriter(path)
		payload := []byte{0x01, 0x02, 0x03}
		w.Append(payload)
		w.Close()

		// Read and verify the record
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		content, _ := os.ReadFile(path)

		// Decode header
		payloadLen, crc, err := DecodeRecordHeader(content[:recordHeaderSize])
		if err != nil {
			t.Fatalf("DecodeRecordHeader failed: %v", err)
		}

		//nolint:gosec // G115: len(payload) is bounded by test data, overflow not possible
		if payloadLen != uint32(len(payload)) {
			t.Errorf("payload length = %d, want %d", payloadLen, len(payload))
		}

		// Verify checksum
		if err := ValidateRecord(content[recordHeaderSize:], crc); err != nil {
			t.Errorf("ValidateRecord failed: %v", err)
		}

		// Verify payload
		if !bytes.Equal(content[recordHeaderSize:], payload) {
			t.Error("payload mismatch")
		}
	})
}

func TestWriter_Sync(t *testing.T) {
	t.Run("flushes buffered data to disk", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sync.wal")

		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter failed: %v", err)
		}
		defer w.Close()

		payload := []byte("data to sync")
		if err := w.Append(payload); err != nil {
			t.Fatalf("Append failed: %v", err)
		}

		// Before sync, file might be empty due to buffering
		info1, _ := os.Stat(path)
		sizeBeforeSync := info1.Size()

		// Sync should flush to disk
		if err := w.Sync(); err != nil {
			t.Fatalf("Sync failed: %v", err)
		}

		info2, _ := os.Stat(path)
		sizeAfterSync := info2.Size()

		expectedSize := int64(recordHeaderSize + len(payload))
		if sizeAfterSync != expectedSize {
			t.Errorf("file size after sync = %d, want %d", sizeAfterSync, expectedSize)
		}
		if sizeAfterSync < sizeBeforeSync {
			t.Error("file should not shrink after sync")
		}
	})

	t.Run("data persisted after sync is readable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sync_read.wal")

		w, _ := NewWriter(path)
		defer w.Close()

		payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
		w.Append(payload)
		w.Sync()

		// Read the file and verify content
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}

		// Verify record is valid
		payloadLen, crc, err := DecodeRecordHeader(content[:recordHeaderSize])
		if err != nil {
			t.Fatalf("DecodeRecordHeader failed: %v", err)
		}

		//nolint:gosec // G115: len(payload) is bounded by test data, overflow not possible
		if payloadLen != uint32(len(payload)) {
			t.Errorf("payload length = %d, want %d", payloadLen, len(payload))
		}

		if err := ValidateRecord(content[recordHeaderSize:], crc); err != nil {
			t.Errorf("ValidateRecord failed: %v", err)
		}
	})

	t.Run("multiple syncs are safe", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "multi_sync.wal")

		w, _ := NewWriter(path)
		defer w.Close()

		w.Append([]byte("first"))
		if err := w.Sync(); err != nil {
			t.Errorf("first Sync failed: %v", err)
		}

		w.Append([]byte("second"))
		if err := w.Sync(); err != nil {
			t.Errorf("second Sync failed: %v", err)
		}

		// Empty sync (no new data) should also succeed
		if err := w.Sync(); err != nil {
			t.Errorf("empty Sync failed: %v", err)
		}

		// Verify total size
		info, _ := os.Stat(path)
		expectedSize := int64(recordHeaderSize+len("first")) + int64(recordHeaderSize+len("second"))
		if info.Size() != expectedSize {
			t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
		}
	})

	t.Run("can write after sync", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "write_after_sync.wal")

		w, _ := NewWriter(path)
		defer w.Close()

		w.Append([]byte("before"))
		w.Sync()

		// Should be able to continue writing after sync
		if err := w.Append([]byte("after")); err != nil {
			t.Fatalf("Append after Sync failed: %v", err)
		}
		w.Sync()

		info, _ := os.Stat(path)
		expectedSize := int64(recordHeaderSize+len("before")) + int64(recordHeaderSize+len("after"))
		if info.Size() != expectedSize {
			t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
		}
	})
}

func TestWriter_Close(t *testing.T) {
	t.Run("flushes and syncs on close", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "close_flush.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("buffered data"))

		// Before close, file might be empty due to buffering
		info1, _ := os.Stat(path)

		if err := w.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}

		// After close, data should be flushed and synced
		info2, _ := os.Stat(path)
		if info2.Size() == 0 {
			t.Error("file should have data after close")
		}
		if info2.Size() < info1.Size() {
			t.Error("file should not shrink after close")
		}
	})

	t.Run("returns nil on success", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "close_success.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		err := w.Close()
		if err != nil {
			t.Errorf("Close should return nil on success, got: %v", err)
		}
	})

	t.Run("close without any writes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "close_empty.wal")

		w, _ := NewWriter(path)

		// Close immediately without writing anything
		err := w.Close()
		if err != nil {
			t.Errorf("Close without writes should succeed, got: %v", err)
		}

		// File should exist but be empty
		info, _ := os.Stat(path)
		if info.Size() != 0 {
			t.Errorf("file should be empty, got size %d", info.Size())
		}
	})

	t.Run("close after sync", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "close_after_sync.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))
		w.Sync()

		// Close after sync should still work (no double-flush issues)
		err := w.Close()
		if err != nil {
			t.Errorf("Close after Sync should succeed, got: %v", err)
		}
	})

	t.Run("data is durable after close", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "durable.wal")

		payload := []byte("durable data")

		w, _ := NewWriter(path)
		w.Append(payload)
		w.Close()

		// Re-read the file to verify durability
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read file: %v", err)
		}

		expectedSize := recordHeaderSize + len(payload)
		if len(content) != expectedSize {
			t.Errorf("content length = %d, want %d", len(content), expectedSize)
		}

		// Verify it's a valid record
		_, crc, err := DecodeRecordHeader(content[:recordHeaderSize])
		if err != nil {
			t.Fatalf("DecodeRecordHeader failed: %v", err)
		}

		if err := ValidateRecord(content[recordHeaderSize:], crc); err != nil {
			t.Errorf("ValidateRecord failed: %v", err)
		}
	})
}

func TestWriter_Close_EdgeCases(t *testing.T) {
	t.Run("closed writer returns ErrWriterClosed", func(t *testing.T) {
		w := &Writer{
			file: nil,
			buf:  nil,
		}

		err := w.Close()
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("Close on closed writer should return ErrWriterClosed, got: %v", err)
		}
	})

	t.Run("handles nil buf with valid file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nil_buf.wal")
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		file, _ := os.Create(path)

		w := &Writer{
			file: file,
			buf:  nil,
		}

		err := w.Close()
		if err != nil {
			t.Errorf("Close with nil buf should succeed, got: %v", err)
		}

		// Verify file is actually closed
		err = file.Close()
		if err == nil {
			t.Error("file should already be closed")
		}
	})

	t.Run("double close returns ErrWriterClosed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "double_close.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		// First close should succeed
		err1 := w.Close()
		if err1 != nil {
			t.Fatalf("first Close failed: %v", err1)
		}

		// Second close should return ErrWriterClosed
		err2 := w.Close()
		if !errors.Is(err2, ErrWriterClosed) {
			t.Errorf("second Close should return ErrWriterClosed, got: %v", err2)
		}
	})

	t.Run("double close preserves data from first close", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "double_close_data.wal")

		payload := []byte("important data")
		w, _ := NewWriter(path)
		w.Append(payload)

		w.Close()
		w.Close() // Second close fails, but data should be preserved

		// Verify data was written by first close
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		content, _ := os.ReadFile(path)
		expectedSize := recordHeaderSize + len(payload)
		if len(content) != expectedSize {
			t.Errorf("content length = %d, want %d", len(content), expectedSize)
		}
	})

	t.Run("flush failure when underlying file closed externally", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "flush_fail.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("buffered data that needs flushing"))

		// Sabotage: close the underlying file directly (bypassing Writer)
		w.file.Close()

		// Now Close() should fail because flush can't write to closed file
		err := w.Close()
		if err == nil {
			t.Error("Close should fail when underlying file is already closed")
		}

		// Writer should still be marked as closed after failed close
		err2 := w.Close()
		if !errors.Is(err2, ErrWriterClosed) {
			t.Errorf("subsequent Close should return ErrWriterClosed, got: %v", err2)
		}
	})

	t.Run("partial close - flush succeeds but sync fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "partial_close.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		// Flush the buffer first (manually, bypassing mutex - only for test)
		w.buf.Flush()

		// Close the file to make Sync fail
		w.file.Close()

		// Close should fail at Sync stage but still mark writer as closed
		err := w.Close()
		if err == nil {
			t.Error("Close should fail when file is closed before Sync")
		}
	})

	t.Run("close with large buffered data", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "large_buffer.wal")

		w, _ := NewWriter(path)

		// Write enough data to potentially span multiple buffer flushes
		largePayload := make([]byte, 64*1024) // 64KB
		for i := range largePayload {
			largePayload[i] = byte(i % 256)
		}
		w.Append(largePayload)

		err := w.Close()
		if err != nil {
			t.Fatalf("Close with large buffer failed: %v", err)
		}

		// Verify all data was written
		info, _ := os.Stat(path)
		expectedSize := int64(recordHeaderSize + len(largePayload))
		if info.Size() != expectedSize {
			t.Errorf("file size = %d, want %d", info.Size(), expectedSize)
		}
	})

	t.Run("failed close still marks writer as closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fail_marks_closed.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		// Sabotage to cause close to fail
		w.file.Close()

		// First close fails but should still mark as closed
		err1 := w.Close()
		if err1 == nil {
			t.Fatal("first Close should have failed")
		}

		// Second close returns ErrWriterClosed (not the original error)
		err2 := w.Close()
		if !errors.Is(err2, ErrWriterClosed) {
			t.Errorf("second Close should return ErrWriterClosed, got: %v", err2)
		}
	})

	t.Run("append after close returns ErrWriterClosed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "append_after_close.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("first"))
		w.Close()

		// Append after close should return ErrWriterClosed
		err := w.Append([]byte("second"))
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("Append after Close should return ErrWriterClosed, got: %v", err)
		}
	})

	t.Run("sync after close returns ErrWriterClosed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sync_after_close.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))
		w.Close()

		// Sync after close should return ErrWriterClosed
		err := w.Sync()
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("Sync after Close should return ErrWriterClosed, got: %v", err)
		}
	})

	t.Run("triple close all return ErrWriterClosed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "triple_close.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		if err := w.Close(); err != nil {
			t.Fatalf("first Close failed: %v", err)
		}

		for i := 2; i <= 3; i++ {
			err := w.Close()
			if !errors.Is(err, ErrWriterClosed) {
				t.Errorf("Close #%d should return ErrWriterClosed, got: %v", i, err)
			}
		}
	})

	t.Run("concurrent close attempts", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "concurrent_close.wal")

		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		// Run multiple close attempts concurrently
		done := make(chan error, 10)
		for range 10 {
			go func() {
				done <- w.Close()
			}()
		}

		// Collect results - exactly one should succeed, rest should be ErrWriterClosed
		var successCount int
		var closedCount int
		for range 10 {
			err := <-done
			switch {
			case err == nil:
				successCount++
			case errors.Is(err, ErrWriterClosed):
				closedCount++
			}
		}

		if successCount != 1 {
			t.Errorf("expected exactly 1 successful close, got %d", successCount)
		}
		if closedCount != 9 {
			t.Errorf("expected 9 ErrWriterClosed, got %d", closedCount)
		}
	})
}

// TestWriter_Close_NoPanic verifies Close never panics under various edge cases
func TestWriter_Close_NoPanic(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on nil receiver: %v", r)
			}
		}()

		var w *Writer
		_ = w.Close() // Should panic - nil receiver
	})

	t.Run("zero value struct", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on zero value: %v", r)
			}
		}()

		w := Writer{}
		_ = w.Close()
	})

	t.Run("zero value pointer", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on zero value pointer: %v", r)
			}
		}()

		w := &Writer{}
		_ = w.Close()
	})

	t.Run("only file set", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked with only file set: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "only_file.wal")
		//nolint:gosec // G304: file path is from t.TempDir() which is safe
		file, _ := os.Create(path)
		w := &Writer{file: file, buf: nil}
		_ = w.Close()
	})

	t.Run("only buf set with nil underlying writer", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked with only buf set: %v", r)
			}
		}()

		// bufio.Writer with nil underlying writer
		w := &Writer{file: nil, buf: nil}
		_ = w.Close()
	})

	t.Run("100 rapid closes", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked during rapid closes: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "rapid.wal")
		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		for range 100 {
			_ = w.Close()
		}
	})

	t.Run("close after file externally closed", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked after external file close: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "external_close.wal")
		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		// Externally close the file
		w.file.Close()

		// This should not panic
		_ = w.Close()
	})

	t.Run("close with corrupted buf state", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked with corrupted buf: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "corrupted_buf.wal")
		w, _ := NewWriter(path)

		// Reset buf to nil while file is valid
		w.buf = nil

		_ = w.Close()
	})

	t.Run("concurrent close and append", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked during concurrent operations: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "concurrent_ops.wal")
		w, _ := NewWriter(path)

		done := make(chan struct{})

		// Goroutine doing appends
		go func() {
			defer close(done)
			for range 100 {
				_ = w.Append([]byte("data"))
			}
		}()

		// Main goroutine doing closes
		for range 10 {
			_ = w.Close()
		}

		<-done
	})

	t.Run("concurrent close and sync", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked during concurrent sync: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "concurrent_sync.wal")
		w, _ := NewWriter(path)
		w.Append([]byte("data"))

		done := make(chan struct{})

		// Goroutine doing syncs
		go func() {
			defer close(done)
			for range 50 {
				_ = w.Sync()
			}
		}()

		// Main goroutine doing closes
		for range 10 {
			_ = w.Close()
		}

		<-done
	})

	t.Run("close on freshly created writer", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Close panicked on fresh writer: %v", r)
			}
		}()

		path := filepath.Join(t.TempDir(), "fresh.wal")
		w, _ := NewWriter(path)
		// Close immediately without any operations
		_ = w.Close()
	})
}

// ExampleWriter demonstrates basic WAL writer usage.
func ExampleWriter() {
	path := "/tmp/example.wal"
	w, err := NewWriter(path)
	if err != nil {
		return
	}
	defer w.Close()

	w.Append([]byte("payload"))
	w.Sync()
	// Output:
}
