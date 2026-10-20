package loops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectWorkflowGenerationPreservesExecutableIntent(t *testing.T) {
	store := testStore(t)
	project := testProject(t, store)
	service, err := NewService(t.Context(), store, fakeRunner{run: func(_ context.Context, request Request, _ func(Result)) (Result, error) {
		assert.Contains(t, string(request.Config), "trusted coding agent")
		assert.NotContains(t, string(request.Config), "cannot use tools")
		return Result{Output: `{"instruction":"Update dependencies, run tests and create signed commits.","model":"auto","rationale":"Executable workflow"}`}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	loop, err := service.Create(t.Context(), Loop{Name: "Updates", Description: "Bump dependencies", Criteria: "Tests pass", ProjectID: project.ID, Mode: ModeProject})
	require.NoError(t, err)
	service.Wait()
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	require.Len(t, detail.Versions, 1)
	assert.Equal(t, ModeProject, detail.Loop.Mode)
	assert.Equal(t, ModeProject, detail.Versions[0].Mode)
	instruction, err := ProjectInstruction(detail.Versions[0])
	require.NoError(t, err)
	assert.Contains(t, instruction, "signed commits")
	_, err = service.Run(t.Context(), loop.ID, detail.Versions[0].ID, "", project.ID)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.OptimizeDefault(t.Context(), loop.ID, detail.Versions[0].ID)
	require.ErrorIs(t, err, ErrInvalid)
}

func TestWorkflowModeRequiresExplicitChoice(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	require.ErrorIs(t, store.SetMode(t.Context(), loop.ID, "anything"), ErrInvalid)
	require.NoError(t, store.SetMode(t.Context(), loop.ID, ModeProject))
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Equal(t, ModeProject, detail.Loop.Mode)
	require.NoError(t, store.SetMode(t.Context(), loop.ID, ModeAnalysis))
}

func TestModeChangeClearsIncompatibleActiveVersion(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	analysis, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Analyze\n", "")
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE loops SET active_version=? WHERE id=?`, analysis.ID, loop.ID)
	require.NoError(t, err)
	require.NoError(t, store.SetMode(t.Context(), loop.ID, ModeProject))
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Empty(t, detail.Loop.ActiveVersion)
	require.ErrorIs(t, store.Promote(t.Context(), loop.ID, analysis.ID, ""), ErrConflict)
	project, err := store.addVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Implement and verify\n", "", ModeProject)
	require.NoError(t, err)
	require.NoError(t, store.Promote(t.Context(), loop.ID, project.ID, ""))
	require.NoError(t, store.SetMode(t.Context(), loop.ID, ModeAnalysis))
	detail, err = store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Empty(t, detail.Loop.ActiveVersion)
	require.ErrorIs(t, store.Promote(t.Context(), loop.ID, project.ID, ""), ErrNotFound)
}

func TestModeChangeRejectsPendingWorkAndAdmissionRechecksMode(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	job, err := store.newJob(t.Context(), loop.ID, "v1", "run", "input")
	require.NoError(t, err)
	require.ErrorIs(t, store.SetMode(t.Context(), loop.ID, ModeProject), ErrConflict)
	job.Status = "succeeded"
	require.NoError(t, store.saveJob(t.Context(), job))
	require.NoError(t, store.SetMode(t.Context(), loop.ID, ModeProject))
	_, err = store.newJob(t.Context(), loop.ID, "v1", "run", "input")
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, store.admitRound(t.Context(), Round{ID: "r", LoopID: loop.ID, Status: "designing"}), ErrConflict)
}

func TestFirstAnalysisVersionIgnoresOlderProjectWorkflow(t *testing.T) {
	versions := []Version{{ID: "new", Mode: ModeAnalysis}, {ID: "first-analysis", Mode: ModeAnalysis}, {ID: "old-project", Mode: ModeProject}}
	assert.Equal(t, "first-analysis", firstAnalysisVersion(versions))
}
