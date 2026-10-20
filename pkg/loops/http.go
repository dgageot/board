package loops

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// Handler serves the loop workspace without depending on Board's card model.
type Handler struct {
	store   *Store
	changed func()
	service *Service
}

// Register mounts the loop API on Board's same-origin protected router.
func Register(mux *http.ServeMux, store *Store, changed func(), services ...*Service) {
	h := &Handler{store: store, changed: changed}
	if len(services) > 0 {
		h.service = services[0]
	}
	mux.HandleFunc("POST /api/loops/{id}/generate", h.generate)
	mux.HandleFunc("POST /api/loops/{id}/optimize", h.optimize)
	mux.HandleFunc("POST /api/loops/{id}/optimize/default", h.optimizeDefault)
	mux.HandleFunc("POST /api/loops/{id}/promote", h.promote)
	mux.HandleFunc("POST /api/loops/{id}/feedback", h.feedback)
	mux.HandleFunc("POST /api/loops/{id}/rounds/{round}/cancel", h.cancelRound)
	mux.HandleFunc("POST /api/loops/{id}/runs", h.run)
	mux.HandleFunc("POST /api/loops/{id}/runs/{job}/close", h.closeRun)
	mux.HandleFunc("POST /api/loops/{id}/jobs/{job}/cancel", h.cancel)
	mux.HandleFunc("GET /api/loops/{id}/jobs/{job}/events", h.events)
	mux.HandleFunc("GET /api/loops/{id}/jobs/{job}/stderr", h.events)
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
	if !h.available(w) {
		return
	}
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Criteria    string `json:"criteria"`
		Example     string `json:"example"`
		ProjectID   string `json:"projectId"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ProjectID == "" {
		fail(w, fmt.Errorf("%w: choose a project for the first run", ErrInvalid))
		return
	}
	loop, err := h.service.Create(r.Context(), Loop{Name: input.Name, Description: input.Description, Criteria: input.Criteria, Example: input.Example, ProjectID: input.ProjectID})
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusCreated, loop)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	detail, err := h.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	jobs, err := h.store.Jobs(r.Context(), detail.Loop.ID)
	if err != nil {
		fail(w, err)
		return
	}
	rounds, err := h.store.Rounds(r.Context(), detail.Loop.ID)
	if err != nil {
		fail(w, err)
		return
	}
	feedback, err := h.store.Feedback(r.Context(), detail.Loop.ID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, struct {
		Detail

		Jobs     []Job      `json:"jobs"`
		Rounds   []Round    `json:"rounds"`
		Feedback []Feedback `json:"feedback"`
	}{Detail: detail, Jobs: jobs, Rounds: rounds, Feedback: feedback})
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

func (h *Handler) available(w http.ResponseWriter) bool {
	if h.service == nil {
		http.Error(w, "Loop execution is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	job, err := h.service.Generate(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusAccepted, job)
}

func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	var input struct {
		Version   string `json:"version"`
		Input     string `json:"input"`
		ProjectID string `json:"projectId"`
	}
	if !decode(w, r, &input) {
		return
	}
	job, err := h.service.Run(r.Context(), r.PathValue("id"), input.Version, input.Input, input.ProjectID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusAccepted, job)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	job, err := h.store.Job(r.Context(), r.PathValue("id"), r.PathValue("job"))
	if err != nil {
		fail(w, err)
		return
	}
	h.service.Cancel(job.ID)
	respond(w, http.StatusAccepted, map[string]string{"status": "cancel requested"})
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	job, err := h.store.Job(r.Context(), r.PathValue("id"), r.PathValue("job"))
	if err != nil {
		fail(w, err)
		return
	}
	runner, ok := h.service.runner.(interface {
		Log(id, kind string) (io.ReadCloser, error)
	})
	if !ok {
		http.NotFound(w, r)
		return
	}
	kind := "events"
	if r.Pattern == "GET /api/loops/{id}/jobs/{job}/stderr" {
		kind = "stderr"
	}
	file, err := runner.Log(job.ID, kind)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.txt"`, job.ID, kind))
	_, _ = io.Copy(w, io.LimitReader(file, 16*1024*1024))
}

func (h *Handler) optimize(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	var input OptimizeRequest
	if !decode(w, r, &input) {
		return
	}
	round, err := h.service.Optimize(r.Context(), r.PathValue("id"), input)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusAccepted, round)
}

func (h *Handler) promote(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version        string `json:"version"`
		ExpectedActive string `json:"expectedActive"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := h.store.Promote(r.Context(), r.PathValue("id"), input.Version, input.ExpectedActive); err != nil {
		fail(w, err)
		return
	}
	h.changed()
	respond(w, http.StatusOK, map[string]string{"activeVersion": input.Version})
}

func (h *Handler) feedback(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Job    string `json:"job"`
		Rating string `json:"rating"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &input) {
		return
	}
	feedback, err := h.store.AddFeedback(r.Context(), r.PathValue("id"), input.Job, input.Rating, input.Note)
	if err != nil {
		fail(w, err)
		return
	}
	h.changed()
	respond(w, http.StatusCreated, feedback)
}

func (h *Handler) cancelRound(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	rounds, err := h.store.Rounds(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	for _, round := range rounds {
		if round.ID == r.PathValue("round") {
			h.service.Cancel(round.ID)
			respond(w, http.StatusAccepted, map[string]string{"status": "cancel requested"})
			return
		}
	}
	fail(w, ErrNotFound)
}

func (h *Handler) optimizeDefault(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if !decode(w, r, &input) {
		return
	}
	round, err := h.service.OptimizeDefault(r.Context(), r.PathValue("id"), input.Version)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusAccepted, round)
}

func (h *Handler) closeRun(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	job, err := h.service.CloseRun(r.Context(), r.PathValue("id"), r.PathValue("job"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, job)
}
