package board

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskTemplateAPI(t *testing.T) {
	b, store := newTestBoard(t)
	card := &Card{ID: "existing", Title: "Existing card", Prompt: "Original prompt", Status: StatusWaiting}
	require.NoError(t, store.InsertCard(card))
	updates := make(chan struct{}, 10)
	b.addClient(updates)
	defer b.removeClient(updates)
	handler, err := buildMux(b)
	require.NoError(t, err)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	rec := request(http.MethodGet, "/api/task-templates", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Empty(t, updates)

	prompt := " \n  Fix {{issue}} and ${HOME}; $(echo do-not-run).\t\n"
	body, err := json.Marshal(TaskTemplate{ID: "client-id", Name: " \tFix issue\n ", Prompt: prompt})
	require.NoError(t, err)
	rec = request(http.MethodPost, "/api/task-templates", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var first TaskTemplate
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &first))
	assert.Regexp(t, `^[0-9a-f]{16}$`, first.ID)
	assert.NotEqual(t, "client-id", first.ID)
	assert.Equal(t, "Fix issue", first.Name)
	assert.Equal(t, prompt, first.Prompt)
	assert.Len(t, updates, 1)

	rec = request(http.MethodPost, "/api/task-templates", string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var second TaskTemplate
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &second))
	assert.NotEqual(t, first.ID, second.ID, "each creation gets a fresh ID, even with identical input")
	assert.Len(t, updates, 2)

	prompt = "\n Updated {{issue}}\n\t"
	body, err = json.Marshal(TaskTemplate{ID: second.ID, Name: " \tNew name\n", Prompt: prompt})
	require.NoError(t, err)
	rec = request(http.MethodPut, "/api/task-templates/"+first.ID, string(body))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updated TaskTemplate
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	want := TaskTemplate{ID: first.ID, Name: "New name", Prompt: prompt}
	assert.Equal(t, want, updated, "the path ID overrides any body ID")
	assert.Len(t, updates, 3)

	rec = request(http.MethodGet, "/api/task-templates", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var templates []*TaskTemplate
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &templates))
	assert.Equal(t, []*TaskTemplate{&want, &second}, templates, "editing must not reorder templates")
	assert.Len(t, updates, 3, "reads do not broadcast")

	rec = request(http.MethodDelete, "/api/task-templates/"+first.ID, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())
	assert.Len(t, updates, 4)
	templates, err = store.ListTaskTemplates()
	require.NoError(t, err)
	assert.Equal(t, []*TaskTemplate{&second}, templates)

	rec = request(http.MethodDelete, "/api/task-templates/"+first.ID, "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec = request(http.MethodPut, "/api/task-templates/"+first.ID, string(body))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Len(t, updates, 4, "missing resources do not broadcast")

	storedCard, err := store.GetCard(card.ID)
	require.NoError(t, err)
	assert.Equal(t, card, storedCard, "template CRUD must not modify existing cards")
}

func TestTaskTemplateAPIRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		message string
	}{
		{"empty body", "", "invalid json"},
		{"invalid JSON", "{", "invalid json"},
		{"wrong name type", `{"name":42,"prompt":"Prompt"}`, "invalid json"},
		{"wrong prompt type", `{"name":"Name","prompt":42}`, "invalid json"},
		{"array", `[]`, "invalid json"},
		{"null", `null`, "name required"},
		{"missing name", `{"prompt":"Prompt"}`, "name required"},
		{"empty name", `{"name":"","prompt":"Prompt"}`, "name required"},
		{"blank name", `{"name":" \t\n\u2003 ","prompt":"Prompt"}`, "name required"},
		{"missing prompt", `{"name":"Name"}`, "prompt required"},
		{"empty prompt", `{"name":"Name","prompt":""}`, "prompt required"},
		{"blank prompt", `{"name":"Name","prompt":" \t\n\u2003 "}`, "prompt required"},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					b, store := newTestBoard(t)
					original := &TaskTemplate{ID: "target", Name: "Original", Prompt: "Original prompt"}
					require.NoError(t, store.InsertTaskTemplate(original))
					updates := make(chan struct{}, 1)
					b.addClient(updates)
					defer b.removeClient(updates)
					handler, err := buildMux(b)
					require.NoError(t, err)
					path := "/api/task-templates"
					if method == http.MethodPut {
						path += "/target"
					}
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(tt.body)))

					assert.Equal(t, http.StatusBadRequest, rec.Code)
					assert.Contains(t, rec.Body.String(), tt.message)
					assert.Empty(t, updates)
					templates, err := store.ListTaskTemplates()
					require.NoError(t, err)
					assert.Equal(t, []*TaskTemplate{original}, templates)
				})
			}
		})
	}
}

type taskTemplateErrorStore struct {
	Store

	err error
}

func (s taskTemplateErrorStore) ListTaskTemplates() ([]*TaskTemplate, error) {
	return nil, s.err
}

func (s taskTemplateErrorStore) InsertTaskTemplate(*TaskTemplate) error {
	return s.err
}

func (s taskTemplateErrorStore) UpdateTaskTemplate(*TaskTemplate) (*TaskTemplate, error) {
	return nil, s.err
}

func (s taskTemplateErrorStore) DeleteTaskTemplate(string) error {
	return s.err
}

func TestTaskTemplateAPIStoreErrors(t *testing.T) {
	type failure struct {
		name    string
		err     error
		status  int
		message string
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			failures := []failure{
				{"internal", errors.New("private database failure"), http.StatusInternalServerError, "internal error"},
			}
			if method == http.MethodPut || method == http.MethodDelete {
				failures = append(failures, failure{"wrapped missing", fmt.Errorf("store: %w", sql.ErrNoRows), http.StatusNotFound, "not found: task template target"})
			}
			for _, failure := range failures {
				t.Run(failure.name, func(t *testing.T) {
					b, store := newTestBoard(t)
					original := &TaskTemplate{ID: "target", Name: "Original", Prompt: "Original prompt"}
					require.NoError(t, store.InsertTaskTemplate(original))
					b.store = taskTemplateErrorStore{Store: store, err: failure.err}
					updates := make(chan struct{}, 1)
					b.addClient(updates)
					defer b.removeClient(updates)
					handler, err := buildMux(b)
					require.NoError(t, err)
					path := "/api/task-templates"
					if method == http.MethodPut || method == http.MethodDelete {
						path += "/target"
					}
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{"name":"Name","prompt":"Prompt"}`)))

					assert.Equal(t, failure.status, rec.Code)
					assert.Equal(t, failure.message+"\n", rec.Body.String())
					assert.Empty(t, updates, "failures must not broadcast")
					templates, err := store.ListTaskTemplates()
					require.NoError(t, err)
					assert.Equal(t, []*TaskTemplate{original}, templates)
				})
			}
		})
	}
}

func TestTaskTemplateAPIMissing(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			b, store := newTestBoard(t)
			updates := make(chan struct{}, 1)
			b.addClient(updates)
			defer b.removeClient(updates)
			handler, err := buildMux(b)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(method, "/api/task-templates/missing", strings.NewReader(`{"name":"Name","prompt":"Prompt"}`)))
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Equal(t, "not found: task template missing\n", rec.Body.String())
			assert.Empty(t, updates)
			templates, err := store.ListTaskTemplates()
			require.NoError(t, err)
			assert.Empty(t, templates)
		})
	}
}

func TestTaskTemplateAPIRejectsCrossOriginMutations(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			b, store := newTestBoard(t)
			original := &TaskTemplate{ID: "target", Name: "Original", Prompt: "Original prompt"}
			require.NoError(t, store.InsertTaskTemplate(original))
			updates := make(chan struct{}, 1)
			b.addClient(updates)
			defer b.removeClient(updates)
			handler, err := buildMux(b)
			require.NoError(t, err)
			path := "/api/task-templates"
			if method != http.MethodPost {
				path += "/target"
			}
			body, err := json.Marshal(TaskTemplate{Name: "Name", Prompt: "Prompt"})
			require.NoError(t, err)
			req := httptest.NewRequest(method, path, bytes.NewReader(body))
			req.Header.Set("Origin", "https://other.example")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Empty(t, updates)
			templates, err := store.ListTaskTemplates()
			require.NoError(t, err)
			assert.Equal(t, []*TaskTemplate{original}, templates)
		})
	}
}
