package fs

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSyncDir_ValidDirectory(t *testing.T) {
	dir := t.TempDir()

	if err := SyncDir(dir); err != nil {
		t.Fatalf("SyncDir on valid directory: %v", err)
	}
}

func TestSyncDir_WithNewFile(t *testing.T) {
	dir := t.TempDir()

	// Create a file inside the directory (simulates WAL creation)
	path := filepath.Join(dir, "test.wal")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	// Sync the directory — this is the operation we're testing
	if err := SyncDir(dir); err != nil {
		t.Fatalf("SyncDir after file creation: %v", err)
	}
}

func TestSyncDir_NonexistentPath(t *testing.T) {
	err := SyncDir("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
}

func TestSyncDir_FileNotDirectory(t *testing.T) {
	dir := t.TempDir()

	// Create a regular file and try to SyncDir on it
	path := filepath.Join(dir, "not-a-dir.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	// SyncDir on a regular file should still succeed —
	// os.Open + Sync works on files too. This test documents
	// the behavior: SyncDir doesn't validate that path is a directory.
	// If you want to enforce directory-only, add an os.Stat check.
	err := SyncDir(path)
	if err != nil {
		t.Logf("SyncDir on regular file returned: %v (platform-dependent)", err)
	}
}

func TestMkdirAllAndSync_CreatesNestedDirectories(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a", "b", "c")

	if err := MkdirAllAndSync(target); err != nil {
		t.Fatalf("MkdirAllAndSync failed: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("target is not a directory: %s", target)
	}
}

func TestMkdirAllAndSync_ExistingDirectory(t *testing.T) {
	dir := t.TempDir()

	if err := MkdirAllAndSync(dir); err != nil {
		t.Fatalf("MkdirAllAndSync on existing directory failed: %v", err)
	}
}

func TestMkdirAllAndSync_IdempotentNestedPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "path", "db")

	if err := MkdirAllAndSync(target); err != nil {
		t.Fatalf("first MkdirAllAndSync failed: %v", err)
	}
	if err := MkdirAllAndSync(target); err != nil {
		t.Fatalf("second MkdirAllAndSync failed: %v", err)
	}
}

func TestMkdirAllAndSync_PathComponentIsFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatalf("creating blocking file: %v", err)
	}

	target := filepath.Join(file, "child")
	if err := MkdirAllAndSync(target); err == nil {
		t.Fatal("expected error when path component is a file, got nil")
	}
}

func TestMissingDirs_ReturnsCreationOrder(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a", "b", "c")

	got, err := missingDirs(target)
	if err != nil {
		t.Fatalf("missingDirs failed: %v", err)
	}

	want := []string{
		filepath.Join(root, "a"),
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a", "b", "c"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("wrong creation order: got=%v want=%v", got, want)
	}
}

func TestMissingDirs_ExistingPathReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	got, err := missingDirs(dir)
	if err != nil {
		t.Fatalf("missingDirs failed on existing path: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no missing directories, got %v", got)
	}
}

func TestMissingDirs_PathComponentIsFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatalf("creating file: %v", err)
	}

	target := filepath.Join(file, "child")
	if _, err := missingDirs(target); err == nil {
		t.Fatal("expected error when path component is a file, got nil")
	}
}

// --- Benchmarks ---

func BenchmarkSyncDir(b *testing.B) {
	dir := b.TempDir()
	// Create a file in the directory to simulate typical usage
	if err := os.WriteFile(filepath.Join(dir, "test.wal"), []byte("data"), 0600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := SyncDir(dir); err != nil {
			b.Fatalf("SyncDir: %v", err)
		}
	}
}

func BenchmarkMkdirAllAndSync(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		dir := filepath.Join(b.TempDir(), "sub", "dir")
		b.StartTimer()
		if err := MkdirAllAndSync(dir); err != nil {
			b.Fatalf("MkdirAllAndSync: %v", err)
		}
	}
}

func BenchmarkMissingDirs(b *testing.B) {
	// A path where most parents already exist
	root := b.TempDir()
	existing := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(existing, 0750); err != nil {
		b.Fatal(err)
	}
	target := filepath.Join(existing, "c", "d", "e")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = missingDirs(target)
	}
}
