package loops

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverviewKeepsPayloadsOutOfPolling(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	project := &ProjectSnapshot{ID: "project", Files: []ProjectFile{{Path: "file.txt", Content: strings.Repeat("snapshot", 10000)}}}
	job, err := store.newJob(t.Context(), loop.ID, "version", "run", strings.Repeat("input", 10000), project)
	require.NoError(t, err)
	job.Output = strings.Repeat("output", 10000)
	job.Status = "succeeded"
	require.NoError(t, store.saveJob(t.Context(), job))
	view, err := store.Overview(t.Context(), loop.ID)
	require.NoError(t, err)
	require.Len(t, view.Jobs, 1)
	assert.Empty(t, view.Jobs[0].Output)
	assert.Empty(t, view.Jobs[0].Input)
	assert.Empty(t, view.Jobs[0].Project.Files)
	assert.Equal(t, len(job.Output), view.Jobs[0].OutputSize)
	data, err := json.Marshal(view)
	require.NoError(t, err)
	assert.Less(t, len(data), 3000)
	selected, err := store.DisplayJob(t.Context(), loop.ID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, job.Output, selected.Output)
	assert.Empty(t, selected.Project.Files)
	_, err = store.DisplayJob(t.Context(), "other", job.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRecentInputsDoNotLoseOlderCasesBehindRetries(t *testing.T) {
	store := testStore(t)
	loop := testLoop(t, store)
	project := &ProjectSnapshot{ID: "project"}
	inputs := append([]string{"A", "B"}, make([]string, 25)...)
	for i, input := range inputs {
		if input == "" {
			input = "C"
		}
		job, err := store.newJob(t.Context(), loop.ID, "v", "run", input, project)
		require.NoError(t, err)
		_, err = store.db.Exec(`UPDATE loop_jobs SET created_at=? WHERE id=?`, fmt.Sprintf("2026-10-21T10:00:%02dZ", i), job.ID)
		require.NoError(t, err)
	}
	jobs, err := store.LatestRunInputs(t.Context(), loop.ID, "v")
	require.NoError(t, err)
	require.Len(t, jobs, 3)
	assert.Equal(t, "C", jobs[0].Input)
	assert.Equal(t, "B", jobs[1].Input)
	assert.Equal(t, "A", jobs[2].Input)
}
