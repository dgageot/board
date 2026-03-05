package board

import (
	"fmt"
	"net/http"
)

func (b *Board) handleListColumns(w http.ResponseWriter, _ *http.Request) {
	cols, err := b.store.ListColumns()
	if err != nil {
		writeError(w, fmt.Errorf("list columns: %w", err))
		return
	}
	writeJSON(w, cols)
}

func (b *Board) handleUpdateColumns(w http.ResponseWriter, r *http.Request) {
	var updates []Column
	if err := readJSON(r, &updates); err != nil {
		writeError(w, fmt.Errorf("%w: invalid json", errBadInput))
		return
	}

	for _, u := range updates {
		if err := b.store.UpdateColumnPrompt(u.ID, u.Prompt); err != nil {
			writeError(w, fmt.Errorf("update column %s: %w", u.ID, err))
			return
		}
	}

	b.broadcast()
	w.WriteHeader(http.StatusNoContent)
}
