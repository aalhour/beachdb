package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aalhour/beachdb/internal/crashhook"
)

const (
	engineCrashHelperEnv      = "BEACHDB_ENGINE_CRASH_HELPER"
	engineCrashHelperDBDirEnv = "BEACHDB_ENGINE_CRASH_HELPER_DBDIR"
	engineCrashHelperKeyEnv   = "BEACHDB_ENGINE_CRASH_HELPER_KEY"
	engineCrashHelperValueEnv = "BEACHDB_ENGINE_CRASH_HELPER_VALUE"
)

func TestEngineCrashHelperProcess(t *testing.T) {
	scenario := os.Getenv(engineCrashHelperEnv)
	if scenario == "" {
		return
	}

	dbDir := os.Getenv(engineCrashHelperDBDirEnv)
	key := []byte(os.Getenv(engineCrashHelperKeyEnv))
	value := []byte(os.Getenv(engineCrashHelperValueEnv))

	db, err := Open(dbDir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	ctx := context.Background()
	switch scenario {
	case crashhook.PointWALAfterAppend, crashhook.PointWALAfterSync:
		_ = db.Put(ctx, key, value)
	case crashhook.PointFlushAfterPublish:
		if err := db.Put(ctx, key, value); err != nil {
			t.Fatalf("put: %v", err)
		}
		_ = db.Flush()
	default:
		t.Fatalf("unsupported helper scenario %q", scenario)
	}

	t.Fatalf("helper scenario %q completed without crashing", scenario)
}

func TestDB_Write_InjectedWALSyncError(t *testing.T) {
	crashhook.ResetForTesting()
	defer crashhook.ResetForTesting()
	t.Setenv(crashhook.EnvFaultPoint, crashhook.FaultWALSyncError)

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.Put(context.Background(), []byte("alpha"), []byte("value"))
	if !errors.Is(err, crashhook.ErrInjectedWALSync) {
		t.Fatalf("put error = %v, want injected WAL sync error", err)
	}
}

func TestDB_Flush_InjectedSSTWriteError(t *testing.T) {
	crashhook.ResetForTesting()
	defer crashhook.ResetForTesting()
	t.Setenv(crashhook.EnvFaultPoint, crashhook.FaultSSTWriteError)

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := db.Put(context.Background(), []byte("alpha"), []byte("value")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Flush(); !errors.Is(err, crashhook.ErrInjectedSSTWrite) {
		t.Fatalf("flush error = %v, want injected SST write error", err)
	}
}

func TestDB_Flush_InjectedSSTPublishError(t *testing.T) {
	crashhook.ResetForTesting()
	defer crashhook.ResetForTesting()
	t.Setenv(crashhook.EnvFaultPoint, crashhook.FaultSSTPublishError)

	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := db.Put(context.Background(), []byte("alpha"), []byte("value")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Flush(); !errors.Is(err, crashhook.ErrInjectedSSTPublish) {
		t.Fatalf("flush error = %v, want injected SST publish error", err)
	}
}

func TestDB_CrashPoint_WALAfterAppend(t *testing.T) {
	t.Parallel()

	dbDir := filepath.Join(t.TempDir(), "db")
	runCrashPointScenario(t, dbDir, crashhook.PointWALAfterAppend, []byte("append-key"), []byte("append-value"))

	db, err := Open(dbDir)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()

	if _, err := db.Get(context.Background(), []byte("append-key")); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected wal_after_append key to be absent, got %v", err)
	}
}

func TestDB_CrashPoint_WALAfterSync(t *testing.T) {
	t.Parallel()

	dbDir := filepath.Join(t.TempDir(), "db")
	runCrashPointScenario(t, dbDir, crashhook.PointWALAfterSync, []byte("sync-key"), []byte("sync-value"))

	db, err := Open(dbDir)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()

	got, err := db.Get(context.Background(), []byte("sync-key"))
	if err != nil {
		t.Fatalf("get synced key: %v", err)
	}
	if string(got) != "sync-value" {
		t.Fatalf("value = %q, want %q", got, "sync-value")
	}
}

func TestDB_CrashPoint_FlushAfterPublish(t *testing.T) {
	t.Parallel()

	dbDir := filepath.Join(t.TempDir(), "db")
	runCrashPointScenario(t, dbDir, crashhook.PointFlushAfterPublish, []byte("flush-key"), []byte("flush-value"))

	db, err := Open(dbDir)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()

	got, err := db.Get(context.Background(), []byte("flush-key"))
	if err != nil {
		t.Fatalf("get flushed key: %v", err)
	}
	if string(got) != "flush-value" {
		t.Fatalf("value = %q, want %q", got, "flush-value")
	}
}

func runCrashPointScenario(t *testing.T, dbDir, scenario string, key, value []byte) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable path: %v", err)
	}

	//nolint:gosec // exe resolves to the current test binary
	cmd := exec.CommandContext(
		context.Background(),
		exe,
		"-test.run=TestEngineCrashHelperProcess",
	) //nolint:gosec // test-owned subprocess
	cmd.Env = append(os.Environ(),
		engineCrashHelperEnv+"="+scenario,
		engineCrashHelperDBDirEnv+"="+dbDir,
		engineCrashHelperKeyEnv+"="+string(key),
		engineCrashHelperValueEnv+"="+string(value),
		crashhook.EnvCrashPoint+"="+scenario,
	)
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected crash helper to exit with status, got %v", err)
	}
	if exitErr.ExitCode() != crashhook.CrashExitCode {
		t.Fatalf("exit code = %d, want %d", exitErr.ExitCode(), crashhook.CrashExitCode)
	}
}
