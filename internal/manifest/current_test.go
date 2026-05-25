package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrent_WriteRead(t *testing.T) {
	dir := t.TempDir()
	const name = "MANIFEST-000001"

	if err := WriteCurrent(dir, name); err != nil {
		t.Fatalf("WriteCurrent: %v", err)
	}

	got, err := ReadCurrent(dir)
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	if got != name {
		t.Errorf("ReadCurrent = %q, want %q", got, name)
	}
}

func TestCurrent_Overwrite(t *testing.T) {
	dir := t.TempDir()

	if err := WriteCurrent(dir, "MANIFEST-000001"); err != nil {
		t.Fatalf("first WriteCurrent: %v", err)
	}
	if err := WriteCurrent(dir, "MANIFEST-000002"); err != nil {
		t.Fatalf("second WriteCurrent: %v", err)
	}

	got, err := ReadCurrent(dir)
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	if got != "MANIFEST-000002" {
		t.Errorf("ReadCurrent = %q, want second value", got)
	}
}

func TestCurrent_Missing(t *testing.T) {
	dir := t.TempDir()

	_, err := ReadCurrent(dir)
	if !errors.Is(err, ErrNoCurrentFile) {
		t.Errorf("ReadCurrent on empty dir: got %v, want ErrNoCurrentFile", err)
	}
}

func TestCurrent_OnDiskContents(t *testing.T) {
	// The file is human-readable: exactly "MANIFEST-NNNNNN\n", nothing else.
	// Locks down the wire format defined in docs/formats/manifest.md.
	dir := t.TempDir()
	const name = "MANIFEST-000042"

	if err := WriteCurrent(dir, name); err != nil {
		t.Fatalf("WriteCurrent: %v", err)
	}

	//nolint:gosec // G304: path is t.TempDir()
	got, err := os.ReadFile(filepath.Join(dir, currentFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := name + "\n"
	if string(got) != want {
		t.Errorf("CURRENT contents = %q, want %q", got, want)
	}
}

func TestCurrent_NoTmpFileLeftBehind(t *testing.T) {
	// After a successful WriteCurrent, CURRENT.tmp must not linger in the
	// directory — the rename step consumes it.
	dir := t.TempDir()
	if err := WriteCurrent(dir, "MANIFEST-000001"); err != nil {
		t.Fatalf("WriteCurrent: %v", err)
	}

	tmpPath := filepath.Join(dir, currentTmpFileName)
	if _, err := os.Stat(tmpPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("CURRENT.tmp still exists after WriteCurrent: %v", err)
	}
}

func TestCurrent_WriteCurrent_InvalidName(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"contains forward slash", "subdir/MANIFEST-000001"},
		{"absolute path", "/etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteCurrent(dir, tc.input)
			if !errors.Is(err, ErrInvalidManifestName) {
				t.Errorf("got %v, want ErrInvalidManifestName", err)
			}
		})
	}
}

func TestCurrent_ReadCurrent_TrimsWhitespace(t *testing.T) {
	// Spec says exactly "\n" terminated, but recovery is lenient: trim any
	// surrounding whitespace. Catches sloppy hand-written CURRENT files
	// from debugging tools without misreading the manifest name.
	dir := t.TempDir()
	//nolint:gosec // G306: 0600 fine for test fixture
	if err := os.WriteFile(filepath.Join(dir, currentFileName),
		[]byte("  MANIFEST-000007\n\n"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadCurrent(dir)
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	if got != "MANIFEST-000007" {
		t.Errorf("ReadCurrent = %q, want %q", got, "MANIFEST-000007")
	}
}

func TestCurrent_ReadCurrent_Empty(t *testing.T) {
	// CURRENT exists but is empty (corruption / mid-write race that
	// shouldn't be reachable via WriteCurrent but worth defending).
	dir := t.TempDir()
	//nolint:gosec // G306: 0600 fine for test fixture
	if err := os.WriteFile(filepath.Join(dir, currentFileName), []byte(""), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ReadCurrent(dir)
	if !errors.Is(err, ErrInvalidManifestName) {
		t.Errorf("got %v, want ErrInvalidManifestName", err)
	}
}

func TestCurrent_WriteCurrent_BadDir(t *testing.T) {
	// Writing into a non-existent directory should fail at OpenFile.
	_, err := os.Stat("/nonexistent-beachdb-dir")
	if err == nil {
		t.Skip("/nonexistent-beachdb-dir unexpectedly exists")
	}
	if err := WriteCurrent("/nonexistent-beachdb-dir", "MANIFEST-000001"); err == nil {
		t.Error("expected error writing CURRENT into non-existent dir")
	}
}

// --- Benchmarks ---

// BenchmarkWriteCurrent measures the cost of a single CURRENT install:
// open temp, write, fsync, rename, sync parent dir. Dominated by the two
// fsyncs. Allocation count is the focus; wall time is fsync latency.
func BenchmarkWriteCurrent(b *testing.B) {
	dir := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := WriteCurrent(dir, "MANIFEST-000001"); err != nil {
			b.Fatalf("WriteCurrent: %v", err)
		}
	}
}

func BenchmarkReadCurrent(b *testing.B) {
	dir := b.TempDir()
	if err := WriteCurrent(dir, "MANIFEST-000001"); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := ReadCurrent(dir); err != nil {
			b.Fatalf("ReadCurrent: %v", err)
		}
	}
}
