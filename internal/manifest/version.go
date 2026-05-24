package manifest

// Version represents an in-memory snapshot of which SSTables exist.
type Version struct {
	_ [][]FileMetadata // files[level] = sorted list of files at that level (TODO: scaffolding for upcoming work)
}

// NewVersion returns a new Version.
func NewVersion() *Version {
	return &Version{}
}

// Apply applies a single atomic version edit to the current Version.
func (v *Version) Apply(_ *VersionEdit) *Version {
	return &Version{}
}

// Files returns the list of files at a given level.
func (v *Version) Files(_ int) []FileMetadata {
	return nil
}

// AllFiles returns the list of all files in the version.
func (v *Version) AllFiles() []FileMetadata {
	return nil
}

// NumLevels returns the number of SSTable levels in the version.
func (v *Version) NumLevels() int {
	return 0
}
