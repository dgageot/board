package board

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func projectTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
	return repo
}

func TestHandleUpdateProject(t *testing.T) {
	b, store := newTestBoard(t)
	for _, id := range []string{"other", "target", "last"} {
		require.NoError(t, store.InsertProject(&Project{ID: id, Name: id, RepoPath: "/old/repo", Agent: "/old/agent"}))
	}
	require.NoError(t, store.ReorderProjects([]string{"last", "target", "other"}))
	before, err := store.ListProjects()
	require.NoError(t, err)
	card := &Card{ID: "card-1", Project: "target", RepoPath: "/old/repo", Agent: "/old/agent"}
	require.NoError(t, store.InsertCard(card))

	updates := make(chan struct{}, 2)
	b.addClient(updates)
	defer b.removeClient(updates)
	repo := projectTestRepo(t)
	body, err := json.Marshal(Project{
		ID: "other", Name: " \tNew project\n ", RepoPath: " \t" + repo + "\n ", Agent: " \t/new/agent\n ", Pos: 99,
	})
	require.NoError(t, err)
	handler, err := buildMux(b)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/projects/target", bytes.NewReader(body)))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var updated Project
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&updated))
	want := &Project{ID: "target", Name: "New project", RepoPath: repo, Agent: "/new/agent", Pos: 1}
	assert.Equal(t, want, &updated)
	projects, err := store.ListProjects()
	require.NoError(t, err)
	assert.Equal(t, []*Project{before[0], want, before[2]}, projects)
	storedCard, err := store.GetCard(card.ID)
	require.NoError(t, err)
	assert.Equal(t, card, storedCard, "existing cards retain their project snapshots")
	name, agent, repoPath, err := b.resolveProject("target")
	require.NoError(t, err)
	assert.Equal(t, want.Name, name)
	assert.Equal(t, want.Agent, agent)
	assert.Equal(t, want.RepoPath, repoPath)
	assert.Len(t, updates, 1, "a successful edit broadcasts once")
}

func TestHandleUpdateProjectRejectsBadInput(t *testing.T) {
	repo := projectTestRepo(t)
	nonRepo := t.TempDir()
	valid := fmt.Sprintf(`{"name":"New","repoPath":%q,"agent":"/agent"}`, repo)
	tests := []struct {
		name    string
		body    string
		message string
	}{
		{"invalid JSON", `{`, "invalid json"},
		{"wrong field type", `{"name":42}`, "invalid json"},
		{"null", `null`, "name required"},
		{"missing name", strings.Replace(valid, `"name":"New",`, "", 1), "name required"},
		{"blank name", strings.Replace(valid, `"New"`, `" \t\n "`, 1), "name required"},
		{"missing repoPath", `{"name":"New","agent":"/agent"}`, "repoPath required"},
		{"blank repoPath", `{"name":"New","repoPath":" \t\n ","agent":"/agent"}`, "repoPath required"},
		{"missing agent", fmt.Sprintf(`{"name":"New","repoPath":%q}`, repo), "agent required"},
		{"blank agent", strings.Replace(valid, `"/agent"`, `" \t\n "`, 1), "agent required"},
		{"not a repository", fmt.Sprintf(`{"name":"New","repoPath":%q,"agent":"/agent"}`, nonRepo), "not a git repository"},
		{"missing repository", fmt.Sprintf(`{"name":"New","repoPath":%q,"agent":"/agent"}`, filepath.Join(nonRepo, "missing")), "not a git repository"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, store := newTestBoard(t)
			original := &Project{ID: "target", Name: "Old", RepoPath: "/old/repo", Agent: "/old/agent"}
			require.NoError(t, store.InsertProject(original))
			updates := make(chan struct{}, 1)
			b.addClient(updates)
			defer b.removeClient(updates)
			handler, err := buildMux(b)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/projects/target", strings.NewReader(tt.body)))

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.message)
			stored, err := store.GetProject(original.ID)
			require.NoError(t, err)
			assert.Equal(t, original, stored)
			assert.Empty(t, updates, "a rejected edit must not broadcast")
		})
	}
}

type projectUpdateStore struct {
	Store

	update func(*Project) (*Project, error)
}

func (s projectUpdateStore) UpdateProject(p *Project) (*Project, error) {
	return s.update(p)
}

func TestHandleUpdateProjectStoreErrors(t *testing.T) {
	repo := projectTestRepo(t)
	tests := []struct {
		name      string
		missing   bool
		disappear bool
		err       error
		status    int
		message   string
	}{
		{name: "unknown ID", missing: true, status: http.StatusNotFound, message: "not found: project target"},
		{name: "concurrent deletion", disappear: true, status: http.StatusNotFound, message: "not found: project target"},
		{name: "wrapped not found", err: fmt.Errorf("update: %w", sql.ErrNoRows), status: http.StatusNotFound, message: "not found: project target"},
		{name: "internal failure", err: errors.New("private database error"), status: http.StatusInternalServerError, message: "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, store := newTestBoard(t)
			original := &Project{ID: "target", Name: "Old", RepoPath: "/old/repo", Agent: "/old/agent"}
			if !tt.missing {
				require.NoError(t, store.InsertProject(original))
			}
			if tt.err != nil || tt.disappear {
				b.store = projectUpdateStore{Store: store, update: func(p *Project) (*Project, error) {
					if tt.disappear {
						require.NoError(t, store.DeleteProject(p.ID))
						return store.UpdateProject(p)
					}
					return nil, tt.err
				}}
			}
			updates := make(chan struct{}, 1)
			b.addClient(updates)
			defer b.removeClient(updates)
			handler, err := buildMux(b)
			require.NoError(t, err)
			body := fmt.Sprintf(`{"name":"New","repoPath":%q,"agent":"/new/agent"}`, repo)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/projects/target", strings.NewReader(body)))

			assert.Equal(t, tt.status, rec.Code)
			assert.Equal(t, tt.message+"\n", rec.Body.String())
			assert.Empty(t, updates, "a failed edit must not broadcast")
			stored, err := store.GetProject("target")
			if tt.missing || tt.disappear {
				require.ErrorIs(t, err, sql.ErrNoRows)
				assert.Nil(t, stored, "updates must not recreate a missing project")
			} else {
				require.NoError(t, err)
				assert.Equal(t, original, stored)
			}
		})
	}
}

func TestHandleReorderProjectsRoute(t *testing.T) {
	b, store := newTestBoard(t)
	for _, id := range []string{"first", "second"} {
		require.NoError(t, store.InsertProject(&Project{ID: id, Name: id, RepoPath: "/repo", Agent: "/agent"}))
	}
	handler, err := buildMux(b)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/projects/order", strings.NewReader(`["second","first"]`)))

	require.Equal(t, http.StatusNoContent, rec.Code)
	projects, err := store.ListProjects()
	require.NoError(t, err)
	require.Len(t, projects, 2)
	assert.Equal(t, "second", projects[0].ID)
	assert.Equal(t, "first", projects[1].ID)
}
