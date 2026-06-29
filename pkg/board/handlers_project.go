package board

import (
	"fmt"
	"net/http"

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
