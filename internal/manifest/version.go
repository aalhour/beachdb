package manifest

type Version struct {
	files [][]FileMetadata // files[level] = sorted list of files at that level
}

func NewVersion() *Version {
	return &Version{}
}

func (v *Version) Apply(edit *VersionEdit) *Version {
	return &Version{}
}

func (v *Version) Files(level int) []FileMetadata {
	return nil
}

func (v *Version) AllFiles() []FileMetadata {
	return nil
}

func (v *Version) NumLevels() int {
	return 0
}
