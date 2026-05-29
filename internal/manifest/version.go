package manifest

import (
	"slices"
)

// Version represents an in-memory snapshot of which SSTables exist.
type Version struct {
	// files[level] holds the SSTables at that level. For level >= 1 the slice
	// is sorted by SmallestKey ascending. L0 ordering is deferred until
	// iterators land.
	files [][]FileMetadata
}

// NewVersion returns a new Version with the specified capacity.
func NewVersion(capacity uint32) *Version {
	return &Version{
		files: make([][]FileMetadata, 0, capacity),
	}
}

// Clone returns a deep copy of v: a new outer slice plus a fresh inner slice
// per level. FileMetadata values are copied by value.
func (v *Version) Clone() *Version {
	clone := &Version{
		files: make([][]FileMetadata, len(v.files)),
	}
	for level, filesList := range v.files {
		clone.files[level] = slices.Clone(filesList)
	}
	return clone
}

// Apply returns a new Version with edit applied. v is not modified.
// Levels touched by the edit (excluding L0) are sorted by SmallestKey
// before return.
func (v *Version) Apply(edit *VersionEdit) *Version {
	newVersion := v.Clone()
	touched := make(map[uint32]struct{})

	for _, deletedFile := range edit.DeletedFiles {
		level := deletedFile.Level
		fileID := deletedFile.FileID
		touched[level] = struct{}{}

		if int(level) >= len(newVersion.files) {
			continue // idempotent: level never had files
		}

		files := newVersion.files[level]
		for i, fm := range files {
			if fm.FileID == fileID {
				newVersion.files[level] = slices.Delete(files, i, i+1)
				break // at most one file per ID per level
			}
		}
	}

	for _, addedFile := range edit.AddedFiles {
		level := addedFile.Level
		touched[level] = struct{}{}

		for int(level) >= len(newVersion.files) {
			newVersion.files = append(newVersion.files, nil)
		}

		newVersion.files[level] = append(newVersion.files[level], addedFile)
	}

	for level := range touched {
		// L0 overlap; ordering deferred until iterators land.
		if level == 0 {
			continue
		}
		newVersion.sortFilesAt(level)
	}

	return newVersion
}

// Files returns a copy of the files at the given level. Returns nil for
// levels above NumLevels so callers can safely range over levels without
// bounds-checking.
func (v *Version) Files(level uint32) []FileMetadata {
	if int(level) >= len(v.files) {
		return nil
	}
	return slices.Clone(v.files[level])
}

// AllFiles returns a flat slice of every file in the version, ordered by
// level ascending.
func (v *Version) AllFiles() []FileMetadata {
	totalFiles := 0
	for i := range len(v.files) {
		totalFiles += len(v.files[i])
	}

	allFiles := make([]FileMetadata, 0, totalFiles)
	for _, filesAtLevel := range v.files {
		allFiles = append(allFiles, filesAtLevel...)
	}
	return allFiles
}

// NumLevels returns the number of SSTable levels in the version.
func (v *Version) NumLevels() int {
	return len(v.files)
}

// sortFilesAt sorts files at the given level in-place by SmallestKey ascending.
// No-op if level is out of range.
func (v *Version) sortFilesAt(level uint32) {
	if int(level) >= len(v.files) {
		return
	}
	slices.SortFunc(v.files[level], func(a, b FileMetadata) int {
		return a.SmallestKey.Compare(b.SmallestKey)
	})
}
