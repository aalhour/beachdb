package manifest

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/record"
)

// writeFile is a small helper that writes the given bytes to path. Used to
// build hand-crafted MANIFEST files for error-path tests where the Writer
// won't help (e.g. corrupted checksum, bad magic).
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// writeRecords drives a real Writer to produce a MANIFEST containing the
// given payloads, then closes it. Returns the file path. Reusing the Writer
// for the happy-path fixtures keeps reader+writer aligned on the framing.
func writeRecords(t *testing.T, payloads ...[]byte) string {
	t.Helper()
	path := tempManifestPath(t)
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, p := range payloads {
		if err := w.Append(p); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func TestNewReader(t *testing.T) {
	t.Run("opens existing file", func(t *testing.T) {
		path := writeRecords(t, []byte("hello"))
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()
	})

	t.Run("errors on non-existent file", func(t *testing.T) {
		_, err := NewReader(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil {
			t.Fatal("expected error for non-existent file")
		}
	})
}

func TestReader_Next(t *testing.T) {
	t.Run("single record roundtrip", func(t *testing.T) {
		path := writeRecords(t, []byte("hello manifest"))
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !bytes.Equal(got, []byte("hello manifest")) {
			t.Errorf("payload = %q, want %q", got, "hello manifest")
		}

		_, err = r.Next()
		if !errors.Is(err, io.EOF) {
			t.Errorf("second Next: got %v, want io.EOF", err)
		}
	})

	t.Run("multi-record stream returns each payload in order", func(t *testing.T) {
		want := [][]byte{
			[]byte("alpha"),
			[]byte("bravo"),
			[]byte("charlie"),
		}
		path := writeRecords(t, want...)
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		for i, w := range want {
			got, err := r.Next()
			if err != nil {
				t.Fatalf("Next[%d]: %v", i, err)
			}
			if !bytes.Equal(got, w) {
				t.Errorf("Next[%d] = %q, want %q", i, got, w)
			}
		}

		_, err = r.Next()
		if !errors.Is(err, io.EOF) {
			t.Errorf("trailing Next: got %v, want io.EOF", err)
		}
	})

	t.Run("empty file returns io.EOF immediately", func(t *testing.T) {
		path := writeRecords(t)
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		_, err = r.Next()
		if !errors.Is(err, io.EOF) {
			t.Errorf("got %v, want io.EOF", err)
		}
	})

	t.Run("empty payload roundtrips", func(t *testing.T) {
		path := writeRecords(t, []byte{})
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		got, err := r.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("payload len = %d, want 0", len(got))
		}
	})

	t.Run("truncated header returns record.ErrTruncated", func(t *testing.T) {
		full, err := EncodeRecord([]byte("hello"))
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		// Truncate to mid-header.
		path := tempManifestPath(t)
		writeFile(t, path, full[:record.HeaderSize-1])

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		_, err = r.Next()
		if !errors.Is(err, record.ErrTruncated) {
			t.Errorf("got %v, want record.ErrTruncated", err)
		}
	})

	t.Run("truncated payload returns record.ErrTruncated", func(t *testing.T) {
		full, err := EncodeRecord([]byte("hello manifest"))
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		// Keep full header, drop last few payload bytes.
		path := tempManifestPath(t)
		writeFile(t, path, full[:len(full)-3])

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		_, err = r.Next()
		if !errors.Is(err, record.ErrTruncated) {
			t.Errorf("got %v, want record.ErrTruncated", err)
		}
	})

	t.Run("corrupted payload returns record.ErrChecksum", func(t *testing.T) {
		full, err := EncodeRecord([]byte("hello manifest"))
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		// Flip a byte in the payload (after the header).
		corrupted := append([]byte(nil), full...)
		corrupted[record.HeaderSize] ^= 0xFF

		path := tempManifestPath(t)
		writeFile(t, path, corrupted)

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		_, err = r.Next()
		if !errors.Is(err, record.ErrChecksum) {
			t.Errorf("got %v, want record.ErrChecksum", err)
		}
	})

	t.Run("bad magic returns record.ErrBadMagic", func(t *testing.T) {
		full, err := EncodeRecord([]byte("hello manifest"))
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		// Stomp the first magic byte.
		corrupted := append([]byte(nil), full...)
		corrupted[0] ^= 0xFF

		path := tempManifestPath(t)
		writeFile(t, path, corrupted)

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		_, err = r.Next()
		if !errors.Is(err, record.ErrBadMagic) {
			t.Errorf("got %v, want record.ErrBadMagic", err)
		}
	})

	t.Run("Next after Close returns ErrReaderClosed", func(t *testing.T) {
		path := writeRecords(t, []byte("hello"))
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		_, err = r.Next()
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("got %v, want ErrReaderClosed", err)
		}
	})
}

func TestReader_NextEdit(t *testing.T) {
	t.Run("decodes a valid VersionEdit", func(t *testing.T) {
		want := &VersionEdit{
			HasNextFileID: true, NextFileID: 42,
			HasLastSequence: true, LastSequence: 100,
			AddedFiles: []FileMetadata{
				fileMeta(0, 7, 1024, putKey("a", 1), putKey("z", 2)),
			},
		}
		path := writeRecords(t, want.Encode())
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		got, err := r.NextEdit()
		if err != nil {
			t.Fatalf("NextEdit: %v", err)
		}
		assertEditsEqual(t, got, want)
	})

	t.Run("propagates VersionEdit decode errors", func(t *testing.T) {
		// Encode a record whose payload is a single unknown tag byte. The
		// framing layer passes it through (valid record), but
		// DecodeVersionEdit rejects with ErrUnknownTag.
		path := writeRecords(t, []byte{0xFF})
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		_, err = r.NextEdit()
		if !errors.Is(err, ErrUnknownTag) {
			t.Errorf("got %v, want ErrUnknownTag", err)
		}
	})

	t.Run("returns io.EOF at end of stream", func(t *testing.T) {
		path := writeRecords(t)
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		_, err = r.NextEdit()
		if !errors.Is(err, io.EOF) {
			t.Errorf("got %v, want io.EOF", err)
		}
	})
}

func TestReader_ValidOffset(t *testing.T) {
	t.Run("advances by header+payload per successful Next", func(t *testing.T) {
		payloads := [][]byte{
			[]byte("alpha"),
			[]byte("bravo"),
		}
		path := writeRecords(t, payloads...)
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		if got := r.ValidOffset(); got != 0 {
			t.Errorf("initial ValidOffset = %d, want 0", got)
		}

		expected := int64(0)
		for i, p := range payloads {
			if _, err := r.Next(); err != nil {
				t.Fatalf("Next[%d]: %v", i, err)
			}
			expected += int64(record.HeaderSize) + int64(len(p))
			if got := r.ValidOffset(); got != expected {
				t.Errorf("ValidOffset after Next[%d] = %d, want %d", i, got, expected)
			}
		}
	})

	t.Run("does not advance on truncated read", func(t *testing.T) {
		// Build [valid record][truncated record].
		valid, err := EncodeRecord([]byte("alpha"))
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		truncated, err := EncodeRecord([]byte("bravo-payload-bytes"))
		if err != nil {
			t.Fatalf("EncodeRecord: %v", err)
		}
		stream := append([]byte(nil), valid...)
		stream = append(stream, truncated[:len(truncated)-2]...)

		path := tempManifestPath(t)
		writeFile(t, path, stream)

		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		defer r.Close()

		if _, err := r.Next(); err != nil {
			t.Fatalf("first Next: %v", err)
		}
		afterFirst := r.ValidOffset()
		if afterFirst != int64(len(valid)) {
			t.Errorf("ValidOffset after valid record = %d, want %d", afterFirst, len(valid))
		}

		_, err = r.Next()
		if !errors.Is(err, record.ErrTruncated) {
			t.Fatalf("second Next: got %v, want record.ErrTruncated", err)
		}
		if got := r.ValidOffset(); got != afterFirst {
			t.Errorf("ValidOffset after truncated read = %d, want unchanged %d", got, afterFirst)
		}
	})

	t.Run("nil receiver returns zero without panic", func(t *testing.T) {
		var r *Reader
		if got := r.ValidOffset(); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
}

func TestReader_Close(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		path := writeRecords(t, []byte("x"))
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	t.Run("double Close returns ErrReaderClosed", func(t *testing.T) {
		path := writeRecords(t, []byte("x"))
		r, err := NewReader(path)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		err = r.Close()
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("got %v, want ErrReaderClosed", err)
		}
	})

	t.Run("nil receiver returns ErrReaderClosed without panic", func(t *testing.T) {
		var r *Reader
		err := r.Close()
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("got %v, want ErrReaderClosed", err)
		}
	})
}

// End-to-end: write a stream of varied VersionEdits via the Writer, read
// each back via the Reader+NextEdit, and assert byte-for-byte equality.
// Locks down the Writer/Reader framing contract.
func TestWriterReader_RoundTrip(t *testing.T) {
	edits := []*VersionEdit{
		{HasNextFileID: true, NextFileID: 1},
		{
			HasLastSequence: true, LastSequence: 42,
			DeletedFiles: []DeletedFile{{Level: 1, FileID: 9}},
		},
		{
			AddedFiles: []FileMetadata{
				fileMeta(0, 7, 1024, putKey("a", 1), putKey("z", 2)),
				fileMeta(1, 8, 2048, putKey("aa", 3), putKey("zz", 4)),
			},
		},
		{
			HasNextFileID: true, NextFileID: 100,
			HasLastSequence: true, LastSequence: 200,
			HasLogNumber: true, LogNumber: 5,
			DeletedFiles: []DeletedFile{{Level: 0, FileID: 1}},
			AddedFiles: []FileMetadata{
				fileMeta(2, 99, 4096,
					keys.InternalKey{UserKey: []byte("k"), Seqno: 1, Kind: keys.InternalKeyKindPut},
					keys.InternalKey{UserKey: []byte("z"), Seqno: 2, Kind: keys.InternalKeyKindPut},
				),
			},
		},
	}

	// Encode each via the Writer.
	payloads := make([][]byte, len(edits))
	for i, e := range edits {
		payloads[i] = e.Encode()
	}
	path := writeRecords(t, payloads...)

	r, err := NewReader(path)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	for i, want := range edits {
		got, err := r.NextEdit()
		if err != nil {
			t.Fatalf("NextEdit[%d]: %v", i, err)
		}
		assertEditsEqual(t, got, want)
	}

	if _, err := r.NextEdit(); !errors.Is(err, io.EOF) {
		t.Errorf("trailing NextEdit: got %v, want io.EOF", err)
	}
}

// --- Benchmarks ---

// BenchmarkReader_Next measures per-record read cost across payload sizes.
// Each iteration reads one record's header + payload and validates the
// checksum. b.ReportAllocs() exposes allocator churn in the read path —
// expect 2 allocs per Next (header slice + payload slice).
//
// The fixture is built once with raw EncodeRecord + os.WriteFile (skipping
// the Writer's per-record fsync, which would dominate setup cost) and reused
// across all iterations by re-opening a fresh Reader per iteration. Reader
// construction is included in the timer; it's a fixed cost that real callers
// pay once per replay session.
func BenchmarkReader_Next(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"small-64B", 64},
		{"medium-1KB", 1024},
		{"large-64KB", 64 * 1024},
	}
	for _, s := range sizes {
		b.Run(s.name, func(b *testing.B) {
			payload := make([]byte, s.size)
			rec, err := EncodeRecord(payload)
			if err != nil {
				b.Fatal(err)
			}
			path := filepath.Join(b.TempDir(), "bench.manifest")
			if err := os.WriteFile(path, rec, 0600); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				r, err := NewReader(path)
				if err != nil {
					b.Fatalf("NewReader: %v", err)
				}
				if _, err := r.Next(); err != nil {
					b.Fatalf("Next: %v", err)
				}
				_ = r.Close()
			}
		})
	}
}
