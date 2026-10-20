package loops

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDesignEvidencePairsOldFeedbackAndExcludesRecursivePrompts(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	job, err := store.newJob(t.Context(), loop.ID, "v1", "run", "original input")
	require.NoError(t, err)
	job.Status = "succeeded"
	job.Output = "original output"
	require.NoError(t, store.saveJob(t.Context(), job))
	_, err = store.AddFeedback(t.Context(), loop.ID, job.ID, "bad", "fix the ending")
	require.NoError(t, err)
	for range 12 {
		_, err := store.newJob(t.Context(), loop.ID, "", "proposal", "RECURSIVE PROMPT "+strings.Repeat("x", 100000))
		require.NoError(t, err)
	}
	data, err := store.designEvidence(t.Context(), loop, Version{ID: "v1", YAML: "agents: {}"})
	require.NoError(t, err)
	assert.Less(t, len(data), maxDesignEvidence)
	assert.Contains(t, string(data), "original input")
	assert.Contains(t, string(data), "original output")
	assert.Contains(t, string(data), "fix the ending")
	assert.NotContains(t, string(data), "RECURSIVE PROMPT")
	assert.True(t, json.Valid(data))
}

func TestDesignEvidenceBoundsLargeFeedback(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	job, err := store.newJob(t.Context(), loop.ID, "v1", "run", strings.Repeat("x", 60000))
	require.NoError(t, err)
	job.Status = "succeeded"
	job.Output = strings.Repeat("z", 1000000)
	require.NoError(t, store.saveJob(t.Context(), job))
	for range 20 {
		_, err := store.AddFeedback(t.Context(), loop.ID, job.ID, "bad", strings.Repeat("é", 7000))
		require.NoError(t, err)
	}
	data, err := store.designEvidence(t.Context(), loop, Version{ID: "v1", YAML: strings.Repeat("x", 256000)})
	require.NoError(t, err)
	assert.Less(t, len(data), maxDesignEvidence)
	assert.True(t, json.Valid(data))
	assert.Contains(t, string(data), "[truncated]")
}
