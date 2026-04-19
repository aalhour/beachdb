package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aalhour/beachdb/internal/crashhook"
)

const (
	// defaultRun* constants define baseline "full" profile settings.
	defaultRunCycles            = 50
	defaultRunMinDelayMS        = 10
	defaultRunMaxDelayMS        = 100
	defaultRunSeed       uint64 = 424242
	defaultRunOps               = 100
	defaultPutRatio             = 60
	defaultDeleteRatio          = 20
	defaultBatchRatio           = 20
	defaultMaxKeyLen            = 32
	defaultMaxValueLen          = 128
	defaultHotKeyRatio          = 30
)

// runConfig contains validated inputs for a crash run or replay execution.
type runConfig struct {
	DBDir            string
	ArtifactDir      string
	Cycles           int
	MinDelayMS       int
	MaxDelayMS       int
	Seed             uint64
	Ops              int
	PutRatio         int
	DeleteRatio      int
	BatchRatio       int
	MaxKeyLen        int
	MaxValueLen      int
	HotKeyRatio      int
	KeepDBOnFail     bool
	VerifyEveryCycle bool
	Profile          string
	CrashPoint       string
	FaultPoint       string

	workerBinary string
}

// replayConfig contains inputs for replaying a prior crash artifact.
type replayConfig struct {
	ArtifactPath string
	DBDir        string
}

// workerConfig contains inputs required for the worker subprocess.
type workerConfig struct {
	DBDir string
}

// defaultRunConfig returns baseline run settings before profile overrides.
func defaultRunConfig() runConfig {
	return runConfig{
		Cycles:           defaultRunCycles,
		MinDelayMS:       defaultRunMinDelayMS,
		MaxDelayMS:       defaultRunMaxDelayMS,
		Seed:             defaultRunSeed,
		Ops:              defaultRunOps,
		PutRatio:         defaultPutRatio,
		DeleteRatio:      defaultDeleteRatio,
		BatchRatio:       defaultBatchRatio,
		MaxKeyLen:        defaultMaxKeyLen,
		MaxValueLen:      defaultMaxValueLen,
		HotKeyRatio:      defaultHotKeyRatio,
		VerifyEveryCycle: true,
		Profile:          profileFull,
	}
}

// applyProfileDefaults applies preset values for known profiles.
func (cfg *runConfig) applyProfileDefaults() error {
	switch cfg.Profile {
	case profileFull:
		return nil
	case profileCI:
		cfg.Cycles = 6
		cfg.MinDelayMS = 8
		cfg.MaxDelayMS = 20
		cfg.Seed = 777
		cfg.Ops = 24
		cfg.PutRatio = 60
		cfg.DeleteRatio = 20
		cfg.BatchRatio = 20
		cfg.MaxKeyLen = 16
		cfg.MaxValueLen = 64
		cfg.HotKeyRatio = 35
		cfg.VerifyEveryCycle = true
		return nil
	default:
		return fmt.Errorf("unsupported profile %q", cfg.Profile)
	}
}

// validate checks a runConfig for semantic correctness and prepared paths.
func (cfg *runConfig) validate() error {
	if err := cfg.applyProfileDefaults(); err != nil {
		return err
	}
	if err := cfg.validateCore(); err != nil {
		return err
	}
	if err := cfg.validateHooks(); err != nil {
		return err
	}
	return cfg.preparePaths()
}

// validateCore validates generic run parameters and workload ratios.
func (cfg *runConfig) validateCore() error {
	if cfg.DBDir == "" {
		return errors.New("--dbdir is required")
	}
	if cfg.ArtifactDir == "" {
		return errors.New("--artifact-dir is required")
	}
	if cfg.Cycles <= 0 {
		return errors.New("--cycles must be > 0")
	}
	if cfg.Ops <= 0 {
		return errors.New("--ops must be > 0")
	}
	if cfg.MinDelayMS < 0 {
		return errors.New("--min-delay-ms must be >= 0")
	}
	if cfg.MaxDelayMS <= 0 {
		return errors.New("--max-delay-ms must be > 0")
	}
	if cfg.MaxDelayMS < cfg.MinDelayMS {
		return errors.New("--max-delay-ms must be >= --min-delay-ms")
	}
	if cfg.MaxKeyLen <= 0 {
		return errors.New("--max-key-len must be > 0")
	}
	if cfg.MaxValueLen < 0 {
		return errors.New("--max-value-len must be >= 0")
	}
	if err := validateRatios(cfg.PutRatio, cfg.DeleteRatio, cfg.BatchRatio); err != nil {
		return err
	}
	if cfg.HotKeyRatio < 0 || cfg.HotKeyRatio > 100 {
		return errors.New("--hot-key-ratio must be between 0 and 100")
	}
	return nil
}

// validateHooks validates configured crash and fault hook points.
func (cfg *runConfig) validateHooks() error {
	if cfg.CrashPoint != "" && cfg.FaultPoint != "" {
		return errors.New("--crash-point and --fault-point are mutually exclusive")
	}
	if !crashhook.IsCrashPoint(cfg.CrashPoint) {
		return fmt.Errorf("unsupported --crash-point %q", cfg.CrashPoint)
	}
	if !crashhook.IsFaultPoint(cfg.FaultPoint) {
		return fmt.Errorf("unsupported --fault-point %q", cfg.FaultPoint)
	}
	return nil
}

// preparePaths enforces dbdir emptiness and ensures artifact directory exists.
func (cfg *runConfig) preparePaths() error {
	if err := ensureDBDirEmpty(cfg.DBDir); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ArtifactDir, 0o750); err != nil {
		return fmt.Errorf("creating artifact dir: %w", err)
	}
	return nil
}

// validate checks replay arguments and enforces an empty target db directory.
func (cfg *replayConfig) validate() error {
	if cfg.ArtifactPath == "" {
		return errors.New("--artifact is required")
	}
	if cfg.DBDir == "" {
		return errors.New("--dbdir is required")
	}
	return ensureDBDirEmpty(cfg.DBDir)
}

// validate checks worker arguments.
func (cfg *workerConfig) validate() error {
	if cfg.DBDir == "" {
		return errors.New("--dbdir is required")
	}
	return nil
}

// ensureDBDirEmpty creates dir when missing and rejects non-empty directories.
func ensureDBDirEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating db dir: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("reading db dir: %w", err)
	}

	if len(entries) != 0 {
		return fmt.Errorf("--dbdir must be empty, found existing files under %s", filepath.Clean(dir))
	}

	return nil
}

// validateRatios ensures workload mix ratios are bounded and sum to 100.
func validateRatios(putRatio, deleteRatio, batchRatio int) error {
	for _, item := range []struct {
		name  string
		value int
	}{
		{name: "--put-ratio", value: putRatio},
		{name: "--delete-ratio", value: deleteRatio},
		{name: "--batch-ratio", value: batchRatio},
	} {
		if item.value < 0 || item.value > 100 {
			return fmt.Errorf("%s must be between 0 and 100", item.name)
		}
	}

	sum := putRatio + deleteRatio + batchRatio
	if sum != 100 {
		return fmt.Errorf("operation ratios must sum to 100, got %d", sum)
	}

	return nil
}
