package loops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOptimizeDefaultUsesSelectedVersionProjectAndInputs(t *testing.T) {
	store := testStore(t)
	first, recent := testProject(t, store), testProject(t, store)
	loop, err := store.Create(t.Context(), Loop{Name: "Notes", Description: "Summarize", Criteria: "Accurate", ProjectID: first.ID})
	require.NoError(t, err)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Summarize\n", "")
	require.NoError(t, err)
	for _, input := range []string{"older", "latest", "latest"} {
		_, err := store.newJob(t.Context(), loop.ID, version.ID, "run", input, &recent)
		require.NoError(t, err)
	}
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	round, err := service.OptimizeDefault(t.Context(), loop.ID, version.ID)
	require.NoError(t, err)
	assert.Equal(t, recent.ID, round.Project.ID)
	assert.Equal(t, []string{"latest", "older"}, round.Inputs)
	assert.Equal(t, 2, round.Repeats)
	assert.Equal(t, version.ID, round.Baseline)
	_, err = service.OptimizeDefault(t.Context(), loop.ID, version.ID)
	require.ErrorIs(t, err, ErrConflict)
}

func TestOptimizeDefaultKeepsInitialVersionUntilOneIsPromoted(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	loop, err := store.Create(t.Context(), Loop{Name: "Notes", Description: "Summarize", Criteria: "Accurate", ProjectID: project.ID})
	require.NoError(t, err)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Summarize\n", "")
	require.NoError(t, err)
	candidate, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Candidate\n", "")
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	round, err := service.OptimizeDefault(t.Context(), loop.ID, "")
	require.NoError(t, err)
	assert.Equal(t, project.ID, round.Project.ID)
	assert.Equal(t, []string{loop.Description}, round.Inputs)
	assert.Equal(t, version.ID, round.Baseline)
	service.Cancel(round.ID)
	service.Wait()
	_, err = store.db.Exec(`UPDATE loops SET active_version=? WHERE id=?`, candidate.ID, loop.ID)
	require.NoError(t, err)
	promoted, err := service.OptimizeDefault(t.Context(), loop.ID, "")
	require.NoError(t, err)
	assert.Equal(t, candidate.ID, promoted.Baseline)
}
