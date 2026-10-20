package loops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"
)

// Service owns bounded background work, independently of browser connections.
type Service struct {
	ctx     context.Context //nolint:containedctx // Service lifetime is the Board lifetime.
	store   *Store
	runner  Runner
	model   string
	changed func()
	slots   chan struct{}
	mu      sync.Mutex
	cancel  map[string]context.CancelFunc
	closed  bool
	wg      sync.WaitGroup
}

// NewService recovers abandoned work and starts a fresh scheduling lifetime.
func NewService(ctx context.Context, store *Store, runner Runner, model string, changed func()) (*Service, error) {
	if err := store.Recover(ctx); err != nil {
		return nil, err
	}
	return &Service{ctx: ctx, store: store, runner: runner, model: model, changed: changed, slots: make(chan struct{}, 4), cancel: map[string]context.CancelFunc{}}, nil
}

// Wait joins canceled processes before Board closes the shared database.
func (s *Service) Wait() { s.wg.Wait() }

// Close shuts admission before joining all admitted work.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, cancel := range s.cancel {
		cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Generate creates an initial agent without running arbitrary generated YAML.
func (s *Service) Generate(ctx context.Context, loopID string) (Job, error) {
	detail, err := s.store.Get(ctx, loopID)
	if err != nil {
		return Job{}, err
	}
	config, err := designerConfig(s.model)
	if err != nil {
		return Job{}, err
	}
	data, err := json.Marshal(detail.Loop)
	if err != nil {
		return Job{}, err
	}
	input := "Design the initial agent for this task contract.\n" + string(data)
	return s.start(ctx, loopID, "", "generate", input, config)
}

// Run executes exactly one saved version against one explicit input.
func (s *Service) Run(ctx context.Context, loopID, versionID, input string) (Job, error) {
	if input == "" || len(input) > 64*1024 {
		return Job{}, fmt.Errorf("%w: input must contain 1–65536 bytes", ErrInvalid)
	}
	detail, err := s.store.Get(ctx, loopID)
	if err != nil {
		return Job{}, err
	}
	for _, version := range detail.Versions {
		if version.ID != versionID {
			continue
		}
		config, err := executableConfig(version.YAML)
		if err != nil {
			return Job{}, err
		}
		return s.start(ctx, loopID, versionID, "run", input, config)
	}
	return Job{}, ErrNotFound
}

func (s *Service) start(ctx context.Context, loopID, versionID, kind, input string, config []byte) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return Job{}, errors.New("board is shutting down")
	}
	if len(s.cancel) >= 16 {
		return Job{}, fmt.Errorf("%w: experiment queue is full", ErrConflict)
	}
	job, err := s.store.newJob(ctx, loopID, versionID, kind, input)
	if err != nil {
		return Job{}, err
	}
	jobCtx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
	s.cancel[job.ID] = cancel
	s.wg.Go(func() {
		defer cancel()
		defer func() { s.mu.Lock(); delete(s.cancel, job.ID); s.mu.Unlock() }()
		s.execute(jobCtx, job, config)
	})
	s.changed()
	return job, nil
}

func (s *Service) persist(job Job) error {
	// The process lifetime may be canceled; still record its terminal state.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	defer cancel()
	if err := s.store.saveJob(ctx, job); err != nil {
		log.Printf("save loop job %s: %v", job.ID, err)
		return err
	}
	s.changed()
	return nil
}

func (s *Service) execute(ctx context.Context, job Job, config []byte) Job {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		job.Status, job.Error, job.FinishedAt = "canceled", ctx.Err().Error(), now()
		_ = s.persist(job)
		return job
	}
	job.Status = "running"
	if err := s.persist(job); err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		return job
	}
	// A leading slash in user data must never become a CLI command.
	input := strconv.Quote(job.Input)
	result, err := s.runner.Run(ctx, Request{ID: job.ID, Config: config, Input: "Process this input (JSON-encoded text):\n" + input}, func(result Result) {
		job.Output, job.Metrics = result.Output, result.Metrics
		_ = s.persist(job)
	})
	job.Output, job.Metrics, job.FinishedAt = result.Output, result.Metrics, now()
	job.Status = "succeeded"
	if err == nil && (job.Kind == "generate" || job.Kind == "design") {
		job.VersionID, err = s.saveProposal(ctx, job.LoopID, result.Output)
	}
	if err != nil {
		job.Status, job.Error = "failed", err.Error()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			job.Status = "canceled"
		}
	}
	if err := s.persist(job); err != nil {
		job.Status = "failed"
		job.Error = "persist terminal result: " + err.Error()
	}
	return job
}

// Cancel stops a queued or running attempt without restarting it.
func (s *Service) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel := s.cancel[id]; cancel != nil {
		cancel()
	}
}

func (s *Service) saveProposal(ctx context.Context, loopID, output string) (string, error) {
	var p proposal
	if err := json.Unmarshal([]byte(output), &p); err != nil {
		return "", fmt.Errorf("decode agent proposal: %w", err)
	}
	config, err := p.config()
	if err != nil {
		return "", err
	}
	version, err := s.store.AddVersion(ctx, loopID, config, p.Rationale)
	return version.ID, err
}
