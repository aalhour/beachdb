package engine

import (
	"fmt"
	"os"
)

// syncDir fsyncs a directory to ensure that metadata changes (new files,
// renames, deletes) are persisted to stable storage. Without this, a
// newly created file may not appear in the directory after a crash.
//
// See fsync(2): "Calling fsync() does not necessarily ensure that the
// entry in the directory containing the file has also reached disk.
// For that an explicit fsync() on a file descriptor for the directory
// is also needed."
func syncDir(path string) error {
	dir, err := os.Open(path) //nolint:gosec // path is controlled by the engine, not user input
	if err != nil {
		return fmt.Errorf("beachdb: failed to open directory for sync: %w", err)
	}
	defer dir.Close()

	if err := dir.Sync(); err != nil {
		return fmt.Errorf("beachdb: failed to sync directory: %w", err)
	}
	return nil
}
