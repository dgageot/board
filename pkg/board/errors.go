package board

import (
	"errors"
	"log"
	"net/http"
)

var (
	errNotFound = errors.New("not found")
	errBadInput = errors.New("bad input")
)

// writeError maps domain errors to HTTP responses.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errBadInput):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		log.Printf("internal error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
