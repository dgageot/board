package loops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cycleFixture(t *testing.T, runner Runner) (*Service, *Store, Loop, OptimizeRequest) {
	t.Helper()
	store := testStore(t)
	project := testProject(t, store)
	loop := testLoop(t, store)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Stage zero. Preserve facts and all task requirements.\n", "initial")
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, runner, "auto", func() {})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	return service, store, loop, OptimizeRequest{ProjectID: project.ID, Version: version.ID, Inputs: []string{"input"}, Repeats: 2}
}

func finalRound(t *testing.T, service *Service, store *Store, loopID string) Round {
	t.Helper()
	service.Wait()
	rounds, err := store.Rounds(t.Context(), loopID)
	require.NoError(t, err)
	require.Len(t, rounds, 1)
	return rounds[0]
}

func patchResponse(t *testing.T, req Request, next string) Result {
	t.Helper()
	input := patchPayload(t, req.Input)
	data, err := json.Marshal(instructionPatch{BaseHash: input.BaseHash, Rationale: "Target reported failure", Edits: []instructionEdit{{Old: "Stage zero.", New: next}}})
	require.NoError(t, err)
	return Result{Output: string(data)}
}

func TestOptimizerStopsOnCapabilityMismatch(t *testing.T) {
	var calls atomic.Int32
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		calls.Add(1)
		assert.Contains(t, string(req.Config), "capability_check")
		return Result{Output: `{"feasible":false,"reason":"Requires writable files, CI execution and signed commits; runtime is read-only."}`}, nil
	}})
	_, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	got := finalRound(t, service, store, loop.ID)
	assert.Equal(t, "blocked", got.Status)
	assert.Empty(t, got.Winner)
	assert.Equal(t, int32(1), calls.Load())
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Len(t, detail.Versions, 1)
	assert.Empty(t, detail.Loop.ActiveVersion)
}

func TestOptimizerUsesFailedEvaluationsUntilRequirementsPass(t *testing.T) {
	var designs atomic.Int32
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		switch {
		case strings.Contains(string(req.Config), "capability_check"):
			return Result{Output: `{"feasible":true,"reason":"Text task"}`}, nil
		case strings.Contains(string(req.Config), "instruction_patch"):
			n := designs.Add(1)
			assert.Contains(t, req.Input, "Missing required summary")
			if n <= 2 {
				return patchResponse(t, req, "Stage one."), nil
			}
			return patchResponse(t, req, "Stage two."), nil
		case strings.Contains(string(req.Config), "quality_judgment"):
			if strings.Contains(req.Input, "complete answer") {
				return Result{Output: `{"passed":true,"reason":"All requirements satisfied"}`}, nil
			}
			return Result{Output: `{"passed":false,"reason":"Missing required summary"}`}, nil
		default:
			answer := "missing summary"
			if strings.Contains(string(req.Config), "Stage two.") {
				answer = "complete answer"
			}
			return Result{Output: answer}, nil
		}
	}})
	_, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	got := finalRound(t, service, store, loop.ID)
	assert.Equal(t, "completed", got.Status, got.Error)
	assert.Equal(t, "improved", got.Outcome)
	assert.Equal(t, 2, got.Pass)
	assert.NotEmpty(t, got.Winner)
	assert.Equal(t, int32(4), designs.Load())
	detail, err := store.Get(t.Context(), loop.ID)
	require.NoError(t, err)
	assert.Empty(t, detail.Loop.ActiveVersion)
}

func TestOptimizerExhaustionIsNotSuccess(t *testing.T) {
	var designs atomic.Int32
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		switch {
		case strings.Contains(string(req.Config), "capability_check"):
			return Result{Output: `{"feasible":true,"reason":"Text task"}`}, nil
		case strings.Contains(string(req.Config), "instruction_patch"):
			designs.Add(1)
			return patchResponse(t, req, "Stage one."), nil
		case strings.Contains(string(req.Config), "quality_judgment"):
			return Result{Output: `{"passed":false,"reason":"Still incorrect"}`}, nil
		default:
			return Result{Output: "incorrect"}, nil
		}
	}})
	_, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	got := finalRound(t, service, store, loop.ID)
	assert.Equal(t, "exhausted", got.Status, got.Error)
	assert.Equal(t, 4, got.Pass)
	assert.Empty(t, got.Winner)
	assert.Equal(t, int32(8), designs.Load())
}

func TestOptimizerDoesNotTreatProviderFailureAsQualityFeedback(t *testing.T) {
	var designs atomic.Int32
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		if strings.Contains(string(req.Config), "capability_check") {
			return Result{Output: `{"feasible":true,"reason":"Text task"}`}, nil
		}
		if strings.Contains(string(req.Config), "instruction_patch") {
			designs.Add(1)
		}
		return Result{}, errors.New("provider unavailable")
	}})
	_, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	got := finalRound(t, service, store, loop.ID)
	assert.Equal(t, "failed", got.Status)
	assert.Contains(t, got.Error, "provider unavailable")
	assert.Zero(t, designs.Load())
}

func TestPreservesPreviouslyPassingCases(t *testing.T) {
	parent := Candidate{Trials: []Trial{{Case: 0, Repeat: 0, Passed: true}, {Case: 1, Repeat: 0, Passed: false}}}
	regressed := Candidate{Trials: []Trial{{Case: 0, Repeat: 0, Passed: false}, {Case: 1, Repeat: 0, Passed: true}}}
	assert.False(t, preservesPasses(parent, regressed))
	improved := Candidate{Trials: []Trial{{Case: 0, Repeat: 0, Passed: true}, {Case: 1, Repeat: 0, Passed: true}}}
	assert.True(t, preservesPasses(parent, improved))
}

func TestOptimizerRetainsPassingBaselineWhenEditsRegress(t *testing.T) {
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		switch {
		case strings.Contains(string(req.Config), "capability_check"):
			return Result{Output: `{"feasible":true,"reason":"Text task"}`}, nil
		case strings.Contains(string(req.Config), "instruction_patch"):
			return patchResponse(t, req, "Stage broken."), nil
		case strings.Contains(string(req.Config), "quality_judgment"):
			if strings.Contains(req.Input, "baseline answer") {
				return Result{Output: `{"passed":true,"reason":"Correct"}`}, nil
			}
			return Result{Output: `{"passed":false,"reason":"Regression"}`}, nil
		default:
			if strings.Contains(string(req.Config), "Stage zero.") {
				return Result{Output: "baseline answer"}, nil
			}
			return Result{Output: "broken answer"}, nil
		}
	}})
	_, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	got := finalRound(t, service, store, loop.ID)
	assert.Equal(t, "completed", got.Status)
	assert.Equal(t, "unchanged", got.Outcome)
	assert.Equal(t, input.Version, got.Winner)
	assert.Equal(t, 4, got.Pass)
}

func TestOptimizerCancellationStopsFurtherDesigns(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(ctx context.Context, _ Request, _ func(Result)) (Result, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}})
	round, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	<-started
	service.Cancel(round.ID)
	got := finalRound(t, service, store, loop.ID)
	assert.Equal(t, "canceled", got.Status)
	assert.Equal(t, int32(1), calls.Load())
}

func TestCapabilityCheckIncludesActualRunInputs(t *testing.T) {
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		assert.Contains(t, req.Input, "Commit signed dependency updates")
		return Result{Output: `{"feasible":false,"reason":"Cannot sign commits in read-only mode"}`}, nil
	}})
	input.Inputs = []string{"Commit signed dependency updates"}
	_, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	assert.Equal(t, "blocked", finalRound(t, service, store, loop.ID).Status)
}

func TestOptimizerStopsOnRoundPersistenceFailure(t *testing.T) {
	var calls atomic.Int32
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(context.Context, Request, func(Result)) (Result, error) {
		calls.Add(1)
		return Result{Output: `{"feasible":true,"reason":"Text task"}`}, nil
	}})
	_, err := store.db.Exec(`CREATE TRIGGER fail_round_update BEFORE UPDATE ON loop_rounds BEGIN SELECT RAISE(FAIL,'disk failure'); END`)
	require.NoError(t, err)
	_, err = service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	service.Wait()
	assert.Equal(t, int32(1), calls.Load())
}

func TestLiveCapabilityBlock(t *testing.T) {
	if os.Getenv("BOARD_LOOPS_SMOKE") != "1" {
		t.Skip("set BOARD_LOOPS_SMOKE=1 to call provider")
	}
	store := testStore(t)
	project := testProject(t, store)
	loop, err := store.Create(t.Context(), Loop{Name: "Dependency updates", Description: "Bump the direct Go dependencies, adapt code, run CI and license checks.", Criteria: "Changes are committed in signed green commits with a table describing dependency updates.", ProjectID: project.ID})
	require.NoError(t, err)
	version, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Review dependencies from the supplied project.\n", "baseline")
	require.NoError(t, err)
	service, err := NewService(t.Context(), store, &ProcessRunner{Directory: t.TempDir()}, "auto", func() {})
	require.NoError(t, err)
	_, err = service.OptimizeDefault(t.Context(), loop.ID, version.ID)
	require.NoError(t, err)
	got := finalRound(t, service, store, loop.ID)
	require.Equal(t, "blocked", got.Status, got.Error)
	assert.Len(t, got.Candidates, 1)
	t.Log(got.Error)
}

func TestRejectedCandidateCannotUseInitialPromotionException(t *testing.T) {
	service, store, loop, input := cycleFixture(t, fakeRunner{})
	_ = service
	candidate, err := store.AddVersion(t.Context(), loop.ID, "agents:\n  root:\n    model: auto\n    instruction: Candidate.\n", "candidate")
	require.NoError(t, err)
	job, err := store.newJob(t.Context(), loop.ID, candidate.ID, "run", "input")
	require.NoError(t, err)
	job.Status = "succeeded"
	require.NoError(t, store.saveJob(t.Context(), job))
	require.ErrorIs(t, store.Promote(t.Context(), loop.ID, candidate.ID, ""), ErrConflict)
	first, err := store.newJob(t.Context(), loop.ID, input.Version, "run", "input")
	require.NoError(t, err)
	first.Status = "succeeded"
	require.NoError(t, store.saveJob(t.Context(), first))
	require.NoError(t, store.Promote(t.Context(), loop.ID, input.Version, ""))
}

func TestCandidateFailingConfirmationIsNotRecommended(t *testing.T) {
	var attempts atomic.Int32
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		switch {
		case strings.Contains(string(req.Config), "capability_check"):
			return Result{Output: `{"feasible":true,"reason":"Text task"}`}, nil
		case strings.Contains(string(req.Config), "instruction_patch"):
			return patchResponse(t, req, "Stage one."), nil
		case strings.Contains(string(req.Config), "quality_judgment"):
			if strings.Contains(req.Input, "valid answer") {
				return Result{Output: `{"passed":true,"reason":"Correct"}`}, nil
			}
			return Result{Output: `{"passed":false,"reason":"Missing requirement"}`}, nil
		default:
			if strings.Contains(string(req.Config), "Stage one.") && attempts.Add(1) <= 2 {
				return Result{Output: "valid answer"}, nil
			}
			return Result{Output: "missing requirement"}, nil
		}
	}})
	_, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	got := finalRound(t, service, store, loop.ID)
	assert.Equal(t, "exhausted", got.Status)
	assert.Empty(t, got.Winner)
	require.NotEmpty(t, got.Candidates[1].Confirmation)
	assert.Contains(t, got.Candidates[1].Decision, "confirmation")
	require.ErrorIs(t, store.Promote(t.Context(), loop.ID, got.Candidates[1].VersionID, ""), ErrConflict)
}

func TestInitialRoundResponseIsIndependentOfWorkerProgress(t *testing.T) {
	service, store, loop, input := cycleFixture(t, fakeRunner{run: func(_ context.Context, req Request, _ func(Result)) (Result, error) {
		switch {
		case strings.Contains(string(req.Config), "capability_check"):
			return Result{Output: `{"feasible":true,"reason":"Text task"}`}, nil
		case strings.Contains(string(req.Config), "quality_judgment"):
			return Result{Output: `{"passed":false,"reason":"Missing summary"}`}, nil
		case strings.Contains(string(req.Config), "instruction_patch"):
			return patchResponse(t, req, "Stage one."), nil
		default:
			return Result{Output: "answer"}, nil
		}
	}})
	response, err := service.Optimize(t.Context(), loop.ID, input)
	require.NoError(t, err)
	for range 1000 {
		_, err := json.Marshal(response)
		require.NoError(t, err)
	}
	finalRound(t, service, store, loop.ID)
	assert.Empty(t, response.Candidates[0].Trials)
	assert.Zero(t, response.Pass)
}
