package manifest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/aalhour/beachdb/internal/record"
)

// Reader provides sequential read access to a MANIFEST record stream.
type Reader struct {
	// Single-reader per replay session. Mutex defends against accidental
	// concurrent use.
	mu sync.Mutex

	// Open file handle for the MANIFEST on disk.
	file *os.File

	// Buffered reader sitting on file.
	buf *bufio.Reader

	// Byte offset immediately after the last fully validated record.
	// On a clean stream, equals total bytes consumed by Next calls.
	// On a truncated tail, points at the last good boundary — useful for
	// recovery (e.g. to truncate the file back to the last valid record).
	pos int64
}

// NewReader creates a new Reader for the MANIFEST file at the given path.
func NewReader(path string) (*Reader, error) {
	//nolint:gosec // G304: path is controlled by the engine, not user input
	file, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("beachdb/manifest: failed to open file: %w", err)
	}

	reader := &Reader{
		file: file,
		buf:  bufio.NewReader(file),
	}

	return reader, nil
}

// Next reads and returns the next MANIFEST record payload (the encoded
// VersionEdit body). Returns io.EOF cleanly at end of stream. A header or
// payload short-read returns record.ErrTruncated; callers may treat a
// trailing truncation as expected (crash during write) or as corruption
// depending on policy.
func (r *Reader) Next() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		return nil, ErrReaderClosed
	}

	header := make([]byte, record.HeaderSize)
	n, err := io.ReadFull(r.buf, header)

	if errors.Is(err, io.EOF) {
		// Clean EOF at a record boundary.
		return nil, io.EOF
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || n < record.HeaderSize {
		return nil, record.ErrTruncated
	}
	if err != nil {
		return nil, fmt.Errorf("beachdb/manifest: failed to read header: %w", err)
	}

	payloadLen, checksum, err := DecodeRecordHeader(header)
	if err != nil {
		return nil, err
	}

	payload := make([]byte, int(payloadLen))
	n, err = io.ReadFull(r.buf, payload)
	if errors.Is(err, io.ErrUnexpectedEOF) || n < int(payloadLen) {
		return nil, record.ErrTruncated
	}
	if err != nil {
		return nil, fmt.Errorf("beachdb/manifest: failed to read payload: %w", err)
	}

	if err := ValidateRecord(payload, checksum); err != nil {
		return nil, err
	}

	r.pos += int64(record.HeaderSize) + int64(payloadLen)

	return payload, nil
}

// NextEdit reads the next record and decodes it as a VersionEdit. Convenience
// wrapper over Next + DecodeVersionEdit. Returns io.EOF at end of stream.
func (r *Reader) NextEdit() (*VersionEdit, error) {
	payload, err := r.Next()
	if err != nil {
		return nil, err
	}
	return DecodeVersionEdit(payload)
}

// ValidOffset returns the byte offset immediately after the last fully
// validated record. Replay code can truncate the file to this offset to
// drop a partial trailing record after a crash.
func (r *Reader) ValidOffset() int64 {
	if r == nil {
		return 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.pos
}

// Close closes the reader and releases associated resources.
func (r *Reader) Close() error {
	if r == nil {
		return ErrReaderClosed
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		return ErrReaderClosed
	}

	err := r.file.Close()

	// Mark as closed even if Close returned an error.
	r.buf = nil
	r.file = nil

	if err != nil {
		return fmt.Errorf("beachdb/manifest: failed to close file: %w", err)
	}
	return nil
}
