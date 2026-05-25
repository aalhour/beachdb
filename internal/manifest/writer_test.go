package manifest

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aalhour/beachdb/internal/record"
)

// tempManifestPath returns a fresh path under t.TempDir() suitable for a
// MANIFEST file. The directory exists but the file does not.
func tempManifestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "MANIFEST-000001")
}

// readAllRecords walks the file at path and returns the decoded payload of
// every record in order. Fails the test on any header decode or checksum
// mismatch. Validates the durability + framing claims of Append end-to-end
// without depending on the (still stubbed) Reader.
func readAllRecords(t *testing.T, path string) [][]byte {
	t.Helper()
	//nolint:gosec // G304: path is from t.TempDir() which is safe.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var payloads [][]byte
	off := 0
	for off < len(data) {
		if off+record.HeaderSize > len(data) {
			t.Fatalf("trailing bytes at offset %d: %d bytes left, < HeaderSize=%d",
				off, len(data)-off, record.HeaderSize)
		}
		payloadLen, crc, err := DecodeRecordHeader(data[off : off+record.HeaderSize])
		if err != nil {
			t.Fatalf("decode header at offset %d: %v", off, err)
		}
		off += record.HeaderSize
		end := off + int(payloadLen)
		if end > len(data) {
			t.Fatalf("payload past EOF: header at %d wants %d bytes, file has %d",
				off-record.HeaderSize, payloadLen, len(data)-off+record.HeaderSize)
		}
		payload := data[off:end]
		if err := ValidateRecord(payload, crc); err != nil {
			t.Fatalf("validate record at offset %d: %v", off-record.HeaderSize, err)
		}
		payloads = append(payloads, append([]byte(nil), payload...))
		off = end
	}
	return payloads
}

func TestNewWriter(t *testing.T) {
	t.Run("creates new file at path", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		defer w.Close()

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Size() != 0 {
			t.Errorf("new file size = %d, want 0", info.Size())
		}
	})

	t.Run("reopens existing file in append mode preserving content", func(t *testing.T) {
		path := tempManifestPath(t)

		w1, err := NewWriter(path)
		if err != nil {
			t.Fatalf("first NewWriter: %v", err)
		}
		if err := w1.Append([]byte("first")); err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := w1.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		w2, err := NewWriter(path)
		if err != nil {
			t.Fatalf("reopen NewWriter: %v", err)
		}
		defer w2.Close()
		if err := w2.Append([]byte("second")); err != nil {
			t.Fatalf("Append after reopen: %v", err)
		}

		got := readAllRecords(t, path)
		if len(got) != 2 {
			t.Fatalf("record count = %d, want 2", len(got))
		}
		if string(got[0]) != "first" || string(got[1]) != "second" {
			t.Errorf("payloads = %q, %q, want %q, %q", got[0], got[1], "first", "second")
		}
	})

	t.Run("populates struct fields", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		defer w.Close()

		if w.file == nil {
			t.Error("file field is nil")
		}
		if w.path != path {
			t.Errorf("path field = %q, want %q", w.path, path)
		}
	})
}

func TestNewWriter_InvalidPath(t *testing.T) {
	t.Run("path is an existing directory", func(t *testing.T) {
		// OpenFile with O_WRONLY against a directory must fail.
		dir := t.TempDir()
		_, err := NewWriter(dir)
		if err == nil {
			t.Fatal("expected error when path is a directory")
		}
	})

	t.Run("path under non-existent parent directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing-subdir", "MANIFEST-000001")
		_, err := NewWriter(path)
		if err == nil {
			t.Fatal("expected error when parent directory does not exist")
		}
	})
}

func TestWriter_Append(t *testing.T) {
	t.Run("single record on disk equals EncodeRecord output", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		defer w.Close()

		payload := []byte("hello manifest")
		if err := w.Append(payload); err != nil {
			t.Fatalf("Append: %v", err)
		}

		want, err := EncodeRecord(payload)
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		//nolint:gosec // G304: path is from t.TempDir() which is safe.
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("on-disk bytes != EncodeRecord output")
		}
	})

	t.Run("multi-record stream is concatenation", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		defer w.Close()

		payloads := [][]byte{
			[]byte("alpha"),
			[]byte("bravo"),
			[]byte("charlie"),
		}
		for _, p := range payloads {
			if err := w.Append(p); err != nil {
				t.Fatalf("Append(%q): %v", p, err)
			}
		}

		got := readAllRecords(t, path)
		if len(got) != len(payloads) {
			t.Fatalf("record count = %d, want %d", len(got), len(payloads))
		}
		for i := range payloads {
			if !bytes.Equal(got[i], payloads[i]) {
				t.Errorf("record[%d] = %q, want %q", i, got[i], payloads[i])
			}
		}
	})

	t.Run("records are durable before Close", func(t *testing.T) {
		// Append returns only after fsync. Read the file mid-stream (without
		// calling Close) and confirm the record is already on disk.
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		defer w.Close()

		payload := []byte("durable-on-return")
		if err := w.Append(payload); err != nil {
			t.Fatalf("Append: %v", err)
		}

		got := readAllRecords(t, path)
		if len(got) != 1 || !bytes.Equal(got[0], payload) {
			t.Errorf("mid-stream read: got %v, want one record %q", got, payload)
		}
	})

	t.Run("empty payload roundtrips", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		defer w.Close()

		if err := w.Append([]byte{}); err != nil {
			t.Fatalf("Append empty: %v", err)
		}

		got := readAllRecords(t, path)
		if len(got) != 1 {
			t.Fatalf("record count = %d, want 1", len(got))
		}
		if len(got[0]) != 0 {
			t.Errorf("empty payload decoded to %d bytes, want 0", len(got[0]))
		}
	})

	t.Run("Append after Close returns ErrWriterClosed", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		err = w.Append([]byte("post-close"))
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("Append after Close: got %v, want ErrWriterClosed", err)
		}
	})

	t.Run("Append fails when underlying file closed externally", func(t *testing.T) {
		// Sabotage path: close the *os.File directly so Write/Sync fail.
		// Confirms the wrapped error path in Append returns a non-nil error
		// rather than panicking or silently succeeding.
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		_ = w.file.Close() // bypass Writer.Close

		err = w.Append([]byte("dead-file"))
		if err == nil {
			t.Error("expected error from Append against closed underlying file")
		}
		// Don't expect a particular sentinel — Write returns os.ErrClosed,
		// wrapped under "failed to write record".
	})
}

func TestWriter_Append_Concurrent(t *testing.T) {
	// N goroutines each Append a unique payload. After all complete, every
	// record must be present exactly once and the stream must be parseable.
	// Run under `go test -race` to catch unprotected-state corruption.
	const N = 100
	path := tempManifestPath(t)
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		payload := []byte{byte(i / 256), byte(i % 256)}
		go func() {
			defer wg.Done()
			if err := w.Append(payload); err != nil {
				t.Errorf("Append: %v", err)
			}
		}()
	}
	wg.Wait()

	got := readAllRecords(t, path)
	if len(got) != N {
		t.Fatalf("record count = %d, want %d", len(got), N)
	}
	seen := make(map[[2]byte]bool, N)
	for _, p := range got {
		if len(p) != 2 {
			t.Errorf("record payload length = %d, want 2", len(p))
			continue
		}
		key := [2]byte{p[0], p[1]}
		if seen[key] {
			t.Errorf("duplicate payload %v", key)
		}
		seen[key] = true
	}
	if len(seen) != N {
		t.Errorf("unique payloads = %d, want %d", len(seen), N)
	}
}

func TestWriter_Close(t *testing.T) {
	t.Run("happy path after Append", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		if err := w.Append([]byte("payload")); err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	t.Run("double Close returns ErrWriterClosed", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}

		if err := w.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		err = w.Close()
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("second Close: got %v, want ErrWriterClosed", err)
		}
	})

	t.Run("nil receiver returns ErrWriterClosed without panic", func(t *testing.T) {
		var w *Writer
		err := w.Close()
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("got %v, want ErrWriterClosed", err)
		}
	})

	t.Run("Close fails when underlying file closed externally", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		_ = w.file.Close() // sabotage

		// Close's Sync + Close on an already-closed file should return
		// a non-nil error but still mark the writer as closed.
		err = w.Close()
		if err == nil {
			t.Error("expected error from Close against closed underlying file")
		}

		err = w.Close()
		if !errors.Is(err, ErrWriterClosed) {
			t.Errorf("subsequent Close: got %v, want ErrWriterClosed", err)
		}
	})

	t.Run("data from before Close is preserved on disk", func(t *testing.T) {
		path := tempManifestPath(t)
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("NewWriter: %v", err)
		}
		payload := []byte("survives close")
		if err := w.Append(payload); err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		got := readAllRecords(t, path)
		if len(got) != 1 || !bytes.Equal(got[0], payload) {
			t.Errorf("got %v, want one record %q", got, payload)
		}
	})
}

// --- Benchmarks ---

// BenchmarkWriter_Append measures per-Append cost across payload sizes.
// Each iteration encodes + writes + fsyncs one record, so wall time is
// dominated by Sync. b.ReportAllocs() surfaces allocator churn in the
// encoder path; the fsync cost is unrelated to allocations.
func BenchmarkWriter_Append(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"small-64B", 64},
		{"medium-1KB", 1024},
		{"large-64KB", 64 * 1024},
	}
	for _, s := range sizes {
		payload := make([]byte, s.size)
		b.Run(s.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "bench.manifest")
			w, err := NewWriter(path)
			if err != nil {
				b.Fatal(err)
			}
			defer w.Close()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := w.Append(payload); err != nil {
					b.Fatalf("Append: %v", err)
				}
			}
		})
	}
}

// BenchmarkNewWriter measures the cost of file creation + parent-dir fsync.
// This path runs once per DB open / manifest rotation, so the wall time
// matters less than the allocation profile.
func BenchmarkNewWriter(b *testing.B) {
	dir := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := filepath.Join(dir, "bench-"+itoa(i)+".manifest")
		w, err := NewWriter(path)
		if err != nil {
			b.Fatalf("NewWriter: %v", err)
		}
		_ = w.Close()
	}
}

// itoa avoids strconv in the benchmark hot path to keep allocation counts
// honest. Benchmarks should not import strconv just to build a path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
