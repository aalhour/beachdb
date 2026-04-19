package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aalhour/beachdb/engine"
	"github.com/aalhour/beachdb/internal/crashhook"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "worker" {
		if err := workerCommand(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestOperationMessage_RoundTripBinarySafe(t *testing.T) {
	t.Parallel()

	original := operation{
		ID:   7,
		Kind: opBatch,
		Batch: []batchItem{
			{Kind: opPut, Key: []byte{0x00, 0x0a, 0xff}, Value: []byte{0x01, 0x02}},
			{Kind: opDelete, Key: []byte{0x0a, 0x0b}},
		},
	}

	roundTripped, err := original.toMessage().toOperation()
	if err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if !reflect.DeepEqual(roundTripped, original) {
		t.Fatalf("round-tripped operation mismatch:\n got: %#v\nwant: %#v", roundTripped, original)
	}
}

func TestOperationMessage_RejectsMalformedBase64(t *testing.T) {
	t.Parallel()

	_, err := (operationMessage{
		Kind:   opPut,
		OpID:   1,
		KeyB64: "%%%not-base64%%%",
	}).toOperation()
	if err == nil {
		t.Fatal("expected malformed base64 decode to fail")
	}
}

func TestGenerateWorkload_Deterministic(t *testing.T) {
	t.Parallel()

	cfg := defaultRunConfig()
	cfg.Seed = 1234
	cfg.Ops = 12

	first := generateWorkload(cfg)
	second := generateWorkload(cfg)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("expected workload generation to be deterministic for a fixed seed")
	}

	for _, op := range first {
		for _, item := range op.Batch {
			if item.Kind != opPut && item.Kind != opDelete {
				t.Fatalf("unexpected batch item kind %q", item.Kind)
			}
		}
	}
}

func TestVerifyDatabaseState_AllowsPendingOperationOutcome(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "db")
	db, err := engine.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	ctx := context.Background()
	if err := db.Put(ctx, []byte("alpha"), []byte("present")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	base := newOracle()
	pending := &operation{ID: 1, Kind: opPut, Key: []byte("alpha"), Value: []byte("present")}
	result, failure, err := verifyDatabaseState(ctx, dir, base, pending)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if failure != nil {
		t.Fatalf("unexpected verification failure: %+v", failure)
	}
	if result.CheckedKeys != 1 {
		t.Fatalf("checked keys = %d, want 1", result.CheckedKeys)
	}
	if !reflect.DeepEqual(result.Allowed, []int{1}) {
		t.Fatalf("allowed ops = %v, want [1]", result.Allowed)
	}
}

func TestWorker_ProtocolLifecycle(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable path: %v", err)
	}

	dbDir := filepath.Join(t.TempDir(), "db")
	//nolint:gosec // exe resolves to the current test binary
	cmd := exec.CommandContext(
		context.Background(),
		exe,
		"worker",
		"--dbdir="+dbDir,
	) //nolint:gosec // test-owned subprocess
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}

	dec := json.NewDecoder(stdout)
	expectEvent := func(want eventKind) eventMessage {
		t.Helper()
		var event eventMessage
		if err := dec.Decode(&event); err != nil {
			t.Fatalf("decode event %q: %v", want, err)
		}
		if event.Kind != want {
			t.Fatalf("event kind = %q, want %q", event.Kind, want)
		}
		return event
	}

	expectEvent(eventReady)
	op := operation{ID: 3, Kind: opPut, Key: []byte{0x00, 0x0a, 0xff}, Value: []byte("value")}
	if _, err := stdin.Write(mustEncodeNDJSON(t, op.toMessage())); err != nil {
		t.Fatalf("write operation: %v", err)
	}
	if got := expectEvent(eventStart); got.OpID != op.ID {
		t.Fatalf("start event op_id = %d, want %d", got.OpID, op.ID)
	}
	if got := expectEvent(eventAck); got.OpID != op.ID {
		t.Fatalf("ack event op_id = %d, want %d", got.OpID, op.ID)
	}

	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait worker: %v", err)
	}

	db, err := engine.Open(dbDir)
	if err != nil {
		t.Fatalf("open db for verification: %v", err)
	}
	defer db.Close()

	got, err := db.Get(context.Background(), op.Key)
	if err != nil {
		t.Fatalf("get key: %v", err)
	}
	if string(got) != "value" {
		t.Fatalf("value = %q, want %q", got, "value")
	}
}

func TestController_RunCycle_TracksPendingCrashPoint(t *testing.T) {
	t.Parallel()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable path: %v", err)
	}

	dbDir := filepath.Join(t.TempDir(), "db")
	artDir := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(artDir, 0o750); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}

	cfg := defaultRunConfig()
	cfg.DBDir = dbDir
	cfg.ArtifactDir = artDir
	cfg.Cycles = 1
	cfg.Ops = 1
	cfg.CrashPoint = crashhook.PointWALAfterAppend
	cfg.workerBinary = exe

	ops := []operation{{ID: 0, Kind: opPut, Key: []byte("k"), Value: []byte("v")}}
	ctrl := &controller{
		cfg:          cfg,
		artifactPath: filepath.Join(artDir, "artifact.json"),
		artifact:     newArtifact(cfg, ops),
		ops:          ops,
		statuses:     []opStatus{opStatusPlanned},
		retryIndex:   -1,
		lastAckedID:  -1,
		oracle:       newOracle(),
	}

	record, pending, err := ctrl.runCycle(context.Background(), 0, 250)
	if err != nil {
		t.Fatalf("run cycle: %v", err)
	}
	if pending == nil || pending.ID != 0 {
		t.Fatalf("pending op = %+v, want op 0 pending", pending)
	}
	if record.ExitCode != crashhook.CrashExitCode {
		t.Fatalf("exit code = %d, want %d", record.ExitCode, crashhook.CrashExitCode)
	}
	if record.CrashPoint != crashhook.PointWALAfterAppend {
		t.Fatalf("crash point = %q, want %q", record.CrashPoint, crashhook.PointWALAfterAppend)
	}

	result, failure, err := verifyDatabaseState(context.Background(), dbDir, ctrl.oracle, pending)
	if err != nil {
		t.Fatalf("verify state: %v", err)
	}
	if failure != nil {
		t.Fatalf("unexpected verification failure: %+v", failure)
	}
	if result.CheckedKeys != 1 {
		t.Fatalf("checked keys = %d, want 1", result.CheckedKeys)
	}
}

func mustEncodeNDJSON(t *testing.T, v any) []byte {
	t.Helper()

	data, err := encodeNDJSON(v)
	if err != nil {
		t.Fatalf("encode ndjson: %v", err)
	}
	return data
}
