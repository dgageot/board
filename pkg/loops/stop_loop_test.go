package loops

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStopLoopCancelsAndJoinsOnlyItsJobs(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	other := testLoop(t, store)
	started := make(chan string, 2)
	canceled := make(chan string, 2)
	release := make(chan struct{})
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, r Request, _ func(Result)) (Result, error) {
		started <- r.ID
		<-ctx.Done()
		canceled <- r.ID
		<-release
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	defer service.Close()
	defer close(release)
	job, err := service.start(t.Context(), loop.ID, "", "run", "input", nil)
	require.NoError(t, err)
	_, err = service.start(t.Context(), other.ID, "", "run", "input", nil)
	require.NoError(t, err)
	<-started
	<-started
	stopped := make(chan error, 1)
	go func() { stopped <- service.StopLoop(t.Context(), loop.ID) }()
	assert.Equal(t, job.ID, <-canceled)
	select {
	case err := <-stopped:
		require.FailNow(t, "stop returned before runner exit", "%v", err)
	default:
	}
	select {
	case id := <-canceled:
		require.FailNow(t, "canceled unrelated job", "%s", id)
	default:
	}
	_, err = service.Generate(t.Context(), loop.ID)
	require.Error(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	require.ErrorIs(t, service.StopLoop(ctx, loop.ID), context.DeadlineExceeded)
	// Release only the deleted loop's runner; the other is canceled during cleanup.
	release <- struct{}{}
	require.NoError(t, <-stopped)
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.True(t, detail.Loop.Deleting)
	saved, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "canceled", saved.Status)
}

func TestStopLoopJoinsLearningJobsAndRejectsLateRevisions(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	started := make(chan struct{})
	finished := make(chan struct{})
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		close(started)
		<-ctx.Done()
		close(finished)
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	defer service.Close()
	result := make(chan Job, 1)
	go func() { result <- service.experimentJob(t.Context(), loop.ID, "v", "run_judge", "input", nil) }()
	<-started
	require.NoError(t, service.StopLoop(t.Context(), loop.ID))
	select {
	case <-finished:
	default:
		require.FailNow(t, "learning job not stopped")
	}
	assert.Equal(t, "canceled", (<-result).Status)
	assert.Equal(t, "failed", service.experimentJob(t.Context(), loop.ID, "v", "run_revision", "input", nil).Status)
}

func TestDeletingLoopRejectsAllNewEvidenceAndWork(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	job, err := store.newJob(t.Context(), loop.ID, "v", "run", "input")
	require.NoError(t, err)
	job.Status = "succeeded"
	require.NoError(t, store.saveJob(t.Context(), job))
	service, err := NewService(t.Context(), store, fakeRunner{}, "auto", func() {})
	require.NoError(t, err)
	defer service.Close()
	require.NoError(t, service.StopLoop(t.Context(), loop.ID))
	_, err = store.AddVersion(t.Context(), loop.ID, "agents: {root: {}}", "")
	require.Error(t, err)
	_, err = store.AddFeedback(t.Context(), loop.ID, job.ID, "good", "")
	require.ErrorIs(t, err, ErrConflict)
	require.Error(t, store.SetMode(t.Context(), loop.ID, ModeProject))
	require.Error(t, store.admitRound(t.Context(), Round{ID: "round", LoopID: loop.ID}))
	require.NoError(t, store.saveRound(t.Context(), Round{ID: "late", LoopID: loop.ID}))
	rounds, err := store.Rounds(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Empty(t, rounds)
	_, err = store.db.Exec(`DELETE FROM loop_jobs WHERE loop_id=?`, loop.ID)
	require.NoError(t, err)
	_, err = store.db.Exec(`DELETE FROM loops WHERE id=?`, loop.ID)
	require.NoError(t, err)
	_, err = store.newJob(t.Context(), loop.ID, "", "run_revision", "input")
	require.Error(t, err)
	require.NoError(t, store.saveRound(t.Context(), Round{ID: "late", LoopID: loop.ID}))
	rounds, err = store.Rounds(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Empty(t, rounds)
}

func TestStopLoopJoinsOptimizerParentAndChildren(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	project := testProject(t, store)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Summarize the input.\n", "")
	require.NoError(t, err)
	started := make(chan struct{})
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	defer service.Close()
	_, err = service.Optimize(t.Context(), loop.ID, OptimizeRequest{Version: version.ID, ProjectID: project.ID, Inputs: []string{"notes"}, Repeats: 1})
	require.NoError(t, err)
	<-started
	require.NoError(t, service.StopLoop(t.Context(), loop.ID))
	service.mu.Lock()
	assert.Empty(t, service.running)
	assert.Empty(t, service.cancel)
	service.mu.Unlock()
}
