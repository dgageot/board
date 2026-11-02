package board

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaceholderTitle(t *testing.T) {
	// Short prompts are used as-is.
	assert.Equal(t, "Fix the login bug", placeholderTitle("  Fix the login bug  "))

	// Only the first line is kept.
	assert.Equal(t, "Add tests", placeholderTitle("Add tests\nand make them pass"))

	// Long prompts are cut on a word boundary with an ellipsis.
	long := "Refactor the authentication module to support single sign-on across all services"
	got := placeholderTitle(long)
	assert.True(t, strings.HasSuffix(got, "…"), "long title is truncated")
	assert.LessOrEqual(t, len([]rune(got)), 41, "stays within the cap (40 + ellipsis)")
	assert.False(t, strings.HasSuffix(strings.TrimSuffix(got, "…"), " "), "no trailing space before ellipsis")
	assert.True(t, strings.HasPrefix(long, strings.TrimSuffix(got, "…")), "prefix of the prompt")
}

func TestPlaceholderTitleNoMidWordCut(t *testing.T) {
	// A single very long word (no space to break on) is still bounded.
	got := placeholderTitle(strings.Repeat("x", 100))
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.Len(t, []rune(got), 41)
}

func TestCreateCardSnapshotsProjectInstructions(t *testing.T) {
	b, store := newTestBoard(t)
	require.NoError(t, store.SeedColumns(defaultColumns))
	sessions := newFakeSessionManager()
	b.sessions = sessions
	b.controller.sessions = sessions
	project := &Project{ID: "project", Name: "Project", RepoPath: projectTestRepo(t), Agent: "/agent", Instructions: " \nAlways test.\n"}
	require.NoError(t, store.InsertProject(project))

	card, err := b.createCard("Do the work", project.ID)
	require.NoError(t, err)
	t.Cleanup(func() { b.controller.Stop(card.ID) })
	assert.Equal(t, project.Instructions, card.Instructions)
	assert.Equal(t, "Do the work", card.Prompt)
	require.Len(t, sessions.calls(), 1)
	assert.Equal(t, card.Instructions, sessions.calls()[0].instructions)
	assert.Equal(t, card.Prompt, sessions.calls()[0].prompt)

	project.Instructions = "New rules"
	_, err = store.UpdateProject(project)
	require.NoError(t, err)
	stored, err := store.GetCard(card.ID)
	require.NoError(t, err)
	assert.Equal(t, card.Instructions, stored.Instructions)
	future, err := b.createCard("Next task", project.ID)
	require.NoError(t, err)
	t.Cleanup(func() { b.controller.Stop(future.ID) })
	assert.Equal(t, project.Instructions, future.Instructions)
	assert.Equal(t, project.Instructions, sessions.calls()[1].instructions)
	// Resume from the persisted snapshot, even if its project has been deleted.
	require.NoError(t, store.DeleteProject(project.ID))
	require.NoError(t, b.controller.relaunch(stored, "Next column"))
	require.Len(t, sessions.calls(), 3)
	assert.Equal(t, card.Instructions, sessions.calls()[2].instructions)
	assert.Equal(t, "Next column", sessions.calls()[2].prompt)
	assert.Empty(t, sessions.calls()[2].worktreeName)
}
