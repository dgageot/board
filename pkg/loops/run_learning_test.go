package loops

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunLearningJudgesThenPatchesWithoutExecutingTask(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	parent := Version{ID: "v1", YAML: "agents:\n  root:\n    model: auto\n    instruction: Execute the task. Verify the changes.\n"}
	calls := 0
	service, err := NewService(t.Context(), store, fakeRunner{run: func(_ context.Context, request Request, _ func(Result)) (Result, error) {
		calls++
		assert.NotContains(t, string(request.Config), "toolsets")
		if strings.Contains(string(request.Config), "run_assessment") {
			return Result{Output: `{"quality":"failed","efficiency":"mixed","changeImpact":"inconclusive","summary":"Missing tests","improvements":["Run tests"]}`}, nil
		}
		data, err := json.Marshal(instructionPatch{BaseHash: configHash(parent.YAML), Edits: []instructionEdit{{Old: "Verify the changes.", New: "Verify the changes by running tests."}}, Rationale: "Add test verification"})
		require.NoError(t, err)
		return Result{Output: string(data)}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	result, err := service.LearnRun(t.Context(), loop.ID, RunLearningRequest{Parent: parent, Task: "Implement", Criteria: "Tests pass", Evidence: json.RawMessage(`{}`), History: json.RawMessage(`[]`)})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Contains(t, result.YAML, "running tests")
	require.NotEmpty(t, result.Assessment)
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Empty(t, detail.Versions, "publication belongs to the atomic run ledger transaction")
}

func TestRepairingPatchClearsEarlierValidationError(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	parent := Version{ID: "v1", YAML: "agents:\n  root:\n    model: auto\n    instruction: Execute the task. Verify the changes.\n"}
	attempts := 0
	service, err := NewService(t.Context(), store, fakeRunner{run: func(_ context.Context, request Request, _ func(Result)) (Result, error) {
		if strings.Contains(string(request.Config), "run_assessment") {
			return Result{Output: `{"quality":"unknown","efficiency":"unknown","changeImpact":"inconclusive","summary":"Incomplete evidence","improvements":[]}`}, nil
		}
		attempts++
		if attempts == 1 {
			return Result{Output: `{"base_hash":"wrong","edits":[],"rationale":"retry"}`}, nil
		}
		data, err := json.Marshal(instructionPatch{BaseHash: configHash(parent.YAML), Edits: []instructionEdit{{"Verify the changes.", "Run tests to verify the changes."}}, Rationale: "Tests"})
		require.NoError(t, err)
		return Result{Output: string(data)}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	result, err := service.LearnRun(t.Context(), loop.ID, RunLearningRequest{Parent: parent, Evidence: json.RawMessage(`{}`), History: json.RawMessage(`[]`)})
	require.NoError(t, err)
	assert.NotEmpty(t, result.YAML)
	assert.Empty(t, result.Error)
	assert.Len(t, result.RevisionJobs, 2)
}

func TestInvalidJudgeStopsBeforeDesign(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	calls := 0
	service, err := NewService(t.Context(), store, fakeRunner{run: func(context.Context, Request, func(Result)) (Result, error) {
		calls++
		return Result{Output: `{"quality":"satisfied","efficiency":"efficient","changeImpact":"helped","summary":"` + strings.Repeat("x", 8001) + `","improvements":[]}`}, nil
	}}, "auto", func() {})
	require.NoError(t, err)
	result, err := service.LearnRun(t.Context(), loop.ID, RunLearningRequest{Evidence: json.RawMessage(`{}`), History: json.RawMessage(`[]`)})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.NotEmpty(t, result.JudgeJob)
	assert.Empty(t, result.RevisionJobs)
}

func TestLiveRunLearning(t *testing.T) {
	if os.Getenv("BOARD_LOOPS_SMOKE") != "1" {
		t.Skip("set BOARD_LOOPS_SMOKE=1 to call the provider")
	}
	store := testStore(t)
	loop := testLoop(t, store)
	service, err := NewService(t.Context(), store, &ProcessRunner{Directory: t.TempDir()}, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	result, err := service.LearnRun(t.Context(), loop.ID, RunLearningRequest{
		RunID: "synthetic-test", Project: "test-fixture", Complete: true, Task: "Update dependencies and run tests", Criteria: "Actual updated files and passing tests",
		Parent:   Version{ID: "fixture", YAML: "agents:\n  root:\n    model: auto\n    instruction: Update the dependencies. Report the result.\n"},
		Evidence: json.RawMessage(`{"fixture":true,"outcome":"normal","events":[{"type":"tool_call","command":"go get example.com/module@v1.2.3"},{"type":"tool_call_response","response":"dependency updated"},{"type":"agent_choice","content":"Dependency updated. I did not run tests."}]}`),
		History:  json.RawMessage(`{"recentRuns":[]}`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.YAML, result.Error)
	var assessment RunAssessment
	require.NoError(t, json.Unmarshal(result.Assessment, &assessment))
	require.NoError(t, assessment.Validate())
	assert.NotEqual(t, "satisfied", assessment.Quality)
	_, err = ProjectInstruction(Version{YAML: result.YAML})
	require.NoError(t, err)
	jobs, err := store.Jobs(t.Context(), loop.ID)
	require.NoError(t, err)
	for _, job := range jobs {
		assert.Contains(t, []string{"run_judge", "run_revision"}, job.Kind)
	}
	t.Logf("Judged synthetic execution and prepared a validated patch; quality=%s. No project task executed.", assessment.Quality)
}
