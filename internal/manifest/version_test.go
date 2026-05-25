package manifest

import (
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
)

// assertFileIDsAt fails the test if the files at level do not match the
// expected fileID sequence (order matters). Reads the version through Files()
// to exercise the defensive-copy path.
func assertFileIDsAt(t *testing.T, v *Version, level uint32, want ...uint64) {
	t.Helper()
	got := v.Files(level)
	if len(got) != len(want) {
		t.Fatalf("level %d: got %d files, want %d", level, len(got), len(want))
	}
	for i := range want {
		if got[i].FileID != want[i] {
			t.Errorf("level %d, files[%d].FileID: got %d, want %d",
				level, i, got[i].FileID, want[i])
		}
	}
}

func TestNewVersion(t *testing.T) {
	t.Run("zero capacity is valid and starts empty", func(t *testing.T) {
		v := NewVersion(0)
		if v.NumLevels() != 0 {
			t.Errorf("NumLevels = %d, want 0", v.NumLevels())
		}
		if len(v.AllFiles()) != 0 {
			t.Errorf("AllFiles = %v, want empty", v.AllFiles())
		}
	})

	t.Run("positive capacity hint does not pre-fill levels", func(t *testing.T) {
		v := NewVersion(7)
		if v.NumLevels() != 0 {
			t.Errorf("NumLevels = %d, want 0 (capacity is a hint, not length)", v.NumLevels())
		}
	})
}

func TestVersion_Clone(t *testing.T) {
	build := func() *Version {
		v := NewVersion(0)
		return v.Apply(&VersionEdit{
			AddedFiles: []FileMetadata{
				fileMeta(0, 1, 100, putKey("a", 1), putKey("b", 2)),
				fileMeta(1, 2, 200, putKey("c", 3), putKey("d", 4)),
			},
		})
	}

	t.Run("empty version clones to empty", func(t *testing.T) {
		v := NewVersion(0)
		c := v.Clone()
		if c.NumLevels() != 0 {
			t.Errorf("clone.NumLevels = %d, want 0", c.NumLevels())
		}
	})

	t.Run("clone has equal AllFiles", func(t *testing.T) {
		v := build()
		c := v.Clone()
		vAll := v.AllFiles()
		cAll := c.AllFiles()
		if len(vAll) != len(cAll) {
			t.Fatalf("AllFiles len: original=%d clone=%d", len(vAll), len(cAll))
		}
		for i := range vAll {
			if vAll[i].FileID != cAll[i].FileID {
				t.Errorf("AllFiles[%d].FileID: original=%d clone=%d",
					i, vAll[i].FileID, cAll[i].FileID)
			}
		}
	})

	t.Run("mutating clone does not affect original", func(t *testing.T) {
		v := build()
		c := v.Clone()
		c.files[0][0].FileID = 999
		if v.files[0][0].FileID == 999 {
			t.Error("mutating clone bled into original")
		}
	})

	t.Run("mutating original does not affect clone", func(t *testing.T) {
		v := build()
		c := v.Clone()
		v.files[0][0].FileID = 888
		if c.files[0][0].FileID == 888 {
			t.Error("mutating original bled into clone")
		}
	})
}

func TestVersion_Apply_Empty(t *testing.T) {
	v := NewVersion(0).Apply(&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 100, putKey("a", 1), putKey("b", 2)),
		},
	})

	v2 := v.Apply(&VersionEdit{})
	if v2.NumLevels() != v.NumLevels() {
		t.Errorf("NumLevels diverged after empty Apply: got %d, want %d",
			v2.NumLevels(), v.NumLevels())
	}
	assertFileIDsAt(t, v2, 0, 1)
}

func TestVersion_Apply_AddedFiles(t *testing.T) {
	t.Run("adds at L0 preserve insertion order (no sort)", func(t *testing.T) {
		v := NewVersion(0).Apply(&VersionEdit{
			AddedFiles: []FileMetadata{
				fileMeta(0, 3, 100, putKey("c", 1), putKey("cz", 2)),
				fileMeta(0, 1, 100, putKey("a", 3), putKey("az", 4)),
				fileMeta(0, 2, 100, putKey("b", 5), putKey("bz", 6)),
			},
		})
		assertFileIDsAt(t, v, 0, 3, 1, 2) // insertion order, not key order
	})

	t.Run("adds at L2 gap-fill empty L0 and L1", func(t *testing.T) {
		v := NewVersion(0).Apply(&VersionEdit{
			AddedFiles: []FileMetadata{
				fileMeta(2, 1, 100, putKey("a", 1), putKey("z", 2)),
			},
		})
		if v.NumLevels() != 3 {
			t.Errorf("NumLevels = %d, want 3 (L0, L1 nil; L2 has the file)", v.NumLevels())
		}
		if len(v.Files(0)) != 0 || len(v.Files(1)) != 0 {
			t.Error("gap-fill levels should be empty")
		}
		assertFileIDsAt(t, v, 2, 1)
	})

	t.Run("adds at L1 sort by SmallestKey ascending", func(t *testing.T) {
		v := NewVersion(0).Apply(&VersionEdit{
			AddedFiles: []FileMetadata{
				fileMeta(1, 30, 100, putKey("c", 1), putKey("cz", 2)),
				fileMeta(1, 10, 100, putKey("a", 3), putKey("az", 4)),
				fileMeta(1, 20, 100, putKey("b", 5), putKey("bz", 6)),
			},
		})
		assertFileIDsAt(t, v, 1, 10, 20, 30) // sorted by SmallestKey: a, b, c
	})
}

func TestVersion_Apply_DeletedFiles(t *testing.T) {
	seed := &VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(1, 1, 100, putKey("a", 1), putKey("az", 2)),
			fileMeta(1, 2, 100, putKey("b", 3), putKey("bz", 4)),
			fileMeta(1, 3, 100, putKey("c", 5), putKey("cz", 6)),
		},
	}

	t.Run("delete existing file", func(t *testing.T) {
		v := NewVersion(0).Apply(seed)
		v2 := v.Apply(&VersionEdit{
			DeletedFiles: []DeletedFile{{Level: 1, FileID: 2}},
		})
		assertFileIDsAt(t, v2, 1, 1, 3)
	})

	t.Run("delete non-existent fileID is idempotent", func(t *testing.T) {
		v := NewVersion(0).Apply(seed)
		v2 := v.Apply(&VersionEdit{
			DeletedFiles: []DeletedFile{{Level: 1, FileID: 999}},
		})
		assertFileIDsAt(t, v2, 1, 1, 2, 3)
	})

	t.Run("delete from level beyond NumLevels is idempotent", func(t *testing.T) {
		v := NewVersion(0).Apply(seed)
		v2 := v.Apply(&VersionEdit{
			DeletedFiles: []DeletedFile{{Level: 99, FileID: 1}},
		})
		assertFileIDsAt(t, v2, 1, 1, 2, 3)
	})
}

func TestVersion_Apply_Combined(t *testing.T) {
	v := NewVersion(0).Apply(&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(1, 1, 100, putKey("a", 1), putKey("az", 2)),
			fileMeta(1, 2, 100, putKey("b", 3), putKey("bz", 4)),
		},
	})

	v2 := v.Apply(&VersionEdit{
		DeletedFiles: []DeletedFile{{Level: 1, FileID: 1}},
		AddedFiles: []FileMetadata{
			fileMeta(1, 3, 100, putKey("ab", 5), putKey("abz", 6)),
		},
	})

	// L1 should now hold {2, 3} sorted by SmallestKey: "ab" < "b" → fileIDs [3, 2].
	assertFileIDsAt(t, v2, 1, 3, 2)
}

func TestVersion_Apply_Immutability(t *testing.T) {
	v := NewVersion(0).Apply(&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 100, putKey("a", 1), putKey("b", 2)),
			fileMeta(1, 2, 100, putKey("c", 3), putKey("d", 4)),
		},
	})

	before := v.AllFiles()
	_ = v.Apply(&VersionEdit{
		DeletedFiles: []DeletedFile{{Level: 0, FileID: 1}},
		AddedFiles: []FileMetadata{
			fileMeta(1, 9, 100, putKey("e", 5), putKey("f", 6)),
		},
	})
	after := v.AllFiles()

	if len(before) != len(after) {
		t.Fatalf("Apply mutated original: before=%d files, after=%d files",
			len(before), len(after))
	}
	for i := range before {
		if before[i].FileID != after[i].FileID {
			t.Errorf("AllFiles[%d].FileID mutated: before=%d, after=%d",
				i, before[i].FileID, after[i].FileID)
		}
	}
}

func TestVersion_Apply_L0NotSortedByKey(t *testing.T) {
	// L0 files overlap; ordering by key is meaningless. Verify the sort step
	// is skipped for L0 by adding three files in descending key order and
	// asserting insertion order is preserved.
	v := NewVersion(0).Apply(&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 100, putKey("z", 1), putKey("zz", 2)),
			fileMeta(0, 2, 100, putKey("m", 3), putKey("mz", 4)),
			fileMeta(0, 3, 100, putKey("a", 5), putKey("az", 6)),
		},
	})
	assertFileIDsAt(t, v, 0, 1, 2, 3) // insertion order, not sorted
}

func TestVersion_Apply_SortInvariant_AfterDelete(t *testing.T) {
	v := NewVersion(0).Apply(&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(1, 10, 100, putKey("a", 1), putKey("az", 2)),
			fileMeta(1, 20, 100, putKey("b", 3), putKey("bz", 4)),
			fileMeta(1, 30, 100, putKey("c", 5), putKey("cz", 6)),
		},
	})

	v2 := v.Apply(&VersionEdit{
		DeletedFiles: []DeletedFile{{Level: 1, FileID: 20}},
	})

	// Remaining files were already sorted; sort-on-touched is a no-op.
	got := v2.Files(1)
	for i := 1; i < len(got); i++ {
		if got[i-1].SmallestKey.Compare(got[i].SmallestKey) > 0 {
			t.Errorf("sort invariant broken at L1: %s > %s",
				got[i-1].SmallestKey.UserKey, got[i].SmallestKey.UserKey)
		}
	}
}

func TestVersion_Files(t *testing.T) {
	v := NewVersion(0).Apply(&VersionEdit{
		AddedFiles: []FileMetadata{
			fileMeta(0, 1, 100, putKey("a", 1), putKey("b", 2)),
		},
	})

	t.Run("existing level returns files", func(t *testing.T) {
		got := v.Files(0)
		if len(got) != 1 || got[0].FileID != 1 {
			t.Errorf("Files(0) = %+v, want one file with FileID=1", got)
		}
	})

	t.Run("level beyond NumLevels returns nil", func(t *testing.T) {
		got := v.Files(99)
		if got != nil {
			t.Errorf("Files(99) = %v, want nil", got)
		}
	})

	t.Run("returned slice is a defensive copy", func(t *testing.T) {
		got := v.Files(0)
		got[0].FileID = 12345
		again := v.Files(0)
		if again[0].FileID == 12345 {
			t.Error("mutating returned slice leaked into Version internals")
		}
	})
}

func TestVersion_AllFiles(t *testing.T) {
	t.Run("empty Version returns empty slice", func(t *testing.T) {
		v := NewVersion(0)
		got := v.AllFiles()
		if len(got) != 0 {
			t.Errorf("AllFiles = %v, want empty", got)
		}
	})

	t.Run("multi-level concatenates ordered by level ascending", func(t *testing.T) {
		v := NewVersion(0).Apply(&VersionEdit{
			AddedFiles: []FileMetadata{
				fileMeta(2, 200, 100, putKey("a", 1), putKey("z", 2)),
				fileMeta(0, 1, 100, putKey("a", 3), putKey("z", 4)),
				fileMeta(0, 2, 100, putKey("a", 5), putKey("z", 6)),
				fileMeta(1, 100, 100, putKey("a", 7), putKey("z", 8)),
			},
		})
		got := v.AllFiles()
		wantOrder := []uint64{1, 2, 100, 200}
		if len(got) != len(wantOrder) {
			t.Fatalf("AllFiles len = %d, want %d", len(got), len(wantOrder))
		}
		for i, fid := range wantOrder {
			if got[i].FileID != fid {
				t.Errorf("AllFiles[%d].FileID = %d, want %d (level-ascending order)",
					i, got[i].FileID, fid)
			}
		}
	})
}

func TestVersion_NumLevels(t *testing.T) {
	t.Run("empty Version", func(t *testing.T) {
		v := NewVersion(0)
		if v.NumLevels() != 0 {
			t.Errorf("NumLevels = %d, want 0", v.NumLevels())
		}
	})

	t.Run("after Apply adding at L3 reports 4 levels", func(t *testing.T) {
		v := NewVersion(0).Apply(&VersionEdit{
			AddedFiles: []FileMetadata{
				fileMeta(3, 1, 100,
					keys.InternalKey{UserKey: []byte("a"), Seqno: 1, Kind: keys.InternalKeyKindPut},
					keys.InternalKey{UserKey: []byte("b"), Seqno: 2, Kind: keys.InternalKeyKindPut},
				),
			},
		})
		if v.NumLevels() != 4 {
			t.Errorf("NumLevels = %d, want 4", v.NumLevels())
		}
	})
}
