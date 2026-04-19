package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/aalhour/beachdb/internal/crashhook"
)

type opStatus string

const (
	// opStatus* tracks controller-side progress of each workload operation.
	opStatusPlanned opStatus = "planned"
	opStatusStarted opStatus = "started"
	opStatusAcked   opStatus = "acked"
	opStatusFailed  opStatus = "failed"
)

// controller orchestrates worker cycles, verification, and artifact emission.
type controller struct {
	cfg          runConfig
	artifactPath string
	artifact     *artifact
	ops          []operation
	statuses     []opStatus
	queueIndex   int
	retryIndex   int
	lastAckedID  int
	oracle       *oracle
}

// runCommand executes a fresh crash-harness run from CLI args.
func runCommand(args []string) error {
	cfg := defaultRunConfig()
	fs := newFlagSet("run")
	fs.StringVar(&cfg.DBDir, "dbdir", "", "Database directory (must be empty)")
	fs.StringVar(&cfg.ArtifactDir, "artifact-dir", "", "Directory to write run artifacts to")
	fs.IntVar(&cfg.Cycles, "cycles", cfg.Cycles, "Number of crash cycles to run")
	fs.IntVar(&cfg.MinDelayMS, "min-delay-ms", cfg.MinDelayMS, "Minimum delay before killing worker (ms)")
	fs.IntVar(&cfg.MaxDelayMS, "max-delay-ms", cfg.MaxDelayMS, "Maximum delay before killing worker (ms)")
	fs.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "Deterministic workload seed")
	fs.IntVar(&cfg.Ops, "ops", cfg.Ops, "Total operations to generate")
	fs.IntVar(&cfg.PutRatio, "put-ratio", cfg.PutRatio, "Put workload ratio (0-100)")
	fs.IntVar(&cfg.DeleteRatio, "delete-ratio", cfg.DeleteRatio, "Delete workload ratio (0-100)")
	fs.IntVar(&cfg.BatchRatio, "batch-ratio", cfg.BatchRatio, "Batch workload ratio (0-100)")
	fs.IntVar(&cfg.MaxKeyLen, "max-key-len", cfg.MaxKeyLen, "Maximum key length in bytes")
	fs.IntVar(&cfg.MaxValueLen, "max-value-len", cfg.MaxValueLen, "Maximum value length in bytes")
	fs.IntVar(
		&cfg.HotKeyRatio,
		"hot-key-ratio",
		cfg.HotKeyRatio,
		"Percentage of puts/batches that reuse existing hot keys",
	)
	fs.BoolVar(&cfg.KeepDBOnFail, "keep-db-on-fail", false, "Preserve the dbdir when verification fails")
	fs.BoolVar(&cfg.VerifyEveryCycle, "verify-every-cycle", cfg.VerifyEveryCycle, "Verify state after every crash cycle")
	fs.StringVar(&cfg.Profile, "profile", cfg.Profile, "Run profile: ci or full")
	fs.StringVar(&cfg.CrashPoint, "crash-point", "", "Internal crash point to arm for the first cycle")
	fs.StringVar(&cfg.FaultPoint, "fault-point", "", "Internal fault point to arm for the first cycle")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	ops := generateWorkload(cfg)
	artifactPath := createArtifactPath(cfg.ArtifactDir)
	ctrl := &controller{
		cfg:          cfg,
		artifactPath: artifactPath,
		artifact:     newArtifact(cfg, ops),
		ops:          ops,
		statuses:     make([]opStatus, len(ops)),
		retryIndex:   -1,
		lastAckedID:  -1,
		oracle:       newOracle(),
	}
	for i := range ctrl.statuses {
		ctrl.statuses[i] = opStatusPlanned
	}

	if err := saveArtifact(ctrl.artifactPath, ctrl.artifact); err != nil {
		return err
	}

	if err := ctrl.run(context.Background()); err != nil {
		log.Printf("crash harness failed; artifact preserved at %s", ctrl.artifactPath)
		log.Printf("dbdir preserved at %s for inspection", cfg.DBDir)
		return err
	}

	if err := os.RemoveAll(cfg.DBDir); err != nil {
		return fmt.Errorf("removing successful run dbdir: %w", err)
	}
	log.Printf("crash harness succeeded; artifact written to %s", ctrl.artifactPath)
	return nil
}

// replayCommand replays a prior artifact with deterministic scheduling.
func replayCommand(args []string) error {
	var cfg replayConfig
	fs := newFlagSet("replay")
	fs.StringVar(&cfg.ArtifactPath, "artifact", "", "Artifact file to replay")
	fs.StringVar(&cfg.DBDir, "dbdir", "", "Database directory for the replay run (must be empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	art, ops, err := loadArtifact(cfg.ArtifactPath)
	if err != nil {
		return err
	}

	runCfg := defaultRunConfig()
	runCfg.DBDir = cfg.DBDir
	runCfg.ArtifactDir = filepathDir(cfg.ArtifactPath)
	runCfg.Cycles = art.Config.Cycles
	runCfg.MinDelayMS = art.Config.MinDelayMS
	runCfg.MaxDelayMS = art.Config.MaxDelayMS
	runCfg.Seed = art.Seed
	runCfg.Ops = art.Config.Ops
	runCfg.PutRatio = art.Config.PutRatio
	runCfg.DeleteRatio = art.Config.DeleteRatio
	runCfg.BatchRatio = art.Config.BatchRatio
	runCfg.MaxKeyLen = art.Config.MaxKeyLen
	runCfg.MaxValueLen = art.Config.MaxValueLen
	runCfg.HotKeyRatio = art.Config.HotKeyRatio
	runCfg.VerifyEveryCycle = art.Config.VerifyEveryCycle
	runCfg.Profile = art.Config.Profile
	runCfg.CrashPoint = art.Config.CrashPoint
	runCfg.FaultPoint = art.Config.FaultPoint
	runCfg.KeepDBOnFail = true

	replayArtifactPath := createArtifactPath(filepathDir(cfg.ArtifactPath))

	ctrl := &controller{
		cfg:          runCfg,
		artifactPath: replayArtifactPath,
		artifact:     newArtifact(runCfg, ops),
		ops:          ops,
		statuses:     make([]opStatus, len(ops)),
		retryIndex:   -1,
		lastAckedID:  -1,
		oracle:       newOracle(),
	}
	for i := range ctrl.statuses {
		ctrl.statuses[i] = opStatusPlanned
	}

	if err := ctrl.run(context.Background()); err != nil {
		log.Printf("replay failed; source artifact preserved at %s", cfg.ArtifactPath)
		log.Printf("replay artifact preserved at %s", replayArtifactPath)
		return err
	}

	if err := os.RemoveAll(runCfg.DBDir); err != nil {
		return fmt.Errorf("removing successful replay dbdir: %w", err)
	}
	log.Printf("replay succeeded for %s; replay artifact written to %s", cfg.ArtifactPath, replayArtifactPath)
	return nil
}

//nolint:gocognit // controller orchestration is intentionally linear and stateful
func (c *controller) run(ctx context.Context) error {
	//nolint:gosec // deterministic pseudorandom kill schedule
	rng := rand.New(rand.NewPCG(c.cfg.Seed^0xdecafbad, c.cfg.Seed^0x123456789))

	log.Printf("Starting crash harness: %d cycles, %d operations", c.cfg.Cycles, len(c.ops))

	for cycle := range c.cfg.Cycles {
		if c.retryIndex < 0 && c.queueIndex >= len(c.ops) {
			break
		}

		delayMS := c.cfg.MinDelayMS
		if c.cfg.MaxDelayMS > c.cfg.MinDelayMS {
			delayMS += rng.IntN(c.cfg.MaxDelayMS - c.cfg.MinDelayMS + 1)
		}

		log.Printf("Cycle %d: starting worker", cycle)
		cycleRecord, pending, err := c.runCycle(ctx, cycle, delayMS)
		if err != nil {
			return err
		}
		c.artifact.Cycles = append(c.artifact.Cycles, cycleRecord)
		if err := saveArtifact(c.artifactPath, c.artifact); err != nil {
			return err
		}
		if err := c.verifyCycle(ctx, cycle, pending); err != nil {
			return err
		}
	}

	return c.verifyFinalState(ctx)
}

//nolint:gocognit,gocyclo,funlen // subprocess protocol handling is stateful by design
func (c *controller) runCycle(ctx context.Context, cycle, delayMS int) (artifactCycle, *operation, error) {
	workerBinary := c.cfg.workerBinary
	if workerBinary == "" {
		path, err := os.Executable()
		if err != nil {
			return artifactCycle{}, nil, fmt.Errorf("resolving worker binary: %w", err)
		}
		workerBinary = path
	}

	//nolint:gosec // workerBinary resolves to the local crash binary or test helper
	cmd := exec.CommandContext(
		ctx,
		workerBinary,
		"worker",
		"--dbdir="+c.cfg.DBDir,
	) //nolint:gosec // trusted local binary + controlled args
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), c.workerEnv(cycle)...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return artifactCycle{}, nil, fmt.Errorf("creating worker stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return artifactCycle{}, nil, fmt.Errorf("creating worker stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return artifactCycle{}, nil, fmt.Errorf("starting worker: %w", err)
	}

	eventCh := make(chan eventMessage, 8)
	readerErrCh := make(chan error, 1)
	var readerWG sync.WaitGroup
	readerWG.Go(func() {
		dec := json.NewDecoder(stdout)
		for {
			var event eventMessage
			if err := dec.Decode(&event); err != nil {
				if errors.Is(err, io.EOF) {
					close(eventCh)
					readerErrCh <- nil
					return
				}
				close(eventCh)
				readerErrCh <- err
				return
			}
			eventCh <- event
		}
	})

	ready, err := waitForReady(eventCh)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return artifactCycle{}, nil, fmt.Errorf("waiting for worker ready: %w", err)
	}
	c.recordEvent(cycle, ready)

	timer := time.NewTimer(time.Duration(delayMS) * time.Millisecond)
	defer timer.Stop()

	lastEvent := &artifactEventRef{Kind: ready.Kind}
	processEnded := false

	for !processEnded {
		op := c.nextOperation()
		if op == nil {
			break
		}

		data, err := encodeNDJSON(op.toMessage())
		if err != nil {
			return artifactCycle{}, nil, fmt.Errorf("encoding operation %d: %w", op.ID, err)
		}
		if _, err := stdin.Write(data); err != nil {
			break
		}
		c.retryIndex = op.ID

	terminalWait:
		for {
			select {
			case event, ok := <-eventCh:
				if !ok {
					processEnded = true
					break terminalWait
				}
				c.recordEvent(cycle, event)
				lastEvent = &artifactEventRef{Kind: event.Kind, OpID: event.OpID}
				switch event.Kind {
				case eventStart:
					c.statuses[event.OpID] = opStatusStarted
				case eventAck:
					c.statuses[event.OpID] = opStatusAcked
					c.lastAckedID = event.OpID
					c.artifact.LastAckedOpID = event.OpID
					c.oracle.apply(c.ops[event.OpID])
					c.retryIndex = -1
					break terminalWait
				case eventFail:
					c.statuses[event.OpID] = opStatusFailed
					c.retryIndex = -1
					if armedFaultPoint(c.cfg, cycle) == "" {
						return artifactCycle{}, nil, fmt.Errorf("worker reported failure for op %d: %s", event.OpID, event.Error)
					}
					break terminalWait
				}
			case <-timer.C:
				log.Printf("Cycle %d: killing worker pid %d after %dms", cycle, cmd.Process.Pid, delayMS)
				if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
					return artifactCycle{}, nil, fmt.Errorf("killing worker: %w", err)
				}
				processEnded = true
				break terminalWait
			}
		}
	}

	_ = stdin.Close()
	waitErr := cmd.Wait()
	readerWG.Wait()
	if decodeErr := <-readerErrCh; decodeErr != nil {
		return artifactCycle{}, nil, fmt.Errorf("decoding worker events: %w", decodeErr)
	}
	for event := range eventCh {
		c.recordEvent(cycle, event)
		lastEvent = &artifactEventRef{Kind: event.Kind, OpID: event.OpID}
		switch event.Kind {
		case eventStart:
			c.statuses[event.OpID] = opStatusStarted
		case eventAck:
			c.statuses[event.OpID] = opStatusAcked
			c.lastAckedID = event.OpID
			c.artifact.LastAckedOpID = event.OpID
			c.oracle.apply(c.ops[event.OpID])
			c.retryIndex = -1
		case eventFail:
			c.statuses[event.OpID] = opStatusFailed
			c.retryIndex = -1
			if armedFaultPoint(c.cfg, cycle) == "" {
				return artifactCycle{}, nil, fmt.Errorf("worker reported failure for op %d: %s", event.OpID, event.Error)
			}
		}
	}

	exitCode := exitCode(waitErr)
	record := artifactCycle{
		Index:              cycle,
		WorkerPID:          cmd.Process.Pid,
		PlannedKillDelayMS: delayMS,
		ActualEndUnixMilli: time.Now().UTC().UnixMilli(),
		ExitCode:           exitCode,
		LastEvent:          lastEvent,
		CrashPoint:         armedCrashPoint(c.cfg, cycle),
		FaultPoint:         armedFaultPoint(c.cfg, cycle),
	}

	return record, c.pendingOperation(), nil
}

// workerEnv returns hook env vars armed for the given cycle.
func (c *controller) workerEnv(cycle int) []string {
	env := []string{}
	if point := armedCrashPoint(c.cfg, cycle); point != "" {
		env = append(env, crashhook.EnvCrashPoint+"="+point)
	}
	if point := armedFaultPoint(c.cfg, cycle); point != "" {
		env = append(env, crashhook.EnvFaultPoint+"="+point)
	}
	return env
}

// armedCrashPoint enables a crash point only for the first cycle.
func armedCrashPoint(cfg runConfig, cycle int) string {
	if cycle == 0 {
		return cfg.CrashPoint
	}
	return ""
}

// armedFaultPoint enables a fault point only for the first cycle.
func armedFaultPoint(cfg runConfig, cycle int) string {
	if cycle == 0 {
		return cfg.FaultPoint
	}
	return ""
}

// nextOperation returns either the retry op or the next queued operation.
func (c *controller) nextOperation() *operation {
	if c.retryIndex >= 0 {
		return &c.ops[c.retryIndex]
	}
	if c.queueIndex >= len(c.ops) {
		return nil
	}

	op := &c.ops[c.queueIndex]
	c.queueIndex++
	return op
}

// pendingOperation returns a started-but-not-acked operation, if any.
func (c *controller) pendingOperation() *operation {
	if c.retryIndex < 0 {
		return nil
	}
	if c.statuses[c.retryIndex] != opStatusStarted {
		return nil
	}
	pending := c.ops[c.retryIndex]
	return &pending
}

// recordEvent appends one protocol event into the artifact stream.
func (c *controller) recordEvent(cycle int, event eventMessage) {
	c.artifact.Events = append(c.artifact.Events, artifactEvent{
		Cycle: cycle,
		Time:  time.Now().UTC().UnixMilli(),
		Event: event,
	})
}

// waitForReady waits for an initial ready event from worker stdout.
func waitForReady(eventCh <-chan eventMessage) (eventMessage, error) {
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case event, ok := <-eventCh:
			if !ok {
				return eventMessage{}, errors.New("worker exited before sending ready event")
			}
			if event.Kind == eventReady {
				return event, nil
			}
			return eventMessage{}, fmt.Errorf("expected ready event, got %s", event.Kind)
		case <-timeout.C:
			return eventMessage{}, errors.New("timed out waiting for worker ready event")
		}
	}
}

// exitCode extracts process exit code from exec errors.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// filepathDir returns path directory or "." for empty input.
func filepathDir(path string) string {
	if path == "" {
		return "."
	}
	return filepath.Dir(path)
}

// verifyCycle checks DB state against allowed outcomes after one cycle.
func (c *controller) verifyCycle(ctx context.Context, cycle int, pending *operation) error {
	if !c.cfg.VerifyEveryCycle {
		return nil
	}

	result, failure, err := verifyDatabaseState(ctx, c.cfg.DBDir, c.oracle, pending)
	if err != nil {
		return err
	}
	c.artifact.Cycles[len(c.artifact.Cycles)-1].Verification = artifactVerification(result)
	if failure == nil {
		return saveArtifact(c.artifactPath, c.artifact)
	}

	failure.Cycle = cycle
	c.artifact.Failure = failure
	if err := saveArtifact(c.artifactPath, c.artifact); err != nil {
		return err
	}
	return fmt.Errorf("verification failed in cycle %d: %s", cycle, failure.Message)
}

// verifyFinalState checks state after all cycles and persists final artifact.
func (c *controller) verifyFinalState(ctx context.Context) error {
	finalPending := c.pendingOperation()
	result, failure, err := verifyDatabaseState(ctx, c.cfg.DBDir, c.oracle, finalPending)
	if err != nil {
		return err
	}
	if len(c.artifact.Cycles) > 0 {
		c.artifact.Cycles[len(c.artifact.Cycles)-1].Verification = artifactVerification(result)
	}
	if failure != nil {
		failure.Cycle = len(c.artifact.Cycles)
		c.artifact.Failure = failure
		if err := saveArtifact(c.artifactPath, c.artifact); err != nil {
			return err
		}
		return fmt.Errorf("final verification failed: %s", failure.Message)
	}

	return saveArtifact(c.artifactPath, c.artifact)
}
