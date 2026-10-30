package board

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func (b *Board) handleListTaskTemplates(w http.ResponseWriter, _ *http.Request) {
	templates, err := b.store.ListTaskTemplates()
	if err != nil {
		writeError(w, fmt.Errorf("list task templates: %w", err))
		return
	}
	writeJSON(w, templates)
}

func decodeTaskTemplate(w http.ResponseWriter, r *http.Request) (*TaskTemplate, bool) {
	var template TaskTemplate
	if !decodeJSON(w, r, &template) {
		return nil, false
	}
	template.Name = strings.TrimSpace(template.Name)
	if template.Name == "" {
		writeError(w, fmt.Errorf("%w: name required", errBadInput))
		return nil, false
	}
	if strings.TrimSpace(template.Prompt) == "" {
		writeError(w, fmt.Errorf("%w: prompt required", errBadInput))
		return nil, false
	}
	return &template, true
}

func (b *Board) handleCreateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	template, ok := decodeTaskTemplate(w, r)
	if !ok {
		return
	}
	template.ID = newID()
	if err := b.store.InsertTaskTemplate(template); err != nil {
		writeError(w, fmt.Errorf("insert task template: %w", err))
		return
	}

	b.broadcast()
	writeJSON(w, template)
}

func (b *Board) handleUpdateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	template, ok := decodeTaskTemplate(w, r)
	if !ok {
		return
	}
	template.ID = r.PathValue("id")
	stored, err := b.store.UpdateTaskTemplate(template)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, fmt.Errorf("%w: task template %s", errNotFound, template.ID))
		return
	case err != nil:
		writeError(w, fmt.Errorf("update task template %s: %w", template.ID, err))
		return
	}

	b.broadcast()
	writeJSON(w, stored)
}

func (b *Board) handleDeleteTaskTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch err := b.store.DeleteTaskTemplate(id); {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, fmt.Errorf("%w: task template %s", errNotFound, id))
		return
	case err != nil:
		writeError(w, fmt.Errorf("delete task template %s: %w", id, err))
		return
	}

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}
