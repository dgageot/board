package loops

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
)

// Handler serves the loop workspace without depending on Board's card model.
type Handler struct {
	store   *Store
	changed func()
}

// Register mounts the loop API on Board's same-origin protected router.
func Register(mux *http.ServeMux, store *Store, changed func()) {
	h := &Handler{store: store, changed: changed}
	mux.HandleFunc("GET /api/loops", h.list)
	mux.HandleFunc("POST /api/loops", h.create)
	mux.HandleFunc("GET /api/loops/{id}", h.get)
	mux.HandleFunc("POST /api/loops/{id}/versions", h.addVersion)
	mux.HandleFunc("GET /api/loops/{id}/versions/{version}/export", h.export)
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("loop response: %v", err)
	}
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("loops: %v", err)
		http.Error(w, "loop operation failed", http.StatusInternalServerError)
	}
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "expected one JSON object", http.StatusBadRequest)
		return false
	}
	return true
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	loops, err := h.store.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, loops)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Criteria    string `json:"criteria"`
		Example     string `json:"example"`
	}
	if !decode(w, r, &input) {
		return
	}
	loop, err := h.store.Create(r.Context(), Loop{Name: input.Name, Description: input.Description, Criteria: input.Criteria, Example: input.Example})
	if err != nil {
		fail(w, err)
		return
	}
	h.changed()
	respond(w, http.StatusCreated, loop)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	detail, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, detail)
}

func (h *Handler) addVersion(w http.ResponseWriter, r *http.Request) {
	var input struct {
		YAML      string `json:"yaml"`
		Rationale string `json:"rationale"`
	}
	if !decode(w, r, &input) {
		return
	}
	version, err := h.store.AddVersion(r.Context(), r.PathValue("id"), input.YAML, input.Rationale)
	if err != nil {
		fail(w, err)
		return
	}
	h.changed()
	respond(w, http.StatusCreated, version)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	detail, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	for _, version := range detail.Versions {
		if version.ID == r.PathValue("version") {
			w.Header().Set("Content-Type", "application/yaml")
			w.Header().Set("Content-Disposition", `attachment; filename="agent.yaml"`)
			_, _ = io.WriteString(w, version.YAML)
			return
		}
	}
	fail(w, ErrNotFound)
}
