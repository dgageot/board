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
	// errCardRunning rejects a forward move of a running card. It is checked
	// inside the store transaction so a watcher flipping the status
	// concurrently cannot slip a running card past the handler's check.
	errCardRunning = fmt.Errorf("%w: cannot move a running card forward", errBadInput)
)

// writeError maps domain errors to HTTP responses.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errBadInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, errAgentStarting):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		log.Printf("internal error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
