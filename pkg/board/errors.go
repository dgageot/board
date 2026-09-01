package board

import (
	"errors"
	"fmt"
	"log"
	"net/http"
)

var (
	errNotFound      = errors.New("not found")
	errBadInput      = errors.New("bad input")
	errAgentStarting = errors.New("agent is still starting")
	// errAgentUnreachable reports a control plane that does not answer, so the
	// card's session cannot be read (e.g. to export its transcript for the
	// harness coach). Like errAgentStarting it is a "try again later", not a
	// board failure.
	errAgentUnreachable = errors.New("agent session is not reachable")
	// errCardRunning rejects a forward move of a running card. It is checked
	// inside the store transaction so a watcher flipping the status
	// concurrently cannot slip a running card past the handler's check.
	errCardRunning = fmt.Errorf("%w: cannot move a running card forward", errBadInput)
	// errColumnHasCards rejects a column update that would delete a column
	// still holding cards. It is checked inside the store transaction so a
	// concurrent card move cannot slip past the handler's view.
	errColumnHasCards = fmt.Errorf("%w: cannot delete a column that still has cards", errBadInput)
)

// writeError maps domain errors to HTTP responses.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errBadInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errAgentStarting), errors.Is(err, errAgentUnreachable):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		log.Printf("internal error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
