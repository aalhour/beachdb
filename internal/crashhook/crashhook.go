// Package crashhook exposes internal-only crash and fault injection points
// used by the crash harness and engine tests.
package crashhook

import (
	"errors"
	"os"
	"sync/atomic"
)

const (
	// EnvCrashPoint arms a crash point in the current worker process.
	EnvCrashPoint = "BEACHDB_CRASH_POINT"
	// EnvFaultPoint arms a fault point in the current worker process.
	EnvFaultPoint = "BEACHDB_FAULT_POINT"

	// PointWALAfterAppend crashes immediately after a WAL append succeeds.
	PointWALAfterAppend = "wal_after_append"
	// PointWALAfterSync crashes immediately after a WAL sync succeeds.
	PointWALAfterSync = "wal_after_sync"
	// PointFlushAfterFileSync crashes after an SSTable file and directory are durable.
	PointFlushAfterFileSync = "flush_after_file_sync"
	// PointFlushAfterPublish crashes after a flushed SSTable is published in memory.
	PointFlushAfterPublish = "flush_after_publish"
	// PointManifestAfterSSTSync crashes after the SSTable is durable but before the manifest
	// edit is appended.
	PointManifestAfterSSTSync = "manifest_after_sst_sync"
	// PointManifestAfterAppend crashes after the manifest edit is appended and synced but before
	// the in-memory version is updated.
	PointManifestAfterAppend = "manifest_after_append"

	// FaultWALSyncError injects a deterministic WAL sync failure.
	FaultWALSyncError = "wal_sync_error"
	// FaultSSTPublishError injects a deterministic SST publish failure.
	FaultSSTPublishError = "sst_publish_error"
	// FaultSSTWriteError injects a deterministic SST write failure.
	FaultSSTWriteError = "sst_write_error"

	// CrashExitCode is the process exit code used for armed crash points.
	CrashExitCode = 86
)

var (
	// ErrInjectedWALSync is returned when the WAL sync fault point is armed.
	ErrInjectedWALSync = errors.New("beachdb: injected WAL sync error")
	// ErrInjectedSSTPublish is returned when the SST publish fault point is armed.
	ErrInjectedSSTPublish = errors.New("beachdb: injected SST publish error")
	// ErrInjectedSSTWrite is returned when the SST write fault point is armed.
	ErrInjectedSSTWrite = errors.New("beachdb: injected SST write error")

	// crashConsumed ensures crash hook triggers at most once per process.
	crashConsumed atomic.Bool
	// faultConsumed ensures fault hook injects at most one error per process.
	faultConsumed atomic.Bool
	// exitFunc allows tests to replace process termination behavior.
	exitFunc = os.Exit
)

// IsCrashPoint reports whether point names a supported crash point.
func IsCrashPoint(point string) bool {
	switch point {
	case "", PointWALAfterAppend, PointWALAfterSync, PointFlushAfterFileSync, PointFlushAfterPublish,
		PointManifestAfterSSTSync, PointManifestAfterAppend:
		return true
	default:
		return false
	}
}

// IsFaultPoint reports whether point names a supported fault point.
func IsFaultPoint(point string) bool {
	switch point {
	case "", FaultWALSyncError, FaultSSTPublishError, FaultSSTWriteError:
		return true
	default:
		return false
	}
}

// CrashIfArmed terminates the current process when the named crash point is armed.
func CrashIfArmed(point string) {
	if point == "" || os.Getenv(EnvCrashPoint) != point {
		return
	}
	if !crashConsumed.CompareAndSwap(false, true) {
		return
	}
	exitFunc(CrashExitCode)
}

// MaybeFault returns an injected error once when the named fault point is armed.
func MaybeFault(point string) error {
	if point == "" || os.Getenv(EnvFaultPoint) != point {
		return nil
	}
	if !faultConsumed.CompareAndSwap(false, true) {
		return nil
	}

	switch point {
	case FaultWALSyncError:
		return ErrInjectedWALSync
	case FaultSSTPublishError:
		return ErrInjectedSSTPublish
	case FaultSSTWriteError:
		return ErrInjectedSSTWrite
	default:
		return nil
	}
}

// ResetForTesting clears one-shot hook state for in-process tests.
func ResetForTesting() {
	crashConsumed.Store(false)
	faultConsumed.Store(false)
	exitFunc = os.Exit
}
