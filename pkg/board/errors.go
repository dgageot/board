package board

import (
	"errors"
	"log"
	"net/http"
)

var (
	errNotFound      = errors.New("not found")
	errBadInput      = errors.New("bad input")
	errAgentStarting = errors.New("agent is still starting")
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
