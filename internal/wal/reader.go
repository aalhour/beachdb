package wal

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sync"
)

// Reader provides sequential read access to WAL records.
type Reader struct {
	mu   sync.Mutex
	file *os.File
	buf  *bufio.Reader
}

// NewReader creates a new Reader for the WAL file at the given path.
func NewReader(path string) (*Reader, error) {
	//nolint:gosec // G302: 0644 is acceptable for read-only access
	file, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("wal: failed to open file: %w", err)
	}

	reader := &Reader{
		file: file,
		buf:  bufio.NewReader(file),
	}

	return reader, nil
}

// Next reads and returns the next WAL record payload.
// It returns io.EOF when there are no more records.
func (r *Reader) Next() (payload []byte, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.file == nil {
		return nil, ErrReaderClosed
	}

	header := make([]byte, recordHeaderSize)
	_, err = io.ReadFull(r.buf, header)

	// err could be io.EOF, io.ErrShortBuffer or io.ErrUnexpectedEOF
	if err != nil {
		return nil, fmt.Errorf("wal: failed to read header: %w", err)
	}

	// Decode the header and get the payload length and checksum
	payloadLen, checksum, err := DecodeRecordHeader(header)
	if err != nil {
		return nil, err
	}

	// Read N=payloadLen in bytes from the file
	payload = make([]byte, payloadLen)
	_, err = io.ReadFull(r.buf, payload)
	if err != nil {
		return nil, ErrTruncated
	}

	// Validate the payload's checksum
	err = ValidateRecord(payload, checksum)
	if err != nil {
		return nil, err
	}

	return payload, nil
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

	// Mark as closed
	r.buf = nil
	r.file = nil

	if err != nil {
		return fmt.Errorf("wal: failed to close file: %w", err)
	}
	return nil
}
