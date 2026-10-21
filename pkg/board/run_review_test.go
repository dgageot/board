package board

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunReviewTracksActualInitialRootTurn(t *testing.T) {
	review := RunReview{State: "observing"}
	events := []string{
		`{"type":"stream_started","session_id":"child","timestamp":"2026-10-21T10:00:00Z"}`,
		`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T10:00:01Z"}`,
		`{"type":"stream_started","session_id":"child","timestamp":"2026-10-21T10:00:02Z"}`,
		`{"type":"token_usage","session_id":"child","usage":{"cost":0.05}}`,
		`{"type":"stream_stopped","session_id":"child","reason":"normal","timestamp":"2026-10-21T10:00:03Z"}`,
		`{"type":"token_usage","session_id":"root","usage":{"cost":0.1}}`,
		`{"type":"stream_stopped","session_id":"root","reason":"normal","timestamp":"2026-10-21T10:00:06Z"}`,
	}
	for i, event := range events {
		require.NoError(t, review.observe("root", uint64(i+1), []byte(event)))
	}
	require.NotNil(t, review.DurationMS)
	assert.Equal(t, int64(5000), *review.DurationMS)
	require.NotNil(t, review.Cost)
	assert.InDelta(t, .15, *review.Cost, .00001)
	assert.Equal(t, "pending", review.State)
	assert.True(t, review.Complete)
	saved, err := json.Marshal(review)
	require.NoError(t, err)
	require.NoError(t, review.observe("root", 99, []byte(`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T11:00:00Z"}`)))
	after, err := json.Marshal(review)
	require.NoError(t, err)
	assert.JSONEq(t, string(saved), string(after))
}

func TestRunReviewNeverInfersCompletionFromIdleOrChildStop(t *testing.T) {
	review := RunReview{State: "observing"}
	require.NoError(t, review.observe("root", 1, []byte(`{"type":"stream_stopped","session_id":"root","reason":"normal"}`)))
	assert.Empty(t, review.FinishedAt)
	require.NoError(t, review.observe("root", 2, []byte(`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T10:00:00Z"}`)))
	require.NoError(t, review.observe("root", 3, []byte(`{"type":"stream_stopped","session_id":"child","reason":"normal","timestamp":"2026-10-21T10:00:02Z"}`)))
	assert.Empty(t, review.FinishedAt)
}

func TestRunAssessmentValidatesEvidenceLabels(t *testing.T) {
	require.NoError(t, validAssessment(RunAssessment{Quality: "unknown", Efficiency: "mixed", ChangeImpact: "inconclusive", Summary: "CI evidence absent"}))
	require.Error(t, validAssessment(RunAssessment{Quality: "perfect", Summary: "Trust me"}))
}

func TestRunReviewGapsNeverClaimCompleteEvidence(t *testing.T) {
	for _, gap := range []bool{false, true} {
		review := RunReview{State: "observing"}
		require.NoError(t, review.observe("root", 1, []byte(`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T10:00:00Z"}`)))
		if gap {
			require.NoError(t, review.observe("root", 0, []byte(`{"type":"gap"}`)))
		}
		require.NoError(t, review.observe("root", 3, []byte(`{"type":"stream_stopped","session_id":"root","reason":"normal","timestamp":"2026-10-21T10:00:02Z"}`)))
		assert.False(t, review.Complete)
		assert.Nil(t, review.Cost)
	}
}

func TestRunReviewTracksDetachedChildrenAndMissingUsage(t *testing.T) {
	review := RunReview{State: "observing"}
	for i, event := range []string{
		`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T10:00:00Z"}`,
		`{"type":"background_agent_status","session_id":"child","running":true}`,
		`{"type":"token_usage","session_id":"root","usage":{"cost":0.1}}`,
		`{"type":"stream_stopped","session_id":"root","reason":"normal","timestamp":"2026-10-21T10:00:02Z"}`,
	} {
		require.NoError(t, review.observe("root", uint64(i+1), []byte(event)))
	}
	assert.False(t, review.Complete)
	assert.Nil(t, review.Cost)
	complete := RunReview{State: "observing"}
	for i, event := range []string{
		`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T10:00:00Z"}`,
		`{"type":"stream_started","session_id":"child","timestamp":"2026-10-21T10:00:00Z"}`,
		`{"type":"stream_stopped","session_id":"child","reason":"normal","timestamp":"2026-10-21T10:00:01Z"}`,
		`{"type":"token_usage","session_id":"root","usage":{"cost":0.1}}`,
		`{"type":"stream_stopped","session_id":"root","reason":"normal","timestamp":"2026-10-21T10:00:02Z"}`,
	} {
		require.NoError(t, complete.observe("root", uint64(i+1), []byte(event)))
	}
	assert.Nil(t, complete.Cost)
}

func TestUnpricedBudgetKeepsPositiveSubtotalUnknown(t *testing.T) {
	review := RunReview{State: "observing"}
	for i, event := range []string{
		`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T10:00:00Z"}`,
		`{"type":"budget_usage","budgets":[{"unpriced":true}]}`,
		`{"type":"token_usage","session_id":"root","usage":{"cost":0.4}}`,
		`{"type":"stream_stopped","session_id":"root","reason":"normal","timestamp":"2026-10-21T10:00:02Z"}`,
	} {
		require.NoError(t, review.observe("root", uint64(i+1), []byte(event)))
	}
	assert.True(t, review.Complete)
	assert.True(t, review.Unpriced)
	assert.Nil(t, review.Cost)
}

func TestStoredReviewReplayKeepsFirstResultAndGapUncertainty(t *testing.T) {
	worker, task := learningFixture(t)
	_, err := worker.store.db.Exec(`UPDATE loop_run_reviews SET state='observing',data='{}' WHERE task_id=?`, task.ID)
	require.NoError(t, err)
	start := []byte(`{"type":"stream_started","session_id":"root","timestamp":"2026-10-21T10:00:00Z"}`)
	require.NoError(t, worker.store.ObserveRun(task.CardID, "root", 1, start))
	require.NoError(t, worker.store.ObserveRun(task.CardID, "root", 1, start))
	require.NoError(t, worker.store.ObserveRun(task.CardID, "root", 0, []byte(`{"type":"gap"}`)))
	require.NoError(t, worker.store.ObserveRun(task.CardID, "root", 2, []byte(`{"type":"stream_stopped","session_id":"root","reason":"normal","timestamp":"2026-10-21T10:00:02Z"}`)))
	review, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", review.State)
	assert.False(t, review.Complete)
	assert.Len(t, review.Events, 2)
	require.NoError(t, worker.store.ObserveRun(task.CardID, "root", 3, start))
	frozen, err := worker.store.RunReview(task.ID)
	require.NoError(t, err)
	assert.Equal(t, review, frozen)
}
