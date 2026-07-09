package board

import (
	"fmt"
	"net/http"
	"strings"
)

func (b *Board) handleListColumns(w http.ResponseWriter, _ *http.Request) {
	cols, err := b.store.ListColumns()
	if err != nil {
		writeError(w, fmt.Errorf("list columns: %w", err))
		return
	}
	writeJSON(w, cols)
}

// handleUpdateColumns replaces the whole column configuration with the posted
// list: names, emojis, prompts and order. Columns without an id are new and
// get one generated; columns missing from the list are deleted (rejected if
// they still hold cards). The saved list, including generated ids, is
// returned.
func (b *Board) handleUpdateColumns(w http.ResponseWriter, r *http.Request) {
	var cols []Column
	if !decodeJSON(w, r, &cols) {
		return
	}

	if err := normalizeColumns(cols); err != nil {
		writeError(w, err)
		return
	}

	if err := b.store.ReplaceColumns(cols); err != nil {
		writeError(w, fmt.Errorf("update columns: %w", err))
		return
	}

	b.broadcast()
	writeJSON(w, cols)
}

// normalizeColumns validates a posted column list in place: every column
// needs a name, ids must be unique, and columns without an id (new ones) get
// one generated.
func normalizeColumns(cols []Column) error {
	if len(cols) == 0 {
		return fmt.Errorf("%w: at least one column required", errBadInput)
	}
	seen := make(map[string]bool, len(cols))
	for i := range cols {
		cols[i].Name = strings.TrimSpace(cols[i].Name)
		if cols[i].Name == "" {
			return fmt.Errorf("%w: column name required", errBadInput)
		}
		if cols[i].ID == "" {
			cols[i].ID = newID()
		}
		if seen[cols[i].ID] {
			return fmt.Errorf("%w: duplicate column id %q", errBadInput, cols[i].ID)
		}
		seen[cols[i].ID] = true
	}
	return nil
}
