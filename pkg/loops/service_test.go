package loops

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	job, err := service.Run(t.Context(), loop.ID, version.ID, "/not-a-command")
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
	loop, err := store.Create(t.Context(), Loop{Name: "Release notes", Description: "Turn commit messages into short user-facing release notes.", Criteria: "Mention fixes and features. Do not invent changes.", Example: "fix: resolve login timeout\nfeat: export CSV"})
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, &ProcessRunner{Directory: t.TempDir()}, "auto", func() {})
	require.NoError(t, err)
	generation, err := service.Generate(t.Context(), loop.ID)
	require.NoError(t, err)
	service.Wait()
	generated, err := store.Job(t.Context(), loop.ID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", generated.Status, generated.Error)
	run, err := service.Run(t.Context(), loop.ID, generated.VersionID, loop.Example)
	require.NoError(t, err)
	service.Wait()
	completed, err := store.Job(t.Context(), loop.ID, run.ID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", completed.Status, completed.Error)
	assert.Contains(t, strings.ToLower(completed.Output), "csv")
	assert.Positive(t, completed.Metrics.DurationMS)
	assert.Positive(t, completed.Metrics.InputTokens)
	t.Logf("generated v1 and ran it: model=%s duration=%dms cost=%v", completed.Metrics.Model, completed.Metrics.DurationMS, completed.Metrics.Cost)
}
