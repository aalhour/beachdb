package wal

import (
	"bufio"
	"fmt"
	"os"
	"sync"
)

// Writer provides sequential write access to WAL records.
type Writer struct {
	mu   sync.Mutex
	file *os.File
	buf  *bufio.Writer
}

// NewWriter creates a new Writer for the WAL file at the given path.
// If the file exists, it will be opened in append mode.
func NewWriter(path string) (*Writer, error) {
	//nolint:gosec // G302: 0644 is acceptable for WAL files
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("wal: failed to open file: %w", err)
	}

	writer := &Writer{
		file: file,
		buf:  bufio.NewWriter(file),
	}

	return writer, nil
}

// Append writes a new WAL record containing the given payload.
func (w *Writer) Append(payload []byte) error {
	// First, encode the payload outside the lock
	record := EncodeRecord(payload)

	// Second, acquire the lock to synchronize buffer writing
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return ErrWriterClosed
	}

	// Write to the buffer inside the lock
	_, err := w.buf.Write(record)
	if err != nil {
		return fmt.Errorf("wal: failed to write record: %w", err)
	}

	return nil
}

// Sync flushes buffered data and syncs the file to disk.
func (w *Writer) Sync() error {
	// Acquire the lock to synchronize flushes + syncs
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return ErrWriterClosed
	}

	err := w.buf.Flush()
	if err != nil {
		return fmt.Errorf("wal: failed to flush buffer: %w", err)
	}
	err = w.file.Sync()
	if err != nil {
		return fmt.Errorf("wal: failed to sync file: %w", err)
	}
	return nil
}

// Close flushes buffered data, syncs the file, and closes the writer.
func (w *Writer) Close() error {
	if w == nil {
		return ErrWriterClosed
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return ErrWriterClosed
	}

	var firstErr error

	if w.buf != nil {
		if err := w.buf.Flush(); err != nil {
			firstErr = fmt.Errorf("wal: flush buffer failed during close: %w", err)
		}
	}

	if err := w.file.Sync(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("wal: sync file failed during close: %w", err)
	}

	if err := w.file.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("wal: close file failed: %w", err)
	}

	// Mark as closed even if there was an error
	w.file = nil
	w.buf = nil

	return firstErr
}
