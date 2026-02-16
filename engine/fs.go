package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// mkdirAllAndSync creates path (including any missing parents) and then
// fsyncs every newly created directory and its parent directory entry.
//
// Why both?
// - fsync(parent) persists "child name -> inode" directory entries
// - fsync(child) persists the child's own inode metadata
//
// This closes the durability gap for nested directory creation where
// os.MkdirAll may create several missing path components.
func mkdirAllAndSync(path string) error {
	cleanPath := filepath.Clean(path)
	created, err := missingDirs(cleanPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cleanPath, 0750); err != nil {
		return fmt.Errorf("beachdb: failed to create directory: %w", err)
	}

	// Nothing new was created, no directory metadata changed.
	if len(created) == 0 {
		return nil
	}

	for _, dir := range created {
		parent := filepath.Dir(dir)
		if err := syncDir(parent); err != nil {
			return fmt.Errorf("beachdb: failed to sync parent directory %q: %w", parent, err)
		}
		if err := syncDir(dir); err != nil {
			return fmt.Errorf("beachdb: failed to sync created directory %q: %w", dir, err)
		}
	}

	return nil
}

// missingDirs returns path components that do not exist yet and would be
// created by os.MkdirAll(path, ...), in creation order (shallow -> deep).
func missingDirs(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("beachdb: empty directory path")
	}

	var created []string
	current := path

	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return nil, fmt.Errorf("beachdb: path exists but is not a directory: %q", current)
			}
			break
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("beachdb: failed to stat directory %q: %w", current, err)
		}

		created = append(created, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	// Reverse from deep->shallow to shallow->deep.
	for i, j := 0, len(created)-1; i < j; i, j = i+1, j-1 {
		created[i], created[j] = created[j], created[i]
	}

	return created, nil
}

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
