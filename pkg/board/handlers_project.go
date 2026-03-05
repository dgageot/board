package board

import (
	"fmt"
	"net/http"
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
	if err := readJSON(r, &p); err != nil {
		writeError(w, fmt.Errorf("%w: invalid json", errBadInput))
		return
	}

	p.ID = newID()
	if p.Agent == "" {
		p.Agent = b.config.DefaultAgent
	}
	if p.RepoPath == "" {
		p.RepoPath = b.config.DefaultRepoPath
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
