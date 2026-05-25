package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/aalhour/beachdb/internal/fs"
)

// Writer provides sequential write access to a MANIFEST record stream.
type Writer struct {
	// Single-writer per DB. Mutex defends against accidental concurrent use.
	mu sync.Mutex

	// Open file handle for the MANIFEST on disk.
	file *os.File

	// Path of the MANIFEST file on disk.
	path string
}

// NewWriter creates a new Writer for the MANIFEST file at the given path.
// If the file does not exist, it is created. The file is always opened in
// append mode.
func NewWriter(path string) (*Writer, error) {
	//nolint:gosec // G302: 0644 is acceptable for MANIFEST files
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("beachdb/manifest: failed to open file: %w", err)
	}

	// Fsync the parent directory so the new file's directory entry is
	// durable on disk. Without this, a crash after the file was created
	// above can leave the file contents on disk but the dirent lost,
	// making the MANIFEST invisible after recovery.
	if err := fs.SyncDir(filepath.Dir(path)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("beachdb/manifest: failed to sync parent dir: %w", err)
	}

	writer := &Writer{
		file: file,
		path: path,
	}

	return writer, nil
}

// Append writes a new MANIFEST record containing the given payload,
// and syncs the file to disk.
func (w *Writer) Append(payload []byte) error {
	// Encode outside the lock: CPU work and the MaxPayloadSize check happen
	// before any contention with concurrent callers.
	rec, err := EncodeRecord(payload)
	if err != nil {
		return err
	}

	// Serialize concurrent Append callers.
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return ErrWriterClosed
	}

	if _, err := w.file.Write(rec); err != nil {
		return fmt.Errorf("beachdb/manifest: failed to write record to %s: %w", w.path, err)
	}

	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("beachdb/manifest: failed to sync file to %s: %w", w.path, err)
	}

	return nil
}

// Close syncs the file and closes the writer.
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

	if err := w.file.Sync(); err != nil {
		firstErr = fmt.Errorf("beachdb/manifest: sync file failed during close: %w", err)
	}

	if err := w.file.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("beachdb/manifest: close file failed: %w", err)
	}

	// Mark as closed even if there was an error
	w.file = nil

	return firstErr
}
