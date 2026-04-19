package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// artifactVersion is the JSON schema version for persisted crash artifacts.
const artifactVersion = 1

// artifact captures a full run or replay transcript for deterministic analysis.
type artifact struct {
	Version       int                  `json:"version"`
	Config        artifactConfig       `json:"config"`
	Seed          uint64               `json:"seed"`
	Ops           []operationMessage   `json:"ops"`
	Cycles        []artifactCycle      `json:"cycles"`
	Events        []artifactEvent      `json:"events"`
	LastAckedOpID int                  `json:"last_acked_op_id,omitempty"`
	Failure       *verificationFailure `json:"failure,omitempty"`
}

// artifactConfig records the effective run configuration embedded in an artifact.
type artifactConfig struct {
	DBDir            string `json:"dbdir"`
	Cycles           int    `json:"cycles"`
	MinDelayMS       int    `json:"min_delay_ms"`
	MaxDelayMS       int    `json:"max_delay_ms"`
	Ops              int    `json:"ops"`
	PutRatio         int    `json:"put_ratio"`
	DeleteRatio      int    `json:"delete_ratio"`
	BatchRatio       int    `json:"batch_ratio"`
	MaxKeyLen        int    `json:"max_key_len"`
	MaxValueLen      int    `json:"max_value_len"`
	HotKeyRatio      int    `json:"hot_key_ratio"`
	VerifyEveryCycle bool   `json:"verify_every_cycle"`
	Profile          string `json:"profile"`
	CrashPoint       string `json:"crash_point,omitempty"`
	FaultPoint       string `json:"fault_point,omitempty"`
}

// artifactCycle stores cycle-level worker execution and verification metadata.
type artifactCycle struct {
	Index              int                  `json:"index"`
	WorkerPID          int                  `json:"worker_pid"`
	PlannedKillDelayMS int                  `json:"planned_kill_delay_ms"`
	ActualEndUnixMilli int64                `json:"actual_end_unix_ms"`
	ExitCode           int                  `json:"exit_code"`
	LastEvent          *artifactEventRef    `json:"last_event,omitempty"`
	Verification       artifactVerification `json:"verification"`
	CrashPoint         string               `json:"crash_point,omitempty"`
	FaultPoint         string               `json:"fault_point,omitempty"`
}

// artifactVerification stores verification summary data for one cycle.
type artifactVerification struct {
	CheckedKeys int   `json:"checked_keys"`
	Allowed     []int `json:"allowed_optional_ops,omitempty"`
}

// artifactEventRef points to the last event seen for a cycle.
type artifactEventRef struct {
	Kind eventKind `json:"kind"`
	OpID int       `json:"op_id,omitempty"`
}

// artifactEvent is one timestamped worker/controller protocol event.
type artifactEvent struct {
	Cycle int          `json:"cycle"`
	Time  int64        `json:"time_unix_ms"`
	Event eventMessage `json:"event"`
}

// newArtifact constructs an artifact with encoded operations and config metadata.
func newArtifact(cfg runConfig, ops []operation) *artifact {
	encodedOps := make([]operationMessage, len(ops))
	for i, op := range ops {
		encodedOps[i] = op.toMessage()
	}

	return &artifact{
		Version: artifactVersion,
		Config: artifactConfig{
			DBDir:            cfg.DBDir,
			Cycles:           cfg.Cycles,
			MinDelayMS:       cfg.MinDelayMS,
			MaxDelayMS:       cfg.MaxDelayMS,
			Ops:              cfg.Ops,
			PutRatio:         cfg.PutRatio,
			DeleteRatio:      cfg.DeleteRatio,
			BatchRatio:       cfg.BatchRatio,
			MaxKeyLen:        cfg.MaxKeyLen,
			MaxValueLen:      cfg.MaxValueLen,
			HotKeyRatio:      cfg.HotKeyRatio,
			VerifyEveryCycle: cfg.VerifyEveryCycle,
			Profile:          cfg.Profile,
			CrashPoint:       cfg.CrashPoint,
			FaultPoint:       cfg.FaultPoint,
		},
		Seed: cfg.Seed,
		Ops:  encodedOps,
	}
}

// createArtifactPath returns a timestamped artifact filename in the given directory.
func createArtifactPath(dir string) string {
	return filepath.Join(dir, fmt.Sprintf("crash-%s.json", time.Now().UTC().Format("20060102T150405.000Z")))
}

// loadArtifact reads and validates an artifact file and decodes its operations.
func loadArtifact(path string) (*artifact, []operation, error) {
	//nolint:gosec // artifact path is an explicit user-selected local file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading artifact: %w", err)
	}

	var art artifact
	if err := json.Unmarshal(data, &art); err != nil {
		return nil, nil, fmt.Errorf("decoding artifact: %w", err)
	}
	if art.Version != artifactVersion {
		return nil, nil, fmt.Errorf("unsupported artifact version %d", art.Version)
	}

	ops := make([]operation, len(art.Ops))
	for i, msg := range art.Ops {
		op, err := msg.toOperation()
		if err != nil {
			return nil, nil, fmt.Errorf("decoding artifact operation %d: %w", i, err)
		}
		ops[i] = op
	}

	return &art, ops, nil
}

// saveArtifact atomically writes an artifact to disk via temp-file rename.
func saveArtifact(path string, art *artifact) error {
	data, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding artifact: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing artifact temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming artifact temp file: %w", err)
	}

	return nil
}
