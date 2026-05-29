// Package main provides the manifest_dump CLI tool for inspecting MANIFEST files.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/aalhour/beachdb/internal/keys"
	"github.com/aalhour/beachdb/internal/manifest"
	"github.com/aalhour/beachdb/internal/record"
)

// main is the entry point. It dumps the live manifest of the database
// directory named by the single positional argument.
func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: manifest_dump <db-dir>\n")
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}

	if err := dumpManifest(flag.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// dumpManifest reads CURRENT in dir, replays the manifest it points at, prints
// each VersionEdit and the final Version summary. A missing CURRENT is treated
// as a fresh database (not an error). A corrupt manifest prints the failing
// edit and returns an error so the process exits non-zero.
func dumpManifest(dir string) error {
	name, err := manifest.ReadCurrent(dir)
	if errors.Is(err, manifest.ErrNoCurrentFile) {
		fmt.Printf("No CURRENT file in %s — fresh database, nothing to dump.\n", dir)
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading CURRENT: %w", err)
	}

	manifestPath := filepath.Join(dir, name)
	fmt.Printf("Manifest: %s\n", name)
	fmt.Printf("Path:     %s\n\n", manifestPath)

	reader, err := manifest.NewReader(manifestPath)
	if err != nil {
		return fmt.Errorf("opening manifest: %w", err)
	}
	defer reader.Close()

	version := manifest.NewVersion(0)
	editIdx := 0
	var corruptErr error

	for ; ; editIdx++ {
		edit, err := reader.NextEdit()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, record.ErrTruncated) {
			fmt.Printf("Edit #%d: incomplete trailing record (crash mid-append, recoverable)\n\n", editIdx)
			break
		}
		if err != nil {
			corruptErr = fmt.Errorf("corrupt manifest at edit #%d: %w", editIdx, err)
			break
		}

		printEdit(editIdx, edit)
		version = version.Apply(edit)
	}

	printVersion(version)

	if corruptErr != nil {
		fmt.Printf("\nLast valid edit: #%d\n", editIdx-1)
		return corruptErr
	}
	return nil
}

// printEdit renders a single VersionEdit in human-readable form, emitting only
// the fields the edit actually sets.
func printEdit(idx int, edit *manifest.VersionEdit) {
	fmt.Printf("Edit #%d:\n", idx)
	if edit.HasNextFileID {
		fmt.Printf("  next_file_id:  %d\n", edit.NextFileID)
	}
	if edit.HasLastSequence {
		fmt.Printf("  last_sequence: %d\n", edit.LastSequence)
	}
	if edit.HasLogNumber {
		fmt.Printf("  log_number:    %d\n", edit.LogNumber)
	}
	for _, f := range edit.AddedFiles {
		fmt.Printf("  add_file:    level=%d id=%d size=%d smallest=%q largest=%q\n",
			f.Level, f.FileID, f.Size, formatInternalKey(f.SmallestKey), formatInternalKey(f.LargestKey))
	}
	for _, d := range edit.DeletedFiles {
		fmt.Printf("  delete_file: level=%d id=%d\n", d.Level, d.FileID)
	}
}

// printVersion renders the final replayed Version: one block per non-empty
// level, listing each file's id, key range, and size.
func printVersion(version *manifest.Version) {
	fmt.Println("Current Version:")

	levels := version.NumLevels()
	if levels == 0 {
		fmt.Println("  (empty — no files)")
		return
	}

	for level := range levels {
		files := version.Files(uint32(level)) //nolint:gosec // level is a small non-negative loop index
		if len(files) == 0 {
			continue
		}

		var total uint64
		for _, f := range files {
			total += f.Size
		}

		fmt.Printf("  Level %d: %d files (%d bytes total)\n", level, len(files), total)
		for _, f := range files {
			fmt.Printf("    [%d] %s..%s (%d bytes)\n",
				f.FileID, string(f.SmallestKey.UserKey), string(f.LargestKey.UserKey), f.Size)
		}
	}
}

// formatInternalKey renders an InternalKey as "<userkey>/<seqno>/<kind>",
// matching the convention used by sst_dump.
func formatInternalKey(k keys.InternalKey) string {
	kind := "Put"
	if k.Kind == keys.InternalKeyKindDelete {
		kind = "Delete"
	}
	return fmt.Sprintf("%s/%d/%s", string(k.UserKey), k.Seqno, kind)
}
