package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aalhour/beachdb/internal/fs"
)

const (
	// currentFileName is the bare filename of the CURRENT pointer file
	// inside the database directory.
	currentFileName = "CURRENT"

	// currentTmpFileName is the staging filename used by WriteCurrent. The
	// final installation step renames it to currentFileName atomically.
	currentTmpFileName = "CURRENT.tmp"
)

// ReadCurrent reads the CURRENT pointer file in dir and returns the live
// manifest filename (no directory prefix). Returns ErrNoCurrentFile when
// the CURRENT file does not exist — callers should treat that as a fresh
// database signal, not a hard error.
func ReadCurrent(dir string) (string, error) {
	path := filepath.Join(dir, currentFileName)

	//nolint:gosec // G304: path is constructed from the trusted DB directory
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoCurrentFile
		}
		return "", fmt.Errorf("beachdb/manifest: failed to read CURRENT: %w", err)
	}

	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", fmt.Errorf("beachdb/manifest: CURRENT is empty: %w", ErrInvalidManifestName)
	}
	// Symmetric validation with WriteCurrent: CURRENT must hold a bare
	// filename, not a path. Without this, a malformed CURRENT lets the
	// caller escape the database directory via path traversal.
	if err := validateManifestName(name); err != nil {
		return "", err
	}

	return name, nil
}

// WriteCurrent installs manifestName as the active manifest pointer in dir
// atomically: write to a temp file, fsync the temp file, rename it onto
// CURRENT, then fsync the parent directory so the rename is durable.
//
// manifestName must be a bare filename (no path separators); WriteCurrent
// validates this and returns ErrInvalidManifestName on misuse.
//
// A crash at any step leaves either the old CURRENT intact or the new
// CURRENT fully written — never a partial CURRENT. This is the same
// rename-into-place pattern used for SSTable installation.
func WriteCurrent(dir, manifestName string) error {
	if err := validateManifestName(manifestName); err != nil {
		return err
	}

	tmpPath := filepath.Join(dir, currentTmpFileName)
	finalPath := filepath.Join(dir, currentFileName)

	// Write "MANIFEST-NNNNNN\n" to CURRENT.tmp.
	//nolint:gosec // G304: tmpPath is constructed from the trusted DB directory
	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("beachdb/manifest: failed to open CURRENT.tmp: %w", err)
	}

	if _, err := tmp.WriteString(manifestName + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("beachdb/manifest: failed to write CURRENT.tmp: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("beachdb/manifest: failed to sync CURRENT.tmp: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("beachdb/manifest: failed to close CURRENT.tmp: %w", err)
	}

	// Atomic rename — POSIX guarantees the dirent flips from old to new
	// in a single step.
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("beachdb/manifest: failed to rename CURRENT.tmp -> CURRENT: %w", err)
	}

	// Fsync the parent directory so the rename's dirent change is durable
	// on disk. Without this, a crash can leave the rename visible to the
	// running process but invisible after recovery.
	if err := fs.SyncDir(dir); err != nil {
		return fmt.Errorf("beachdb/manifest: failed to sync parent dir after CURRENT rename: %w", err)
	}

	return nil
}

// validateManifestName rejects empty names and any name containing a path
// separator. CURRENT must hold a bare filename so its interpretation is
// unambiguous and confined to the database directory.
func validateManifestName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty", ErrInvalidManifestName)
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		return fmt.Errorf("%w: contains path separator %q", ErrInvalidManifestName, name)
	}
	// Also reject literal '/' on platforms where PathSeparator is something
	// else (e.g. Windows uses '\\'); CURRENT should never contain either.
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("%w: contains '/' %q", ErrInvalidManifestName, name)
	}
	return nil
}
