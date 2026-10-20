package loops

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

type fakeRunner struct {
	run func(context.Context, Request, func(Result)) (Result, error)
}

func (r fakeRunner) Run(ctx context.Context, req Request, progress func(Result)) (Result, error) {
	return r.run(ctx, req, progress)
}

func TestGeneratePersistsVersionAndRun(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		assert.Contains(t, string(req.Config), "structured_output")
		assert.Contains(t, req.Input, loop.Description)
		return Result{Output: `{"instruction":"Summarize accurately","model":"auto","rationale":"Focused prompt"}`}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	job, err := service.Generate(t.Context(), loop.ID)
	require.NoError(t, err)
	service.Wait()
	saved, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", saved.Status)
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	require.Len(t, detail.Versions, 1)
	assert.Equal(t, detail.Versions[0].ID, saved.VersionID)
	assert.Empty(t, detail.Loop.ActiveVersion)
}

func TestRunFailureRetainsEvidence(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: summarize\n", "")
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(_ context.Context, _ Request, progress func(Result)) (Result, error) {
		result := Result{Output: "partial output"}
		progress(result)
		return result, errors.New("provider unavailable")
	}}, "auto", func() {})
	require.NoError(t, err)
	job, err := service.Run(t.Context(), loop.ID, version.ID, "/not-a-command", testProject(t, store).ID)
	require.NoError(t, err)
	service.Wait()
	saved, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", saved.Status)
	assert.Equal(t, "partial output", saved.Output)
	assert.Contains(t, saved.Error, "provider unavailable")
}

func TestRecoverDoesNotReplayInterruptedJobs(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	job, err := store.newJob(t.Context(), loop.ID, "", "generate", "prompt")
	require.NoError(t, err)
	require.NoError(t, store.Recover(t.Context()))
	saved, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "interrupted", saved.Status)
	assert.NotEmpty(t, saved.FinishedAt)
}

func TestLiveLoopSmoke(t *testing.T) {
	if os.Getenv("BOARD_LOOPS_SMOKE") != "1" {
		t.Skip("set BOARD_LOOPS_SMOKE=1 to call the configured model provider")
	}
	store := testStore(t)
	project := testProject(t, store)
	loop, err := store.Create(t.Context(), Loop{Name: "Project overview", Description: "Describe the project based on its README.", Criteria: "Mention the committed project description without inventing details.", ProjectID: project.ID})
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, &ProcessRunner{Directory: t.TempDir()}, "auto", func() {})
	require.NoError(t, err)
	generation, err := service.Generate(t.Context(), loop.ID)
	require.NoError(t, err)
	service.Wait()
	generated, err := store.Job(t.Context(), loop.ID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", generated.Status, generated.Error)
	run, err := service.Run(t.Context(), loop.ID, generated.VersionID, "", project.ID)
	require.NoError(t, err)
	service.Wait()
	completed, err := store.Job(t.Context(), loop.ID, run.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", completed.Status, completed.Error)
	assert.Contains(t, strings.ToLower(completed.Output), "committed project")
	assert.Positive(t, completed.Metrics.DurationMS)
	assert.Positive(t, completed.Metrics.InputTokens)
	t.Logf("generated v1 and ran it: model=%s duration=%dms cost=%v", completed.Metrics.Model, completed.Metrics.DurationMS, completed.Metrics.Cost)
	if os.Getenv("BOARD_LOOPS_OPTIMIZE_SMOKE") == "1" {
		_, err := service.Optimize(t.Context(), loop.ID, OptimizeRequest{ProjectID: project.ID, Version: generated.VersionID, Inputs: []string{loop.Description}, Repeats: 1, Models: []string{"auto", "auto"}})
		require.NoError(t, err)
		service.Wait()
		rounds, err := store.Rounds(t.Context(), loop.ID)
		require.NoError(t, err)
		require.Len(t, rounds, 1)
		require.Equal(t, "completed", rounds[0].Status, rounds[0].Error)
		require.Len(t, rounds[0].Candidates, 3)
		for _, candidate := range rounds[0].Candidates {
			require.Empty(t, candidate.Error)
			require.Len(t, candidate.Trials, 1)
			require.NotEmpty(t, candidate.Trials[0].JudgeID)
			execution, err := store.Job(t.Context(), loop.ID, candidate.Trials[0].JobID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", execution.Status, execution.Error)
			judgment, err := store.Job(t.Context(), loop.ID, candidate.Trials[0].JudgeID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", judgment.Status, judgment.Error)
			require.True(t, candidate.Trials[0].Passed, candidate.Trials[0].Reason)
			t.Logf("%s: passed=%v reason=%s", candidate.Label, candidate.Trials[0].Passed, candidate.Trials[0].Reason)
		}
	}
}

func TestServiceCloseStopsAdmissionAndJoinsWork(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	started := make(chan struct{})
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	job, err := service.Generate(t.Context(), loop.ID)
	require.NoError(t, err)
	<-started
	service.Close()
	saved, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "canceled", saved.Status)
	_, err = service.Generate(t.Context(), loop.ID)
	require.Error(t, err)
}

func TestOptimizationRunsCompleteSuiteAndGatesPromotion(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: summarize\n", "baseline")
	require.NoError(t, err)
	cost := 0.01
	project := testProject(t, store)
	snapshot, err := store.projectSnapshot(t.Context(), project.ID)
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		switch {
		case strings.Contains(string(req.Config), "agent_proposal"):
			return Result{Output: `{"instruction":"Summarize precisely","model":"auto","rationale":"Less repetition"}`}, nil
		case strings.Contains(string(req.Config), "quality_judgment"):
			assert.Contains(t, req.Input, "A committed project")
			assert.Contains(t, req.Input, snapshot.Digest)
			return Result{Output: `{"passed":true,"reason":"Meets all criteria"}`}, nil
		default:
			assert.Contains(t, req.Input, "A committed project")
			assert.Contains(t, req.Input, snapshot.Digest)
			return Result{Output: "Good answer", Metrics: Metrics{Cost: &cost, DurationMS: 100}}, nil
		}
	}}, "auto", func() {})
	require.NoError(t, err)
	round, err := service.Optimize(t.Context(), loop.ID, OptimizeRequest{ProjectID: project.ID, Version: version.ID, Inputs: []string{"case one", "case two"}, Repeats: 2, Models: []string{"auto", "auto"}})
	require.NoError(t, err)
	service.Wait()
	rounds, err := store.Rounds(t.Context(), loop.ID)
	require.NoError(t, err)
	require.Len(t, rounds, 1)
	assert.Equal(t, round.ID, rounds[0].ID)
	assert.Equal(t, "completed", rounds[0].Status)
	require.Len(t, rounds[0].Candidates, 3)
	for _, candidate := range rounds[0].Candidates {
		assert.True(t, candidatePassed(rounds[0], candidate))
		assert.Len(t, candidate.Trials, 4)
	}
	candidate := rounds[0].Candidates[1]
	require.NoError(t, store.Promote(t.Context(), loop.ID, candidate.VersionID, ""))
	require.ErrorIs(t, store.Promote(t.Context(), loop.ID, version.ID, ""), ErrConflict)
}

func TestIncompleteOrDuplicateTrialsCannotQualify(t *testing.T) {
	round := Round{Inputs: []string{"one", "two"}, Repeats: 1}
	trial := Trial{JobID: "run", JudgeID: "judge", Passed: true, Case: 0, Repeat: 0}
	assert.False(t, candidatePassed(round, Candidate{Trials: []Trial{trial}}))
	assert.False(t, candidatePassed(round, Candidate{Trials: []Trial{trial, trial}}))
	trial.Case = 1
	trial.Passed = false
	assert.False(t, candidatePassed(round, Candidate{Trials: []Trial{{JobID: "a", JudgeID: "b", Passed: true}, trial}}))
}

func TestFailedTerminalPersistenceCannotProducePassingTrial(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	_, err := store.db.Exec(`CREATE TRIGGER reject_terminal BEFORE UPDATE ON loop_jobs WHEN NEW.status='succeeded' BEGIN SELECT RAISE(FAIL,'disk failure'); END`)
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(context.Context, Request, func(Result)) (Result, error) { return Result{Output: "answer"}, nil }}, "auto", func() {})
	require.NoError(t, err)
	trial := service.evaluate(t.Context(), loop, "version", "input", 0, 0, []byte("config"), []byte("judge"))
	assert.False(t, trial.Passed)
	assert.Empty(t, trial.JudgeID)
	assert.Contains(t, trial.Reason, "disk failure")
}

func TestPromotionRejectsSummaryWithoutPersistedEvidence(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root: {}", "")
	require.NoError(t, err)
	round := Round{ID: newID(), LoopID: loop.ID, Status: "completed", Inputs: []string{"input"}, Repeats: 1, Candidates: []Candidate{{VersionID: version.ID, Trials: []Trial{{JobID: "missing", JudgeID: "missing-judge", Passed: true}}}}}
	require.NoError(t, store.saveRound(t.Context(), round))
	require.ErrorIs(t, store.Promote(t.Context(), loop.ID, version.ID, ""), ErrConflict)
}

func TestCancelExperimentChild(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	started := make(chan string, 1)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, req Request, _ func(Result)) (Result, error) {
		started <- req.ID
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	done := make(chan Job, 1)
	go func() {
		done <- service.experimentJob(t.Context(), loop.ID, "version", "test", "input", []byte("config"))
	}()
	id := <-started
	service.Cancel(id)
	select {
	case job := <-done:
		assert.Equal(t, "canceled", job.Status)
	case <-time.After(time.Second):
		t.Fatal("child cancellation did not complete")
	}
}

func TestExperimentPublicationRegistersCancellationAtomically(t *testing.T) {
	published := make(chan string, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	function := "publish_" + newID()
	require.NoError(t, sqlite.RegisterScalarFunction(function, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		id, ok := args[0].(string)
		if !ok {
			return nil, errors.New("invalid published id")
		}
		published <- id
		<-release
		return int64(0), nil
	}))
	store := testStore(t)
	loop := testLoop(t, store)
	require.NoError(t, func() error {
		_, err := store.db.Exec("CREATE TRIGGER publication AFTER INSERT ON loop_jobs BEGIN SELECT " + function + "(NEW.id); END")
		return err
	}())
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	// Keep the child queued even after publication completes.
	for range cap(service.slots) {
		service.slots <- struct{}{}
	}
	done := make(chan Job, 1)
	go func() { done <- service.experimentJob(t.Context(), loop.ID, "v1", "test", "input", nil) }()
	id := <-published
	locked := !service.mu.TryLock()
	if !locked {
		service.mu.Unlock()
	}
	assert.True(t, locked, "Cancel must not pass publication before registration")
	canceled := make(chan struct{})
	go func() { service.Cancel(id); close(canceled) }()
	unblock()
	<-canceled
	select {
	case job := <-done:
		assert.Equal(t, "canceled", job.Status)
	case <-time.After(time.Second):
		t.Fatal("published job ignored cancellation")
	}
}

func TestCreateAutomaticallyGeneratesAgent(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	started := make(chan struct{})
	release := make(chan struct{})
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		return Result{Output: `{"instruction":"Summarize","model":"auto","rationale":"Purpose built"}`}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	loop, err := service.Create(t.Context(), Loop{Name: "Notes", Description: "Summarize", Criteria: "Accurate", ProjectID: project.ID})
	require.NoError(t, err)
	<-started
	jobs, err := store.Jobs(t.Context(), loop.ID)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "generate", jobs[0].Kind)
	assert.Equal(t, "running", jobs[0].Status)
	close(release)
	service.Wait()
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	require.Len(t, detail.Versions, 1)
}

func TestCreateRollsBackIfInitialJobCannotBeSaved(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	_, err := store.db.Exec(`CREATE TRIGGER reject_new_job BEFORE INSERT ON loop_jobs BEGIN SELECT RAISE(FAIL,'disk failure'); END`)
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, fakeRunner{}, "auto", func() {})
	require.NoError(t, err)
	_, err = service.Create(t.Context(), Loop{Name: "Notes", Description: "Summarize", Criteria: "Accurate", ProjectID: project.ID})
	require.ErrorContains(t, err, "disk failure")
	all, err := store.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestCreateRejectsAdmissionBeforeSavingLoop(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	service, err := NewService(t.Context(), store, fakeRunner{}, "auto", func() {})
	require.NoError(t, err)
	for range 16 {
		service.cancel[newID()] = func() {}
	}
	_, err = service.Create(t.Context(), Loop{Name: "Notes", Description: "Summarize", Criteria: "Accurate", ProjectID: project.ID})
	require.ErrorIs(t, err, ErrConflict)
	all, err := store.List(t.Context())
	require.NoError(t, err)
	assert.Empty(t, all)
}
