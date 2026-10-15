package board

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dgageot/board/pkg/git"
)

func (b *Board) handleListProjects(w http.ResponseWriter, _ *http.Request) {
	projects, err := b.store.ListProjects()
	if err != nil {
		writeError(w, fmt.Errorf("list projects: %w", err))
		return
	}
	writeJSON(w, projects)
}

func (b *Board) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var p Project
	if !decodeJSON(w, r, &p) {
		return
	}

	p.ID = newID()

	if p.RepoPath == "" {
		writeError(w, fmt.Errorf("%w: repoPath required", errBadInput))
		return
	}
	if p.Agent == "" {
		writeError(w, fmt.Errorf("%w: agent required", errBadInput))
		return
	}

	if !git.IsRepo(p.RepoPath) {
		writeError(w, fmt.Errorf("%w: %q is not a git repository", errBadInput, p.RepoPath))
		return
	}

	if err := b.store.InsertProject(&p); err != nil {
		writeError(w, fmt.Errorf("insert project: %w", err))
		return
	}

	b.broadcast()
	writeJSON(w, p)
}

func (b *Board) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	var p Project
	if !decodeJSON(w, r, &p) {
		return
	}

	p.ID = r.PathValue("id")
	p.Name = strings.TrimSpace(p.Name)
	p.RepoPath = strings.TrimSpace(p.RepoPath)
	p.Agent = strings.TrimSpace(p.Agent)

	if p.Name == "" {
		writeError(w, fmt.Errorf("%w: name required", errBadInput))
		return
	}
	if p.RepoPath == "" {
		writeError(w, fmt.Errorf("%w: repoPath required", errBadInput))
		return
	}
	if p.Agent == "" {
		writeError(w, fmt.Errorf("%w: agent required", errBadInput))
		return
	}
	if !git.IsRepo(p.RepoPath) {
		writeError(w, fmt.Errorf("%w: %q is not a git repository", errBadInput, p.RepoPath))
		return
	}

	project, err := b.store.UpdateProject(&p)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, fmt.Errorf("%w: project %s", errNotFound, p.ID))
		return
	case err != nil:
		writeError(w, fmt.Errorf("update project %s: %w", p.ID, err))
		return
	}

	b.broadcast()
	writeJSON(w, project)
}

func (b *Board) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := b.store.DeleteProject(id); err != nil {
		writeError(w, fmt.Errorf("delete project: %w", err))
		return
	}

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}

func (b *Board) handleReorderProjects(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if !decodeJSON(w, r, &ids) {
		return
	}

	if err := b.store.ReorderProjects(ids); err != nil {
		writeError(w, fmt.Errorf("reorder projects: %w", err))
		return
	}

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}
