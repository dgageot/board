package loops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloseRunStopsWorkerAndPreservesOutput(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	started := make(chan struct{})
	stopped := make(chan struct{})
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, progress func(Result)) (Result, error) {
		result := Result{Output: "Partial result"}
		progress(result)
		close(started)
		<-ctx.Done()
		close(stopped)
		return result, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	job, err := service.start(t.Context(), loop.ID, "version", "run", "input", nil)
	require.NoError(t, err)
	<-started
	closed, err := service.CloseRun(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	select {
	case <-stopped:
	default:
		t.Fatal("close returned while worker still running")
	}
	assert.Equal(t, "canceled", closed.Status)
	assert.Equal(t, "Partial result", closed.Output)
	assert.NotEmpty(t, closed.ClosedAt)
	again, err := service.CloseRun(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, closed.ClosedAt, again.ClosedAt)
}

func TestCloseRunChecksOwnershipAndKeepsSuccessfulResult(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	service, err := NewService(t.Context(), store, fakeRunner{}, "auto", func() {})
	require.NoError(t, err)
	job, err := store.newJob(t.Context(), loop.ID, "v1", "run", "input")
	require.NoError(t, err)
	job.Status = "succeeded"
	job.Output = "Answer"
	require.NoError(t, store.saveJob(t.Context(), job))
	_, err = service.CloseRun(t.Context(), "other", job.ID)
	require.ErrorIs(t, err, ErrNotFound)
	closed, err := service.CloseRun(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", closed.Status)
	assert.Equal(t, "Answer", closed.Output)
	require.NoError(t, store.saveJob(t.Context(), job))
	persisted, err := store.Job(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, closed.ClosedAt, persisted.ClosedAt)
}
